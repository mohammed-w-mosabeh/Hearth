package scheduler

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// pastPages bounds how many calendar-past calls one replay makes: 12,000
// elapses, more than a schedule every minute for 7 days.
const pastPages = 12

// PastElapses lists when calendar expression expr elapsed in (from, to),
// oldest first, from systemd-analyze itself (the automations dry run,
// ROADMAP 12.8), a page of elapses at a time. truncated is true when the
// pages ran out before to.
func PastElapses(ctx context.Context, run Runner, expr string, from, to time.Time) (times []time.Time, truncated bool, err error) {
	if err := safeexec.CalendarExpr(expr); err != nil {
		return nil, false, err
	}
	base := from
	for range pastPages {
		res, err := run(ctx, "calendar-past", safeexec.CalendarPastArgs(base.Unix(), expr)...)
		if err != nil {
			return nil, false, fmt.Errorf("scheduler: calendar: %w", err)
		}
		page, listed, err := parseElapses(res.Stdout)
		if err != nil {
			return nil, false, err
		}
		for _, t := range page {
			if t.After(base) && t.Before(to) && (len(times) == 0 || t.After(times[len(times)-1])) {
				times = append(times, t)
			}
		}
		// A short page, or one reaching to, is the end.
		if listed < safeexec.CalendarPastIterations || len(page) == 0 || !page[len(page)-1].Before(to) {
			return times, false, nil
		}
		base = page[len(page)-1]
	}
	return times, true, nil
}

// parseElapses reads every elapse systemd-analyze lists. Outside UTC each
// is followed by "(in UTC): Wed 2025-11-05 07:00:00 UTC"; in UTC that line
// is left out and the elapse itself ends in "UTC". listed counts the
// elapses; one that cannot be read in UTC is an error, never "none".
func parseElapses(out []byte) (times []time.Time, listed int, err error) {
	var utc, plain []time.Time
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "(in UTC):"); ok {
			if t, ok := utcStamp(rest); ok {
				utc = append(utc, t)
			}
			continue
		}
		_, rest, ok := strings.Cut(line, ":")
		head := line[:max(0, len(line)-len(rest)-1)]
		if !ok || (head != "Next elapse" && !strings.HasPrefix(head, "Iteration #")) {
			continue
		}
		if strings.TrimSpace(rest) == "never" {
			continue
		}
		listed++
		if t, ok := utcStamp(rest); ok {
			plain = append(plain, t)
		}
	}
	switch {
	case len(utc) == listed:
		return utc, listed, nil
	case len(plain) == listed:
		return plain, listed, nil
	}
	return nil, listed, errors.New("scheduler: the elapses could not be read in UTC")
}

// utcStamp reads " Wed 2025-11-05 07:00:00 UTC".
func utcStamp(s string) (time.Time, bool) {
	f := strings.Fields(s)
	if len(f) != 4 || f[3] != "UTC" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02 15:04:05", f[1]+" "+f[2])
	return t, err == nil
}
