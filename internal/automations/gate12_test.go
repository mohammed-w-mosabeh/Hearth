package automations

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/automations/spec"
	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Gate 12: every trigger kind fires from a simulated event, metric series
// or clock, and not again within its cooldown: the second event is seen
// and recorded as skipped by the cooldown, not missed. The validator's own
// list drives the table, so a new kind without a case fails.
//
// The alert kind is the exception by design (ROADMAP 12.7): an incident is
// debounced per instance by the alert engine, so each one notifies.
type gateCase struct {
	trigger string
	// first causes the event once (after a first look where needed), again
	// causes it a second time, a minute later.
	first, again func(t *testing.T, g *engineRig, ws *watchState)
}

// advance moves the rig's clock under its lock.
func (g *engineRig) advance(d time.Duration) {
	g.clockMu.Lock()
	g.now = g.now.Add(d)
	g.clockMu.Unlock()
}

func setState(f func()) { stateMu.Lock(); f(); stateMu.Unlock() }

func observeEvent(metric, inst string) func(*testing.T, *engineRig, *watchState) {
	return func(_ *testing.T, g *engineRig, _ *watchState) {
		g.observe(metric, map[string]float64{inst: 1})
	}
}

func clearThen(metric, inst string) func(*testing.T, *engineRig, *watchState) {
	return func(_ *testing.T, g *engineRig, _ *watchState) {
		g.observe(metric, map[string]float64{})
		g.observe(metric, map[string]float64{inst: 1})
	}
}

func tickWith(f func(ws *watchState)) func(*testing.T, *engineRig, *watchState) {
	return func(_ *testing.T, g *engineRig, ws *watchState) {
		ws.set(func() { f(ws) })
		g.tick()
	}
}

const uuidGate = "0b5b1f6e-7c43-4c8e-9a3e-2f1d9b0c6a11"

