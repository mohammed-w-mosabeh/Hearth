package homepage

import (
	"bytes"
	"encoding/xml"
	"image"
	"image/png"
	"strings"
	"testing"
)

func pngOf(w, h int) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)))
	return b.Bytes()
}

func TestCheckIcon(t *testing.T) {
	if m, _, err := CheckIcon(pngOf(48, 48)); err != nil || m != "image/png" {
		t.Fatalf("png %s %v", m, err)
	}
	if _, _, err := CheckIcon(pngOf(3000, 10)); err == nil {
		t.Fatal("huge png accepted")
	}
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 20)...)
	if m, _, err := CheckIcon(webp); err != nil || m != "image/webp" {
		t.Fatalf("webp %s %v", m, err)
	}
	for name, b := range map[string][]byte{
		"empty": nil, "html": []byte("<html><script>alert(1)</script></html>"), "gif": []byte("GIF89a...."),
		"too big": append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxIcon)...), "bad png": []byte("\x89PNG\r\n\x1a\nnope"),
	} {
		if _, _, err := CheckIcon(b); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestSanitizeSVG(t *testing.T) {
	ok := `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><title>Logo &amp; co</title>` +
		`<defs><linearGradient id="g"><stop offset="0" stop-color="#f00"/></linearGradient></defs>` +
		`<path d="M0 0h24v24H0z" fill="url(#g)"/></svg>`
	out, err := SanitizeSVG([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`viewBox="0 0 24 24"`, `fill="url(#g)"`, `<title>Logo &amp; co</title>`, `xmlns="http://www.w3.org/2000/svg"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("lost %s: %s", want, out)
		}
	}

	attacks := []string{
		`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script><rect width="1"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"><rect width="1"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><a href="javascript:alert(1)"><rect width="1"/></a></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="http://evil/x.svg#a"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><foreignObject><iframe src="http://evil"/></foreignObject></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><style>@import url(http://evil)</style><rect width="1"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><rect width="1" style="background:url(http://evil)" fill="url(http://evil/a)"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><image href="data:image/png;base64,AAAA"/><rect width="1"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><animate attributeName="href" to="javascript:alert(1)"/><set attributeName="onload"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><rect width="1" fill="javascript:alert(1)"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><h:script xmlns:h="http://www.w3.org/1999/xhtml">alert(1)</h:script></svg>`,
	}
	for _, a := range attacks {
		out, err := SanitizeSVG([]byte(a))
		if err != nil {
			continue
		}
		low := strings.ToLower(string(out))
		for _, bad := range []string{"script", "onload", "href", "javascript", "iframe", "style", "evil", "data:", "animate", "<set", "foreignobject", "<image", "<use", "<a"} {
			if strings.Contains(low, bad) {
				t.Errorf("%q survived in %s", bad, out)
			}
		}
	}
	for _, refused := range []string{
		`<!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]><svg xmlns="http://www.w3.org/2000/svg"><text>&x;</text></svg>`,
		`<html><svg/></html>`,
		`<svg xmlns="http://www.w3.org/1999/xhtml"/>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><g>`,
		`<?php echo 1 ?><svg xmlns="http://www.w3.org/2000/svg"/>`,
		strings.Repeat("<svg>", 100) + strings.Repeat("</svg>", 100),
		`not xml`,
	} {
		if out, err := SanitizeSVG([]byte(refused)); err == nil {
			t.Errorf("accepted %.60q as %s", refused, out)
		}
	}
}

func FuzzSanitizeSVG(f *testing.F) {
	for _, s := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0"/></svg>`,
		`<svg><script>x</script></svg>`,
		`<svg onload="x"><a href="javascript:x"/></svg>`,
		`<svg><text>a &lt; b</text></svg>`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, err := SanitizeSVG([]byte(s))
		if err != nil {
			return
		}
		// Whatever comes out parses, is an <svg>, and has only allowed parts.
		dec := xml.NewDecoder(bytes.NewReader(out))
		dec.Strict = true
		first := true
		for {
			tok, err := dec.Token()
			if err != nil {
				if err.Error() != "EOF" {
					t.Fatalf("output does not parse: %v\n%s", err, out)
				}
				break
			}
			if se, ok := tok.(xml.StartElement); ok {
				if first && se.Name.Local != "svg" {
					t.Fatalf("root %s", se.Name.Local)
				}
				first = false
				if !svgElements[se.Name.Local] {
					t.Fatalf("element %s in %s", se.Name.Local, out)
				}
				for _, a := range se.Attr {
					if a.Name.Local == "xmlns" && a.Value == svgNS {
						continue
					}
					if !svgAttrs[a.Name.Local] || a.Name.Space != "" {
						t.Fatalf("attribute %s in %s", a.Name.Local, out)
					}
					if badValue.MatchString(a.Value) && !localRef.MatchString(a.Value) {
						t.Fatalf("value %q in %s", a.Value, out)
					}
				}
			}
		}
	})
}
