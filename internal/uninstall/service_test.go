package uninstall

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
)

type fakePlanner struct{ p Plan }

func (f fakePlanner) Build(context.Context, string) (Plan, error) { return f.p, nil }

func sel(keys ...string) string { b, _ := json.Marshal(keys); return string(b) }

func TestServicePlan(t *testing.T) {
	p := Plan{Item: "package:foo", Name: "foo", Candidates: []Candidate{
		{Group: GroupProgram, Kind: KindPackage, ID: "foo:amd64", Name: "foo", Preselect: true},
		{Group: GroupProgram, Kind: KindPackage, ID: "foo-plugin:amd64", Name: "foo-plugin", Preselect: true},
		{Group: GroupOrphans, Kind: KindPackage, ID: "libfoo:amd64", Name: "libfoo", Preselect: true},
		{Group: GroupSystem, Kind: KindArchive, ID: "/var/lib/mysql", Name: "/var/lib/mysql", Warn: "data"},
		{Group: GroupRemains, Kind: KindNothing, ID: "libmine:amd64", Name: "libmine"},
	}}
	s := &Service{Builder: fakePlanner{p}}
	ctx := context.Background()
	plan := func(params map[string]string) (actions.Plan, error) {
		return s.plan(ctx, actions.Request{Target: "package:foo", Params: params})
	}
	// The program alone: what it takes along is added, no name to type.
	ap, err := plan(map[string]string{"select": sel("program|foo:amd64")})
	if err != nil || len(ap.Items) != 2 || ap.Confirm != "" {
		t.Fatalf("%+v %v", ap, err)
	}
	// More than the program: the name is typed.
	ap, err = plan(map[string]string{"select": sel("program|foo:amd64", "orphans|libfoo:amd64")})
	if err != nil || ap.Confirm != "foo" {
		t.Fatalf("%+v %v", ap, err)
	}
	for name, params := range map[string]map[string]string{
		"no selection":  {},
		"bad selection": {"select": "not json"},
		"unknown row":   {"select": sel("program|foo:amd64", "system|/etc/shadow")},
		"remains":       {"select": sel("program|foo:amd64", "remains|libmine:amd64")},
		"data untyped":  {"select": sel("program|foo:amd64", "system|/var/lib/mysql")},
		"data mistyped": {"select": sel("program|foo:amd64", "system|/var/lib/mysql"), "data": sel("/var/lib/mysq")},
	} {
		if _, err := plan(params); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The program's packages always go: they cannot be left out.
	if ap, err := plan(map[string]string{"select": sel("orphans|libfoo:amd64")}); err != nil || len(ap.Items) != 3 {
		t.Fatalf("%+v %v", ap, err)
	}
	if _, err := plan(map[string]string{"select": sel("program|foo:amd64", "system|/var/lib/mysql"), "data": sel("/var/lib/mysql")}); err != nil {
		t.Fatalf("typed data path refused: %v", err)
	}

	// Desktop packages need the words typed, and the package's name.
	p.Desktop = []string{"foo:amd64"}
	s.Builder = fakePlanner{p}
	if _, err := plan(map[string]string{"select": sel("program|foo:amd64")}); err == nil || !strings.Contains(err.Error(), DesktopWords) {
		t.Fatalf("%v", err)
	}
	ap, err = plan(map[string]string{"select": sel("program|foo:amd64"), "desktop": DesktopWords})
	if err != nil || ap.Confirm != "foo:amd64" {
		t.Fatalf("%+v %v", ap, err)
	}

	// A blocked plan has nothing to run.
	p.Desktop, p.Blocked = nil, "bash can never be removed (essential)"
	s.Builder = fakePlanner{p}
	ap, err = plan(map[string]string{"select": sel("program|foo:amd64")})
	if err != nil || len(ap.Items) != 0 || len(ap.Blocked) != 1 || ap.State != nil {
		t.Fatalf("%+v %v", ap, err)
	}
}

func TestValidItem(t *testing.T) {
	for _, ok := range []string{"app:vlc", "tool:rg", "package:vlc:amd64", "game:steam.570", "flatpak:system/app/a.b.C/x86_64/stable", "snap:x"} {
		if validItem(ok) != nil {
			t.Errorf("refused %s", ok)
		}
	}
	for _, bad := range []string{"", "vlc", "app:", "shell:x", "app:a\nb", strings.Repeat("a", 5000)} {
		if validItem(bad) == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
