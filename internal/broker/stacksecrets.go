package broker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"

	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/secrets"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// Stack secrets (ROADMAP 9.5, SECURITY S12c). A stack's secrets.env holds
// the values its services name in their spec's secrets lists; Compose
// passes each service only those, through ${KEY} references. Setting and
// revealing a value each need org.hearth.stack.secrets (auth_admin, no
// keep). The audit hashes only the stack and key names (Redact), errors
// never quote a value, and stack.status shows names alone.

// ActionStackSecrets sets or reveals a stack's secret values.
const ActionStackSecrets = "org.hearth.stack.secrets" // #nosec G101 -- a polkit action name

// StackSecretsSetParams is stack.secrets.set's params.
type StackSecretsSetParams struct {
	Name   string            `json:"name"`
	Set    map[string]string `json:"set"`
	Remove []string          `json:"remove"`
}

// StackSecretRevealParams is stack.secrets.reveal's params.
type StackSecretRevealParams struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// StackSecret is one key in stack.status: never its value.
type StackSecret struct {
	Key      string   `json:"key"`
	Services []string `json:"services"`
	Set      bool     `json:"set"`
}

func (s *stacks) secretMethods() []Method {
	return []Method{
		{Name: "stack.secrets.set", Privileged: true, Action: ActionStackSecrets,
			Check: func(_ context.Context, p json.RawMessage) error {
				_, _, err := s.checkSecretsSet(p)
				return pre(err)
			},
			Handle: s.secretsSet, Redact: redactSecretsSet},
		{Name: "stack.secrets.reveal", Privileged: true, Action: ActionStackSecrets,
			Check: func(_ context.Context, p json.RawMessage) error {
				_, err := s.checkReveal(p)
				return pre(err)
			},
			Handle: s.secretsReveal},
	}
}

// referenced maps each key the spec's services name to those services.
func referenced(sp spec.Spec) map[string][]string {
	out := map[string][]string{}
	for name, svc := range sp.Services {
		for _, k := range svc.Secrets {
			out[k] = append(out[k], name)
		}
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

// current reads a stack's secrets; a missing file has none.
func (s *stacks) current(name string) (map[string]string, error) {
	b, err := s.c.Store.Read(name, FileSecrets)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, Errorf(CodeFailed, "the stack's secrets could not be read")
	}
	m, err := secrets.Parse(b)
	if err != nil {
		return nil, Errorf(CodeFailed, "the stack's secrets.env is not as Hearth writes it")
	}
	return m, nil
}

// checkSecretsSet validates a set and returns the file it would write.
func (s *stacks) checkSecretsSet(p json.RawMessage) (StackSecretsSetParams, []byte, error) {
	var v StackSecretsSetParams
	if err := decodeParams(p, &v); err != nil {
		return v, nil, badParam("params are name, set and remove")
	}
	sp, _, _, err := s.stored(v.Name)
	if err != nil {
		return v, nil, err
	}
	if len(v.Set) == 0 && len(v.Remove) == 0 {
		return v, nil, badParam("nothing to change")
	}
	refs := referenced(sp)
	for k, val := range v.Set {
		switch {
		case !secrets.ValidKey(k):
			return v, nil, badParam("%q cannot name a secret", clipName(k))
		case refs[k] == nil:
			return v, nil, badParam("no service of %s names the secret %s", v.Name, k)
		}
		if why := secrets.CheckValue(val); why != "" {
			return v, nil, badParam("%s: %s", k, why)
		}
	}
	m, err := s.current(v.Name)
	if err != nil {
		return v, nil, err
	}
	for _, k := range v.Remove {
		if !secrets.ValidKey(k) {
			return v, nil, badParam("%q cannot name a secret", clipName(k))
		}
		if _, both := v.Set[k]; both {
			return v, nil, badParam("%s is both set and removed", k)
		}
		delete(m, k)
	}
	for k, val := range v.Set {
		m[k] = val
	}
	b, err := secrets.Encode(m)
	if err != nil {
		return v, nil, badParam("%s", err.Error()) // names keys and limits, never values
	}
	return v, b, nil
}

func (s *stacks) secretsSet(_ context.Context, _ Caller, p json.RawMessage) (any, error) {
	v, _, err := s.checkSecretsSet(p)
	if err != nil {
		return nil, err
	}
	defer s.locks.lock(v.Name)()
	_, b, err := s.checkSecretsSet(p) // again under the lock: the file may have changed
	if err != nil {
		return nil, err
	}
	if err := s.c.Store.Write(v.Name, map[string][]byte{FileSecrets: b}); err != nil {
		return nil, Errorf(CodeFailed, "the stack's secrets could not be written")
	}
	// Containers get the new values when they are next created: a start
	// (up) recreates those whose values changed.
	return map[string]bool{"done": true}, nil
}

func (s *stacks) checkReveal(p json.RawMessage) (StackSecretRevealParams, error) {
	var v StackSecretRevealParams
	if err := decodeParams(p, &v); err != nil {
		return v, err
	}
	if _, _, _, err := s.stored(v.Name); err != nil {
		return v, err
	}
	if !secrets.ValidKey(v.Key) {
		return v, badParam("not a secret name")
	}
	m, err := s.current(v.Name)
	if err != nil {
		return v, err
	}
	if _, ok := m[v.Key]; !ok {
		return v, badParam("%s is not set", v.Key)
	}
	return v, nil
}

func (s *stacks) secretsReveal(_ context.Context, _ Caller, p json.RawMessage) (any, error) {
	v, err := s.checkReveal(p)
	if err != nil {
		return nil, err
	}
	m, err := s.current(v.Name)
	if err != nil {
		return nil, err
	}
	val, ok := m[v.Key]
	if !ok {
		return nil, Errorf(CodeNotFound, "%s is not set", v.Key)
	}
	return map[string]string{"value": val}, nil
}

// secretList is stack.status's view: every key a service names, or the
// file holds, with whether it is set.
func (s *stacks) secretList(name string, sp spec.Spec) []StackSecret {
	refs := referenced(sp)
	m, err := s.current(name)
	if err != nil {
		m = map[string]string{}
	}
	keys := make([]string, 0, len(refs)+len(m))
	for k := range refs {
		keys = append(keys, k)
	}
	for k := range m {
		if refs[k] == nil {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]StackSecret, 0, len(keys))
	for _, k := range keys {
		_, set := m[k]
		svcs := refs[k]
		if svcs == nil {
			svcs = []string{}
		}
		out = append(out, StackSecret{Key: k, Services: svcs, Set: set})
	}
	return out
}

// redactSecretsSet keeps the stack and key names of a set for the audit.
func redactSecretsSet(p json.RawMessage) json.RawMessage {
	var v StackSecretsSetParams
	if err := json.Unmarshal(p, &v); err != nil {
		return nil
	}
	keys := make([]string, 0, len(v.Set))
	for k := range v.Set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	remove := append([]string{}, v.Remove...)
	sort.Strings(remove)
	b, err := json.Marshal(map[string]any{"name": v.Name, "set": keys, "remove": remove})
	if err != nil {
		return nil
	}
	return b
}
