package modes

import (
	"encoding/json"
	"testing"
)

func TestSessionSteps(t *testing.T) {
	m, err := Parse("work", []byte(work+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	m.Session.Disconnect = []string{"11111111-2222-3333-4444-555555555555"}
	got, _ := json.Marshal(m.SessionSteps("intel_backlight"))
	want := `[{"action":"power/profile","target":"balanced","params":{}},` +
		`{"action":"system/brightness","target":"intel_backlight","params":{"percent":"70"}},` +
		`{"action":"session/dnd","target":"on","params":{}},` +
		`{"action":"session/wallpaper","target":"~/Pictures/desk.jpg","params":{}},` +
		`{"action":"network/disconnect","target":"11111111-2222-3333-4444-555555555555","params":{}},` +
		`{"action":"network/connect","target":"0b5b1f6e-7c43-4c8e-9a3e-2f1d9b0c6a11","params":{}}]`
	if string(got) != want {
		t.Fatalf("%s", got)
	}
	// No backlight: brightness is left out. Apps are not session steps.
	for _, s := range m.SessionSteps("") {
		if s.Action == "system/brightness" || s.Action == "app/launch" {
			t.Fatalf("%+v", s)
		}
	}
	quiet, _ := Parse("quiet", []byte("name = \"Q\"\n[session]\ndo_not_disturb = false\n"))
	if s := quiet.SessionSteps("x"); len(s) != 1 || s[0].Target != "off" {
		t.Fatalf("%+v", s)
	}
}
