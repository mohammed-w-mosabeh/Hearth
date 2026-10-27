package broker

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

func TestPkgHold(t *testing.T) {
	var ran [][]string
	result := safeexec.Result{}
	var runErr error
	m := PackageMethods(func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
		if name != "apt-mark-hold" {
			t.Fatalf("ran %q", name)
		}
		ran = append(ran, args)
		return result, runErr
	})[0]
	if m.Name != "pkg.hold" || !m.Privileged || m.Action != ActionPkgHold {
		t.Fatalf("%+v", m)
	}
	ctx := context.Background()
	for _, bad := range []string{
		`{"package":"-x","hold":true}`, `{"package":"a b"}`, `{"package":"vim;rm"}`, `{"package":"x:AMD64"}`,
		`{"package":"vim","extra":1}`, `{}`, `{"package":"../../etc"}`, `{"package":"vim\n"}`,
	} {
		if err := m.Check(ctx, json.RawMessage(bad)); !errors.Is(err, &Error{Code: CodeBadParams}) {
			t.Errorf("Check(%s) = %v", bad, err)
		}
		if _, err := m.Handle(ctx, Caller{}, json.RawMessage(bad)); err == nil {
			t.Errorf("Handle(%s) ran", bad)
		}
	}
	if len(ran) != 0 {
		t.Fatalf("ran %v", ran)
	}
	for p, want := range map[string][]string{
		`{"package":"vim","hold":true}`:  {"hold", "vim"},
		`{"package":"libc6:amd64"}`:      {"unhold", "libc6:amd64"},
		`{"package":"g++","hold":false}`: {"unhold", "g++"},
	} {
		ran = nil
		if err := m.Check(ctx, json.RawMessage(p)); err != nil {
			t.Fatalf("Check(%s): %v", p, err)
		}
		if _, err := m.Handle(ctx, Caller{}, json.RawMessage(p)); err != nil || len(ran) != 1 || !slices.Equal(ran[0], want) {
			t.Errorf("%s: %v %v", p, err, ran)
		}
	}
	result, runErr = safeexec.Result{ExitCode: 100, Stderr: []byte("E: Unable to locate package nope\n")}, errors.New("exit 100")
	if _, err := m.Handle(ctx, Caller{}, json.RawMessage(`{"package":"nope","hold":true}`)); !errors.Is(err, &Error{Code: CodeNotFound}) {
		t.Errorf("not installed: %v", err)
	}
	result = safeexec.Result{ExitCode: 100, Stderr: []byte("E: Could not get lock /var/lib/dpkg/lock-frontend")}
	if _, err := m.Handle(ctx, Caller{}, json.RawMessage(`{"package":"vim","hold":true}`)); !errors.Is(err, &Error{Code: CodeFailed}) || !strings.Contains(err.Error(), "lock") {
		t.Errorf("failure: %v", err)
	}
}
