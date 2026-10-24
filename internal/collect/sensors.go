package collect

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Temp is one temperature sensor, in °C. MaxC/CritC are 0 when unknown.
type Temp struct {
	Chip  string  `json:"chip"`
	Label string  `json:"label"`
	C     float64 `json:"c"`
	MaxC  float64 `json:"max_c,omitempty"`
	CritC float64 `json:"crit_c,omitempty"`
}

// Fan is one fan, in RPM.
type Fan struct {
	Chip  string  `json:"chip"`
	Label string  `json:"label"`
	RPM   float64 `json:"rpm"`
}

// skipHwmon are chips reported elsewhere: batteries/AC (battery collector)
// and GPUs (read only while the GPU is awake, see gpu.go).
var skipHwmon = map[string]bool{
	"BAT0": true, "BAT1": true, "BAT2": true, "ACAD": true, "AC": true, "ADP1": true,
	"amdgpu": true, "radeon": true, "nouveau": true, "i915": true, "xe": true,
}

type sensorFile struct {
	chip, label, path string // path is relative to sysRoot
	maxC, critC       float64
	milli             bool // value in milli-units (temps); fans are plain RPM
}

type sensorSet struct {
	temps, fans []sensorFile
}

// discoverSensors lists hwmon temp/fan inputs and thermal zones not already
// covered by a hwmon chip of the same name.
func discoverSensors(sysRoot string) sensorSet {
	var set sensorSet
	chips := map[string]bool{}
	hw := filepath.Join(sysRoot, "class/hwmon")
	entries, _ := os.ReadDir(hw)
	for _, e := range entries {
		dir := filepath.Join("class/hwmon", e.Name())
		chip := strings.TrimSpace(readSmall(sysRoot, filepath.Join(dir, "name")))
		if chip == "" || skipHwmon[chip] || strings.HasPrefix(chip, "BAT") {
			continue
		}
		chips[chip] = true
		files, _ := os.ReadDir(filepath.Join(sysRoot, dir))
		for _, f := range files {
			name := f.Name()
			kind, idx, ok := sensorInput(name)
			if !ok {
				continue
			}
			label := strings.TrimSpace(readSmall(sysRoot, filepath.Join(dir, kind+idx+"_label")))
			if label == "" {
				label = kind + idx
			}
			sf := sensorFile{chip: chip, label: label, path: filepath.Join(dir, name)}
			switch kind {
			case "temp":
				sf.milli = true
				sf.maxC = readMilli(sysRoot, filepath.Join(dir, "temp"+idx+"_max"))
				sf.critC = readMilli(sysRoot, filepath.Join(dir, "temp"+idx+"_crit"))
				set.temps = append(set.temps, sf)
			case "fan":
				set.fans = append(set.fans, sf)
			}
		}
	}

	zones, _ := os.ReadDir(filepath.Join(sysRoot, "class/thermal"))
	for _, z := range zones {
		if !strings.HasPrefix(z.Name(), "thermal_zone") {
			continue
		}
		dir := filepath.Join("class/thermal", z.Name())
		typ := strings.TrimSpace(readSmall(sysRoot, filepath.Join(dir, "type")))
		if typ == "" || chips[typ] {
			continue
		}
		set.temps = append(set.temps, sensorFile{chip: "thermal", label: typ, path: filepath.Join(dir, "temp"), milli: true})
	}

	sort.Slice(set.temps, func(i, j int) bool {
		return set.temps[i].chip+set.temps[i].label < set.temps[j].chip+set.temps[j].label
	})
	sort.Slice(set.fans, func(i, j int) bool { return set.fans[i].chip+set.fans[i].label < set.fans[j].chip+set.fans[j].label })
	return set
}

// sensorInput matches "temp3_input" / "fan1_input".
func sensorInput(name string) (kind, idx string, ok bool) {
	base, found := strings.CutSuffix(name, "_input")
	if !found {
		return "", "", false
	}
	for _, k := range []string{"temp", "fan"} {
		if rest, ok := strings.CutPrefix(base, k); ok && rest != "" {
			if _, err := strconv.Atoi(rest); err == nil {
				return k, rest, true
			}
		}
	}
	return "", "", false
}

func (s *Sampler) readSensors() ([]Temp, []Fan) {
	temps := make([]Temp, 0, len(s.sensors.temps))
	for _, f := range s.sensors.temps {
		v, ok := s.readNumber(f.path)
		if !ok {
			continue
		}
		temps = append(temps, Temp{Chip: f.chip, Label: f.label, C: v / 1000, MaxC: f.maxC, CritC: f.critC})
	}
	fans := make([]Fan, 0, len(s.sensors.fans))
	for _, f := range s.sensors.fans {
		if v, ok := s.readNumber(f.path); ok {
			fans = append(fans, Fan{Chip: f.chip, Label: f.label, RPM: v})
		}
	}
	return temps, fans
}

// cpuTemp picks the sensor that best represents "the CPU": the package
// sensor (Intel coretemp), Tctl/Tdie (AMD k10temp), the x86_pkg_temp zone,
// else the hottest coretemp core.
func cpuTemp(temps []Temp) (float64, bool) {
	prefs := []struct{ chip, label string }{
		{"coretemp", "Package id 0"}, {"k10temp", "Tctl"}, {"k10temp", "Tdie"}, {"zenpower", "Tdie"},
		{"thermal", "x86_pkg_temp"},
	}
	for _, p := range prefs {
		for _, t := range temps {
			if t.Chip == p.chip && t.Label == p.label {
				return t.C, true
			}
		}
	}
	best, ok := 0.0, false
	for _, t := range temps {
		if t.Chip == "coretemp" && t.C > best {
			best, ok = t.C, true
		}
	}
	return best, ok
}

// readNumber reads a sysfs integer through the reusable reader.
func (s *Sampler) readNumber(rel string) (float64, bool) {
	b, err := s.rd.read(s.cfg.SysRoot, rel)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	return v, err == nil
}

// readSmall reads a short sysfs attribute at discovery time.
func readSmall(root, rel string) string {
	b, err := readFile(filepath.Join(root, rel))
	if err != nil {
		return ""
	}
	return string(b)
}

func readMilli(root, rel string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(readSmall(root, rel)), 64)
	if err != nil {
		return 0
	}
	return v / 1000
}
