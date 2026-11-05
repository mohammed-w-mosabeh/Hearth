package broker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/secrets"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// stack.import (ROADMAP 9.8, SECURITY S12b and S12c) takes over a Compose
// project Hearth does not manage: same project name, so Compose recreates
// its containers once with the stack's labels, its named volumes reused by
// name and its binds in place; nothing is copied. The secrets come in the
// same call, so the values never pass through a file Hearth did not write.
// Everything deploy checks is checked, before polkit and after it, and
// the action is deploy's or danger's, as the spec's flags say.

// StackImportParams is stack.import's params.
type StackImportParams struct {
	Spec     json.RawMessage   `json:"spec"`
	AckFlags []string          `json:"ack_flags"`
	Secrets  map[string]string `json:"secrets"`
}

type importCheck struct {
	spec    spec.Spec
	raw     []byte
	secrets []byte // secrets.env as written
}

func (s *stacks) importMethod() Method {
	return Method{Name: "stack.import", Privileged: true, Action: ActionStackDeploy, Timeout: StackTimeout,
		Check: func(ctx context.Context, p json.RawMessage) error {
			_, err := s.checkImport(ctx, p, true)
			return pre(err)
		},
		Authorize: func(p json.RawMessage) (string, map[string]string) {
			var v StackImportParams
			if err := json.Unmarshal(p, &v); err != nil {
				return ActionStackDanger, nil
			}
			sp, _, err := spec.Parse(v.Spec)
			if err != nil {
				return ActionStackDanger, nil
			}
			return dangerAction(sp, "import")
		},
		Handle: s.importStack, Redact: redactImport}
}

func (s *stacks) checkImport(ctx context.Context, p json.RawMessage, pre bool) (importCheck, error) {
	var v StackImportParams
	if err := decodeParams(p, &v); err != nil {
		return importCheck{}, badParam("params are spec, ack_flags and secrets")
	}
	if len(v.Spec) == 0 {
		return importCheck{}, badParam("no spec")
	}
	sp, rep, err := spec.Parse(v.Spec)
	if err != nil {
		return importCheck{}, badParam("%s", err.Error())
	}
	if !rep.OK() {
		return importCheck{}, badParam("refused: %s", strings.Join(rep.Refused, "; "))
	}
	ack := slices.Clone(v.AckFlags)
	sort.Strings(ack)
	if !slices.Equal(ack, rep.Flags) {
		return importCheck{}, badParam("the acknowledged flags are not the spec's: %s", strings.Join(rep.Flags, ", "))
	}
	if _, err := s.c.Store.Read(sp.Name, FileSpec); !errors.Is(err, os.ErrNotExist) {
		return importCheck{}, badParam("%s is already an app; deploy it instead", sp.Name)
	}
	// Exactly the keys the services name, each with its value.
	refs := referenced(sp)
	for k := range refs {
		if _, ok := v.Secrets[k]; !ok {
			return importCheck{}, badParam("the secret %s has no value", k)
		}
	}
	for k := range v.Secrets {
		if refs[k] == nil {
			return importCheck{}, badParam("no service of %s names the secret %s", sp.Name, clipName(k))
		}
	}
	enc, err := secrets.Encode(v.Secrets)
	if err != nil {
		return importCheck{}, badParam("%s", err.Error()) // names and limits, never values
	}
	if pre {
		if _, err := s.paths(ctx, v.Spec, true); err != nil {
			return importCheck{}, err
		}
	}
	// Last, as in deploy: the project must exist and be nobody's stack.
	list, err := s.c.Engine.StackContainers(ctx, sp.Name)
	if err != nil {
		return importCheck{}, err
	}
	if len(list) == 0 {
		return importCheck{}, badParam("no Compose project named %s has containers; deploy it instead", sp.Name)
	}
	for _, c := range list {
		if c.Stack != "" {
			return importCheck{}, badParam("%s is already managed", sp.Name)
		}
	}
	return importCheck{spec: sp, raw: v.Spec, secrets: enc}, nil
}

func (s *stacks) importStack(ctx context.Context, c Caller, p json.RawMessage) (any, error) {
	chk, err := s.checkImport(ctx, p, false)
	if err != nil {
		return nil, err
	}
	defer s.locks.lock(chk.spec.Name)()
	if chk, err = s.checkImport(ctx, p, false); err != nil { // again under the lock
		return nil, err
	}
	paths, err := s.paths(ctx, chk.raw, false)
	if err != nil {
		return nil, err
	}
	// secrets.env first: Prepare never overwrites it.
	if err := s.c.Store.Write(chk.spec.Name, map[string][]byte{FileSecrets: chk.secrets}); err != nil {
		return nil, Errorf(CodeFailed, "the app's secrets could not be written")
	}
	if _, err := s.c.Store.Prepare(chk.raw, "up"); err != nil {
		return nil, Errorf(CodeFailed, "the app's files could not be written")
	}
	res, err := s.runAndVerify(ctx, chk.spec.Name, "up", paths)
	if err == nil {
		s.auditDanger(ctx, c, "stack.import", chk.spec)
	}
	return res, err
}

// redactImport keeps the spec's hash-worthy shape for the audit: the name,
// the flags and the secret names, never a value.
func redactImport(p json.RawMessage) json.RawMessage {
	var v StackImportParams
	if err := json.Unmarshal(p, &v); err != nil {
		return nil
	}
	var head struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(v.Spec, &head)
	keys := make([]string, 0, len(v.Secrets))
	for k := range v.Secrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	flags := append([]string{}, v.AckFlags...)
	sort.Strings(flags)
	b, err := json.Marshal(map[string]any{"name": head.Name, "ack_flags": flags, "secrets": keys})
	if err != nil {
		return nil
	}
	return b
}
