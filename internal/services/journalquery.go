package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// JournalQuery is the response of GET /api/v1/journal.
type JournalQuery struct {
	Entries []Entry `json:"entries"` // newest first
	// Next continues (after=) with older entries, when there may be some.
	Next    string `json:"next,omitempty"`
	Limited bool   `json:"limited,omitempty"`
}

var (
	journalBootRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
	journalTimeRe = regexp.MustCompile(`^[0-9]{1,12}$`)
)

// journalQuery turns a query into journalctl's arguments. Values are
// checked here for a useful answer and again by safeexec.
func journalQuery(get func(string) string) ([]string, int, error) {
	lines := 200
	if s := get("lines"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > safeexec.MaxJournalLines {
			return nil, 0, errors.New("lines must be 1 to 1000")
		}
		lines = n
	}
	args := append(append([]string{}, safeexec.JournalQueryFixedArgs...), "--lines="+strconv.Itoa(lines))
	if s := get("priority"); s != "" {
		if len(s) != 1 || s[0] < '0' || s[0] > '7' {
			return nil, 0, errors.New("priority must be 0 to 7")
		}
		args = append(args, "--priority="+s)
	}
	for _, f := range []string{"since", "until"} {
		s := get(f)
		if s == "" {
			continue
		}
		if !journalTimeRe.MatchString(s) {
			return nil, 0, errors.New(f + " must be unix seconds")
		}
		// journalctl refuses --since with a cursor: older pages are cut by
		// the handler instead.
		if f == "until" || get("after") == "" {
			args = append(args, "--"+f+"=@"+s)
		}
	}
	if s := get("boot"); s != "" {
		if !journalBootRe.MatchString(s) {
			return nil, 0, errors.New("not a boot ID")
		}
		args = append(args, "--boot="+s)
	}
	if s := get("unit"); s != "" {
		scope, unit, ok := strings.Cut(s, "/")
		if !ok || (scope != ScopeSystem && scope != ScopeUser) || safeexec.UnitName(unit) != nil {
			return nil, 0, errors.New(`unit must be "system/<unit>" or "user/<unit>"`)
		}
		if scope == ScopeUser {
			args = append(args, "--user-unit="+unit)
		} else {
			args = append(args, "--unit="+unit)
		}
	}
	if s := get("after"); s != "" {
		args = append(args, "--after-cursor="+s)
	}
	if s := strings.TrimSpace(get("text")); s != "" {
		// Text, not a pattern: what people type, and nothing to backtrack on.
		args = append(args, "--grep="+regexp.QuoteMeta(s))
	}
	return args, lines, nil
}

// HandleJournalQuery serves GET /api/v1/journal?lines=&priority=&since=
// &until=&boot=&unit=&text=&after=: the journal as this user may read it.
func HandleJournalQuery(run Runner, canReadSystem func() bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		args, lines, err := journalQuery(r.URL.Query().Get)
		if err == nil {
			err = safeexec.Check("journalctl-query", args...)
		}
		if err != nil {
			httpJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		res, err := run(r.Context(), "journalctl-query", args...)
		// A search that matches nothing exits 1 with no output.
		noMatch := res.ExitCode == 1 && len(res.Stdout) == 0 && len(res.Stderr) == 0
		if err != nil && !noMatch {
			if errors.Is(err, context.DeadlineExceeded) {
				httpJSONError(w, http.StatusGatewayTimeout, "this took too long: narrow the search by time, unit or priority")
				return
			}
			httpJSONError(w, http.StatusBadGateway, "the journal could not be read")
			return
		}
		out := JournalQuery{Entries: parseJournal(res.Stdout, res.Truncated), Limited: !canReadSystem()}
		more := len(out.Entries) == lines || res.Truncated
		if s := r.URL.Query().Get("since"); s != "" && r.URL.Query().Get("after") != "" {
			since, _ := strconv.ParseInt(s, 10, 64) // checked by journalQuery
			kept := slices.IndexFunc(out.Entries, func(e Entry) bool { return e.Time < since*1000 })
			if kept >= 0 {
				out.Entries, more = out.Entries[:kept], false
			}
		}
		if n := len(out.Entries); n > 0 && more {
			out.Next = out.Entries[n-1].Cursor
		}
		writeNoStore(w, out)
	}
}

// Boot is one boot the journal has logs of.
type Boot struct {
	ID    string `json:"id"`
	First int64  `json:"first"` // unix milliseconds
	Last  int64  `json:"last"`
}

// maxBoots bounds the boots listed.
const maxBoots = 30

// parseBoots reads journalctl --list-boots --output=json, newest first.
func parseBoots(b []byte) ([]Boot, error) {
	var raw []struct {
		ID    string `json:"boot_id"`
		First int64  `json:"first_entry"`
		Last  int64  `json:"last_entry"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	out := make([]Boot, 0, min(len(raw), maxBoots))
	for i := len(raw) - 1; i >= 0 && len(out) < maxBoots; i-- {
		if journalBootRe.MatchString(raw[i].ID) {
			out = append(out, Boot{ID: raw[i].ID, First: raw[i].First / 1000, Last: raw[i].Last / 1000})
		}
	}
	return out, nil
}

// HandleBoots serves GET /api/v1/journal/boots.
func HandleBoots(run Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		boots := []Boot{}
		if res, err := run(r.Context(), "journalctl-boots", "--list-boots", "--output=json", "--no-pager"); err == nil {
			if list, err := parseBoots(res.Stdout); err == nil {
				boots = list
			}
		}
		writeNoStore(w, map[string]any{"boots": boots})
	}
}

func httpJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeNoStore(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
