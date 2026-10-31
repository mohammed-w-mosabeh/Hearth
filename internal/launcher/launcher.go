// Package launcher opens Hearth's login link in the user's browser
// (SECURITY S2). The link holds the launch token, so it goes over the
// session bus to the desktop portal, which starts the browser in its own
// app scope. A transient unit is never used: the token would show in its
// description and argv, and stopping the unit would kill a browser it
// started. No error or notification here ever carries the link.
package launcher

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	portalDest  = "org.freedesktop.portal.Desktop"
	portalPath  = dbus.ObjectPath("/org/freedesktop/portal/desktop")
	portalIface = "org.freedesktop.portal.OpenURI"
	requestIfc  = "org.freedesktop.portal.Request"

	// Portal responses.
	responseOK        = uint32(0)
	responseCancelled = uint32(1)

	portalCall = 10 * time.Second // the portal may be started by this call
	// responseWait bounds waiting for the outcome. A chooser left open is
	// not a failure: the fallbacks would then open a second browser.
	responseWait = 5 * time.Second
	notifyWait   = 3 * time.Second
)

// Bus is a session bus connection (dbusx.Conn in production).
type Bus interface {
	Call(ctx context.Context, dest string, path dbus.ObjectPath, method string, out []any, args ...any) error
	Watch(ctx context.Context, opts ...dbus.MatchOption) (<-chan *dbus.Signal, error)
	Close() error
}

// Config says how to reach the desktop.
type Config struct {
	Session func(ctx context.Context) (Bus, error)
	// Start starts an allowlisted program detached (safeexec.Start).
	Start func(name string, args ...string) error
	// PortalOnly is for hearthd.service: a program started from there
	// would inherit its sandbox.
	PortalOnly bool
}

// Open opens link. If nothing can, it tells the user through a desktop
// notification with the plain address, and returns the reason.
func Open(ctx context.Context, cfg Config, link string) error {
	err := open(ctx, cfg, link)
	if err == nil {
		return nil
	}
	err = errors.New(scrub(err.Error(), link))
	Notify(ctx, cfg, "Hearth could not open your browser", err.Error()+"\nOpen "+plain(link))
	return err
}

func open(ctx context.Context, cfg Config, link string) error {
	perr := portal(ctx, cfg, link)
	if perr == nil || cfg.PortalOnly {
		return perr
	}
	gerr := cfg.Start("gio-open", "open", link) // GIO's default handler
	if gerr == nil {
		return nil
	}
	xerr := cfg.Start("xdg-open", link)
	if xerr == nil {
		return nil
	}
	return fmt.Errorf("portal: %w; gio: %w; xdg-open: %w", perr, gerr, xerr)
}

func portal(ctx context.Context, cfg Config, link string) error {
	ctx, cancel := context.WithTimeout(ctx, portalCall+responseWait)
	defer cancel()
	bus, err := cfg.Session(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = bus.Close() }()
	// Watching first: the response can arrive before the call returns.
	sigs, err := bus.Watch(ctx, dbus.WithMatchInterface(requestIfc), dbus.WithMatchMember("Response"))
	if err != nil {
		return err
	}
	cctx, ccancel := context.WithTimeout(ctx, portalCall)
	defer ccancel()
	var handle dbus.ObjectPath
	if err := bus.Call(cctx, portalDest, portalPath, portalIface+".OpenURI", []any{&handle},
		"", link, map[string]dbus.Variant{}); err != nil {
		return err
	}
	wait := time.After(responseWait)
	for {
		select {
		case s, ok := <-sigs:
			if !ok {
				return nil
			}
			if s.Path != handle || len(s.Body) == 0 {
				continue
			}
			code, _ := s.Body[0].(uint32)
			if code == responseOK || code == responseCancelled {
				return nil
			}
			return fmt.Errorf("the portal answered %d", code)
		case <-wait:
			return nil
		}
	}
}

// Notify shows a desktop notification, best effort.
func Notify(ctx context.Context, cfg Config, summary, body string) {
	ctx, cancel := context.WithTimeout(ctx, notifyWait)
	defer cancel()
	bus, err := cfg.Session(ctx)
	if err != nil {
		return
	}
	defer func() { _ = bus.Close() }()
	var id uint32
	_ = bus.Call(ctx, "org.freedesktop.Notifications", "/org/freedesktop/Notifications",
		"org.freedesktop.Notifications.Notify", []any{&id},
		"Hearth", uint32(0), "dialog-warning", summary, body, []string{},
		map[string]dbus.Variant{"desktop-entry": dbus.MakeVariant("hearth")}, int32(-1))
}

// plain is the link without its token.
func plain(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return "Hearth from its icon"
	}
	return u.Scheme + "://" + u.Host + "/"
}

// scrub removes the link and its query from text.
func scrub(text, link string) string {
	text = strings.ReplaceAll(text, link, plain(link))
	if _, q, ok := strings.Cut(link, "?"); ok && q != "" {
		text = strings.ReplaceAll(text, q, "…")
	}
	return text
}
