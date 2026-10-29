package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/actions"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// Runner runs an allowlisted command (safeexec.Run in production).
type Runner func(ctx context.Context, name string, args ...string) (safeexec.Result, error)

// AudioDevice is one PipeWire sink (output) or source (input).
type AudioDevice struct {
	ID      int     `json:"id"`
	Name    string  `json:"name"` // node.description
	Kind    string  `json:"kind"` // output, input
	Volume  float64 `json:"volume"`
	Muted   bool    `json:"muted"`
	Default bool    `json:"default"`
}

// Audio is the GET /api/v1/system/audio answer.
type Audio struct {
	Available bool          `json:"available"` // PipeWire answered
	Devices   []AudioDevice `json:"devices"`
}

// pwObject is the part of a pw-dump entry Hearth reads.
type pwObject struct {
	ID    int    `json:"id"`
	Type  string `json:"type"`
	Props struct {
		MetadataName string `json:"metadata.name"`
	} `json:"props"`
	Info *struct {
		Props map[string]any `json:"props"`
	} `json:"info"`
	Metadata []struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	} `json:"metadata"`
}

var mediaKinds = map[string]string{"Audio/Sink": "output", "Audio/Source": "input"}

// parseDump reads the sinks and sources of a pw-dump, with the defaults
// from the "default" metadata. Volumes are filled in by the caller.
func parseDump(b []byte) ([]AudioDevice, error) {
	var objs []pwObject
	if err := json.Unmarshal(b, &objs); err != nil {
		return nil, err
	}
	defaults := map[string]bool{}
	for _, o := range objs {
		if o.Type != "PipeWire:Interface:Metadata" || o.Props.MetadataName != "default" {
			continue
		}
		for _, m := range o.Metadata {
			if m.Key != "default.audio.sink" && m.Key != "default.audio.source" {
				continue
			}
			var v struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(m.Value, &v) == nil && v.Name != "" {
				defaults[v.Name] = true
			}
		}
	}
	out := []AudioDevice{}
	for _, o := range objs {
		if o.Type != "PipeWire:Interface:Node" || o.Info == nil {
			continue
		}
		class, _ := o.Info.Props["media.class"].(string)
		kind, ok := mediaKinds[class]
		if !ok || o.ID < 0 {
			continue
		}
		name, _ := o.Info.Props["node.name"].(string)
		desc, _ := o.Info.Props["node.description"].(string)
		if desc == "" {
			desc = name
		}
		out = append(out, AudioDevice{ID: o.ID, Name: desc, Kind: kind, Default: defaults[name]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == "output"
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

var volumeLineRe = regexp.MustCompile(`^Volume: ([0-9]+\.[0-9]+)( \[MUTED\])?\s*$`)

// parseVolume reads wpctl get-volume's "Volume: 0.40 [MUTED]".
func parseVolume(s string) (float64, bool, error) {
	m := volumeLineRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false, fmt.Errorf("unexpected wpctl output %q", s)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	return v, m[2] != "", err
}

// Audio lists the outputs and inputs with their volumes.
func (s *Settings) Audio(ctx context.Context) Audio {
	out := Audio{Devices: []AudioDevice{}}
	if s.cfg.Audio == nil {
		return out
	}
	res, err := s.cfg.Audio(ctx, "pw-dump", "--no-colors")
	if err != nil || res.ExitCode != 0 || res.Truncated {
		return out
	}
	devs, err := parseDump(res.Stdout)
	if err != nil {
		return out
	}
	out.Available = true
	for i := range devs {
		r, err := s.cfg.Audio(ctx, "wpctl-get-volume", "get-volume", strconv.Itoa(devs[i].ID))
		if err == nil && r.ExitCode == 0 {
			devs[i].Volume, devs[i].Muted, _ = parseVolume(string(r.Stdout))
		}
	}
	out.Devices = devs
	return out
}

// audioDevice finds node id in the current graph (only sinks and
// sources may be changed).
func (s *Settings) audioDevice(ctx context.Context, id string) (AudioDevice, error) {
	for _, d := range s.Audio(ctx).Devices {
		if strconv.Itoa(d.ID) == id {
			return d, nil
		}
	}
	return AudioDevice{}, actions.Errorf(http.StatusNotFound, "no audio device %s", id)
}

func (s *Settings) registerAudio(reg *actions.Registry) {
	target := func(r actions.Request) error {
		if safeexec.PWNodeID(r.Target) != nil {
			return errors.New("target must be an audio device ID")
		}
		return nil
	}
	plan := func(verb string) func(context.Context, actions.Request) (actions.Plan, error) {
		return func(ctx context.Context, r actions.Request) (actions.Plan, error) {
			d, err := s.audioDevice(ctx, r.Target)
			if err != nil {
				return actions.Plan{}, err
			}
			return one(verb+" "+d.Name, r.Target, d.Name), nil
		}
	}
	reg.Register(actions.Action{Kind: "audio", Name: "volume", Risk: actions.Safe,
		Validate: func(r actions.Request) error {
			if err := target(r); err != nil {
				return err
			}
			if len(r.Params) != 1 || safeexec.Volume(r.Params["volume"]) != nil {
				return errors.New(`params must be {"volume": "0.00".."1.50"}`)
			}
			return nil
		},
		Plan: plan("set the volume of"),
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			return s.wpctl(ctx, p, "wpctl-set-volume", "set-volume", r.Target, r.Params["volume"])
		}})
	reg.Register(actions.Action{Kind: "audio", Name: "mute", Risk: actions.Safe,
		Validate: func(r actions.Request) error {
			if err := target(r); err != nil {
				return err
			}
			if m := r.Params["muted"]; len(r.Params) != 1 || (m != "0" && m != "1") {
				return errors.New(`params must be {"muted": "0" or "1"}`)
			}
			return nil
		},
		Plan: plan("mute or unmute"),
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			return s.wpctl(ctx, p, "wpctl-set-mute", "set-mute", r.Target, r.Params["muted"])
		}})
	reg.Register(actions.Action{Kind: "audio", Name: "default", Risk: actions.Safe,
		Validate: func(r actions.Request) error {
			if err := target(r); err != nil {
				return err
			}
			if len(r.Params) != 0 {
				return errors.New("params must be empty")
			}
			return nil
		},
		Plan: plan("use"),
		Run: func(ctx context.Context, r actions.Request, p actions.Plan) (actions.Result, error) {
			return s.wpctl(ctx, p, "wpctl-set-default", "set-default", r.Target)
		}})
}

func (s *Settings) wpctl(ctx context.Context, p actions.Plan, name string, args ...string) (actions.Result, error) {
	res := actions.Result{Failed: []actions.Blocked{}}
	if s.cfg.Audio == nil {
		return res, actions.Errorf(http.StatusServiceUnavailable, "audio is not available")
	}
	r, err := s.cfg.Audio(ctx, name, args...)
	if err != nil || r.ExitCode != 0 {
		it := p.Items[0]
		it.Detail = strings.TrimSpace(string(r.Stderr))
		if err != nil {
			it.Detail = err.Error()
		}
		res.Failed = append(res.Failed, actions.Blocked{Item: it, Reason: ReasonError})
		return res, nil
	}
	res.Done = 1
	return res, nil
}
