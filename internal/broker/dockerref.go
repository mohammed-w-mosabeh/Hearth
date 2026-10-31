package broker

import (
	"regexp"
	"strings"
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

	// Image references, from github.com/distribution/reference.
	refDomainComp = `(?:[a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9-]*[a-zA-Z0-9])`
	refDomain     = refDomainComp + `(?:\.` + refDomainComp + `)*(?::[0-9]{1,5})?`
	refPathComp   = `[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*`
	refName       = `(?:` + refDomain + `/)?` + refPathComp + `(?:/` + refPathComp + `)*`
	refTag        = `[\w][\w.-]{0,127}`
	refDigest     = `sha256:[a-f0-9]{64}`
	imageName     = regexp.MustCompile(`^` + refName + `$`)
	imageTag      = regexp.MustCompile(`^` + refTag + `$`)
	imageRef      = regexp.MustCompile(`^(` + refName + `)(?::(` + refTag + `))?(?:@(` + refDigest + `))?$`)
	imageDigestID = regexp.MustCompile(`^` + refDigest + `$`)
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
	if hexID.MatchString(s) || imageDigestID.MatchString(s) {
		return true
	}
	_, _, _, ok := ParseImageRef(s)
	return ok
}

// ValidImageName reports whether s is a repository name without tag.
func ValidImageName(s string) bool { return len(s) <= 255 && imageName.MatchString(s) }

// ValidImageTag reports whether s is a tag.
func ValidImageTag(s string) bool { return imageTag.MatchString(s) }

// ParseImageRef splits name[:tag][@digest]. The name is at most 255
// characters, as in the reference grammar.
func ParseImageRef(s string) (name, tag, digest string, ok bool) {
	m := imageRef.FindStringSubmatch(s)
	if m == nil || len(m[1]) > 255 {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}

// secretEnvKey matches environment variable names whose values are
// masked (CLAUDE.md invariant 7).
var secretEnvKey = regexp.MustCompile(`(?i)PASSWORD|PASSWD|TOKEN|KEY|SECRET`)

// EnvMask replaces a masked value.
const EnvMask = "********"

// MaskEnv returns env ("KEY=value" entries) with the values of secret-
// looking keys replaced by EnvMask.
func MaskEnv(env []string) []string {
	out := make([]string, len(env))
	for i, e := range env {
		k, _, found := strings.Cut(e, "=")
		if found && secretEnvKey.MatchString(k) {
			e = k + "=" + EnvMask
		}
		out[i] = e
	}
	return out
}
