// Package dropin writes Hearth's own systemd drop-in, <unit>.d/hearth.conf
// (SPEC 10, SECURITY S16d). The original unit is never touched, and the
// file is rendered from a closed list of resource directives: Exec*,
// Environment, User and paths equal root, so they cannot be expressed here.
package dropin

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// FileName is the only file Hearth writes in a unit's .d directory.
const FileName = "hearth.conf"

// Directives maps a directive to its value.
type Directives = map[string]string

var (
	size     = regexp.MustCompile(`^(infinity|[1-9][0-9]{0,14}[KMGT]?|([1-9][0-9]?|100)%)$`)
	percent  = regexp.MustCompile(`^[1-9][0-9]{0,4}%$`)
	duration = regexp.MustCompile(`^[0-9]{1,6}(ms|s|min|h)?$`)
	restarts = []string{"no", "always", "on-success", "on-failure", "on-abnormal", "on-abort", "on-watchdog"}
)

func between(lo, hi int) func(string) bool {
	return func(s string) bool {
		n, err := strconv.Atoi(s)
		return err == nil && n >= lo && n <= hi && s == strconv.Itoa(n)
	}
}

// allowed is the complete allowlist, each with its value's shape.
var allowed = map[string]func(string) bool{
	"MemoryMax":      size.MatchString,
	"CPUQuota":       percent.MatchString,
	"Restart":        func(s string) bool { return slices.Contains(restarts, s) },
	"RestartSec":     duration.MatchString,
	"Nice":           between(-20, 19),
	"IOWeight":       between(1, 10000),
	"TimeoutStopSec": duration.MatchString,
}

// Validate refuses any directive outside the allowlist and any value
// outside its shape.
func Validate(d Directives) error {
	for k, v := range d {
		ok, known := allowed[k]
		if !known {
			return fmt.Errorf("directive %q is not allowed", k)
		}
		if !ok(v) {
			return fmt.Errorf("bad value for %s", k)
		}
	}
	return nil
}

// ValidUnit accepts the units a drop-in may name: instances and plain
// services, not templates, and never Hearth's own.
func ValidUnit(name string) error {
	if err := safeexec.UnitName(name); err != nil {
		return err
	}
	switch {
	case !strings.HasSuffix(name, ".service"):
		return errors.New("only a service has these settings")
	case strings.Contains(name, "/") || strings.HasPrefix(name, "."):
		return errors.New("bad unit name")
	case strings.Contains(name, "@."):
		return errors.New("a template unit cannot be acted on; pick an instance")
	case strings.HasPrefix(name, "hearth"):
		return errors.New("hearth's own units keep their limits")
	}
	return nil
}

// Render is the file's content.
func Render(d Directives) []byte {
	keys := make([]string, 0, len(d))
	for k := range d {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b bytes.Buffer
	b.WriteString("# Written by Hearth. Changes here are replaced when it saves.\n[Service]\n")
	for _, k := range keys {
		b.WriteString(k + "=" + d[k] + "\n")
	}
	return b.Bytes()
}

// Parse reads what Render wrote. Anything else is left out, so a file
// edited by hand can never carry more than the allowlist back to the UI.
func Parse(data []byte) Directives {
	d := Directives{}
	for line := range strings.SplitSeq(string(data), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok || Validate(Directives{k: v}) != nil {
			continue
		}
		d[k] = v
	}
	return d
}

// Read returns the unit's current directives under dir (none if no file).
func Read(dir, unit string) (Directives, error) {
	data, err := os.ReadFile(filepath.Join(dir, unit+".d", FileName)) // #nosec G304 -- unit passed ValidUnit
	if errors.Is(err, os.ErrNotExist) {
		return Directives{}, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(data), nil
}

// Write replaces the unit's drop-in under dir; no directives removes it.
// A link in place of the directory or the file is refused, not followed.
func Write(dir, unit string, d Directives) error {
	if err := ValidUnit(unit); err != nil {
		return err
	}
	if err := Validate(d); err != nil {
		return err
	}
	sub := filepath.Join(dir, unit+".d")
	path := filepath.Join(sub, FileName)
	if len(d) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_ = os.Remove(sub) // only if nothing else lives there
		return nil
	}
	if err := os.Mkdir(sub, 0o755); err != nil && !errors.Is(err, os.ErrExist) { // #nosec G301 -- systemd unit directories are world-readable
		return err
	}
	fi, err := os.Lstat(sub)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.New(sub + " is not a directory")
	}
	if err := os.Chmod(sub, 0o755); err != nil { // #nosec G302 -- the broker's umask is 0077; systemd's are world-readable
		return err
	}
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return errors.New(path + " is not a regular file")
	}
	tmp, err := os.CreateTemp(sub, ".hearth-*") // never read by systemd: not *.conf
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.Write(Render(d))
	if err == nil {
		err = tmp.Chmod(0o644)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
