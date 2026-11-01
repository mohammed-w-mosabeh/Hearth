package hardware

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
)

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fakeRoot(t *testing.T) Paths {
	t.Helper()
	r := t.TempDir()
	p := Paths{Proc: r + "/proc", Sys: r + "/sys", PCIIDs: r + "/pci.ids", USBIDs: r + "/usb.ids"}
	write(t, p.Proc+"/cpuinfo", "processor\t: 0\nmodel name\t: Fast CPU\nprocessor\t: 1\nmodel name\t: Fast CPU\n")
	write(t, p.Proc+"/meminfo", "MemTotal:       16000 kB\nMemFree: 1 kB\n")
	dmi := p.Sys + "/class/dmi/id/"
	for k, v := range map[string]string{"sys_vendor": "Example Computers\n", "product_name": "Default string\n", "board_name": "EX-1000\n", "bios_version": "1.02\n", "product_serial": "SERIALSECRET\n", "board_serial": "SERIALSECRET\n", "product_uuid": "SERIALSECRET\n"} {
		write(t, dmi+k, v)
	}
	pci := p.Sys + "/bus/pci/devices/0000:00:02.0/"
	write(t, pci+"vendor", "0x8086\n")
	write(t, pci+"device", "0x0a16\n")
	write(t, pci+"class", "0x030000\n")
	write(t, p.PCIIDs, "# comment\n8086  Intel Corporation\n\t0a16  HD Graphics\n\t\t1028 0000  subsystem\n10de  NVIDIA\n\t0a16  Not this one\nC 00  Class\n\t01  Sub\n")
	usb := p.Sys + "/bus/usb/devices/"
	write(t, usb+"1-1/idVendor", "046d\n")
	write(t, usb+"1-1/idProduct", "c52b\n")
	write(t, usb+"1-1/bDeviceClass", "00\n")
	write(t, usb+"1-1/product", "<b>Receiver</b>\n")
	write(t, usb+"1-1/serial", "SERIALSECRET\n")
	write(t, usb+"1-2/idVendor", "1d6b\n")
	write(t, usb+"1-2/idProduct", "0002\n")
	write(t, usb+"1-2/bDeviceClass", "00\n")
	write(t, usb+"1-3/idVendor", "1d6b\n")
	write(t, usb+"1-3/idProduct", "0003\n")
	write(t, usb+"1-3/bDeviceClass", "09\n")
	write(t, usb+"1-1:1.0/idVendor", "ffff\n")
	write(t, usb+"usb1/idVendor", "ffff\n")
	write(t, p.USBIDs, "046d  Logitech, Inc.\n\tc52b  Unifying\n1d6b  Linux Foundation\n\t0002  2.0 root hub\n")
	write(t, p.Sys+"/block/sda/size", "2000\n")
	write(t, p.Sys+"/block/sda/device/model", "Disk <x>\n")
	write(t, p.Sys+"/block/sda1/size", "1\n")
	write(t, p.Sys+"/block/loop0/size", "1\n")
	return p
}

func TestRead(t *testing.T) {
	inv := Read(fakeRoot(t))
	if inv.CPU != "Fast CPU" || inv.Threads != 2 || inv.MemBytes != 16000*1024 {
		t.Fatalf("%+v", inv)
	}
	if inv.Vendor != "Example Computers" || inv.Product != "" || inv.Board != "EX-1000" || inv.BIOS != "1.02" {
		t.Fatalf("%+v", inv)
	}
	if len(inv.PCI) != 1 || inv.PCI[0] != (Device{ID: "8086:0a16", Vendor: "Intel Corporation", Name: "HD Graphics", Class: "display"}) {
		t.Fatalf("%+v", inv.PCI)
	}
	if len(inv.USB) != 2 || inv.USB[0].Name != "<b>Receiver</b>" || inv.USB[0].Vendor != "Logitech, Inc." || inv.USB[1].Name != "2.0 root hub" {
		t.Fatalf("%+v", inv.USB)
	}
	if len(inv.Disks) != 1 || inv.Disks[0].Name != "sda" || inv.Disks[0].Bytes != 2000*512 {
		t.Fatalf("%+v", inv.Disks)
	}
}

