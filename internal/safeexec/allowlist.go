package safeexec

import (
	"fmt"
	"time"
)

// allowlist is the complete set of programs Hearth may run. Adding an entry
// needs a validator that accepts only the argument shapes the caller uses.
var allowlist = map[string]Spec{
	// xdg-open <http(s) URL>: open the UI (or a local service) in the browser.
	"xdg-open": {
		Paths:    []string{"/usr/bin/xdg-open"},
		Validate: exactly(HTTPURL),
		Env:      []string{"DISPLAY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "XDG_CURRENT_DESKTOP", "HOME"},
	},
	// systemd-run --user --collect --quiet -- /usr/bin/xdg-open <URL>: open a
	// URL from inside hearthd.service without the browser inheriting the
	// service sandbox; the user manager starts it in a transient unit.
	"systemd-run-xdg-open": {
		Paths: []string{"/usr/bin/systemd-run"},
		Validate: exactly(
			Literal("--user"), Literal("--collect"), Literal("--quiet"), Literal("--"),
			Literal("/usr/bin/xdg-open"), HTTPURL,
		),
		Env: []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "HOME"},
	},
	// nvidia-smi with one fixed query (collect/gpu.go): NVIDIA exposes no
	// usable sysfs metrics, and NVML would need cgo.
	"nvidia-smi": {
		Paths: []string{"/usr/bin/nvidia-smi"},
		Validate: exactly(
			Literal("--query-gpu=pci.bus_id,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw"),
			Literal("--format=csv,noheader,nounits"),
		),
		Timeout: 2 * time.Second,
	},
}

// exactly returns a Spec.Validate that requires one argument per validator,
// in order.
func exactly(vs ...Validator) func([]string) error {
	return func(args []string) error {
		if len(args) != len(vs) {
			return invalid("want %d arguments, got %d", len(vs), len(args))
		}
		for i, v := range vs {
			if err := v(args[i]); err != nil {
				return fmt.Errorf("argument %d: %w", i, err)
			}
		}
		return nil
	}
}
