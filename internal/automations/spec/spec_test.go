package spec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const good = `{
  "trigger": {"kind": "metric", "metric": "cpu.temp", "op": ">", "threshold": 90, "for_s": 60},
  "if": {"match": "all", "conditions": [{"kind": "power", "on": "ac"}, {"kind": "time", "from": "08:00", "to": "23:00"},
    {"kind": "wifi", "uuid": "0b5b1f6e-7c43-4c8e-9a3e-2f1d9b0c6a11", "is": true}]},
  "then": [
    {"step": "action", "action": "power/profile", "target": "power-saver"},
    {"step": "wait", "seconds": 30},
    {"step": "check", "what": "container_health", "target": "web", "expect": "healthy",
      "then": [{"step": "notify", "channels": ["desktop"], "text": "ok"}],
      "else": [{"step": "action", "action": "container/restart", "target": "web"}, {"step": "notify", "channels": ["desktop", "ntfy"], "text": "CPU at {value}; top: {top}"}]}
  ],
  "options": {"cooldown_s": 300, "max_per_hour": 6, "quiet_from": "23:00", "quiet_to": "07:00"}
}`

func TestParseGood(t *testing.T) {
	s, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if s.Trigger.Metric != "cpu.temp" || len(s.If.Conditions) != 3 || len(s.Then) != 3 || len(s.Then[2].ElseDo) != 2 {
		t.Fatalf("%+v", s)
	}
	n := 0
	s.Steps(func(Step) { n++ })
	if n != 6 {
		t.Fatalf("visited %d", n)
	}
	// Defaults when options are left out.
	d, err := Parse([]byte(`{"trigger":{"kind":"login"},"then":[{"step":"wait","seconds":1}]}`))
	if err != nil || d.Options.CooldownS != DefaultCool || d.Options.MaxPerHour != DefaultPerHour {
		t.Fatalf("%+v %v", d.Options, err)
	}
}

