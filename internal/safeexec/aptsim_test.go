package safeexec

import (
	"slices"
	"strings"
	"testing"
)

func TestAptSimulateArgs(t *testing.T) {
	v := allowlist["apt-get-simulate"].Validate
	for _, ok := range [][]string{AptSimulate(), AptSimulate("vlc"), AptSimulate("vlc", "libvlc5:i386")} {
		if err := v(ok); err != nil {
			t.Errorf("%v: %v", ok, err)
		}
	}
	many := make([]string, MaxAptSimulate+1)
	for i := range many {
		many[i] = "p" + strings.Repeat("a", i+1)
	}
	for _, bad := range [][]string{
		{}, {"-s"}, {"install", "vlc"}, {"-s", "-q", "-o", "Debug::NoLocking=true", "install", "vlc"},
		{"-q", "-s", "-o", "Debug::NoLocking=true", "autoremove"}, AptSimulate("-y"), AptSimulate("--purge"),
		AptSimulate("vlc", "vlc"), AptSimulate("a/b"), AptSimulate("../x"), AptSimulate("-o", "APT::Get::Assume-Yes=1"),
		AptSimulate("Vlc"), AptSimulate(many...),
	} {
		if err := v(bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

// Only the fixed simulation reaches apt-get: never without -s, never a
// name that is an option.
func FuzzAptSimulateArgs(f *testing.F) {
	f.Add("vlc", "libvlc5:i386")
	f.Add("-y", "--purge")
	v := allowlist["apt-get-simulate"].Validate
	f.Fuzz(func(t *testing.T, a, b string) {
		args := AptSimulate(a, b)
		if v(args) != nil {
			return
		}
		if !slices.Equal(args[:5], []string{"-s", "-q", "-o", "Debug::NoLocking=true", "autoremove"}) {
			t.Fatal("prefix changed")
		}
		for _, n := range []string{a, b} {
			if strings.HasPrefix(n, "-") || strings.ContainsAny(n, " /=\x00") || DebPackageArch(n) != nil {
				t.Fatalf("accepted %q", n)
			}
		}
	})
}
