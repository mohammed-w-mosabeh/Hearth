package broker

import (
	"regexp"

	"github.com/mohammed-w-mosabeh/hearth/internal/imageref"
	"github.com/mohammed-w-mosabeh/hearth/internal/secret"
)

// Validators for the names and references Docker methods take (SECURITY
// S8). Each accepts only what Docker itself would, never a leading "-",
// "/" or "." and never anything that could change the request path once
// escaped.

var (
	hexID = regexp.MustCompile(`^[a-f0-9]{12,64}$`)
	// Container and network names: Docker's [a-zA-Z0-9][a-zA-Z0-9_.-]+.
	objName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	// Volume names: the same alphabet, up to 255.
	volName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,254}$`)
)

// ValidContainer reports whether s is a container ID (or its prefix of
// at least 12) or name.
func ValidContainer(s string) bool { return hexID.MatchString(s) || objName.MatchString(s) }

// ValidNetwork reports whether s is a network ID or name.
func ValidNetwork(s string) bool { return hexID.MatchString(s) || objName.MatchString(s) }

// ValidVolume reports whether s is a volume name.
func ValidVolume(s string) bool { return volName.MatchString(s) }

// ValidImage reports whether s is an image ID (hex or sha256:hex) or a
// reference (name[:tag][@digest]).
func ValidImage(s string) bool {
	if hexID.MatchString(s) || imageref.IsDigest(s) {
		return true
	}
	_, _, _, ok := ParseImageRef(s)
	return ok
}

// ValidImageName reports whether s is a repository name without tag.
func ValidImageName(s string) bool { return imageref.ValidName(s) }

// ValidImageTag reports whether s is a tag.
func ValidImageTag(s string) bool { return imageref.ValidTag(s) }

// ParseImageRef splits name[:tag][@digest]. The name is at most 255
// characters, as in the reference grammar.
func ParseImageRef(s string) (name, tag, digest string, ok bool) { return imageref.Parse(s) }

// EnvMask replaces a masked value.
const EnvMask = secret.Mask

// MaskEnv returns env ("KEY=value" entries) with the values SECURITY S19
// masks replaced by EnvMask: secret-looking names, names ending _URL, _URI
// or _DSN, and values holding a URL with user info.
func MaskEnv(env []string) []string { return secret.Env(env) }
