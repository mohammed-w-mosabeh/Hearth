package homepage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"image/png"
	"io"
	"regexp"
	"strings"
)

// Uploaded icons (SECURITY S5, S6): PNG and WebP are checked by their
// headers (PNG also decoded far enough to read its size); SVG is parsed
// and written back with only allowlisted elements and attributes, so no
// script, event handler, style, link or external reference survives.

// MaxIcon bounds an uploaded icon (stricter than S5's 5 MiB: an icon is
// shown at 48 px and sent as a data: URI).
const MaxIcon = 512 << 10

// maxIconSide bounds a PNG's width and height.
const maxIconSide = 2048

var errIcon = errors.New("not a PNG, WebP or SVG icon")

// CheckIcon returns the MIME type and the bytes to store for an upload.
func CheckIcon(b []byte) (mime string, out []byte, err error) {
	if len(b) == 0 || len(b) > MaxIcon {
		return "", nil, errors.New("an icon must be 1 byte to 512 KiB")
	}
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		cfg, err := png.DecodeConfig(bytes.NewReader(b))
		if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxIconSide || cfg.Height > maxIconSide {
			return "", nil, errors.New("not a PNG of at most 2048×2048")
		}
		return "image/png", b, nil
	case len(b) >= 16 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP" &&
		(string(b[12:16]) == "VP8 " || string(b[12:16]) == "VP8L" || string(b[12:16]) == "VP8X"):
		return "image/webp", b, nil
	}
	svg, err := SanitizeSVG(b)
	if err != nil {
		return "", nil, errIcon
	}
	return "image/svg+xml", svg, nil
}

// IconSHA is the key an icon is stored under.
func IconSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

const svgNS = "http://www.w3.org/2000/svg"

var svgElements = map[string]bool{
	"svg": true, "g": true, "path": true, "circle": true, "ellipse": true, "rect": true, "line": true,
	"polyline": true, "polygon": true, "defs": true, "linearGradient": true, "radialGradient": true,
	"stop": true, "title": true, "desc": true, "clipPath": true, "mask": true, "symbol": true,
	"text": true, "tspan": true,
}

// textElements may hold text; elsewhere text is dropped.
var textElements = map[string]bool{"title": true, "desc": true, "text": true, "tspan": true}

var svgAttrs = map[string]bool{
	"id": true, "d": true, "fill": true, "stroke": true, "stroke-width": true, "stroke-linecap": true,
	"stroke-linejoin": true, "stroke-miterlimit": true, "stroke-dasharray": true, "stroke-dashoffset": true,
	"stroke-opacity": true, "fill-opacity": true, "fill-rule": true, "clip-rule": true, "opacity": true,
	"transform": true, "viewBox": true, "width": true, "height": true, "x": true, "y": true, "x1": true,
	"y1": true, "x2": true, "y2": true, "cx": true, "cy": true, "r": true, "rx": true, "ry": true,
	"fx": true, "fy": true, "points": true, "offset": true, "stop-color": true, "stop-opacity": true,
	"gradientUnits": true, "gradientTransform": true, "spreadMethod": true, "clip-path": true,
	"mask": true, "clipPathUnits": true, "maskUnits": true, "maskContentUnits": true,
	"preserveAspectRatio": true, "version": true, "font-size": true, "font-family": true,
	"font-weight": true, "text-anchor": true, "dominant-baseline": true, "letter-spacing": true,
}

// localRef is the only url() an attribute may hold: a reference to an
// element of the same document.
var localRef = regexp.MustCompile(`^url\(#[A-Za-z][A-Za-z0-9_.-]{0,63}\)$`)

var badValue = regexp.MustCompile(`(?i)(url\s*\(|javascript:|data:|expression\s*\(|[<>&\\])`)

const (
	maxSVGDepth  = 64
	maxSVGTokens = 50000
)

// SanitizeSVG parses b as an SVG document and writes it back with only
// allowlisted elements and attributes. Disallowed elements are dropped
// with everything inside them. DOCTYPEs (and so entities) are refused.
func SanitizeSVG(b []byte) ([]byte, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = true
	var out bytes.Buffer
	enc := xml.NewEncoder(&out)
	depth, skip, tokens := 0, 0, 0
	var stack []string // allowed open elements
	sawRoot := false
	for {
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if tokens++; tokens > maxSVGTokens {
			return nil, errors.New("svg: too large")
		}
		switch t := tok.(type) {
		case xml.Directive:
			return nil, errors.New("svg: no DOCTYPE")
		case xml.ProcInst:
			if t.Target != "xml" {
				return nil, errors.New("svg: processing instruction")
			}
		case xml.Comment:
			// dropped
		case xml.StartElement:
			if depth++; depth > maxSVGDepth {
				return nil, errors.New("svg: too deep")
			}
			if skip > 0 {
				skip++
				continue
			}
			name := t.Name.Local
			ns := elementNS(t)
			if !sawRoot {
				if name != "svg" || (ns != "" && ns != svgNS) {
					return nil, errors.New("svg: root is not <svg>")
				}
				sawRoot = true
			}
			if !svgElements[name] || (t.Name.Space != "" && t.Name.Space != "svg") {
				skip = 1
				continue
			}
			start := xml.StartElement{Name: xml.Name{Local: name}}
			if len(stack) == 0 {
				start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: "xmlns"}, Value: svgNS})
			}
			for _, a := range t.Attr {
				if a.Name.Space != "" || !svgAttrs[a.Name.Local] {
					continue
				}
				v := strings.TrimSpace(a.Value)
				if badValue.MatchString(v) && !localRef.MatchString(v) {
					continue
				}
				start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: a.Name.Local}, Value: v})
			}
			if err := enc.EncodeToken(start); err != nil {
				return nil, err
			}
			stack = append(stack, name)
		case xml.EndElement:
			depth--
			if skip > 0 {
				skip--
				continue
			}
			if len(stack) == 0 {
				return nil, errors.New("svg: unbalanced")
			}
			name := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if err := enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: name}}); err != nil {
				return nil, err
			}
		case xml.CharData:
			if skip > 0 || len(stack) == 0 || !textElements[stack[len(stack)-1]] {
				continue
			}
			if err := enc.EncodeToken(xml.CharData(bytes.Clone(t))); err != nil {
				return nil, err
			}
		}
	}
	if !sawRoot || len(stack) != 0 {
		return nil, errors.New("svg: incomplete")
	}
	if err := enc.Flush(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// elementNS is the default namespace an element declares, if any.
func elementNS(t xml.StartElement) string {
	for _, a := range t.Attr {
		if a.Name.Space == "" && a.Name.Local == "xmlns" {
			return a.Value
		}
	}
	return ""
}
