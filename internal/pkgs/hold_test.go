package pkgs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

type fakeBroker struct {
	calls []string
	err   error
}

func (b *fakeBroker) Call(_ context.Context, method string, params, _ any) error {
	j, _ := json.Marshal(params)
	b.calls = append(b.calls, method+" "+string(j))
	return b.err
}

var installed = fakeDpkg{
	"vim":  {Name: "vim", Arch: "amd64", State: "installed", Version: "9.1"},
	"bash": {Name: "bash", Arch: "amd64", State: "installed", Hold: true},
	"gone": {Name: "gone", Arch: "amd64", State: "config-files"},
}

func TestHoldPlan(t *testing.T) {
	cfg := Config{Dpkg: installed}
	if p, err := holdPlan(cfg, true, "vim"); err != nil || len(p.Items) != 1 || p.Items[0].ID != "vim" {
		t.Fatalf("%v %+v", err, p)
	}
	if p, err := holdPlan(cfg, false, "bash:amd64"); err != nil || len(p.Items) != 1 {
		t.Fatalf("unhold: %v %+v", err, p)
	}
	for _, c := range []struct {
		hold   bool
		target string
		status int
	}{
		{true, "bash", http.StatusConflict}, {false, "vim", http.StatusConflict},
		{true, "gone", http.StatusNotFound}, {true, "nope", http.StatusNotFound}, {true, "vim:i386", http.StatusNotFound},
	} {
		if _, err := holdPlan(cfg, c.hold, c.target); status(err) != c.status {
			t.Errorf("%v %s: %v", c.hold, c.target, err)
		}
	}
}

func TestHoldRun(t *testing.T) {
	ctx := context.Background()
	p, _ := holdPlan(Config{Dpkg: installed}, true, "vim")
	b := &fakeBroker{}
	if res, err := holdRun(ctx, b, true, p); err != nil || res.Done != 1 || len(b.calls) != 1 || b.calls[0] != `pkg.hold {"package":"vim","hold":true}` {
		t.Fatalf("%v %+v %v", err, res, b.calls)
	}
	for err, reason := range map[error]string{
		broker.ErrUnauthorized:                             ReasonUnauthorized,
		broker.Errorf(broker.CodeNotFound, "x"):            ReasonNotInstalled,
		broker.Errorf(broker.CodeFailed, "apt-mark: lock"): ReasonError,
	} {
		res, rerr := holdRun(ctx, &fakeBroker{err: err}, true, p)
		if rerr != nil || res.Done != 0 || len(res.Failed) != 1 || res.Failed[0].Reason != reason {
			t.Errorf("%v: %v %+v", err, rerr, res)
		}
	}
	if _, err := holdRun(ctx, &fakeBroker{err: broker.ErrUnavailable}, true, p); status(err) != http.StatusServiceUnavailable {
		t.Errorf("unavailable: %v", err)
	}
	if _, err := holdRun(ctx, &fakeBroker{err: broker.ErrNotMember}, true, p); status(err) != http.StatusForbidden {
		t.Errorf("not member: %v", err)
	}
}

func TestUpdateAllSkipsBlocked(t *testing.T) {
	cfg, f := setup(func(m string, _ []any) []sig {
		if m == "GetUpdates" {
			return []sig{pkg(8, vim), pkg(5, cowsay), pkg(9, fortune)}
		}
		return []sig{pkg(11, vim), pkg(11, cowsay)}
	})
	p, err := updateAllPlan(context.Background(), cfg)
	if err != nil || len(p.Items) != 2 || p.Confirm != "" || p.Summary != "update all" {
		t.Fatalf("%v %+v", err, p)
	}
	if c := f.called(); len(c) != 2 || !strings.HasPrefix(c[1], "UpdatePackages [4 ["+vim+" "+cowsay+"]]") {
		t.Fatalf("calls %v", c)
	}
	cfg, _ = setup(func(m string, _ []any) []sig {
		if m == "GetUpdates" {
			return []sig{pkg(9, fortune)}
		}
		return nil
	})
	if _, err := updateAllPlan(context.Background(), cfg); status(err) != http.StatusNotFound {
		t.Fatalf("nothing to update: %v", err)
	}
}

func TestUpdatesListAndCache(t *testing.T) {
	cfg, f := setup(func(string, []any) []sig {
		return []sig{pkg(5, cowsay), pkg(8, vim), pkg(9, fortune), pkg(5, cowsay)}
	})
	stamp := "a"
	u := &Updates{PK: cfg.PK, Stamp: func() string { return stamp }}
	rec := httptest.NewRecorder()
	u.HandleList(rec, httptest.NewRequest("GET", "/api/v1/pkgs/updates", nil))
	var body struct{ Updates []Update }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	l := body.Updates
	if len(l) != 3 || l[0].Name != "vim" || !l[0].Security || l[1].Name != "cowsay" || l[2].Severity != "blocked" {
		t.Fatalf("%+v", l)
	}
	_, _ = u.List(context.Background())
	if n := len(f.called()); n != 1 {
		t.Fatalf("not cached: %d calls", n)
	}
	stamp = "b"
	_, _ = u.List(context.Background())
	if n := len(f.called()); n != 2 {
		t.Fatalf("stamp change not noticed: %d calls", n)
	}
}

// PackageKit 1.2.6+ sends an update's severity in the upper 16 bits of
// the info (seen on Debian 13: 0x80008 for a security update).
func TestUpdatesPackedSeverity(t *testing.T) {
	cfg, _ := setup(func(string, []any) []sig {
		return []sig{pkg(5<<16|5, cowsay), pkg(8<<16|8, vim), pkg(9, fortune)}
	})
	l, err := (&Updates{PK: cfg.PK}).List(context.Background())
	if err != nil || len(l) != 3 {
		t.Fatalf("%v %+v", err, l)
	}
	if l[0].Name != "vim" || !l[0].Security || l[0].Severity != "security" || l[1].Severity != "normal" || l[2].Severity != "blocked" {
		t.Fatalf("%+v", l)
	}
	cfg, _ = setup(func(m string, _ []any) []sig {
		if m == "GetUpdates" {
			return []sig{pkg(8<<16|8, vim)}
		}
		return []sig{pkg(11, vim)}
	})
	if p, err := updateAllPlan(context.Background(), cfg); err != nil || len(p.Items) != 1 {
		t.Fatalf("update all with packed info: %v %+v", err, p)
	}
}
