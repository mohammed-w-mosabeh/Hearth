package alerts

import (
	"context"
	"strings"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/collect"
	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
)

// FromSample returns the catalog metrics in a metrics sample, by instance.
// Every metric the sample can speak for is present, even with no
// instances (no battery discharging resolves "battery low"); CPU usage is
// left out of a partial sample, which has no CPU figures yet.
func FromSample(s collect.Sample) map[string]map[string]float64 {
	out := map[string]map[string]float64{
		"mem.used_pct":        {},
		"swap.used_pct":       {},
		"disk.used_pct":       {},
		"cpu.temp":            {},
		"gpu.temp":            {},
		"battery.discharging": {},
	}
	if !s.Partial {
		out["cpu.usage"] = map[string]float64{"": s.CPU.Usage}
	}
	if s.Mem.Total > 0 {
		out["mem.used_pct"][""] = float64(s.Mem.Used) / float64(s.Mem.Total) * 100
	}
	if s.Mem.SwapTotal > 0 {
		out["swap.used_pct"][""] = float64(s.Mem.SwapUsed) / float64(s.Mem.SwapTotal) * 100
	}
	for _, d := range s.Disks {
		out["disk.used_pct"][d.Mount] = d.Pct
	}
	if s.CPUTempC != nil {
		out["cpu.temp"][""] = *s.CPUTempC
	}
	for _, g := range s.GPUs {
		if g.TempC != nil {
			out["gpu.temp"][g.PCI] = *g.TempC // the PCI address is stable across boots
		}
	}
	for _, b := range s.Power.Batteries {
		if b.State == "discharging" {
			out["battery.discharging"][b.ID] = b.Pct
		}
	}
	return out
}

// DesktopNotifier shows notifications through org.freedesktop.Notifications
// on the session bus.
type DesktopNotifier struct {
	Dial func(ctx context.Context) (*dbusx.Conn, error)
}

// markup escapes text for the notification body: servers may interpret a
// subset of HTML there, and system-derived strings (unit names, mounts)
// must show as text (SECURITY S6).
var markup = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// Notify sends one notification. Critical alerts use critical urgency,
// which desktops keep on screen until dismissed.
func (n DesktopNotifier) Notify(ctx context.Context, summary, body, severity string) error {
	conn, err := n.Dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	urgency := byte(1)
	icon := "dialog-warning"
	switch severity {
	case "critical":
		urgency, icon = 2, "dialog-error"
	case "info":
		urgency, icon = 0, "dialog-information"
	}
	hints := map[string]dbus.Variant{"urgency": dbus.MakeVariant(urgency), "desktop-entry": dbus.MakeVariant("hearth")}
	var id uint32
	return conn.Call(ctx, "org.freedesktop.Notifications", "/org/freedesktop/Notifications",
		"org.freedesktop.Notifications.Notify", []any{&id},
		"Hearth", uint32(0), icon, summary, markup.Replace(body), []string{}, hints, int32(-1))
}