var gateCases = map[string]gateCase{
	"schedule": {`{"kind":"schedule","calendar":"*-*-* 12:01"}`,
		func(_ *testing.T, g *engineRig, _ *watchState) {
			g.tick()
			g.advance(2 * time.Minute)
			g.tick()
		},
		func(_ *testing.T, g *engineRig, _ *watchState) { g.advance(2 * time.Minute); g.tick() }},
	"login": {`{"kind":"login"}`,
		func(_ *testing.T, g *engineRig, _ *watchState) { g.e.Login(context.Background()); g.e.Wait() },
		func(_ *testing.T, g *engineRig, _ *watchState) { g.e.Login(context.Background()); g.e.Wait() }},
	"metric": {`{"kind":"metric","metric":"cpu.temp","op":">","threshold":90}`,
		func(_ *testing.T, g *engineRig, _ *watchState) { g.observe("cpu.temp", map[string]float64{"": 95}) },
		func(_ *testing.T, g *engineRig, _ *watchState) {
			g.observe("cpu.temp", map[string]float64{"": 40}) // closed past its hysteresis
			g.observe("cpu.temp", map[string]float64{"": 95})
		}},
	"alert": {`{"kind":"alert","metric":"cpu.temp","op":">","threshold":90,"severity":"warning"}`,
		func(_ *testing.T, g *engineRig, _ *watchState) {
			g.e.FireAlert(context.Background(), 1, "", 95, "hot")
			g.e.Wait()
		},
		func(_ *testing.T, g *engineRig, _ *watchState) {
			g.e.FireAlert(context.Background(), 1, "", 96, "hot")
			g.e.Wait()
		}},
	"container":         {`{"kind":"container","event":"unhealthy"}`, observeEvent("container.unhealthy", "web"), clearThen("container.unhealthy", "web")},
	"unit_failed":       {`{"kind":"unit_failed"}`, observeEvent("unit.failed", "user/x.service"), clearThen("unit.failed", "user/x.service")},
	"new_listener":      {`{"kind":"new_listener"}`, observeEvent("net.new_listener", "nc:9"), clearThen("net.new_listener", "nc:9")},
	"usb_device":        {`{"kind":"usb_device"}`, observeEvent("usb.new_device", "1-2"), clearThen("usb.new_device", "1-2")},
	"integrity_changed": {`{"kind":"integrity_changed"}`, observeEvent("integrity.changed", "/etc/hosts"), clearThen("integrity.changed", "/etc/hosts")},
	"internet":          {`{"kind":"internet","state":"down"}`, observeEvent("net.internet_down", ""), clearThen("net.internet_down", "")},
	"security_updates": {`{"kind":"security_updates"}`,
		func(_ *testing.T, g *engineRig, ws *watchState) {
			g.tick()
			tickWith(func(ws *watchState) { ws.sec = 1 })(nil, g, ws)
		},
		tickWith(func(ws *watchState) { ws.sec = 2 })},
	"image_update": {`{"kind":"image_update"}`,
		func(_ *testing.T, g *engineRig, ws *watchState) {
			g.tick()
			tickWith(func(ws *watchState) { ws.images = map[string]bool{"web": true} })(nil, g, ws)
		},
		func(_ *testing.T, g *engineRig, ws *watchState) {
			tickWith(func(ws *watchState) { ws.images = map[string]bool{} })(nil, g, ws)
			tickWith(func(ws *watchState) { ws.images = map[string]bool{"web": true} })(nil, g, ws)
		}},
	"ssh_failed": {`{"kind":"ssh_failed","count":10}`,
		func(_ *testing.T, g *engineRig, ws *watchState) {
			g.tick()
			g.advance(SSHEvery)
			tickWith(func(ws *watchState) { ws.ssh = 12 })(nil, g, ws)
		},
		func(_ *testing.T, g *engineRig, ws *watchState) {
			g.advance(SSHEvery)
			tickWith(func(ws *watchState) { ws.ssh = 2 })(nil, g, ws)
			g.advance(SSHEvery)
			tickWith(func(ws *watchState) { ws.ssh = 15 })(nil, g, ws)
		}},
	"power": {`{"kind":"power","event":"unplugged"}`,
		func(_ *testing.T, g *engineRig, _ *watchState) {
			g.tick()
			setState(func() { unplugged = true })
			g.tick()
		},
		func(_ *testing.T, g *engineRig, _ *watchState) {
			setState(func() { unplugged = false })
			g.tick()
			setState(func() { unplugged = true })
			g.tick()
		}},
	"wifi": {`{"kind":"wifi","uuid":"` + uuidGate + `","state":"up"}`,
		func(_ *testing.T, g *engineRig, _ *watchState) {
			g.tick()
			setState(func() { connsUp = map[string]bool{uuidGate: true} })
			g.tick()
		},
		func(_ *testing.T, g *engineRig, _ *watchState) {
			setState(func() { connsUp = map[string]bool{} })
			g.tick()
			setState(func() { connsUp = map[string]bool{uuidGate: true} })
			g.tick()
		}},
	"vpn": {`{"kind":"vpn","uuid":"` + uuidGate + `","state":"up"}`,
		func(_ *testing.T, g *engineRig, _ *watchState) {
			g.tick()
			setState(func() { connsUp = map[string]bool{uuidGate: true} })
			g.tick()
		},
		func(_ *testing.T, g *engineRig, _ *watchState) {
			setState(func() { connsUp = map[string]bool{} })
			g.tick()
			setState(func() { connsUp = map[string]bool{uuidGate: true} })
			g.tick()
		}},
	"idle": {`{"kind":"idle","minutes":30}`,
		func(_ *testing.T, g *engineRig, ws *watchState) {
			g.tick()
			tickWith(func(ws *watchState) { ws.idle = 1900 })(nil, g, ws)
		},
		func(_ *testing.T, g *engineRig, ws *watchState) {
			tickWith(func(ws *watchState) { ws.idle = 1 })(nil, g, ws)
			tickWith(func(ws *watchState) { ws.idle = 1900 })(nil, g, ws)
		}},
	"display": {`{"kind":"display","event":"connected"}`,
		func(_ *testing.T, g *engineRig, _ *watchState) {
			g.tick()
			setState(func() { displaysN = 2 })
			g.tick()
		},
		func(_ *testing.T, g *engineRig, _ *watchState) {
			setState(func() { displaysN = 1 })
			g.tick()
			setState(func() { displaysN = 2 })
			g.tick()
		}},
	"app": {`{"kind":"app","state":"started","game":true}`,
		func(_ *testing.T, g *engineRig, ws *watchState) {
			g.tick()
			tickWith(func(ws *watchState) { ws.games = []string{"steam.1"} })(nil, g, ws)
		},
		func(_ *testing.T, g *engineRig, ws *watchState) {
			for range GameGone {
				tickWith(func(ws *watchState) { ws.games = nil })(nil, g, ws)
			}
			tickWith(func(ws *watchState) { ws.games = []string{"steam.2"} })(nil, g, ws)
		}},
	"resume": {`{"kind":"resume"}`,
		func(t *testing.T, g *engineRig, ws *watchState) { ws.resumes <- struct{}{}; waitRuns(t, g, 1) },
		func(t *testing.T, g *engineRig, ws *watchState) { ws.resumes <- struct{}{}; waitRuns(t, g, 2) }},
	"files": {`{"kind":"files","folder":"~/Inbox","event":"new","depth":0,"pattern":"*"}`,
		func(t *testing.T, g *engineRig, _ *watchState) {
			write(t, filepath.Join(g.e.cfg.Watch.Home, "Inbox", "a.txt"))
			waitRuns(t, g, 1)
		},
		func(t *testing.T, g *engineRig, _ *watchState) {
			write(t, filepath.Join(g.e.cfg.Watch.Home, "Inbox", "b.txt"))
			waitRuns(t, g, 2)
		}},
}

