package dbusx

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/godbus/dbus/v5"

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

// The interactive flag is on the wire: byte 2 of a D-Bus message is its
// flags, 0x4 is ALLOW_INTERACTIVE_AUTHORIZATION. Without it systemd asks
// polkit with no interaction and the user is never asked.
func TestInteractiveCallCarriesTheFlag(t *testing.T) {
	msg := interactiveCall("org.freedesktop.systemd1", "/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager.StopUnit", "cups.service", "replace")
	if err := msg.IsValid(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := msg.EncodeTo(&buf, binary.LittleEndian); err != nil {
		t.Fatal(err)
	}
	if b := buf.Bytes(); b[1] != 1 || b[2] != 0x4 {
		t.Fatalf("type %d flags %#x", b[1], b[2])
	}
	if got := msg.Headers[dbus.FieldInterface].Value(); got != "org.freedesktop.systemd1.Manager" {
		t.Fatalf("interface %v", got)
	}
	if got := msg.Headers[dbus.FieldMember].Value(); got != "StopUnit" {
		t.Fatalf("member %v", got)
	}
	if got := msg.Headers[dbus.FieldSignature].Value().(dbus.Signature).String(); got != "ss" {
		t.Fatalf("signature %q", got)
	}
}
