package modes

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/mohammed-w-mosabeh/hearth/internal/store"
)

// Gate 10: a mode Hearth writes reads back as the same mode. A name or
// value cannot carry a key into the file, so saving in the app cannot
// plant a command, a stack or a trigger the user did not pick.
func FuzzEncodeRoundTrip(f *testing.F) {
	f.Add("work", work, `[]`, false)
	f.Add("m", "name = \"M\"\n[session]\nbrightness = 5\n", `[{"kind":"app","target":"a\"\ncommand = \"x","do":"start"}]`, true)
	f.Add("m", "name = \"M\"\n[[stack]]\nname = \"web\"\ndo = \"stop\"\n", `[{"kind":"setting","target":"wallpaper","do":"~/a'''\n[[trigger]]\nkind = \"game\"\n.png"}]`, false)
	f.Add("m", "name = \"M\\n[[stack]]\"\n[session]\ndo_not_disturb = true\n", `[{"kind":"stack","target":"x]]\n","do":"start"}]`, false)
	f.Fuzz(func(t *testing.T, id, src, items string, game bool) {
		m, err := Parse(id, []byte(src))
		if err != nil {
			return
		}
		if json.Valid([]byte(items)) {
			// As a save through the profile API changes it; the engine
			// validates first, Parse checks again below.
			if n, err := apply(m, store.Profile{Name: m.Name, Items: items, AutoGame: game}); err == nil {
				m = n
			}
		}
		raw, err := Encode(m)
		if err != nil {
			return
		}
		back, err := Parse(m.ID, raw)
		if err != nil {
			if m.check() == nil {
				t.Fatalf("a valid mode does not read back: %v\n%s", err, raw)
			}
			return
		}
		if back.Name != m.Name || !reflect.DeepEqual(back.Session, m.Session) || !reflect.DeepEqual(back.Stacks, m.Stacks) ||
			!reflect.DeepEqual(back.Items, m.Items) || !reflect.DeepEqual(back.Triggers, m.Triggers) {
			t.Fatalf("read back differently:\n%+v\n%+v\n%s", m, back, raw)
		}
	})
}
