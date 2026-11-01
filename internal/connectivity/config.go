// Package connectivity checks the way out (ROADMAP 6.5, SECURITY S21h): the
// gateway, DNS and targets the user chose, only once the user turns it on.
// It also counts the bytes each interface moved, from the kernel's counters,
// which sends nothing anywhere.
package connectivity

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"regexp"
	"slices"
	"strings"
)

// Limits of the form.
const (
	maxTargets = 8
	maxCapGiB  = 1 << 20
)

var hostname = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*\.?$`)
var ifaceName = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,15}$`)

// Target is a host to reach: a TCP connect, or a ping when Port is 0.
type Target struct {
	Host string `json:"host"`
	Port int    `json:"port,omitempty"`
}

// Config is what the user chose. The zero value checks nothing.
type Config struct {
	Enabled bool     `json:"enabled"`
	DNSName string   `json:"dns_name,omitempty"`
	Targets []Target `json:"targets"`
	// CapGiB, above 0, alerts when CapIface's month goes over it.
	CapGiB   float64 `json:"cap_gib,omitempty"`
	CapIface string  `json:"cap_iface,omitempty"`
}

// ValidHost accepts a name or an address, never a URL.
func ValidHost(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Zone() == ""
	}
	return hostname.MatchString(s)
}

// Validate checks a config from the form.
func (c Config) Validate() error {
	if len(c.Targets) > maxTargets {
		return fmt.Errorf("at most %d targets", maxTargets)
	}
	if c.Enabled && len(c.Targets) == 0 {
		return errors.New("choose at least one target to check")
	}
	if c.DNSName != "" {
		if _, err := netip.ParseAddr(c.DNSName); err == nil || !ValidHost(c.DNSName) {
			return errors.New("the DNS check needs a host name")
		}
	}
	seen := make([]Target, 0, len(c.Targets))
	for _, t := range c.Targets {
		switch {
		case !ValidHost(t.Host):
			return errors.New("a target is a host name or an address")
		case t.Port < 0 || t.Port > 65535:
			return errors.New("a port is 1 to 65535, or none for a ping")
		case slices.Contains(seen, t):
			return errors.New("a target is listed twice")
		}
		seen = append(seen, t)
	}
	switch {
	case math.IsNaN(c.CapGiB) || c.CapGiB < 0 || c.CapGiB > maxCapGiB:
		return errors.New("the data cap is out of range")
	case c.CapGiB > 0 && !ifaceName.MatchString(c.CapIface):
		return errors.New("a data cap needs an interface")
	case c.CapGiB == 0 && c.CapIface != "":
		return errors.New("an interface needs a data cap")
	}
	return nil
}

// label names a target in charts and logs.
func (t Target) label() string {
	if t.Port == 0 {
		return t.Host
	}
	if strings.Contains(t.Host, ":") {
		return fmt.Sprintf("[%s]:%d", t.Host, t.Port)
	}
	return fmt.Sprintf("%s:%d", t.Host, t.Port)
}
