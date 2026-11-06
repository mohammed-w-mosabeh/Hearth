// Package changes keeps the change timeline (SPEC 18): what was
// installed, removed or changed on the machine, from dpkg's log, from
// hourly snapshots of units, containers and listeners, and from Hearth's
// own audit log (SECURITY S18); and diffs between any two snapshots.
package changes

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// Sources of a snapshot, in the order they are shown.
const (
	SourcePackages   = "packages"
	SourceUnits      = "units"
	SourceContainers = "containers"
	SourceListeners  = "listeners"
	SourceApps       = "apps"   // apps Hearth manages: state, spec, last backup
	SourceHearth     = "hearth" // the audit log: actions taken through Hearth
	SourceSystem     = "system" // the machine itself: boots
)

// Snapshotted are the sources a snapshot holds.
var Snapshotted = []string{SourcePackages, SourceUnits, SourceContainers, SourceListeners, SourceApps}

// State is the machine at one time: per source, a key and its value
// (a package "name:arch" and its version, a unit "system/cups.service"
// and its unit file state, a container's name and image, a listener's
// label and its addresses). Missing lists the sources that could not be
// read; they are left out of diffs rather than shown as all removed.
type State struct {
	Sections map[string]map[string]string `json:"sections"`
	Missing  []string                     `json:"missing,omitempty"`
}

func (s State) has(source string) bool {
	for _, m := range s.Missing {
		if m == source {
			return false
		}
	}
	_, ok := s.Sections[source]
	return ok
}

// maxSnapshotBytes bounds a decoded snapshot.
const maxSnapshotBytes = 64 << 20

func encode(s State) ([]byte, error) {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	if err := json.NewEncoder(zw).Encode(s); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func decode(data []byte) (State, error) {
	var s State
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return s, fmt.Errorf("changes: snapshot: %w", err)
	}
	defer func() { _ = zr.Close() }()
	if err := json.NewDecoder(io.LimitReader(zr, maxSnapshotBytes)).Decode(&s); err != nil {
		return s, fmt.Errorf("changes: snapshot: %w", err)
	}
	if s.Sections == nil {
		s.Sections = map[string]map[string]string{}
	}
	return s, nil
}

// Entry is one key in a diff.
type Entry struct {
	Key    string `json:"key"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// SectionDiff is what changed in one source.
type SectionDiff struct {
	Source  string  `json:"source"`
	Added   []Entry `json:"added"`
	Removed []Entry `json:"removed"`
	Changed []Entry `json:"changed"`
	// Missing is set when either side could not read this source.
	Missing bool `json:"missing,omitempty"`
}

// Empty reports whether nothing changed.
func (d SectionDiff) Empty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0
}

// Diff compares two states, source by source.
func Diff(a, b State) []SectionDiff {
	out := make([]SectionDiff, 0, len(Snapshotted))
	for _, src := range Snapshotted {
		d := SectionDiff{Source: src, Added: []Entry{}, Removed: []Entry{}, Changed: []Entry{}}
		if !a.has(src) || !b.has(src) {
			d.Missing = true
			out = append(out, d)
			continue
		}
		before, after := a.Sections[src], b.Sections[src]
		for k, v := range after {
			old, ok := before[k]
			switch {
			case !ok:
				d.Added = append(d.Added, Entry{Key: k, After: v})
			case old != v:
				d.Changed = append(d.Changed, Entry{Key: k, Before: old, After: v})
			}
		}
		for k, v := range before {
			if _, ok := after[k]; !ok {
				d.Removed = append(d.Removed, Entry{Key: k, Before: v})
			}
		}
		for _, l := range [][]Entry{d.Added, d.Removed, d.Changed} {
			sort.Slice(l, func(i, j int) bool { return l[i].Key < l[j].Key })
		}
		out = append(out, d)
	}
	return out
}

// Same reports whether two states hold the same thing.
func Same(a, b State) bool {
	for _, d := range Diff(a, b) {
		if !d.Empty() {
			return false
		}
	}
	return a.has(SourceContainers) == b.has(SourceContainers) && a.has(SourceUnits) == b.has(SourceUnits) &&
		a.has(SourceListeners) == b.has(SourceListeners) && a.has(SourcePackages) == b.has(SourcePackages)
}
