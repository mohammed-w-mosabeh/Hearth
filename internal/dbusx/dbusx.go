// Package dbusx is a thin layer over godbus with the timeouts Hearth uses
// for every D-Bus call (ARCHITECTURE: 5 s) and a small interface modules can
// fake in tests.
package dbusx

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"
)

// CallTimeout bounds every D-Bus method call.
const CallTimeout = 5 * time.Second

// Bus is what Hearth modules need from a D-Bus connection.
type Bus interface {
	// GetAll returns all properties of iface on dest/path.
	GetAll(ctx context.Context, dest string, path dbus.ObjectPath, iface string) (map[string]dbus.Variant, error)
	// Call invokes method and stores its results in out.
	Call(ctx context.Context, dest string, path dbus.ObjectPath, method string, out []any, args ...any) error
	// Set sets a property.
	Set(ctx context.Context, dest string, path dbus.ObjectPath, iface, prop string, value any) error
	// Watch delivers signals matching opts until ctx is cancelled. The
	// channel is closed when ctx ends or the connection drops.
	Watch(ctx context.Context, opts ...dbus.MatchOption) (<-chan *dbus.Signal, error)
}

// Conn implements Bus over a godbus connection.
type Conn struct {
	c *dbus.Conn
}

// SystemBus opens a private connection to the system bus.
func SystemBus(ctx context.Context) (*Conn, error) {
	c, err := dbus.SystemBusPrivate(dbus.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("dbusx: system bus: %w", err)
	}
	// Auth and Hello are bounded by the connection context above.
	if err := c.Auth(nil); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("dbusx: auth: %w", err)
	}
	if err := c.Hello(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("dbusx: hello: %w", err)
	}
	return &Conn{c: c}, nil
}

// Close closes the connection.
func (c *Conn) Close() error { return c.c.Close() }

func (c *Conn) GetAll(ctx context.Context, dest string, path dbus.ObjectPath, iface string) (map[string]dbus.Variant, error) {
	var props map[string]dbus.Variant
	err := c.Call(ctx, dest, path, "org.freedesktop.DBus.Properties.GetAll", []any{&props}, iface)
	return props, err
}

func (c *Conn) Call(ctx context.Context, dest string, path dbus.ObjectPath, method string, out []any, args ...any) error {
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	call := c.c.Object(dest, path).CallWithContext(ctx, method, 0, args...)
	if call.Err != nil {
		return fmt.Errorf("dbusx: %s %s: %w", dest, method, call.Err)
	}
	if len(out) > 0 {
		if err := call.Store(out...); err != nil {
			return fmt.Errorf("dbusx: %s %s: %w", dest, method, err)
		}
	}
	return nil
}

func (c *Conn) Set(ctx context.Context, dest string, path dbus.ObjectPath, iface, prop string, value any) error {
	return c.Call(ctx, dest, path, "org.freedesktop.DBus.Properties.Set", nil, iface, prop, dbus.MakeVariant(value))
}

func (c *Conn) Watch(ctx context.Context, opts ...dbus.MatchOption) (<-chan *dbus.Signal, error) {
	actx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	if err := c.c.AddMatchSignalContext(actx, opts...); err != nil {
		return nil, fmt.Errorf("dbusx: add match: %w", err)
	}
	in := make(chan *dbus.Signal, 32)
	c.c.Signal(in)
	out := make(chan *dbus.Signal, 32)
	go func() {
		defer close(out)
		defer func() {
			c.c.RemoveSignal(in)
			rctx, rcancel := context.WithTimeout(context.Background(), time.Second)
			defer rcancel()
			_ = c.c.RemoveMatchSignalContext(rctx, opts...)
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case s, ok := <-in:
				if !ok {
					return
				}
				select {
				case out <- s:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// ErrorName returns the D-Bus error name of err (e.g.
// "org.freedesktop.DBus.Error.AccessDenied"), or "".
func ErrorName(err error) string {
	var de dbus.Error
	if errors.As(err, &de) {
		return de.Name
	}
	var dp *dbus.Error
	if errors.As(err, &dp) && dp != nil {
		return dp.Name
	}
	return ""
}
