package collect

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const pciIDsFixture = `# pci.ids excerpt
8086  Intel Corporation
	5917  UHD Graphics 620
1002  Advanced Micro Devices, Inc. [AMD/ATI]
	67df  Ellesmere [Radeon RX 470/480/570/570X/580/580X/590]
		1043 04a8  Radeon RX 480
	67ff  Baffin
10de  NVIDIA Corporation
	1c03  GP106 [GeForce GTX 1060 6GB]
`

// sysFixture builds a fake /sys with hwmon chips, thermal zones and GPUs.
func sysFixture(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	w := func(rel, v string) { f.write(f.sys, rel, v+"\n") }

	w("class/hwmon/hwmon0/name", "coretemp")
	w("class/hwmon/hwmon0/temp1_input", "89000")
	w("class/hwmon/hwmon0/temp1_label", "Package id 0")
	w("class/hwmon/hwmon0/temp1_max", "100000")
	w("class/hwmon/hwmon0/temp1_crit", "105000")
	w("class/hwmon/hwmon0/temp2_input", "83000")
	w("class/hwmon/hwmon0/temp2_label", "Core 0")
	w("class/hwmon/hwmon1/name", "acpitz")
	w("class/hwmon/hwmon1/temp1_input", "27800")
	w("class/hwmon/hwmon2/name", "amdgpu") // GPU chip: read via gpu.go only
	w("class/hwmon/hwmon2/temp1_input", "64000")
	w("class/hwmon/hwmon3/name", "BAT1")
	w("class/hwmon/hwmon3/in0_input", "12000")
	w("class/hwmon/hwmon4/name", "nct6775")
	w("class/hwmon/hwmon4/fan1_input", "1200")
	w("class/hwmon/hwmon4/fan2_input", "0")
	w("class/hwmon/hwmon4/fan2_label", "Chassis")

	w("class/thermal/thermal_zone0/type", "acpitz") // duplicate of hwmon1
	w("class/thermal/thermal_zone0/temp", "27800")
	w("class/thermal/thermal_zone1/type", "x86_pkg_temp")
	w("class/thermal/thermal_zone1/temp", "97000")

	gpu := func(card, pci, vendor, device, driver string) string {
		dev := filepath.Join(f.sys, "devices/pci0000:00", pci)
		w(filepath.Join("devices/pci0000:00", pci, "vendor"), vendor)
		w(filepath.Join("devices/pci0000:00", pci, "device"), device)
		drv := filepath.Join(f.sys, "bus/pci/drivers", driver)
		if err := os.MkdirAll(drv, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(drv, filepath.Join(dev, "driver")); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(f.sys, "class/drm", card), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(dev, filepath.Join(f.sys, "class/drm", card, "device")); err != nil {
			t.Fatal(err)
		}
		return filepath.Join("devices/pci0000:00", pci)
	}
	gpu("card0", "0000:00:02.0", "0x8086", "0x5917", "i915")
	w("class/drm/card0/gt_act_freq_mhz", "300")
	w("class/drm/card0-HDMI-A-1/status", "disconnected") // connector, not a card

	amd := gpu("card1", "0000:01:00.0", "0x1002", "0x67df", "amdgpu")
	w(amd+"/power/runtime_status", "active")
	w(amd+"/gpu_busy_percent", "12")
	w(amd+"/mem_info_vram_used", "104857600")
	w(amd+"/mem_info_vram_total", "2147483648")
	w(amd+"/hwmon/hwmon9/temp1_input", "64000")
	// Garbage power reading seen on a real GCN chip with a 25 W cap: dropped.
	w(amd+"/hwmon/hwmon9/power1_input", "1040996824")
	w(amd+"/hwmon/hwmon9/power1_cap", "25000000")
	w(amd+"/hwmon/hwmon9/freq1_input", "300000000")

	sleepy := gpu("card2", "0000:03:00.0", "0x1002", "0x67ff", "amdgpu")
	w(sleepy+"/power/runtime_status", "suspended")
	// Opening a FIFO blocks until a writer appears: if the collector reads a
	// sleeping GPU's sensors, the test hangs instead of passing silently.
	if err := syscall.Mkfifo(filepath.Join(f.sys, sleepy, "gpu_busy_percent"), 0o600); err != nil {
		t.Fatal(err)
	}

	nv := gpu("card3", "0000:02:00.0", "0x10de", "0x1c03", "nvidia")
	w(nv+"/power/runtime_status", "active")

	ids := filepath.Join(t.TempDir(), "pci.ids")
	if err := os.WriteFile(ids, []byte(pciIDsFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	return f, ids
}

func TestSensorsAndGPUs(t *testing.T) {
	f, ids := sysFixture(t)
	smiCalls := 0
	s := New(Config{
		ProcRoot: f.proc, SysRoot: f.sys, Log: discard(), Statfs: fakeStatfs, PCIIDs: ids,
		RunNvidiaSMI: func(context.Context) ([]byte, error) {
			smiCalls++
			return []byte("00000000:02:00.0, NVIDIA GeForce GTX 1060 6GB, 37, 1024, 6144, 55, [N/A]\n"), nil
		},
	})

	done := make(chan Sample, 1)
	go func() {
		got, err := s.Collect()
		if err != nil {
			t.Error(err)
		}
		done <- got
	}()
	var got Sample
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Collect blocked: it read a suspended GPU's sensors")
	}

	temps := map[string]Temp{}
	for _, tp := range got.Temps {
		temps[tp.Chip+"/"+tp.Label] = tp
	}
	if len(temps) != 4 {
		t.Fatalf("temps %v", got.Temps)
	}
	if p := temps["coretemp/Package id 0"]; p.C != 89 || p.MaxC != 100 || p.CritC != 105 {
		t.Fatalf("package %+v", p)
	}
	if _, ok := temps["acpitz/temp1"]; !ok {
		t.Fatal("unlabelled hwmon temp missing")
	}
	if z := temps["thermal/x86_pkg_temp"]; z.C != 97 {
		t.Fatalf("thermal zone %+v", z)
	}
	if _, ok := temps["thermal/acpitz"]; ok {
		t.Fatal("thermal zone duplicating a hwmon chip included")
	}
	if _, ok := temps["amdgpu/temp1"]; ok {
		t.Fatal("GPU hwmon read as a plain sensor")
	}
	if got.CPUTempC == nil || *got.CPUTempC != 89 {
		t.Fatalf("cpu temp %v", got.CPUTempC)
	}
	if len(got.Fans) != 2 || got.Fans[0].Label != "Chassis" || got.Fans[0].RPM != 0 || got.Fans[1].Label != "fan1" || got.Fans[1].RPM != 1200 {
		t.Fatalf("fans %+v", got.Fans)
	}

	if len(got.GPUs) != 4 {
		t.Fatalf("gpus %+v", got.GPUs)
	}
	intel, amd, sleepy, nv := got.GPUs[0], got.GPUs[1], got.GPUs[2], got.GPUs[3]
	if intel.Vendor != "intel" || intel.Driver != "i915" || intel.Name != "Intel UHD Graphics 620" ||
		intel.FreqMHz == nil || *intel.FreqMHz != 300 || intel.UtilPct != nil {
		t.Fatalf("intel %+v", intel)
	}
	if amd.PCI != "0000:01:00.0" || amd.Name != "AMD Ellesmere [Radeon RX 470/480/570/570X/580/580X/590]" ||
		*amd.UtilPct != 12 || *amd.MemUsed != 104857600 || *amd.MemTotal != 2147483648 || *amd.TempC != 64 || amd.PowerW != nil ||
		amd.FreqMHz == nil || *amd.FreqMHz != 300 {
		t.Fatalf("amd %+v", amd)
	}
	if !sleepy.Sleeping || sleepy.UtilPct != nil || sleepy.TempC != nil || sleepy.Name != "AMD Baffin" {
		t.Fatalf("sleeping gpu %+v", sleepy)
	}
	if nv.Name != "NVIDIA GeForce GTX 1060 6GB" || *nv.UtilPct != 37 || *nv.MemUsed != 1024<<20 || *nv.MemTotal != 6144<<20 ||
		*nv.TempC != 55 || nv.PowerW != nil {
		t.Fatalf("nvidia %+v", nv)
	}

	// nvidia-smi is rate-limited.
	if _, err := s.Collect(); err != nil {
		t.Fatal(err)
	}
	if smiCalls != 1 {
		t.Fatalf("nvidia-smi called %d times within %v", smiCalls, nvidiaEvery)
	}

	pts := map[string]float64{}
	for _, p := range got.Points() {
		pts[p.Metric] = p.Value
	}
	if pts["cpu.temp"] != 89 || pts["gpu.util:0000:01:00.0"] != 12 || pts["fan:nct6775/fan1"] != 1200 || pts["temp:thermal/x86_pkg_temp"] != 97 {
		t.Fatalf("points %v", pts)
	}
	if _, ok := pts["gpu.util:0000:03:00.0"]; ok {
		t.Fatal("sleeping GPU recorded")
	}
}

func TestSlowValuesNeverBlock(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "power1_input") // blocks on open until a writer appears
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	ok := filepath.Join(dir, "power1_average")
	if err := os.WriteFile(ok, []byte("12500000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sv := newSlowValues(time.Hour)

	start := time.Now()
	for range 3 {
		if v := sv.get(fifo, 1e6, nil); v != nil {
			t.Fatal("value from a read that never finished")
		}
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("get blocked on a slow attribute")
	}
	sv.mu.Lock()
	inflight := sv.m[fifo].inflight
	sv.mu.Unlock()
	if !inflight {
		t.Fatal("no background read started")
	}

	// A normal attribute shows up after its background read completes.
	if v := sv.get(ok, 1e6, nil); v != nil {
		t.Fatal("first get should not wait for the read")
	}
	waitFor(t, func() bool { v := sv.get(ok, 1e6, nil); return v != nil && *v == 12.5 })

	// Unblock the FIFO reader so the goroutine exits.
	w, err := os.OpenFile(fifo, os.O_WRONLY, 0) // #nosec G304 -- FIFO under t.TempDir()
	if err == nil {
		_ = w.Close()
	}
}

func TestSlowValuesStrikeOut(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "power1_input")
	if err := os.WriteFile(bad, []byte("1040996824\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sv := newSlowValues(0) // refresh on every get
	never := func(float64) bool { return false }
	for range 20 {
		_ = sv.get(bad, 1e6, never)
		waitFor(t, func() bool { sv.mu.Lock(); defer sv.mu.Unlock(); return !sv.m[bad].inflight })
	}
	sv.mu.Lock()
	strikes := sv.m[bad].strikes
	sv.mu.Unlock()
	if strikes != maxStrikes {
		t.Fatalf("strikes %d, want reads to stop at %d", strikes, maxStrikes)
	}

	// A good value resets strikes.
	good := filepath.Join(dir, "power1_average")
	if err := os.WriteFile(good, []byte("5000000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { v := sv.get(good, 1e6, nil); return v != nil && *v == 5 })
}

func TestPlausiblePower(t *testing.T) {
	for _, c := range []struct {
		w, capW float64
		ok      bool
	}{
		{3.6, 25, true}, {50, 25, true}, {51, 25, false}, {1041, 25, false},
		{450, 0, true}, {2087, 0, false}, {-1, 0, false},
	} {
		if got := plausiblePower(c.w, c.capW); got != c.ok {
			t.Errorf("plausiblePower(%v, %v) = %v", c.w, c.capW, got)
		}
	}
}

func TestCPUTempFallbacks(t *testing.T) {
	cases := []struct {
		temps []Temp
		want  float64
		ok    bool
	}{
		{[]Temp{{Chip: "k10temp", Label: "Tctl", C: 70}, {Chip: "k10temp", Label: "Tccd1", C: 65}}, 70, true},
		{[]Temp{{Chip: "thermal", Label: "x86_pkg_temp", C: 60}}, 60, true},
		{[]Temp{{Chip: "coretemp", Label: "Core 0", C: 50}, {Chip: "coretemp", Label: "Core 1", C: 58}}, 58, true},
		{[]Temp{{Chip: "acpitz", Label: "temp1", C: 30}}, 0, false},
	}
	for _, c := range cases {
		got, ok := cpuTemp(c.temps)
		if got != c.want || ok != c.ok {
			t.Errorf("%v: got %v %v", c.temps, got, ok)
		}
	}
}

func TestPCIName(t *testing.T) {
	_, ids := sysFixture(t)
	cases := map[[2]string]string{
		{"0x8086", "0x5917"}: "Intel UHD Graphics 620",
		{"0x1002", "0x67ff"}: "AMD Baffin",
		{"0x1002", "0xffff"}: "AMD 1002:ffff", // vendor known, device not
		{"0x1234", "0x0001"}: "1234:0001",
	}
	for in, want := range cases {
		if got := pciName(ids, in[0], in[1]); got != want {
			t.Errorf("%v: %q, want %q", in, got, want)
		}
	}
	if got := pciName("/nonexistent", "0x8086", "0x5917"); got != "8086:5917" {
		t.Errorf("missing pci.ids: %q", got)
	}
}

func TestParseNvidiaSMI(t *testing.T) {
	out := "00000000:01:00.0, A, 5, 100, 200, 40, 25.5\n00000000:02:00.0, B, [N/A], [N/A], 8192, [Not Supported], [N/A]\ngarbage\n"
	m := parseNvidiaSMI(out)
	a, b := m["0000:01:00.0"], m["0000:02:00.0"]
	if a.Name != "A" || *a.UtilPct != 5 || *a.PowerW != 25.5 || *a.MemUsed != 100<<20 {
		t.Fatalf("a %+v", a)
	}
	if b.Name != "B" || b.UtilPct != nil || b.MemUsed != nil || *b.MemTotal != 8192<<20 || b.TempC != nil {
		t.Fatalf("b %+v", b)
	}
	if len(m) != 2 {
		t.Fatalf("%d entries", len(m))
	}
}
