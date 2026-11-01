package changes

import (
	"bufio"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Package events, from dpkg's log.
const (
	KindInstalled = "installed"
	KindUpgraded  = "upgraded" // or downgraded: dpkg logs both as upgrade
	KindRemoved   = "removed"
	KindPurged    = "purged"
	// Snapshot changes.
	KindAdded   = "added"
	KindDeleted = "deleted"
	KindChanged = "changed"

	KindBoot      = "boot"      // SourceSystem
	KindRestarted = "restarted" // SourceContainers
	// Audit log entries.
	KindAction = "action"
)

// maxLogEvents bounds the package events kept in memory.
const maxLogEvents = 100000

// logFileRe matches dpkg.log and its rotations (dpkg.log.1, dpkg.log.2.gz).
var logFileRe = regexp.MustCompile(`^dpkg\.log(\.\d{1,3})?(\.gz)?$`)

// dpkgLine is "2026-10-29 17:05:12 upgrade libfoo:amd64 1.0-1 1.0-2".
var dpkgLine = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) (install|upgrade|remove|purge) (\S+) (\S+) (\S+)$`)

// dpkgLog reads dpkg's logs, cached until a file changes.
type dpkgLog struct {
	dir string

	mu     sync.Mutex
	stamp  string
	events []Event // oldest first
}

func (d *dpkgLog) files() ([]os.FileInfo, string) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, ""
	}
	var out []os.FileInfo
	var stamp strings.Builder
	for _, e := range entries {
		if !e.Type().IsRegular() || !logFileRe.MatchString(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, fi)
		stamp.WriteString(fi.Name() + ":" + strconv.FormatInt(fi.Size(), 10) + ":" + strconv.FormatInt(fi.ModTime().UnixNano(), 10) + ";")
	}
	return out, stamp.String()
}

// Events returns the package events, oldest first.
func (d *dpkgLog) Events() []Event {
	files, stamp := d.files()
	d.mu.Lock()
	defer d.mu.Unlock()
	if stamp == d.stamp && d.events != nil {
		return d.events
	}
	var all []Event
	for _, fi := range files {
		all = append(all, readDpkgLog(filepath.Join(d.dir, fi.Name()))...)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Time.Before(all[j].Time) })
	if len(all) > maxLogEvents {
		all = all[len(all)-maxLogEvents:]
	}
	if all == nil {
		all = []Event{}
	}
	d.events, d.stamp = all, stamp
	return all
}

func readDpkgLog(path string) []Event {
	f, err := os.Open(path) // #nosec G304 -- a dpkg.log file in the log directory
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return nil
		}
		defer func() { _ = zr.Close() }()
		r = io.LimitReader(zr, 256<<20)
	}
	var out []Event
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		if e, ok := parseDpkgLine(sc.Text()); ok {
			out = append(out, e)
		}
	}
	return out
}

// parseDpkgLine reads one action line of dpkg.log (local time).
func parseDpkgLine(line string) (Event, bool) {
	m := dpkgLine.FindStringSubmatch(line)
	if m == nil {
		return Event{}, false
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", m[1], time.Local)
	if err != nil {
		return Event{}, false
	}
	none := func(v string) string {
		if v == "<none>" {
			return ""
		}
		return v
	}
	e := Event{Time: t, Source: SourcePackages, Subject: m[3], Before: none(m[4]), After: none(m[5])}
	switch m[2] {
	case "install":
		e.Kind = KindInstalled
		if e.Before != "" && e.Before != e.After {
			e.Kind = KindUpgraded
		}
	case "upgrade":
		e.Kind = KindUpgraded
	case "remove":
		e.Kind = KindRemoved
	case "purge":
		e.Kind = KindPurged
	}
	return e, true
}