func write(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// waitRuns waits for n run records (an event that arrives in the
// background: fsnotify, logind).
func waitRuns(t *testing.T, g *engineRig, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		g.e.Wait()
		if len(g.st.states()) >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waited for %d runs, have %v", n, g.st.states())
}

func TestGate12EveryTriggerKind(t *testing.T) {
	kinds := spec.TriggerKinds()
	if len(kinds) != len(gateCases) {
		t.Errorf("%d trigger kinds, %d cases", len(kinds), len(gateCases))
	}
	for _, kind := range kinds {
		c, ok := gateCases[kind]
		if !ok {
			t.Errorf("no case for trigger kind %q", kind)
			continue
		}
		t.Run(kind, func(t *testing.T) {
			setState(func() { connsUp, unplugged, displaysN = map[string]bool{}, false, 1 })
			g, ws := sourcesRig(t)
			if err := os.MkdirAll(filepath.Join(g.e.cfg.Watch.Home, "Inbox"), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			g.e.runCtx = ctx
			g.e.watchResume(ctx)
			// An hour's cooldown: the SSH case moves the clock 11 minutes
			// (two counts, SSHEvery apart) before its second event.
			raw := `{"trigger":` + c.trigger + `,"then":[{"step":"action","action":"power/profile","target":"x"}],"options":{"cooldown_s":3600,"max_per_hour":60}}`
			s, err := spec.Parse([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			if s.Trigger.Kind != kind {
				t.Fatalf("case of %q holds a %q trigger", kind, s.Trigger.Kind)
			}
			b, _ := json.Marshal(s)
			g.st.autos = append(g.st.autos, store.Automation{ID: 1, Name: kind, Enabled: true, Spec: b, Origin: "ui"})
			if err := g.e.Load(ctx); err != nil {
				t.Fatal(err)
			}
			c.first(t, g, ws)
			g.e.Wait()
			if n := strings.Count(g.ranList(), "power/profile x"); n != 1 {
				t.Fatalf("first event: ran %d times (%q), runs %v", n, g.ranList(), g.st.states())
			}
			g.advance(time.Minute)
			c.again(t, g, ws)
			g.e.Wait()
			states := g.st.states()
			ran := strings.Count(g.ranList(), "power/profile x")
			if kind == "alert" {
				if ran != 2 {
					t.Fatalf("each incident of an alert rule runs: ran %d, runs %v", ran, states)
				}
				return
			}
			if ran != 1 {
				t.Fatalf("ran again within its cooldown: %d (%v)", ran, states)
			}
			if len(states) != 2 || !strings.HasPrefix(states[1], "skipped") || !strings.Contains(states[1], "cooldown") {
				t.Fatalf("the second event was not seen and skipped by the cooldown: %v", states)
			}
		})
	}
}
