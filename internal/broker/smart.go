package broker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
)

// ActionDiskTest is the polkit action for starting a disk self-test.
const ActionDiskTest = "org.hearth.disk.selftest"

// SysRoot is where the broker looks a disk up.
const SysRoot = "/sys"

// smartctl's exit status is a bit mask (smartctl(8)).
const (
	smartBadCommand = 1 << 0 // the command line did not parse
	smartOpenFailed = 1 << 1 // the device did not open, or is asleep with -n
	smartCmdFailed  = 1 << 2 // a SMART command failed; the rest of the report may stand
	smartFailing    = 1 << 3 // the disk itself reports it is failing
	smartPrefail    = 1 << 4 // a pre-failure attribute is at or past its threshold now
)

// SmartParams are disk.smart's params.
type SmartParams struct {
	Device string `json:"device"` // a whole disk's name: sda, nvme0n1
}

// SelfTestParams are disk.selftest's params.
type SelfTestParams struct {
	Device string `json:"device"`
	Kind   string `json:"kind"` // short or long
}

// SmartTest is one finished self-test.
type SmartTest struct {
	Kind   string `json:"kind"`
	Result string `json:"result"`
	Passed bool   `json:"passed"`
	Hours  *int64 `json:"hours,omitempty"` // power-on hours when it ended
}

// SmartReport is what Hearth keeps of smartctl's output. It holds no
// serial number or WWN: only figures, and the model, which comes from the
// disk's firmware and is cleaned of control characters.
type SmartReport struct {
	Device    string `json:"device"`
	Model     string `json:"model,omitempty"`
	Protocol  string `json:"protocol,omitempty"` // ATA, NVMe, SCSI
	Capacity  int64  `json:"capacity,omitempty"`
	Supported bool   `json:"supported"`
	// Asleep means the disk was in standby and was left there: the
	// report has nothing else.
	Asleep bool `json:"asleep,omitempty"`
	// Failing is the disk's own verdict: its SMART status, or a
	// pre-failure attribute at its threshold now.
	Failing      bool       `json:"failing"`
	TempC        *int64     `json:"temp_c,omitempty"`
	PowerOnHours *int64     `json:"power_on_hours,omitempty"`
	Reallocated  *int64     `json:"reallocated,omitempty"`
	Pending      *int64     `json:"pending,omitempty"`
	MediaErrors  *int64     `json:"media_errors,omitempty"` // NVMe
	LifePct      *int64     `json:"life_pct,omitempty"`     // SSD life left, 0 to 100
	Testing      bool       `json:"testing"`
	Remaining    *int64     `json:"remaining_pct,omitempty"` // of the running test
	LastTest     *SmartTest `json:"last_test,omitempty"`
}

