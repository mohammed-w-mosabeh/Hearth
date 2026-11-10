package scheduler

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// PastElapses lists when calendar expression expr elapsed in (from, to),
// oldest first, from systemd-analyze itself (the automations dry run,
// ROADMAP 12.8). truncated is true when systemd-analyze stopped at its
// iteration bound before reaching to.
func PastElapses(ctx context.Context, run Runner, expr string, from, to time.Time) (times []time.Time, truncated bool, err error) {
	if err := safeexec.CalendarExpr(expr); err != nil {
		return nil, false, err
	}
	res, err := run(ctx, "calendar-past", safeexec.CalendarPastArgs(from.Unix(), expr)...)
	if err != nil {
		return nil, false, fmt.Errorf("scheduler: calendar: %w", err)
	}
	times, n := parseElapses(res.Stdout, to)
	return times, res.Truncated || n >= safeexec.CalendarPastIterations, nil
}

// parseElapses reads the "(in UTC): Wed 2025-11-05 07:00:00 UTC" lines
// before to; n is how many elapses were listed in all.
func parseElapses(out []byte, to time.Time) ([]time.Time, int) {
	var times []time.Time
	n := 0
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		rest, ok := strings.CutPrefix(line, "(in UTC):")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) != 4 || f[3] != "UTC" {
			continue
		}
		t, err := time.Parse("2006-01-02 15:04:05", f[1]+" "+f[2])
		if err != nil {
			continue
		}
		n++
		if t.Before(to) {
			times = append(times, t)
		}
	}
	return times, n
}
