package broker

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// ActionPkgHold is the polkit action for holding and unholding packages.
const ActionPkgHold = "org.hearth.packages.hold"

// HoldParams are pkg.hold's params.
type HoldParams struct {
	Package string `json:"package"` // name or name:arch
	Hold    bool   `json:"hold"`    // false unholds
}

// Runner runs an allowlisted command (safeexec.Run in production).
type Runner func(ctx context.Context, name string, args ...string) (safeexec.Result, error)

// PackageMethods are the broker's package methods. PackageKit does
// install, remove and update (it asks polkit itself); it has no hold, so
// pkg.hold runs apt-mark, the one command it may, with a validated name.
func PackageMethods(run Runner) []Method {
	args := func(p json.RawMessage) ([]string, error) {
		var v HoldParams
		if err := decodeParams(p, &v); err != nil {
			return nil, err
		}
		op := "unhold"
		if v.Hold {
			op = "hold"
		}
		a := []string{op, v.Package}
		if err := safeexec.Check("apt-mark-hold", a...); err != nil {
			return nil, badParam("package must be a Debian package name, optionally with :arch")
		}
		return a, nil
	}
	return []Method{{
		Name: "pkg.hold", Privileged: true, Action: ActionPkgHold, Timeout: time.Minute,
		Check: func(_ context.Context, p json.RawMessage) error { _, err := args(p); return err },
		Handle: func(ctx context.Context, _ Caller, p json.RawMessage) (any, error) {
			a, err := args(p)
			if err != nil {
				return nil, err
			}
			res, err := run(ctx, "apt-mark-hold", a...)
			if err != nil || res.ExitCode != 0 {
				msg := strings.TrimSpace(string(res.Stderr))
				if msg == "" && err != nil {
					msg = err.Error()
				}
				if strings.Contains(msg, "Unable to locate") || strings.Contains(msg, "not installed") {
					return nil, Errorf(CodeNotFound, "%s is not installed", a[1])
				}
				return nil, Errorf(CodeFailed, "apt-mark: %s", clipMsg(msg))
			}
			return map[string]bool{"held": a[0] == "hold"}, nil
		},
	}}
}

func clipMsg(s string) string {
	if len(s) > 500 {
		s = s[:500]
	}
	return strings.ToValidUTF8(s, "")
}