// S16f's rule, here too: identifiers stay out of everything Hearth shows.
func TestNoSerials(t *testing.T) {
	inv := Read(fakeRoot(t))
	all := strings.Join([]string{inv.Vendor, inv.Product, inv.Board, inv.BIOS, inv.BIOSDate}, " ")
	for _, d := range append(inv.PCI, inv.USB...) {
		all += d.ID + d.Name + d.Vendor
	}
	if strings.Contains(all, "SERIALSECRET") {
		t.Fatal(all)
	}
}

func TestParseCleans(t *testing.T) {
	if got := clean(strings.Repeat("é", 300)); len(got) > maxText || !utf8.ValidString(got) {
		t.Fatal(len(got))
	}
	if got := clean(" a\x00b\n"); got != "ab" {
		t.Fatalf("%q", got)
	}
}

func TestCheck(t *testing.T) {
	a, b := Device{ID: "1:1", Name: "Mouse"}, Device{ID: "2:2"}
	news, all, changed := check([]Device{a, b}, nil, true)
	if len(news) != 0 || len(all) != 2 || !changed {
		t.Fatalf("the first reading is the baseline: %v %v", news, all)
	}
	news, all2, changed := check([]Device{a, b}, all, false)
	if len(news) != 0 || changed {
		t.Fatalf("%v", news)
	}
	c := Device{ID: "3:3", Vendor: "<i>Evil</i>"}
	news, all3, changed := check([]Device{a, b, c}, all2, false)
	if !changed || len(all3) != 3 || news["<i>Evil</i> (3:3)"] != 1 {
		t.Fatalf("%v", news)
	}
	if news, _, _ := check([]Device{a, c}, all3, false); len(news) != 0 {
		t.Fatalf("a known device came back: %v", news)
	}
}

type fakeBus struct {
	dbusx.Bus
	devices []map[string]dbus.Variant
	err     error
}

func (f *fakeBus) Call(_ context.Context, _ string, _ dbus.ObjectPath, method string, out []any, args ...any) error {
	if f.err != nil {
		return f.err
	}
	rows := out[0].(*[]map[string]dbus.Variant)
	switch {
	case strings.HasSuffix(method, "GetDevices"):
		*rows = f.devices
	case strings.HasSuffix(method, "GetUpgrades"):
		if args[0] != "a" {
			return errors.New("nothing to do")
		}
		*rows = []map[string]dbus.Variant{{"Version": dbus.MakeVariant("2.0")}}
	}
	return nil
}

func TestFirmware(t *testing.T) {
	dev := func(id string, flags uint64) map[string]dbus.Variant {
		return map[string]dbus.Variant{"DeviceId": dbus.MakeVariant(id), "Name": dbus.MakeVariant("<b>UEFI</b>"), "Version": dbus.MakeVariant("1.0"), "Flags": dbus.MakeVariant(flags)}
	}
	got := ReadFirmware(context.Background(), &fakeBus{devices: []map[string]dbus.Variant{dev("a", 2), dev("b", 3), dev("c", 1)}})
	if !got.Available || len(got.Devices) != 2 || got.Devices[0].Update != "2.0" || got.Devices[1].Update != "" {
		t.Fatalf("%+v", got)
	}
	if got := ReadFirmware(context.Background(), &fakeBus{err: errors.New("no fwupd")}); got.Available || got.Devices == nil {
		t.Fatalf("%+v", got)
	}
}

func FuzzParseCPU(f *testing.F) {
	f.Add("processor : 0\nmodel name : x\n")
	f.Fuzz(func(t *testing.T, s string) {
		if m, n := parseCPU(s); len(m) > maxText || n < 0 {
			t.Fatalf("%q", m)
		}
	})
}

func FuzzHexID(f *testing.F) {
	f.Add("0x8086\n")
	f.Fuzz(func(t *testing.T, s string) {
		if got := clean(hexID(s)); !utf8.ValidString(got) || len(got) > maxText {
			t.Fatalf("%q", got)
		}
	})
}

func FuzzParseIDs(f *testing.F) {
	f.Add("8086  Intel\n\t0a16  HD\n")
	f.Fuzz(func(t *testing.T, s string) {
		p := filepath.Join(t.TempDir(), "ids")
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		for k, v := range parseIDs(p, map[string]bool{"8086": true, "8086:0a16": true}) {
			if len(v) > maxText || !utf8.ValidString(k+v) {
				t.Fatalf("%q %q", k, v)
			}
		}
	})
}
