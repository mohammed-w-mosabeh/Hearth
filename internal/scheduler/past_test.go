package scheduler

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

const pastLocal = `  Original form: Mon..Fri 09:00
Normalized form: Mon..Fri *-*-* 09:00:00
    Next elapse: Wed 2025-11-05 09:00:00 EET
       (in UTC): Wed 2025-11-05 07:00:00 UTC
       From now: 1 year 0 months ago
   Iteration #2: Thu 2025-11-06 09:00:00 EET
       (in UTC): Thu 2025-11-06 07:00:00 UTC
       From now: 1 year 0 months ago
   Iteration #3: Fri 2025-11-07 09:00:00 EET
       (in UTC): Fri 2025-11-07 07:00:00 UTC
`

// In UTC, systemd-analyze leaves the "(in UTC)" lines out.
const pastUTC = `  Original form: daily
Normalized form: *-*-* 00:00:00
    Next elapse: Wed 2025-11-05 00:00:00 UTC
       From now: 1 year 0 months ago
   Iteration #2: Thu 2025-11-06 00:00:00 UTC
       From now: 1 year 0 months ago
`

func fixed(out string, calls *[][]string) Runner {
	return func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
		*calls = append(*calls, append([]string{name}, args...))
		return safeexec.Result{Stdout: []byte(out)}, nil
	}
}

func TestPastElapses(t *testing.T) {
	var calls [][]string
	from := time.Date(2025, 11, 4, 0, 0, 0, 0, time.UTC)
	to := time.Date(2025, 11, 7, 0, 0, 0, 0, time.UTC)
	times, trunc, err := PastElapses(context.Background(), fixed(pastLocal, &calls), "Mon..Fri 09:00", from, to)
	if err != nil || trunc || len(times) != 2 || !times[1].Equal(time.Date(2025, 11, 6, 7, 0, 0, 0, time.UTC)) {
		t.Fatal(times, trunc, err)
	}
	if calls[0][0] != "calendar-past" || safeexec.Check(calls[0][0], calls[0][1:]...) != nil {
		t.Fatalf("%q", calls)
	}
	times, _, err = PastElapses(context.Background(), fixed(pastUTC, &calls), "daily", from, to)
	if err != nil || len(times) != 2 || !times[0].Equal(time.Date(2025, 11, 5, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("UTC output", times, err)
	}
	// Elapses listed but unreadable are an error, never "it never fired".
	if _, _, err := PastElapses(context.Background(), fixed("    Next elapse: Wed 2025-11-05 09:00:00 EET\n", &calls), "daily", from, to); err == nil {
		t.Fatal("unreadable elapses accepted")
	}
	if times, _, err := PastElapses(context.Background(), fixed("    Next elapse: never\n", &calls), "2020-01-01", from, to); err != nil || len(times) != 0 {
		t.Fatal(times, err)
	}
	if _, _, err := PastElapses(context.Background(), fixed(pastUTC, &calls), "--help", from, to); err == nil {
		t.Fatal("bad expression accepted")
	}
}

// A full page goes on from its last elapse; pages run out as truncated.
func TestPastElapsesPages(t *testing.T) {
	from := time.Date(2025, 11, 4, 0, 0, 0, 0, time.UTC)
	calls := 0
	run := func(_ context.Context, _ string, args ...string) (safeexec.Result, error) {
		calls++
		var base int64
		_, _ = fmt.Sscanf(args[1], "--base-time=@%d", &base)
		var b strings.Builder
		for i := 1; i <= safeexec.CalendarPastIterations; i++ {
			at := time.Unix(base, 0).UTC().Add(time.Duration(i) * time.Minute)
			fmt.Fprintf(&b, "   Iteration #%d: %s UTC\n", i, at.Format("Mon 2006-01-02 15:04:05"))
		}
		return safeexec.Result{Stdout: []byte(b.String())}, nil
	}
	times, trunc, err := PastElapses(context.Background(), run, "minutely", from, from.Add(48*time.Hour))
	if err != nil || trunc || len(times) != 48*60-1 || calls != 3 {
		t.Fatal(len(times), trunc, err, calls)
	}
	_, trunc, _ = PastElapses(context.Background(), run, "minutely", from, from.Add(30*24*time.Hour))
	if !trunc {
		t.Fatal("pages ran out without saying so")
	}
}

// FuzzParseElapses: whatever systemd-analyze prints, the parser returns
// one time for each elapse it counted or an error, never fewer.
func FuzzParseElapses(f *testing.F) {
	f.Add(pastLocal)
	f.Add(pastUTC)
	f.Add("    Next elapse: never\n")
	f.Add("Iteration #2: Thu 2025-11-06 09:00:00 EET\n(in UTC): x\n")
	f.Fuzz(func(t *testing.T, out string) {
		times, listed, err := parseElapses([]byte(out))
		if err != nil {
			if times != nil {
				t.Fatalf("times beside an error: %v", times)
			}
			return
		}
		if len(times) != listed {
			t.Fatalf("%d times for %d elapses", len(times), listed)
		}
		for _, x := range times {
			if x.Location() != time.UTC {
				t.Fatalf("not a UTC time: %v", x)
			}
		}
	})
}
