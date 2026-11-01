// Package hardware lists what this machine is made of (ROADMAP 6.7), read
// from /proc and sysfs as the user, and reports firmware through fwupd's
// D-Bus API. Serial numbers, UUIDs and asset tags are never read.
package hardware

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
)

const (
	maxText    = 200
	maxDevices = 256
	maxIDsFile = 8 << 20
	fwupdDest  = "org.freedesktop.fwupd"
	fwupdPath  = dbus.ObjectPath("/")
	fwupdIface = "org.freedesktop.fwupd"
)

// Device is one PCI or USB device. Names come from firmware and id
// databases: plain text, escaped by the UI.
type Device struct {
	ID     string `json:"id"`
	Vendor string `json:"vendor,omitempty"`
	Name   string `json:"name,omitempty"`
	Class  string `json:"class,omitempty"`
}

// Disk is one whole disk.
type Disk struct {
	Name  string `json:"name"`
	Model string `json:"model,omitempty"`
	Bytes int64  `json:"bytes"`
}

// Inventory is GET /api/v1/hardware.
type Inventory struct {
	CPU      string   `json:"cpu,omitempty"`
	Threads  int      `json:"threads"`
	MemBytes int64    `json:"mem_bytes"`
	Vendor   string   `json:"vendor,omitempty"`
	Product  string   `json:"product,omitempty"`
	Board    string   `json:"board,omitempty"`
	BIOS     string   `json:"bios,omitempty"`
	BIOSDate string   `json:"bios_date,omitempty"`
	PCI      []Device `json:"pci"`
	USB      []Device `json:"usb"`
	Disks    []Disk   `json:"disks"`
}

// Paths are the roots read; zero values take the real ones.
type Paths struct {
	Proc, Sys, PCIIDs, USBIDs string
}

func (p Paths) withDefaults() Paths {
	if p.Proc == "" {
		p.Proc = "/proc"
	}
	if p.Sys == "" {
		p.Sys = "/sys"
	}
	if p.PCIIDs == "" {
		p.PCIIDs = "/usr/share/misc/pci.ids"
	}
	if p.USBIDs == "" {
		p.USBIDs = "/usr/share/misc/usb.ids"
	}
	return p
}

func clean(s string) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "�")))
	if len(s) > maxText {
		s = strings.ToValidUTF8(s[:maxText], "")
	}
	return s
}

// readText reads a short sysfs attribute.
func readText(parts ...string) string {
	b, err := os.ReadFile(filepath.Join(parts...)) // #nosec G304 -- fixed names under /proc and /sys
	if err != nil {
		return ""
	}
	if len(b) > 4096 {
		b = b[:4096]
	}
	return clean(string(b))
}

// placeholder drops what vendors leave in unset DMI fields.
func placeholder(s string) string {
	switch strings.ToLower(s) {
	case "", "default string", "to be filled by o.e.m.", "not specified", "system product name", "none", "unknown":
		return ""
	}
	return s
}

// idsName is one lookup table: "vvvv" and "vvvv:dddd" to names.
type idsName map[string]string

var (
	idsVendor = regexp.MustCompile(`^([0-9a-f]{4})  (.+)$`)
	idsDevice = regexp.MustCompile(`^\t([0-9a-f]{4})  (.+)$`)
)

// parseIDs reads pci.ids or usb.ids, keeping only the wanted IDs. Later
// sections of usb.ids (classes, languages) start with a word and a space,
// which ends the vendor list.
func parseIDs(path string, want map[string]bool) idsName {
	out := idsName{}
	f, err := os.Open(path) // #nosec G304 -- a fixed system path
	if err != nil {
		return out
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(&limited{r: f, left: maxIDsFile})
	vendor := ""
	for sc.Scan() {
		line := sc.Text()
		if m := idsVendor.FindStringSubmatch(line); m != nil {
			vendor = m[1]
			if want[vendor] {
				out[vendor] = clean(m[2])
			}
			continue
		}
		if m := idsDevice.FindStringSubmatch(line); m != nil && vendor != "" {
			if k := vendor + ":" + m[1]; want[k] {
				out[k] = clean(m[2])
			}
			continue
		}
		if line != "" && line[0] != '#' && line[0] != '\t' {
			vendor = ""
		}
	}
	return out
}

type limited struct {
	r    *os.File
	left int
}

func (l *limited) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, os.ErrClosed
	}
	if len(p) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= n
	return n, err
}

