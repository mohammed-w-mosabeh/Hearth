package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// The CSP (SECURITY S4) blocks inline scripts and styles; make sure the build
// never depends on them.
func TestIndexHasNoInlineCode(t *testing.T) {
	b, err := fs.ReadFile(Dist(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)

	for _, m := range regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`).FindAllStringSubmatch(html, -1) {
		if !strings.Contains(m[1], "src=") || strings.TrimSpace(m[2]) != "" {
			t.Errorf("inline script: %.80s", m[0])
		}
	}
	for _, re := range []string{`(?i)<style\b`, `(?i)\sstyle\s*=`, `(?i)\son[a-z]+\s*=`, `(?i)javascript:`} {
		if loc := regexp.MustCompile(re).FindStringIndex(html); loc != nil {
			t.Errorf("index.html matches %s at %q", re, html[loc[0]:min(len(html), loc[0]+60)])
		}
	}
	if !strings.Contains(html, `<div id="app">`) {
		t.Error("index.html is not the Hearth shell")
	}
}

func TestAssetsEmbedded(t *testing.T) {
	var js, css int
	err := fs.WalkDir(Dist(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case strings.HasSuffix(p, ".js"):
			js++
		case strings.HasSuffix(p, ".css"):
			css++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if js == 0 || css == 0 {
		t.Fatalf("embedded %d js and %d css files", js, css)
	}
}
