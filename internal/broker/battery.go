package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ActionBatteryThreshold is the polkit action for the charge limit.
const ActionBatteryThreshold = "org.hearth.battery.threshold"

// Bounds of battery.threshold: below 50 a laptop barely charges.
const (
	MinChargeEnd = 50
	MaxChargeEnd = 100
)

// ChargeEndFile is the kernel's charge limit, in a power_supply's directory.
const ChargeEndFile = "charge_control_end_threshold"

var batteryName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// ThresholdParams are battery.threshold's params.
type ThresholdParams struct {
	Battery string `json:"battery"`
	End     int    `json:"end"`
}

// ValidBatteryName reports whether s can name a power_supply.
func ValidBatteryName(s string) bool {
	return batteryName.MatchString(s)
}

// chargeEndPath checks the params and finds the file: a real battery with
// the kernel's limit, by a name that cannot leave the directory.
func chargeEndPath(sysRoot string, v ThresholdParams) (string, error) {
	switch {
	case !ValidBatteryName(v.Battery):
		return "", badParam("not a battery name")
	case v.End < MinChargeEnd || v.End > MaxChargeEnd:
		return "", badParam("the limit must be 50 to 100")
	}
	dir := filepath.Join(sysRoot, "class", "power_supply", v.Battery)
	kind, err := os.ReadFile(filepath.Join(dir, "type")) // #nosec G304 -- a validated name under sysfs
	if err != nil || strings.TrimSpace(string(kind)) != "Battery" {
		return "", badParam("no such battery")
	}
	path := filepath.Join(dir, ChargeEndFile)
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return "", badParam("this battery has no charge limit")
	}
	return path, nil
}

// BatteryMethods is battery.threshold (SECURITY S16h): one integer into one
// kernel file. Params are checked before polkit is asked.
func BatteryMethods(sysRoot string) []Method {
	parse := func(p json.RawMessage) (ThresholdParams, string, error) {
		var v ThresholdParams
		if len(p) == 0 {
			return v, "", badParam("params required")
		}
		if err := decodeParams(p, &v); err != nil {
			return v, "", err
		}
		path, err := chargeEndPath(sysRoot, v)
		return v, path, err
	}
	return []Method{{
		Name: "battery.threshold", Privileged: true, Action: ActionBatteryThreshold, Timeout: 5 * time.Second,
		Check: func(_ context.Context, p json.RawMessage) error { _, _, err := parse(p); return err },
		Handle: func(_ context.Context, _ Caller, p json.RawMessage) (any, error) {
			v, path, err := parse(p)
			if err != nil {
				return nil, err
			}
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0) // #nosec G304 -- chargeEndPath built it from a validated name
			if err != nil {
				return nil, Errorf(CodeFailed, "the charge limit cannot be written")
			}
			defer func() { _ = f.Close() }()
			if _, err := fmt.Fprint(f, strconv.Itoa(v.End)); err != nil {
				return nil, Errorf(CodeFailed, "the kernel refused this charge limit")
			}
			return map[string]bool{"done": true}, nil
		},
	}}
}
