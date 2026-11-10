package spec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Import and export (docs/plan/automations.md §5): JSON through the same
// validator as the editor, all or nothing.

// Bounds of an import.
const (
	MaxImport      = 64 << 10
	MaxAutomations = 50
	FormatVersion  = 1
)

// Item is one automation as exported.
type Item struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Spec    Spec   `json:"spec"`
}

// File is an export.
type File struct {
	Version     int    `json:"version"`
	Automations []Item `json:"automations"`
}

// ParseImport decodes and validates an import; one bad automation refuses
// the whole file. Imported automations start disabled, whatever the file
// says, so nothing runs before the user has looked at it.
func ParseImport(b []byte) ([]Item, error) {
	if len(b) > MaxImport {
		return nil, errors.New("import too large")
	}
	var w struct {
		Version     int `json:"version"`
		Automations []struct {
			Name    string          `json:"name"`
			Enabled bool            `json:"enabled"`
			Spec    json.RawMessage `json:"spec"`
		} `json:"automations"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return nil, fmt.Errorf("import: %w", err)
	}
	if dec.More() {
		return nil, errors.New("import: trailing data")
	}
	if w.Version != FormatVersion {
		return nil, fmt.Errorf("import: version must be %d", FormatVersion)
	}
	if len(w.Automations) == 0 || len(w.Automations) > MaxAutomations {
		return nil, fmt.Errorf("import: 1 to %d automations", MaxAutomations)
	}
	out := make([]Item, 0, len(w.Automations))
	for i, a := range w.Automations {
		if err := CheckName(a.Name); err != nil {
			return nil, fmt.Errorf("automation %d: %w", i+1, err)
		}
		s, err := Parse(a.Spec)
		if err != nil {
			return nil, fmt.Errorf("automation %d (%s): %w", i+1, clip(a.Name), err)
		}
		out = append(out, Item{Name: a.Name, Enabled: false, Spec: s})
	}
	return out, nil
}

// Export encodes items as an import file.
func Export(items []Item) ([]byte, error) {
	return json.MarshalIndent(File{Version: FormatVersion, Automations: items}, "", "  ")
}
