package dbusx

import (
	"context"
	"errors"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/unattended"
)

// An interactive call, unattended, is refused before it reaches the bus
// (S15d): nothing could show a password dialog.
func TestUnattendedRefusesInteractive(t *testing.T) {
	var c Conn // never dialled: the guard answers first
	err := c.CallInteractive(unattended.With(context.Background()), "org.freedesktop.systemd1", "/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager.StopUnit", nil, "cups.service", "replace")
	if !errors.Is(err, unattended.ErrWouldPrompt) {
		t.Fatalf("%v", err)
	}
}
