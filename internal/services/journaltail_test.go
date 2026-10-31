package services

import (
	"context"
	"slices"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

func TestJournalTail(t *testing.T) {
	var got [][]string
	out := `{"__CURSOR":"new","MESSAGE":"second"}` + "\n" + `{"__CURSOR":"old","MESSAGE":"first"}` + "\n"
	read := JournalTail(func(_ context.Context, name string, args ...string) (safeexec.Result, error) {
		if err := safeexec.Check(name, args...); err != nil {
			t.Fatal(err)
		}
		got = append(got, args)
		return safeexec.Result{Stdout: []byte(out)}, nil
	})
	lines, next, err := read(context.Background(), "")
	if err != nil || len(lines) != 0 || next != "new" || !slices.Contains(got[0], "--lines=1") {
		t.Fatalf("end of the journal: %v %q %v", lines, next, err)
	}
	lines, next, err = read(context.Background(), "s=abc;i=1")
	if err != nil || len(lines) != 2 || next != "new" || !slices.Contains(got[1], "--after-cursor=s=abc;i=1") {
		t.Fatalf("%v %q %v %q", lines, next, err, got[1])
	}
}
