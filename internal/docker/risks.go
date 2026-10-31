package docker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/broker"
	"github.com/mohammed-w-mosabeh/hearth/internal/posture"
)

// maxRiskInspects bounds the containers inspected for one posture check.
const maxRiskInspects = 200

// Risks lists every container with its SECURITY S12 risks, from the
// broker's inspect (Security posture). Docker missing is
// posture.ErrNoDocker.
func Risks(ctx context.Context, b Broker) ([]posture.Risky, error) {
	var list []struct {
		ID    string `json:"Id"`
		Names []string
		State string
	}
	if err := b.Call(ctx, "docker.containers.list", map[string]bool{"all": true}, &list); err != nil {
		if errors.Is(err, broker.ErrNoDocker) {
			return nil, posture.ErrNoDocker
		}
		return nil, fmt.Errorf("docker: %s", errorText(err))
	}
	out := []posture.Risky{}
	for i, c := range list {
		if i >= maxRiskInspects {
			break
		}
		var res struct {
			Risks []string `json:"risks"`
		}
		if err := b.Call(ctx, "docker.containers.inspect", map[string]string{"id": c.ID}, &res); err != nil {
			continue // removed meanwhile
		}
		name := c.ID
		if len(name) > 12 {
			name = name[:12]
		}
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, posture.Risky{Name: name, Running: c.State == "running", Risks: res.Risks})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ContainerImages returns every container's name and image (the change
// timeline). No Docker is no containers.
func ContainerImages(ctx context.Context, b Broker) (map[string]string, error) {
	var list []struct {
		ID    string `json:"Id"`
		Names []string
		Image string
	}
	if err := b.Call(ctx, "docker.containers.list", map[string]bool{"all": true}, &list); err != nil {
		if errors.Is(err, broker.ErrNoDocker) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("docker: %s", errorText(err))
	}
	out := make(map[string]string, len(list))
	for _, c := range list {
		name := c.ID
		if len(name) > 12 {
			name = name[:12]
		}
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out[name] = c.Image
	}
	return out, nil
}
