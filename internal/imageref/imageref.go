// Package imageref is the grammar of Docker image references, from
// github.com/distribution/reference, shared by the broker and the stack
// spec so both read a reference the same way.
package imageref

import "regexp"

var (
	domainComp = `(?:[a-zA-Z0-9]|[a-zA-Z0-9][a-zA-Z0-9-]*[a-zA-Z0-9])`
	domain     = domainComp + `(?:\.` + domainComp + `)*(?::[0-9]{1,5})?`
	pathComp   = `[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*`
	name       = `(?:` + domain + `/)?` + pathComp + `(?:/` + pathComp + `)*`
	tag        = `[\w][\w.-]{0,127}`
	digest     = `sha256:[a-f0-9]{64}`

	nameRe   = regexp.MustCompile(`^` + name + `$`)
	tagRe    = regexp.MustCompile(`^` + tag + `$`)
	refRe    = regexp.MustCompile(`^(` + name + `)(?::(` + tag + `))?(?:@(` + digest + `))?$`)
	digestRe = regexp.MustCompile(`^` + digest + `$`)
)

// Parse splits name[:tag][@digest]. The name is at most 255 characters, as
// in the reference grammar.
func Parse(s string) (name, tag, digest string, ok bool) {
	m := refRe.FindStringSubmatch(s)
	if m == nil || len(m[1]) > 255 {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}

// ValidName reports whether s is a repository name without tag.
func ValidName(s string) bool { return len(s) <= 255 && nameRe.MatchString(s) }

// ValidTag reports whether s is a tag.
func ValidTag(s string) bool { return tagRe.MatchString(s) }

// IsDigest reports whether s is sha256:<64 hex>.
func IsDigest(s string) bool { return digestRe.MatchString(s) }
