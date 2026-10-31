package broker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Bounds of docker.containers.update. Docker itself refuses less than 6 MiB.
const (
	minContainerMemory = 6 << 20
	maxMemory          = 1 << 50
	minNanoCPUs        = 10_000_000 // 0.01 CPU
	maxNanoCPUs        = 1024_000_000_000
)

// RestartPolicies are the policies a container may be given.
var RestartPolicies = []string{"no", "always", "unless-stopped", "on-failure"}

var (
	byteSize = regexp.MustCompile(`^([1-9][0-9]{0,14})([KMGT]?)$`)
	cpuCount = regexp.MustCompile(`^([0-9]{1,4})(?:\.([0-9]{1,2}))?$`)
)

// ParseBytes reads "512M" (K, M, G and T are powers of 1024).
func ParseBytes(s string) (int64, error) {
	m := byteSize.FindStringSubmatch(s)
	if m == nil {
		return 0, errors.New("a size is a number with K, M, G or T, like 512M")
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, errors.New("size too large")
	}
	shift := strings.Index("KMGT", m[2])*10 + 10
	if m[2] == "" {
		shift = 0
	}
	if n > maxMemory>>shift {
		return 0, errors.New("size too large")
	}
	return n << shift, nil
}

// ParseCPUs reads "1.5" as nanoseconds of CPU per second.
func ParseCPUs(s string) (int64, error) {
	m := cpuCount.FindStringSubmatch(s)
	if m == nil {
		return 0, errors.New("CPUs is a number like 1.5")
	}
	whole, _ := strconv.ParseInt(m[1], 10, 64)
	frac, _ := strconv.ParseInt((m[2] + "00")[:2], 10, 64)
	n := whole*1_000_000_000 + frac*10_000_000
	if n < minNanoCPUs || n > maxNanoCPUs {
		return 0, errors.New("CPUs must be 0.01 to 1024")
	}
	return n, nil
}

// UpdateParams are docker.containers.update's params: these three settings
// and nothing else. The broker's strict decoding refuses any other field.
type UpdateParams struct {
	ID       string `json:"id"`
	Memory   int64  `json:"memory,omitempty"`
	NanoCPUs int64  `json:"nano_cpus,omitempty"`
	Restart  string `json:"restart,omitempty"`
}

func (v UpdateParams) validate() error {
	switch {
	case !ValidContainer(v.ID):
		return badParam("id must be a container ID or name")
	case v.Memory == 0 && v.NanoCPUs == 0 && v.Restart == "":
		return badParam("nothing to update")
	case v.Memory != 0 && (v.Memory < minContainerMemory || v.Memory > maxMemory):
		return badParam("memory must be 6 MiB or more")
	case v.NanoCPUs != 0 && (v.NanoCPUs < minNanoCPUs || v.NanoCPUs > maxNanoCPUs):
		return badParam("CPUs must be 0.01 to 1024")
	case v.Restart != "" && !slices.Contains(RestartPolicies, v.Restart):
		return badParam("restart must be no, always, unless-stopped or on-failure")
	}
	return nil
}

// containerUpdate applies new limits to a container. Swap follows memory
// as docker run sets it, since Docker refuses a limit above the old swap.
func (d *Docker) containerUpdate(ctx context.Context, p json.RawMessage) (any, error) {
	var v UpdateParams
	if err := decodeParams(p, &v); err != nil {
		return nil, err
	}
	if err := v.validate(); err != nil {
		return nil, err
	}
	type policy struct {
		Name string `json:"Name"`
	}
	body := struct {
		Memory        int64   `json:"Memory,omitempty"`
		MemorySwap    int64   `json:"MemorySwap,omitempty"`
		NanoCPUs      int64   `json:"NanoCpus,omitempty"`
		RestartPolicy *policy `json:"RestartPolicy,omitempty"`
	}{Memory: v.Memory, MemorySwap: 2 * v.Memory, NanoCPUs: v.NanoCPUs}
	if v.Restart != "" {
		body.RestartPolicy = &policy{Name: v.Restart}
	}
	resp, err := d.sendBody(ctx, http.MethodPost, "/containers/"+esc(v.ID)+"/update", nil, body, nil)
	if err != nil {
		return nil, err
	}
	_ = resp.Body.Close()
	return map[string]bool{"ok": true}, nil
}