// smartOutput is the part of `smartctl -j -a` Hearth reads; the keys are
// smartmontools 7.4's.
type smartOutput struct {
	Smartctl struct {
		Messages []struct {
			String string `json:"string"`
		} `json:"messages"`
	} `json:"smartctl"`
	Device struct {
		Protocol string `json:"protocol"`
	} `json:"device"`
	ModelName     string `json:"model_name"`
	SCSIModelName string `json:"scsi_model_name"`
	UserCapacity  struct {
		Bytes int64 `json:"bytes"`
	} `json:"user_capacity"`
	NVMeCapacity int64  `json:"nvme_total_capacity"`
	RotationRate *int64 `json:"rotation_rate"`
	SmartSupport *struct {
		Available bool `json:"available"`
	} `json:"smart_support"`
	SmartStatus *struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature *struct {
		Current *int64 `json:"current"`
	} `json:"temperature"`
	PowerOnTime *struct {
		Hours *int64 `json:"hours"`
	} `json:"power_on_time"`
	Attributes struct {
		Table []struct {
			ID    int   `json:"id"`
			Value int64 `json:"value"`
			Raw   struct {
				Value int64 `json:"value"`
			} `json:"raw"`
			WhenFailed string `json:"when_failed"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	ATAData struct {
		SelfTest struct {
			Status struct {
				Remaining *int64 `json:"remaining_percent"`
			} `json:"status"`
		} `json:"self_test"`
	} `json:"ata_smart_data"`
	ATALog struct {
		Standard struct {
			Table []struct {
				Type struct {
					String string `json:"string"`
				} `json:"type"`
				Status struct {
					String string `json:"string"`
					Passed *bool  `json:"passed"`
				} `json:"status"`
				Hours *int64 `json:"lifetime_hours"`
			} `json:"table"`
		} `json:"standard"`
	} `json:"ata_smart_self_test_log"`
	NVMeHealth *struct {
		PercentageUsed *int64 `json:"percentage_used"`
		MediaErrors    *int64 `json:"media_errors"`
	} `json:"nvme_smart_health_information_log"`
	NVMeLog struct {
		Current *struct {
			Value int64 `json:"value"`
		} `json:"current_self_test_operation"`
		Completion *int64 `json:"current_self_test_completion_percent"`
		Table      []struct {
			Code struct {
				String string `json:"string"`
			} `json:"self_test_code"`
			Result struct {
				Value  int64  `json:"value"`
				String string `json:"string"`
			} `json:"self_test_result"`
			Hours *int64 `json:"power_on_hours"`
		} `json:"table"`
	} `json:"nvme_self_test_log"`
}

// cleanText keeps a disk-supplied string printable and short.
func cleanText(s string, limit int) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "")))
	if r := []rune(s); len(r) > limit {
		s = string(r[:limit])
	}
	return s
}

func clampPct(v int64) *int64 {
	return &[]int64{max(0, min(100, v))}[0]
}

func (o *smartOutput) message() string {
	var m []string
	for _, x := range o.Smartctl.Messages {
		if s := cleanText(x.String, 200); s != "" {
			m = append(m, s)
		}
	}
	return clipMsg(strings.Join(m, " "))
}

// asleep reports smartctl's -n standby refusal, "Device is in STANDBY
// mode, exit(2)". It shares its exit status with a device that failed to
// open, so the message decides.
func (o *smartOutput) asleep() bool {
	for _, x := range o.Smartctl.Messages {
		if strings.HasPrefix(x.String, "Device is in ") &&
			(strings.Contains(x.String, "STANDBY") || strings.Contains(x.String, "SLEEP")) {
			return true
		}
	}
	return false
}

// lifeAttrs are the SSD attributes whose normalized value is the life
// left, in order of preference: SSD_Life_Left, Percent_Lifetime_Remain,
// Media_Wearout_Indicator, Wear_Leveling_Count.
var lifeAttrs = []int{231, 202, 233, 177}

// parseSmart turns smartctl's output into a report. Bits 3 and up of the
// exit status describe the disk, not the command, so they are data here.
func parseSmart(name string, res safeexec.Result) (SmartReport, error) {
	r := SmartReport{Device: name}
	if res.Truncated {
		return r, Errorf(CodeFailed, "smartctl: output too long")
	}
	var o smartOutput
	if err := json.Unmarshal(res.Stdout, &o); err != nil {
		return r, Errorf(CodeFailed, "smartctl: unreadable output (exit status %d)", res.ExitCode)
	}
	if o.asleep() {
		r.Asleep = true
		return r, nil
	}
	if res.ExitCode&(smartBadCommand|smartOpenFailed) != 0 {
		return r, Errorf(CodeFailed, "smartctl: %s", o.message())
	}
	r.Model = cleanText(o.ModelName, 64)
	if r.Model == "" {
		r.Model = cleanText(o.SCSIModelName, 64)
	}
	r.Protocol = cleanText(o.Device.Protocol, 16)
	r.Capacity = o.UserCapacity.Bytes
	if r.Capacity == 0 {
		r.Capacity = o.NVMeCapacity
	}
	r.Supported = o.SmartStatus != nil || (o.SmartSupport != nil && o.SmartSupport.Available)
	r.Failing = (o.SmartStatus != nil && !o.SmartStatus.Passed) || res.ExitCode&(smartFailing|smartPrefail) != 0
	if o.Temperature != nil {
		r.TempC = o.Temperature.Current
	}
	if o.PowerOnTime != nil {
		r.PowerOnHours = o.PowerOnTime.Hours
	}
	attrs := map[int]int64{}
	raw := map[int]int64{}
	for _, a := range o.Attributes.Table {
		attrs[a.ID], raw[a.ID] = a.Value, a.Raw.Value
		if a.WhenFailed == "now" {
			r.Failing = true
		}
	}
	if v, ok := raw[5]; ok {
		r.Reallocated = &v
	}
	if v, ok := raw[197]; ok {
		r.Pending = &v
	}
	if o.RotationRate != nil && *o.RotationRate == 0 { // a rotating disk's 177 and 202 mean other things
		for _, id := range lifeAttrs {
			if v, ok := attrs[id]; ok {
				r.LifePct = clampPct(v)
				break
			}
		}
	}
	if o.NVMeHealth != nil {
		r.MediaErrors = o.NVMeHealth.MediaErrors
		if u := o.NVMeHealth.PercentageUsed; u != nil {
			r.LifePct = clampPct(100 - *u)
		}
	}
	if v := o.ATAData.SelfTest.Status.Remaining; v != nil {
		r.Testing, r.Remaining = true, clampPct(*v)
	}
	if o.NVMeLog.Current != nil && o.NVMeLog.Current.Value != 0 {
		r.Testing = true
		if c := o.NVMeLog.Completion; c != nil {
			r.Remaining = clampPct(100 - *c)
		}
	}
	for _, t := range o.ATALog.Standard.Table { // newest first
		if t.Status.Passed != nil { // none while a test runs
			r.LastTest = &SmartTest{Kind: cleanText(t.Type.String, 40), Result: cleanText(t.Status.String, 80), Passed: *t.Status.Passed, Hours: t.Hours}
			break
		}
	}
	if len(o.NVMeLog.Table) > 0 {
		t := o.NVMeLog.Table[0]
		r.LastTest = &SmartTest{Kind: cleanText(t.Code.String, 40), Result: cleanText(t.Result.String, 80), Passed: t.Result.Value == 0, Hours: t.Hours}
	}
	return r, nil
}

// diskPath checks name is a whole disk of this machine and returns its
// device node. Nothing is opened: the name decides, and the disk must be
// a real device the kernel lists.
func diskPath(sysRoot, name string) (string, error) {
	if err := safeexec.DiskName(name); err != nil {
		return "", badParam("device must be a whole disk, like sda or nvme0n1")
	}
	if _, err := os.Stat(filepath.Join(sysRoot, "block", name, "device")); err != nil {
		return "", badParam("no such disk")
	}
	return "/dev/" + name, nil
}

// smartctl runs the command. safeexec reports any non-zero exit as an
// error, but smartctl's exit status is its report: a failing disk, or a
// sleeping one, exits non-zero with a full answer. So only a run that
// never finished by itself (no exit status) fails here.
func smartctl(ctx context.Context, run Runner, name string, args ...string) (safeexec.Result, error) {
	res, err := run(ctx, name, args...)
	switch {
	case err == nil || res.ExitCode > 0:
		return res, nil
	case errors.Is(err, safeexec.ErrBinary):
		return res, ErrNoSmartctl
	}
	return res, Errorf(CodeFailed, "smartctl: %s", clipMsg(err.Error()))
}

// SmartMethods are the broker's disk health methods (SECURITY S16f).
// disk.smart only reads, so hearthd can poll it without a password dialog,
// and with -n standby it leaves a sleeping disk asleep. disk.selftest asks
// polkit: the test runs in the disk's firmware and keeps the data, but a
// long one slows the disk for hours.
func SmartMethods(run Runner, sysRoot string) []Method {
	test := func(p json.RawMessage) ([]string, error) {
		var v SelfTestParams
		if err := decodeParams(p, &v); err != nil {
			return nil, err
		}
		if v.Kind != "short" && v.Kind != "long" {
			return nil, badParam("kind must be short or long")
		}
		dev, err := diskPath(sysRoot, v.Device)
		if err != nil {
			return nil, err
		}
		return []string{"-j", "-t", v.Kind, dev}, nil
	}
	return []Method{{
		Name: "disk.smart", Timeout: 30 * time.Second,
		Handle: func(ctx context.Context, _ Caller, p json.RawMessage) (any, error) {
			var v SmartParams
			if err := decodeParams(p, &v); err != nil {
				return nil, err
			}
			dev, err := diskPath(sysRoot, v.Device)
			if err != nil {
				return nil, err
			}
			res, err := smartctl(ctx, run, "smartctl-read", "-j", "-a", "-n", "standby", dev)
			if err != nil {
				return nil, err
			}
			return parseSmart(v.Device, res)
		},
	}, {
		Name: "disk.selftest", Privileged: true, Action: ActionDiskTest,
		Check: func(_ context.Context, p json.RawMessage) error { _, err := test(p); return err },
		Handle: func(ctx context.Context, _ Caller, p json.RawMessage) (any, error) {
			a, err := test(p)
			if err != nil {
				return nil, err
			}
			res, err := smartctl(ctx, run, "smartctl-test", a...)
			if err != nil {
				return nil, err
			}
			if res.ExitCode&(smartBadCommand|smartOpenFailed|smartCmdFailed) != 0 {
				var o smartOutput
				_ = json.Unmarshal(res.Stdout, &o) // the exit status decides; the text only explains
				return nil, Errorf(CodeFailed, "smartctl: %s", o.message())
			}
			return map[string]bool{"started": true}, nil
		},
	}}
}
