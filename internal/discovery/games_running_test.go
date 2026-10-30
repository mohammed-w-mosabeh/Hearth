package discovery

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func argv(s string) [][]byte {
	var out [][]byte
	for _, a := range strings.Fields(s) {
		out = append(out, []byte(a))
	}
	return out
}

func TestClassifyGame(t *testing.T) {
	for line, want := range map[string]string{
		"/home/me/.steam/ubuntu12_32/reaper SteamLaunch AppId=620 -- /usr/bin/game": "steam.620",
		"reaper SteamLaunch -- /usr/bin/game":                                       "",
		"reaper AppId=620 -- x":                                                     "",
		"/home/me/.steam/ubuntu12_32/steam -silent":                                 "",
		"/usr/bin/python3 /usr/share/lutris/bin/lutris-wrapper Quake 0 0 wine x":    "lutris",
		"/usr/bin/lutris": "",
		"/opt/Heroic/legendary launch abc --wine x":   "heroic",
		"/usr/bin/python3 /opt/gogdl launch /games/x": "heroic",
		"/opt/Heroic/legendary list":                  "",
		"vim launch.txt":                              "",
		"grep launch":                                 "",
		"":                                            "",
	} {
		if got := classifyGame(argv(line)); got != want {
			t.Errorf("%q: %q, want %q", line, got, want)
		}
	}
}

func TestPlaying(t *testing.T) {
	root := t.TempDir()
	write := func(pid, cmd string) {
		t.Helper()
		dir := filepath.Join(root, pid)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.ReplaceAll(cmd, " ", "\x00")+"\x00"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("100", "reaper SteamLaunch AppId=620 -- game")
	write("101", "bash")
	write("self", "reaper SteamLaunch AppId=999 -- not-a-pid")
	g := NewGames(GamesConfig{Proc: root})
	got, err := g.Playing()
	if err != nil || !slices.Equal(got, []string{"steam.620"}) {
		t.Fatalf("%v %v", got, err)
	}
	// Processes of other users are not looked at.
	other := NewGames(GamesConfig{Proc: root, UID: os.Getuid() + 1})
	if got, err := other.Playing(); err != nil || len(got) != 0 {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := NewGames(GamesConfig{Proc: filepath.Join(root, "none")}).Playing(); err == nil {
		t.Error("an unreadable /proc is not no games")
	}
}
