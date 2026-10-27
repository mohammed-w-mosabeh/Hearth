package discovery

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil { // #nosec G703 -- under t.TempDir
		t.Fatal(err)
	}
}

// deploy makes a flatpak deploy with an active link, as flatpak does.
func deploy(t *testing.T, root, kind, id, arch, branch, metadata, metainfo string) {
	t.Helper()
	dir := filepath.Join(root, kind, id, arch, branch)
	write(t, filepath.Join(dir, "abc123/metadata"), metadata)
	if metainfo != "" {
		write(t, filepath.Join(dir, "abc123/files/share/metainfo", id+".metainfo.xml"), metainfo)
	}
	if err := os.Symlink("abc123", filepath.Join(dir, "active")); err != nil {
		t.Fatal(err)
	}
}

func TestFlatpak(t *testing.T) {
	home, sys := t.TempDir(), t.TempDir()
	deploy(t, sys, "app", "org.example.Editor", "x86_64", "stable",
		"[Application]\nname=org.example.Editor\nruntime=org.freedesktop.Platform/x86_64/24.08\n",
		`<component><id>org.example.Editor</id><name xml:lang="fr">Éditeur</name><name>Editor</name>
		<releases><release version="2.1" date="2026-01-01"/><release version="2.0"/></releases></component>`)
	deploy(t, sys, "runtime", "org.freedesktop.Platform", "x86_64", "24.08", "[Runtime]\nname=org.freedesktop.Platform\n", "")
	deploy(t, sys, "runtime", "org.freedesktop.Platform", "x86_64", "23.08", "[Runtime]\n", "")
	deploy(t, filepath.Join(home, ".local/share/flatpak"), "app", "com.example.Game", "x86_64", "master",
		"[Application]\nruntime=org.freedesktop.Platform/x86_64/23.08\n", "")
	_ = os.MkdirAll(filepath.Join(sys, "app/bad id/x86_64/stable"), 0o750) // not a ref: skipped
	l := Flatpak(ExtraConfig{Home: home, Roots: []string{sys}})
	if len(l.Packages) != 4 {
		t.Fatalf("%+v", l.Packages)
	}
	byID := map[string]ExtraPackage{}
	for _, p := range l.Packages {
		byID[p.ID] = p
	}
	ed := byID["system/app/org.example.Editor/x86_64/stable"]
	if ed.Summary != "Editor" || ed.Version != "2.1" || !ed.Removable {
		t.Errorf("editor %+v", ed)
	}
	// 24.08 is used by a system app, 23.08 by a user app (from the system installation).
	for _, id := range []string{"system/runtime/org.freedesktop.Platform/x86_64/24.08", "system/runtime/org.freedesktop.Platform/x86_64/23.08"} {
		if p := byID[id]; p.Removable || p.Why != WhyInUse || len(p.UsedBy) != 1 {
			t.Errorf("%s: %+v", id, p)
		}
	}
	if g := byID["user/app/com.example.Game/x86_64/master"]; !g.Removable || g.Location != "user" {
		t.Errorf("user app %+v", g)
	}
}

func TestPipNpmCargo(t *testing.T) {
	home := t.TempDir()
	site := filepath.Join(home, ".local/lib/python3.13/site-packages")
	write(t, filepath.Join(site, "requests-2.34.2.dist-info/METADATA"), "Metadata-Version: 2.1\nName: requests\nVersion: 2.34.2\nSummary: HTTP\n\nlong description: Name: evil\n")
	write(t, filepath.Join(site, "broken.dist-info/METADATA"), "no headers\n")
	if l := Pip(ExtraConfig{Home: home}); len(l.Packages) != 1 || l.Packages[0].Name != "requests" || l.Packages[0].Removable || l.Packages[0].Why != WhyPip {
		t.Errorf("pip %+v", l.Packages)
	}

	prefix := filepath.Join(home, ".nvm/versions/node/v22.0.0")
	write(t, filepath.Join(prefix, "bin/npm"), "#!/usr/bin/env node\n")
	write(t, filepath.Join(prefix, "lib/node_modules/npm/package.json"), `{"version":"10.0.0"}`)
	write(t, filepath.Join(prefix, "lib/node_modules/@scope/tool/package.json"), `{"version":"1.2.3","description":"a tool"}`)
	write(t, filepath.Join(prefix, "lib/node_modules/-x/package.json"), `{}`)
	l := Npm(ExtraConfig{Home: home})
	got := map[string]ExtraPackage{}
	for _, p := range l.Packages {
		got[p.ID] = p
	}
	if p := got[prefix+"::@scope/tool"]; !p.Removable || p.Version != "1.2.3" {
		t.Errorf("scoped %+v", p)
	}
	if p := got[prefix+"::npm"]; p.Removable || p.Why != WhyManager {
		t.Errorf("npm itself %+v", p)
	}
	if _, ok := got[prefix+"::-x"]; ok {
		t.Error("option-like name listed")
	}

	cargo := filepath.Join(home, ".cargo")
	write(t, filepath.Join(cargo, ".crates2.json"), `{"installs":{"ripgrep 14.1.0 (registry+https://github.com/rust-lang/crates.io-index)":{"bins":["rg"]},"--evil 1 (x)":{}}}`)
	if l := Cargo(ExtraConfig{Home: home}); l.Available || len(l.Packages) != 1 || l.Packages[0].Removable || l.Packages[0].Summary != "rg" {
		t.Errorf("cargo without cargo: %+v", l)
	}
	write(t, filepath.Join(cargo, "bin/cargo"), "")
	if l := Cargo(ExtraConfig{Home: home}); !l.Available || !l.Packages[0].Removable || l.Packages[0].Version != "14.1.0" {
		t.Errorf("cargo %+v", l)
	}
}

func TestGoBinaries(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "gobin")
	self, err := os.Executable() // a Go program, with build info
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(self) // #nosec G304 -- the test binary itself
	write(t, filepath.Join(bin, "mytool"), string(b))
	write(t, filepath.Join(bin, "script.sh"), "#!/bin/sh\n")
	env := map[string]string{"GOBIN": bin}
	l := GoBinaries(ExtraConfig{Home: home, Getenv: func(k string) string { return env[k] }})
	if len(l.Packages) != 1 || l.Packages[0].Name != "mytool" || l.Packages[0].Location != filepath.Join(bin, "mytool") {
		t.Fatalf("%+v", l.Packages)
	}
	write(t, filepath.Join(home, ".config/go/env"), "GOPATH=/opt/go\n")
	if d := GoBin(ExtraConfig{Home: home}); d != "/opt/go/bin" {
		t.Errorf("GoBin from go env file: %s", d)
	}
}

func TestSnap(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "snapd.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"type":"sync","result":[
			{"name":"firefox","version":"131.0","summary":"browser","type":"app","installed-size":250000000},
			{"name":"core22","version":"20240101","type":"base"},
			{"name":"Bad Name","type":"app"}]}`)
	})}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	l := Snap(context.Background(), ExtraConfig{SnapSocket: sock})
	if !l.Available || len(l.Packages) != 2 || l.Packages[1].Name != "firefox" || !l.Packages[1].Removable ||
		l.Packages[0].Removable || l.Packages[0].Why != WhySystem {
		t.Fatalf("%+v", l)
	}
	if l := Snap(context.Background(), ExtraConfig{SnapSocket: sock + ".none"}); l.Available {
		t.Error("available without snapd")
	}
}
