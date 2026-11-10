package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

const pastOut = `  Original form: Mon..Fri 09:00
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

func TestPastElapses(t *testing.T) {
	var got []string
	run := func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
		got = append([]string{name}, args...)
		return safeexec.Result{Stdout: []byte(pastOut)}, nil
	}
	from := time.Date(2025, 11, 5, 0, 0, 0, 0, time.UTC)
	times, trunc, err := PastElapses(context.Background(), run, "Mon..Fri 09:00", from, time.Date(2025, 11, 7, 0, 0, 0, 0, time.UTC))
	if err != nil || trunc || len(times) != 2 || !times[1].Equal(time.Date(2025, 11, 6, 7, 0, 0, 0, time.UTC)) {
		t.Fatal(times, trunc, err)
	}
	if got[0] != "calendar-past" || safeexec.Check(got[0], got[1:]...) != nil {
		t.Fatalf("%q", got)
	}
	if _, _, err := PastElapses(context.Background(), run, "--help", from, from); err == nil {
		t.Fatal("bad expression accepted")
	}
}
