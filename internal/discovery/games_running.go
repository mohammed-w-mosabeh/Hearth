package discovery

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
)

// maxCmdline is how much of a command line is read.
const maxCmdline = 4096

// steamAppRe is the AppId argument of Steam's reaper.
var steamAppRe = regexp.MustCompile(`^AppId=([0-9]{1,10})$`)

// Playing lists what is being played now: game IDs, or just "lutris" or
// "heroic" for their games. Launchers run games inside their own unit, so
// those are found in the command lines of the user's processes (never
// their environment, and nothing of them is kept): Steam's reaper
// ("reaper SteamLaunch AppId=620 -- ...") stays for as long as a game runs.
func (g *Games) Playing() ([]string, error) {
	root := g.cfg.Proc
	if root == "" {
		root = "/proc"
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("discovery: playing: %w", err)
	}
	uid := g.cfg.UID
	if uid == 0 {
		uid = os.Getuid()
	}
	found := map[string]bool{}
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		dir := filepath.Join(root, e.Name())
		fi, err := os.Stat(dir)
		if err != nil {
			continue // gone
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != uid {
			continue
		}
		if id := classifyGame(readCmdline(filepath.Join(dir, "cmdline"))); id != "" {
			found[id] = true
		}
	}
	if g.cfg.Running != nil && g.cfg.Apps != nil {
		desktop := map[string]bool{}
		for _, gm := range g.List() {
			if gm.Source == GameDesktop {
				desktop[gm.appID] = true
			}
		}
		for appID := range g.cfg.Apps.Running(g.cfg.Running()) {
			if desktop[appID] {
				found[GameDesktop+"."+appID] = true
			}
		}
	}
	out := make([]string, 0, len(found))
	for id := range found {
		out = append(out, id)
	}
	return out, nil
}

func readCmdline(path string) [][]byte {
	f, err := os.Open(path) // #nosec G304 -- <proc>/<pid>/cmdline
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxCmdline))
	if err != nil {
		return nil
	}
	return bytes.Split(bytes.TrimRight(b, "\x00"), []byte{0})
}

// classifyGame names the game a command line runs, or "".
func classifyGame(argv [][]byte) string {
	steam := false
	launch := false
	for i, a := range argv {
		s := string(a)
		switch {
		case s == "SteamLaunch":
			steam = true
		case steam:
			if m := steamAppRe.FindStringSubmatch(s); m != nil {
				return GameSteam + "." + m[1]
			}
		case s == "launch":
			launch = true
		}
		if i < 3 && filepath.Base(s) == "lutris-wrapper" {
			return GameLutris
		}
	}
	if launch {
		for _, a := range argv[:min(2, len(argv))] {
			if b := filepath.Base(string(a)); b == "legendary" || b == "gogdl" {
				return GameHeroic
			}
		}
	}
	return ""
}