// hexID normalises "0x8086\n" to "8086".
func hexID(s string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "0x"))
}

var pciClasses = map[string]string{"01": "storage", "02": "network", "03": "display", "04": "multimedia", "06": "bridge", "0c": "serial bus", "0d": "wireless"}

func dirNames(path string) []string {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
		if len(out) == maxDevices {
			break
		}
	}
	sort.Strings(out)
	return out
}

func readPCI(p Paths) []Device {
	root := filepath.Join(p.Sys, "bus/pci/devices")
	out := []Device{}
	want := map[string]bool{}
	for _, name := range dirNames(root) {
		v, d := hexID(readText(root, name, "vendor")), hexID(readText(root, name, "device"))
		class := hexID(readText(root, name, "class"))
		dev := Device{ID: v + ":" + d}
		if len(class) >= 2 {
			dev.Class = pciClasses[class[:2]]
		}
		want[v], want[dev.ID] = true, true
		out = append(out, dev)
	}
	ids := parseIDs(p.PCIIDs, want)
	for i := range out {
		v, _, _ := strings.Cut(out[i].ID, ":")
		out[i].Vendor, out[i].Name = ids[v], ids[out[i].ID]
	}
	return out
}

// usbDevices lists real devices, not interfaces or root hubs. Root hubs
// and other hubs have device class 09.
func usbDevices(p Paths) []Device {
	root := filepath.Join(p.Sys, "bus/usb/devices")
	out := []Device{}
	for _, name := range dirNames(root) {
		if strings.Contains(name, ":") || strings.HasPrefix(name, "usb") {
			continue
		}
		if readText(root, name, "bDeviceClass") == "09" {
			continue
		}
		v, d := hexID(readText(root, name, "idVendor")), hexID(readText(root, name, "idProduct"))
		if v == "" || d == "" {
			continue
		}
		out = append(out, Device{ID: v + ":" + d, Vendor: readText(root, name, "manufacturer"), Name: readText(root, name, "product")})
	}
	return out
}

func readUSB(p Paths) []Device {
	out := usbDevices(p)
	want := map[string]bool{}
	for _, d := range out {
		v, _, _ := strings.Cut(d.ID, ":")
		want[v], want[d.ID] = true, true
	}
	ids := parseIDs(p.USBIDs, want)
	for i := range out {
		v, _, _ := strings.Cut(out[i].ID, ":")
		if out[i].Vendor == "" {
			out[i].Vendor = ids[v]
		}
		if out[i].Name == "" {
			out[i].Name = ids[out[i].ID]
		}
	}
	return out
}

var wholeDisk = regexp.MustCompile(`^(sd[a-z]+|nvme[0-9]+n[0-9]+|vd[a-z]+|mmcblk[0-9]+)$`)

func readDisks(p Paths) []Disk {
	root := filepath.Join(p.Sys, "block")
	out := []Disk{}
	for _, name := range dirNames(root) {
		if !wholeDisk.MatchString(name) {
			continue
		}
		sectors, _ := strconv.ParseInt(readText(root, name, "size"), 10, 64)
		out = append(out, Disk{Name: name, Model: readText(root, name, "device/model"), Bytes: sectors * 512})
	}
	return out
}

// parseCPU reads the model and thread count from /proc/cpuinfo.
func parseCPU(b string) (model string, threads int) {
	for line := range strings.SplitSeq(b, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "processor":
			threads++
		case "model name":
			if model == "" {
				model = clean(v)
			}
		}
	}
	return model, threads
}

