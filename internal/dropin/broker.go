package dropin

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
)

// ActionOverride is the polkit action for system units' drop-ins:
// auth_admin with no keep, since each one changes how a root service runs.
const ActionOverride = "org.hearth.units.override"

// SystemDir is where system units' drop-ins live.
const SystemDir = "/etc/systemd/system"

// Params are unit.override's params.
type Params struct {
	Unit       string     `json:"unit"`
	Directives Directives `json:"directives"`
}

// Reloader is the system manager (dbusx.Conn in production).
type Reloader interface {
	Call(ctx context.Context, dest string, path dbus.ObjectPath, method string, out []any, args ...any) error
	Close() error
}

// BrokerMethods is hearth-broker's unit.override.
func BrokerMethods(dir string, bus func(context.Context) (Reloader, error)) []broker.Method {
	parse := func(p json.RawMessage) (Params, error) {
		var v Params
		dec := json.NewDecoder(bytes.NewReader(p))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&v); err != nil {
			return v, broker.Errorf(broker.CodeBadParams, "bad params")
		}
		if err := ValidUnit(v.Unit); err != nil {
			return v, broker.Errorf(broker.CodeBadParams, "%v", err)
		}
		if err := Validate(v.Directives); err != nil {
			return v, broker.Errorf(broker.CodeBadParams, "%v", err)
		}
		return v, nil
	}
	return []broker.Method{{
		Name: "unit.override", Privileged: true, Action: ActionOverride, Timeout: 2 * time.Minute,
		Check: func(_ context.Context, p json.RawMessage) error { _, err := parse(p); return err },
		Handle: func(ctx context.Context, _ broker.Caller, p json.RawMessage) (any, error) {
			v, err := parse(p)
			if err != nil {
				return nil, err
			}
			if err := Write(dir, v.Unit, v.Directives); err != nil {
				return nil, broker.Errorf(broker.CodeFailed, "%s", err.Error())
			}
			conn, err := bus(ctx)
			if err != nil {
				return nil, broker.Errorf(broker.CodeFailed, "%s", err.Error())
			}
			defer func() { _ = conn.Close() }()
			if err := conn.Call(ctx, "org.freedesktop.systemd1", "/org/freedesktop/systemd1",
				"org.freedesktop.systemd1.Manager.Reload", nil); err != nil {
				return nil, broker.Errorf(broker.CodeFailed, "%s", err.Error())
			}
			return map[string]bool{"done": true}, nil
		},
	}}
}
