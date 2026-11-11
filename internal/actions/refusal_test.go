package actions

import (
	"strings"
	"testing"
)

func TestRefusal(t *testing.T) {
	if Refusal(Plan{Blocked: []Blocked{{Item: Item{ID: "x"}, Reason: "not-found"}}}) != "" {
		t.Fatal("an ordinary blocked item is not a refusal")
	}
	if !strings.Contains(Refusal(Plan{Confirm: "gdm.service"}), "typed confirmation") {
		t.Fatal("confirm")
	}
	for _, r := range []string{"critical", "essential", "protected", "required", "self", "session", "system", "browser"} {
		if got := Refusal(Plan{Blocked: []Blocked{{Item: Item{ID: "id", Name: "n"}, Reason: r}}}); !strings.Contains(got, "n is protected ("+r+")") {
			t.Errorf("%s: %q", r, got)
		}
	}
}
