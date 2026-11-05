package broker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// Stacks (ROADMAP 9.2, SECURITY S12b). Each stack lives root-owned in
// StacksDir/<name>/: spec.json as checked, compose.json generated from it
// alone, secrets.env (S12c) and args, one line naming the verb's arguments
// from StackVerbs. hearth-stack@<name>.service runs Compose on those files
// with a fixed argv. Every write is atomic, through os.Root, so a link in
// the folder cannot send a write elsewhere.
const (
	StacksDir = "/var/lib/hearth/stacks"
	// StackUnitPrefix + name + ".service" runs one verb on one stack.
	StackUnitPrefix = "hearth-stack@"

	FileSpec    = "spec.json"
	FileCompose = "compose.json"
	FileSecrets = "secrets.env"
	FileArgs    = "args"
)

// StackVerbs are the only argument lists the unit runs, after its fixed
// "docker compose --project-name ... --env-file ..." prefix. down keeps
// volumes; only down-volumes, which stack.remove runs after the stack's
// name was typed back, deletes them.
var StackVerbs = map[string]string{
	"up":      "up --detach --no-build --pull missing --quiet-pull --remove-orphans",
	"update":  "up --detach --no-build --pull always --quiet-pull --remove-orphans",
	"start":   "start",
	"stop":    "stop",
	"restart": "restart",
	"down":    "down --remove-orphans",
	// The stack's named volumes go too; binds are never touched.
	"down-volumes": "down --remove-orphans --volumes",
}

// StackStore writes a stack's files.
type StackStore struct {
	Dir string // default StacksDir
}

func (s StackStore) dir() string {
	if s.Dir == "" {
		return StacksDir
	}
	return s.Dir
}

// open returns the stack's folder as a root, making it (0700) when needed.
func (s StackStore) open(name string, create bool) (*os.Root, error) {
	if !spec.ValidName(name) {
		return nil, errors.New("not a stack name")
	}
	if create {
		if err := os.MkdirAll(s.dir(), 0o700); err != nil {
			return nil, err
		}
	}
	top, err := os.OpenRoot(s.dir())
	if err != nil {
		return nil, err
	}
	defer func() { _ = top.Close() }()
	if create {
		if err := top.Mkdir(name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	}
	fi, err := top.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", name)
	}
	if fi.Mode().Perm() != 0o700 {
		if err := top.Chmod(name, 0o700); err != nil {
			return nil, err
		}
	}
	return top.OpenRoot(name)
}

// stackFiles are the names a stack folder may hold.
var stackFiles = map[string]bool{FileSpec: true, FileCompose: true, FileSecrets: true, FileArgs: true}

// Write replaces files of a stack atomically, each 0600: a temporary file
// made with O_EXCL, synced, renamed over the old one, then the folder
// synced. A file not listed in stackFiles is refused.
func (s StackStore) Write(name string, files map[string][]byte) error {
	for f := range files {
		if !stackFiles[f] {
			return fmt.Errorf("%s is not a stack file", f)
		}
	}
	r, err := s.open(name, true)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	for _, f := range writeOrder(files) {
		if err := writeAtomic(r, f, files[f]); err != nil {
			return err
		}
	}
	d, err := r.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

func writeAtomic(r *os.Root, name string, b []byte) error {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return err
	}
	tmp := "." + name + "." + hex.EncodeToString(rnd[:])
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	err = errors.Join(werr, f.Chmod(0o600), f.Sync(), f.Close())
	if err == nil {
		err = r.Rename(tmp, name)
	}
	if err != nil {
		_ = r.Remove(tmp)
	}
	return err
}

// Read returns one of a stack's files.
func (s StackStore) Read(name, file string) ([]byte, error) {
	if !stackFiles[file] {
		return nil, fmt.Errorf("%s is not a stack file", file)
	}
	r, err := s.open(name, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return r.ReadFile(file)
}

// Prepare checks a spec and writes it with its generated compose file and
// the args for verb, creating an empty secrets.env when there is none so
// the unit's --env-file exists. It is what deploy (9.4) calls after polkit.
func (s StackStore) Prepare(raw []byte, verb string) (spec.Spec, error) {
	args, ok := StackVerbs[verb]
	if !ok {
		return spec.Spec{}, errors.New("not a stack verb")
	}
	sp, rep, err := spec.Parse(raw)
	if err != nil {
		return sp, err
	}
	if !rep.OK() {
		return sp, errors.New("refused: " + strings.Join(rep.Refused, "; "))
	}
	compose, err := spec.Compose(sp)
	if err != nil {
		return sp, err
	}
	canonical, err := json.Marshal(sp)
	if err != nil {
		return sp, err
	}
	files := map[string][]byte{FileSpec: canonical, FileCompose: compose, FileArgs: []byte("STACK_ARGS=" + args + "\n")}
	if _, err := s.Read(sp.Name, FileSecrets); errors.Is(err, os.ErrNotExist) {
		files[FileSecrets] = nil
	}
	return sp, s.Write(sp.Name, files)
}

// SetVerb rewrites only the args file, for a stack that exists.
func (s StackStore) SetVerb(name, verb string) error {
	args, ok := StackVerbs[verb]
	if !ok {
		return errors.New("not a stack verb")
	}
	if _, err := s.Read(name, FileCompose); err != nil {
		return errors.New("no such stack")
	}
	return s.Write(name, map[string][]byte{FileArgs: []byte("STACK_ARGS=" + args + "\n")})
}

// List returns the names of the stacks in the store.
func (s StackStore) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir())
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, e := range entries {
		if e.IsDir() && spec.ValidName(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Remove deletes a stack's folder and its files, secrets.env included,
// without following a link out of the store.
func (s StackStore) Remove(name string) error {
	if !spec.ValidName(name) {
		return errors.New("not a stack name")
	}
	top, err := os.OpenRoot(s.dir())
	if err != nil {
		return err
	}
	defer func() { _ = top.Close() }()
	if err := top.RemoveAll(name); err != nil {
		return err
	}
	d, err := top.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// StackUnit is the unit that runs a verb on name.
func StackUnit(name string) string { return StackUnitPrefix + name + ".service" }

// StackPath is a file of a stack, for messages and the unit.
func StackPath(name, file string) string { return filepath.Join(StacksDir, name, file) }

// StackValidateParams is stack.validate's params.
type StackValidateParams struct {
	Spec json.RawMessage `json:"spec"`
}

// StackValidateResult is what stack.validate returns.
type StackValidateResult struct {
	Refused []string `json:"refused"`
	Flags   []string `json:"flags"`
}

// StackMethods are the stack methods of 9.2: stack.validate checks a spec
// and writes nothing, so it needs no polkit.
func StackMethods() []Method {
	return []Method{{
		Name: "stack.validate",
		Handle: func(_ context.Context, _ Caller, p json.RawMessage) (any, error) {
			var v StackValidateParams
			if err := decodeParams(p, &v); err != nil {
				return nil, err
			}
			if len(v.Spec) == 0 {
				return nil, badParam("no spec")
			}
			_, rep, err := spec.Parse(v.Spec)
			if err != nil {
				return nil, badParam("%s", err.Error())
			}
			return StackValidateResult{Refused: rep.Refused, Flags: rep.Flags}, nil
		},
	}}
}

// writeOrder puts spec.json last, so a folder with a new spec always has
// the files generated from it.
func writeOrder(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if k != FileSpec {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	if _, ok := m[FileSpec]; ok {
		out = append(out, FileSpec)
	}
	return out
}
