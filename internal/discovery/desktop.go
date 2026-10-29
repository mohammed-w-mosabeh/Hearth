// Package discovery finds what is installed on the machine. This file and
// apps.go cover applications: .desktop entries (system, user, Flatpak and
// Snap exports) and AppImages, kept current with fsnotify.
package discovery

import (
	"bufio"
	"bytes"
	"strings"
)

// entry is the [Desktop Entry] group of a .desktop file
// (Desktop Entry Specification 1.5).
type entry struct {
	Type, Name, GenericName, Comment, Icon, Exec, TryExec, Path, WMClass string
	Categories, Keywords, OnlyShowIn, NotShowIn                          []string
	NoDisplay, Hidden, Terminal                                          bool
	AutostartOff                                                         bool // X-GNOME-Autostart-enabled=false
	FlatpakID, SnapName                                                  string
}

// maxDesktopFile bounds a .desktop file; real ones are a few KiB.
const maxDesktopFile = 256 << 10

// parseDesktop reads the [Desktop Entry] group. Localized keys (Name[fr])
// are ignored except Name[en], used when Name is missing: the UI is English.
func parseDesktop(b []byte) (entry, bool) {
	var e entry
	if len(b) > maxDesktopFile {
		return e, false
	}
	in := false
	found := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 4096), maxDesktopFile)
	var nameEn string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		if line[0] == '[' {
			in = line == "[Desktop Entry]"
			found = found || in
			continue
		}
		if !in {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "Type":
			e.Type = v
		case "Name":
			e.Name = unescape(v)
		case "Name[en]", "Name[en_US]":
			nameEn = unescape(v)
		case "GenericName":
			e.GenericName = unescape(v)
		case "Comment":
			e.Comment = unescape(v)
		case "Icon":
			e.Icon = unescape(v)
		case "Exec":
			e.Exec = v // kept raw: field codes and quoting are parsed at launch
		case "TryExec":
			e.TryExec = unescape(v)
		case "Path":
			e.Path = unescape(v)
		case "StartupWMClass":
			e.WMClass = unescape(v)
		case "Categories":
			e.Categories = list(v)
		case "Keywords":
			e.Keywords = list(v)
		case "OnlyShowIn":
			e.OnlyShowIn = list(v)
		case "NotShowIn":
			e.NotShowIn = list(v)
		case "NoDisplay":
			e.NoDisplay = v == "true"
		case "Hidden":
			e.Hidden = v == "true"
		case "Terminal":
			e.Terminal = v == "true"
		case "X-GNOME-Autostart-enabled":
			e.AutostartOff = v == "false"
		case "X-Flatpak":
			e.FlatpakID = v
		case "X-SnapInstanceName":
			e.SnapName = v
		}
	}
	if e.Name == "" {
		e.Name = nameEn
	}
	return e, found && sc.Err() == nil
}

// unescape handles the string escapes of the spec: \s \n \t \r \\.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 's':
			b.WriteByte(' ')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// list splits a ";"-separated list; "\;" is a literal semicolon.
func list(s string) []string {
	var out []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s) && s[i+1] == ';':
			cur.WriteByte(';')
			i++
		case s[i] == ';':
			if v := strings.TrimSpace(unescape(cur.String())); v != "" {
				out = append(out, v)
			}
			cur.Reset()
		default:
			cur.WriteByte(s[i])
		}
	}
	if v := strings.TrimSpace(unescape(cur.String())); v != "" {
		out = append(out, v)
	}
	return out
}
