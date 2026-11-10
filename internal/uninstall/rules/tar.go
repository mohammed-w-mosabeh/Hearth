package rules

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// The four tar shapes the archive and restore units run (SECURITY S16k,
// S16l), each a fixed argv with three variable values: the archive file,
// the folder tar works in, and the one member. Links are stored as links
// (tar never follows them when creating), owners as numbers, and ACLs
// kept.

// RemovedDir holds every operation's archives and manifest.
const RemovedDir = "/var/lib/hearth/removed"

var archiveRe = regexp.MustCompile(`^/var/lib/hearth/removed/[0-9a-f]{32}/[0-9]{1,3}\.tar\.zst$`)
var movedRe = regexp.MustCompile(`^\.hearth-rm-[0-9a-f]{32}-[0-9]{1,3}$`)

// ArchivePath is an entry's archive file.
func ArchivePath(op string, i int) string { return path.Join(RemovedDir, op, ArchiveName(i)) }

func checkArchive(p string) error {
	if !archiveRe.MatchString(p) {
		return fmt.Errorf("%q is not an archive of an operation", p)
	}
	return nil
}

// checkMemberArg accepts a moved leftover's member, or a configuration
// file's path without its leading slash.
func checkMemberArg(m string) error {
	if movedRe.MatchString(m) {
		return nil
	}
	if strings.HasPrefix(m, "etc/") && CheckConffile("/"+m) == nil {
		return nil
	}
	return fmt.Errorf("%q is not an archive member", m)
}

// checkDirArg accepts the folder tar works in: a system root for a moved
// leftover, "/" for configuration files.
func checkDirArg(d, member string) error {
	if movedRe.MatchString(member) && slices.Contains(SystemRoots, d) {
		return nil
	}
	if d == "/" && strings.HasPrefix(member, "etc/") {
		return nil
	}
	return fmt.Errorf("tar may not work in %q for %q", d, member)
}

// TarCreateArgs archives member of dir into archive.
func TarCreateArgs(archive, dir, member string) []string {
	return []string{"--zstd", "--create", "--numeric-owner", "--acls", "--file", archive, "-C", dir, "--", member}
}

// TarListArgs lists archive.
func TarListArgs(archive string) []string {
	return []string{"--zstd", "--list", "--file", archive}
}

// TarExtractArgs extracts member of archive into dir, never over an
// existing file or folder.
func TarExtractArgs(archive, dir, member string) []string {
	return []string{"--zstd", "--extract", "--keep-old-files", "--numeric-owner", "--same-owner",
		"--same-permissions", "--acls", "--file", archive, "-C", dir, "--", member}
}

// TarCatArgs writes member of archive to standard output (a configuration
// file, which the unit then puts in place itself).
func TarCatArgs(archive, member string) []string {
	return []string{"--zstd", "--extract", "--to-stdout", "--file", archive, "--", member}
}

// shape compares args with want, where "" in want is a variable slot,
// and returns the slots.
func shape(args, want []string) ([]string, error) {
	if len(args) != len(want) {
		return nil, errors.New("tar: wrong arguments")
	}
	var vars []string
	for i, w := range want {
		if w == "" {
			vars = append(vars, args[i])
			continue
		}
		if args[i] != w {
			return nil, errors.New("tar: wrong arguments")
		}
	}
	return vars, nil
}

// CheckTarCreate accepts only TarCreateArgs' shape.
func CheckTarCreate(args []string) error {
	v, err := shape(args, TarCreateArgs("", "", ""))
	if err != nil {
		return err
	}
	if err := checkArchive(v[0]); err != nil {
		return err
	}
	if err := checkMemberArg(v[2]); err != nil {
		return err
	}
	return checkDirArg(v[1], v[2])
}

// CheckTarList accepts only TarListArgs' shape.
func CheckTarList(args []string) error {
	v, err := shape(args, TarListArgs(""))
	if err != nil {
		return err
	}
	return checkArchive(v[0])
}

// CheckTarExtract accepts only TarExtractArgs' shape, for a moved
// leftover beside its place.
func CheckTarExtract(args []string) error {
	v, err := shape(args, TarExtractArgs("", "", ""))
	if err != nil {
		return err
	}
	if err := checkArchive(v[0]); err != nil {
		return err
	}
	if !movedRe.MatchString(v[2]) {
		return fmt.Errorf("%q is not a moved leftover", v[2])
	}
	return checkDirArg(v[1], v[2])
}

// CheckTarCat accepts only TarCatArgs' shape, for a configuration file.
func CheckTarCat(args []string) error {
	v, err := shape(args, TarCatArgs("", ""))
	if err != nil {
		return err
	}
	if err := checkArchive(v[0]); err != nil {
		return err
	}
	if !strings.HasPrefix(v[1], "etc/") || CheckConffile("/"+v[1]) != nil {
		return fmt.Errorf("%q is not a configuration file", v[1])
	}
	return nil
}