func parseMem(b string) int64 {
	for line := range strings.SplitSeq(b, "\n") {
		if v, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			kb, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			return kb * 1024
		}
	}
	return 0
}

func procFile(p Paths, name string) string {
	b, err := os.ReadFile(filepath.Join(p.Proc, name)) // #nosec G304 -- fixed names under /proc
	if err != nil {
		return ""
	}
	return string(b)
}

// Read builds the inventory.
func Read(p Paths) Inventory {
	p = p.withDefaults()
	inv := Inventory{PCI: readPCI(p), USB: readUSB(p), Disks: readDisks(p)}
	inv.CPU, inv.Threads = parseCPU(procFile(p, "cpuinfo"))
	inv.MemBytes = parseMem(procFile(p, "meminfo"))
	dmi := filepath.Join(p.Sys, "class/dmi/id")
	inv.Vendor = placeholder(readText(dmi, "sys_vendor"))
	inv.Product = placeholder(readText(dmi, "product_name"))
	inv.Board = placeholder(readText(dmi, "board_name"))
	inv.BIOS = placeholder(readText(dmi, "bios_version"))
	inv.BIOSDate = placeholder(readText(dmi, "bios_date"))
	return inv
}

// Firmware is one updatable device, as fwupd reports it.
type Firmware struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Vendor  string `json:"vendor,omitempty"`
	Version string `json:"version,omitempty"`
	Update  string `json:"update,omitempty"` // the newest version offered
}

// FirmwareReport is GET /api/v1/hardware/firmware.
type FirmwareReport struct {
	Available bool       `json:"available"` // fwupd answered
	Devices   []Firmware `json:"devices"`
}

const deviceFlagUpdatable = 1 << 1

func str(m map[string]dbus.Variant, k string) string {
	s, _ := m[k].Value().(string)
	return clean(s)
}

// parseFirmware keeps the devices that can be updated.
func parseFirmware(rows []map[string]dbus.Variant) []Firmware {
	out := []Firmware{}
	for _, m := range rows {
		flags, _ := m["Flags"].Value().(uint64)
		id := str(m, "DeviceId")
		if flags&deviceFlagUpdatable == 0 || id == "" || len(out) == maxDevices {
			continue
		}
		out = append(out, Firmware{ID: id, Name: str(m, "Name"), Vendor: str(m, "Vendor"), Version: str(m, "Version")})
	}
	return out
}

// newest is the version of GetUpgrades' first release.
func newest(rows []map[string]dbus.Variant) string {
	if len(rows) == 0 {
		return ""
	}
	return str(rows[0], "Version")
}

// Firmware asks fwupd, which D-Bus starts if it is installed. It uses the
// metadata fwupd already has: nothing here refreshes it.
func ReadFirmware(ctx context.Context, bus dbusx.Bus) FirmwareReport {
	r := FirmwareReport{Devices: []Firmware{}}
	var rows []map[string]dbus.Variant
	if err := bus.Call(ctx, fwupdDest, fwupdPath, fwupdIface+".GetDevices", []any{&rows}); err != nil {
		return r
	}
	r.Available = true
	r.Devices = parseFirmware(rows)
	for i := range r.Devices {
		var up []map[string]dbus.Variant
		// No upgrade is reported as an error: it is not one here.
		if bus.Call(ctx, fwupdDest, fwupdPath, fwupdIface+".GetUpgrades", []any{&up}, r.Devices[i].ID) == nil {
			r.Devices[i].Update = newest(up)
		}
	}
	return r
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// HandleInventory serves GET /api/v1/hardware.
func HandleInventory(p Paths) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) { reply(w, Read(p)) }
}

// HandleFirmware serves GET /api/v1/hardware/firmware: only when the page
// asks, so fwupd is not started on its own.
func HandleFirmware(withBus func(ctx context.Context, f func(dbusx.Bus))) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out := FirmwareReport{Devices: []Firmware{}}
		withBus(r.Context(), func(bus dbusx.Bus) { out = ReadFirmware(r.Context(), bus) })
		reply(w, out)
	}
}
