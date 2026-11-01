package services

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// Entry is one journal entry as the log viewer shows it.
type Entry struct {
	Cursor string `json:"cursor"`
	Time   int64  `json:"t"` // Unix milliseconds
	Prio   int    `json:"prio"`
	Ident  string `json:"ident,omitempty"`
	Unit   string `json:"unit,omitempty"`
	PID    int    `json:"pid,omitempty"`
	Msg    string `json:"msg"`
}

// Journal is the response of GET /api/v1/services/journal.
type Journal struct {
	Entries []Entry `json:"entries"`
	// Cursor is where to continue (after=) to get only newer entries.
	Cursor string `json:"cursor,omitempty"`
	// Limited is set when system logs cannot be read: the user is not in
	// a group journald grants read access to (adm, systemd-journal, wheel).
	Limited bool `json:"limited,omitempty"`
}

// Runner runs an allowlisted command (safeexec.Run in production).
type Runner func(ctx context.Context, name string, args ...string) (safeexec.Result, error)

// ReadUnit returns the newest entries of "<scope>/<unit>", oldest first.
func ReadUnit(ctx context.Context, run Runner, target string, lines int) (entries []Entry, truncated bool, err error) {
	scope, unit, ok := strings.Cut(target, "/")
	if !ok || (scope != ScopeSystem && scope != ScopeUser) {
		return nil, false, errors.New("not a unit")
	}
	flag := "--unit="
	if scope == ScopeUser {
		flag = "--user-unit="
	}
	args := append(append([]string{}, safeexec.JournalFixedArgs...), "--lines="+strconv.Itoa(lines), flag+unit)
	res, err := run(ctx, "journalctl-unit", args...)
	if err != nil {
		return nil, false, err
	}
	return parseJournal(res.Stdout, res.Truncated), res.Truncated, nil
}

// HandleJournal serves GET /api/v1/services/journal?unit=<scope>/<unit>
// [&lines=N][&after=<cursor>]: the unit's newest entries, oldest first.
// The arguments are checked here and again by safeexec.
func HandleJournal(run Runner, canReadSystem func() bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		scope, unit, ok := strings.Cut(q.Get("unit"), "/")
		if !ok || (scope != ScopeSystem && scope != ScopeUser) || safeexec.UnitName(unit) != nil {
			http.Error(w, `unit must be "system/<unit>" or "user/<unit>"`, http.StatusBadRequest)
			return
		}
		lines := 200
		if s := q.Get("lines"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > safeexec.MaxJournalLines {
				http.Error(w, "lines must be 1 to 1000", http.StatusBadRequest)
				return
			}
			lines = n
		}
		args := append(append([]string{}, safeexec.JournalFixedArgs...), "--lines="+strconv.Itoa(lines))
		if scope == ScopeUser {
			args = append(args, "--user-unit="+unit)
		} else {
			args = append(args, "--unit="+unit)
		}
		after := q.Get("after")
		if after != "" {
			args = append(args, "--after-cursor="+after)
		}
		res, err := run(r.Context(), "journalctl-unit", args...)
		if err != nil {
			http.Error(w, "the journal could not be read", http.StatusBadGateway)
			return
		}
		out := Journal{Entries: parseJournal(res.Stdout, res.Truncated), Cursor: after}
		if n := len(out.Entries); n > 0 {
			out.Cursor = out.Entries[n-1].Cursor
		}
		out.Limited = scope == ScopeSystem && !canReadSystem()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(out)
	}
}

// parseJournal reads journalctl --output=json lines. A cut-off last line
// (truncated output) and lines that do not parse are skipped.
func parseJournal(b []byte, truncated bool) []Entry {
	if truncated {
		if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
			b = b[:i]
		}
	}
	out := []Entry{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var raw map[string]json.RawMessage
		if json.Unmarshal(sc.Bytes(), &raw) != nil {
			continue
		}
		e := Entry{Cursor: str(raw["__CURSOR"]), Msg: str(raw["MESSAGE"]), Ident: str(raw["SYSLOG_IDENTIFIER"])}
		if e.Cursor == "" {
			continue
		}
		if us, err := strconv.ParseInt(str(raw["__REALTIME_TIMESTAMP"]), 10, 64); err == nil {
			e.Time = us / 1000
		}
		e.Prio = 6 // info, when unset
		if p, err := strconv.Atoi(str(raw["PRIORITY"])); err == nil && p >= 0 && p <= 7 {
			e.Prio = p
		}
		e.PID, _ = strconv.Atoi(str(raw["_PID"]))
		if e.Unit = str(raw["_SYSTEMD_USER_UNIT"]); e.Unit == "" {
			e.Unit = str(raw["_SYSTEMD_UNIT"])
		}
		out = append(out, e)
	}
	return out
}

// str decodes a journal JSON field: a string, an array of bytes (binary
// or invalid UTF-8 data) or null (too large to show).
func str(v json.RawMessage) string {
	if len(v) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	var bs []byte
	var ints []int
	if json.Unmarshal(v, &ints) == nil {
		for _, n := range ints {
			bs = append(bs, byte(n)) // #nosec G115 -- journald sends bytes 0..255
		}
		return strings.ToValidUTF8(string(bs), "�")
	}
	return ""
}

// CanReadSystemJournal reports whether this user can read system logs:
// journald grants it to the groups adm, systemd-journal and wheel.
func CanReadSystemJournal() bool {
	if os.Getuid() == 0 {
		return true
	}
	gids, err := os.Getgroups()
	if err != nil {
		return false
	}
	b, err := os.ReadFile("/etc/group")
	if err != nil {
		return false
	}
	return readerGroup(string(b), gids)
}

func readerGroup(etcGroup string, gids []int) bool {
	for _, line := range strings.Split(etcGroup, "\n") {
		f := strings.Split(line, ":")
		if len(f) < 3 || (f[0] != "adm" && f[0] != "systemd-journal" && f[0] != "wheel") {
			continue
		}
		gid, err := strconv.Atoi(f[2])
		if err != nil {
			continue
		}
		for _, g := range gids {
			if g == gid {
				return true
			}
		}
	}
	return false
}