func TestEveryTriggerKind(t *testing.T) {
	for _, tr := range []string{
		`{"kind":"schedule","calendar":"Mon..Fri *-*-* 02:00"}`, `{"kind":"login"}`,
		`{"kind":"metric","metric":"disk.used_pct","op":">","threshold":90,"for_s":0,"target":"/home"}`,
		`{"kind":"container","event":"unhealthy","target":"web"}`, `{"kind":"unit_failed","scope":"system","target":"cups.service"}`,
		`{"kind":"new_listener"}`, `{"kind":"usb_device"}`, `{"kind":"security_updates"}`, `{"kind":"image_update","target":"web"}`,
		`{"kind":"integrity_changed"}`, `{"kind":"ssh_failed","count":10}`, `{"kind":"power","event":"unplugged"}`,
		`{"kind":"wifi","uuid":"0b5b1f6e-7c43-4c8e-9a3e-2f1d9b0c6a11","state":"up"}`,
		`{"kind":"vpn","uuid":"0b5b1f6e-7c43-4c8e-9a3e-2f1d9b0c6a11","state":"down"}`, `{"kind":"internet","state":"back"}`,
		`{"kind":"idle","minutes":30}`, `{"kind":"resume"}`, `{"kind":"display","event":"connected"}`,
		`{"kind":"app","state":"started","game":true}`, `{"kind":"app","state":"stopped","app":"org.gnome.TextEditor"}`,
		`{"kind":"files","folder":"~/Downloads","event":"older_than","days":30,"depth":0,"pattern":"*"}`,
		`{"kind":"files","folder":"~","event":"new","depth":1,"pattern":"*.pdf"}`,
	} {
		if _, err := Parse([]byte(`{"trigger":` + tr + `,"then":[{"step":"wait","seconds":1}]}`)); err != nil {
			t.Errorf("%s: %v", tr, err)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	steps := `"then":[{"step":"wait","seconds":1}]`
	bad := map[string]string{
		"no trigger":       `{` + steps + `}`,
		"unknown trigger":  `{"trigger":{"kind":"shell"},` + steps + `}`,
		"foreign field":    `{"trigger":{"kind":"login","calendar":"daily"},` + steps + `}`,
		"command field":    `{"trigger":{"kind":"login","command":"rm -rf ~"},` + steps + `}`,
		"unknown metric":   `{"trigger":{"kind":"metric","metric":"x","op":">","threshold":1},` + steps + `}`,
		"threshold range":  `{"trigger":{"kind":"metric","metric":"cpu.usage","op":">","threshold":101},` + steps + `}`,
		"ssid not uuid":    `{"trigger":{"kind":"wifi","uuid":"HomeWiFi","state":"up"},` + steps + `}`,
		"folder outside":   `{"trigger":{"kind":"files","folder":"/etc","event":"new","pattern":"*"},` + steps + `}`,
		"folder traversal": `{"trigger":{"kind":"files","folder":"~/../..","event":"new","pattern":"*"},` + steps + `}`,
		"deep files":       `{"trigger":{"kind":"files","folder":"~","event":"new","depth":3,"pattern":"*"},` + steps + `}`,
		"glob":             `{"trigger":{"kind":"files","folder":"~","event":"new","pattern":"*.{a,b}"},` + steps + `}`,
		"app and game":     `{"trigger":{"kind":"app","state":"started","game":true,"app":"x"},` + steps + `}`,
		"idle short":       `{"trigger":{"kind":"idle","minutes":1},` + steps + `}`,
		"no steps":         `{"trigger":{"kind":"login"},"then":[]}`,
		"unknown step":     `{"trigger":{"kind":"login"},"then":[{"step":"exec","command":"id"}]}`,
		"script field":     `{"trigger":{"kind":"login"},"then":[{"step":"action","action":"job/run","target":"user/x","script":"id"}]}`,
		"denied action":    `{"trigger":{"kind":"login"},"then":[{"step":"action","action":"uninstall/remove","target":"package:bash"}]}`,
		"action shape":     `{"trigger":{"kind":"login"},"then":[{"step":"action","action":"sh -c id"}]}`,
		"target dash":      `{"trigger":{"kind":"login"},"then":[{"step":"action","action":"unit/stop","target":"--now"}]}`,
		"control char":     `{"trigger":{"kind":"login"},"then":[{"step":"action","action":"unit/stop","target":"a\nb"}]}`,
		"param key":        `{"trigger":{"kind":"login"},"then":[{"step":"action","action":"unit/stop","target":"x","params":{"Exec Start":"/bin/sh"}}]}`,
		"notify hole":      `{"trigger":{"kind":"login"},"then":[{"step":"notify","channels":["desktop"],"text":"{env.HOME}"}]}`,
		"notify brace":     `{"trigger":{"kind":"login"},"then":[{"step":"notify","channels":["desktop"],"text":"a } b"}]}`,
		"notify channel":   `{"trigger":{"kind":"login"},"then":[{"step":"notify","channels":["sms"],"text":"x"}]}`,
		"long wait":        `{"trigger":{"kind":"login"},"then":[{"step":"wait","seconds":601}]}`,
		"waits add up":     `{"trigger":{"kind":"login"},"then":[{"step":"wait","seconds":600},{"step":"wait","seconds":400}]}`,
		"nested check":     `{"trigger":{"kind":"login"},"then":[{"step":"check","what":"unit_state","target":"x","expect":"active","then":[{"step":"check","what":"unit_state","target":"x","expect":"active","then":[{"step":"wait","seconds":1}]}]}]}`,
		"empty check":      `{"trigger":{"kind":"login"},"then":[{"step":"check","what":"unit_state","target":"x","expect":"active"}]}`,
		"too many cond":    `{"trigger":{"kind":"login"},"if":{"match":"all","conditions":[` + strings.Repeat(`{"kind":"game"},`, 8) + `{"kind":"game"}]},` + steps + `}`,
		"bad match":        `{"trigger":{"kind":"login"},"if":{"match":"xor","conditions":[{"kind":"game"}]},` + steps + `}`,
		"nested if":        `{"trigger":{"kind":"login"},"if":{"match":"all","conditions":[{"kind":"any","conditions":[]}]},` + steps + `}`,
		"cooldown":         `{"trigger":{"kind":"login"},` + steps + `,"options":{"cooldown_s":1,"max_per_hour":6}}`,
		"missed not sched": `{"trigger":{"kind":"login"},` + steps + `,"options":{"cooldown_s":300,"max_per_hour":6,"run_if_missed":true}}`,
		"trailing":         `{"trigger":{"kind":"login"},` + steps + `}{}`,
		"unknown top":      `{"trigger":{"kind":"login"},` + steps + `,"run":"x"}`,
		"oversized":        `{"trigger":{"kind":"login"},"then":[{"step":"notify","channels":["desktop"],"text":"` + strings.Repeat("a", MaxSpec) + `"}]}`,
		"not json":         `trigger: login`,
	}
	for name, raw := range bad {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestImportExport(t *testing.T) {
	s, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Export([]Item{{Name: "Heat", Enabled: true, Spec: s}})
	if err != nil {
		t.Fatal(err)
	}
	items, err := ParseImport(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Enabled || !reflect.DeepEqual(items[0].Spec, s) {
		t.Fatalf("%+v", items)
	}
	for name, raw := range map[string]string{
		"version":   `{"version":2,"automations":[]}`,
		"none":      `{"version":1,"automations":[]}`,
		"bad name":  `{"version":1,"automations":[{"name":"","enabled":true,"spec":` + good + `}]}`,
		"one bad":   `{"version":1,"automations":[{"name":"a","spec":` + good + `},{"name":"b","spec":{"trigger":{"kind":"shell"}}}]}`,
		"extra key": `{"version":1,"automations":[],"script":"x"}`,
		"too big":   `{"version":1,"automations":[{"name":"` + strings.Repeat("a", MaxImport) + `"}]}`,
	} {
		if _, err := ParseImport([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	many := File{Version: 1}
	for range MaxAutomations + 1 {
		many.Automations = append(many.Automations, Item{Name: "x", Spec: s})
	}
	raw, _ := json.Marshal(many)
	if _, err := ParseImport(raw); err == nil {
		t.Error("accepted too many")
	}
}

// Whatever arrives, an accepted spec is within bounds, holds no denied
// action, and reads back identical after an export.
func FuzzParse(f *testing.F) {
	f.Add(good)
	f.Add(`{"trigger":{"kind":"login"},"then":[{"step":"wait","seconds":1}]}`)
	f.Add(`{"trigger":{"kind":"files","folder":"~/a","event":"new","pattern":"*.x"},"then":[{"step":"notify","channels":["mail"],"text":"{trigger}"}]}`)
	f.Fuzz(func(t *testing.T, raw string) {
		s, err := Parse([]byte(raw))
		if err != nil {
			return
		}
		if len(s.Then) == 0 || len(s.Then) > MaxSteps || (s.If != nil && len(s.If.Conditions) > MaxConditions) {
			t.Fatal("bounds")
		}
		s.Steps(func(st Step) {
			for _, d := range Denied {
				if st.Action == d {
					t.Fatalf("denied %s", d)
				}
			}
			if st.Step == "check" && (len(st.ThenDo) > MaxBranch || len(st.ElseDo) > MaxBranch) {
				t.Fatal("branch bounds")
			}
		})
		b, err := Export([]Item{{Name: "x", Spec: s}})
		if err != nil {
			t.Fatal(err)
		}
		items, err := ParseImport(b)
		if err != nil {
			t.Fatalf("export does not read back: %v\n%s", err, b)
		}
		if !reflect.DeepEqual(items[0].Spec, s) {
			t.Fatalf("read back differently:\n%+v\n%+v", items[0].Spec, s)
		}
	})
}

func FuzzParseImport(f *testing.F) {
	f.Add(`{"version":1,"automations":[{"name":"a","enabled":true,"spec":` + good + `}]}`)
	f.Fuzz(func(t *testing.T, raw string) {
		items, err := ParseImport([]byte(raw))
		if err != nil {
			return
		}
		if len(items) == 0 || len(items) > MaxAutomations {
			t.Fatal("count")
		}
		for _, it := range items {
			if it.Enabled || CheckName(it.Name) != nil || it.Spec.Check() != nil {
				t.Fatalf("%+v", it)
			}
		}
	})
}
