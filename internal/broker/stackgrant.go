package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// Unattended grants (ROADMAP 10.8, SECURITY S15b): the owner allows, once
// and with a password, a trigger to stop (and, if chosen, start) one stack
// with nobody there. A grant is per stack and per user, root-owned, checked
// here, and lapses after 90 days. Its risk is an outage, not privilege:
// anything that reaches hearthd can then stop that stack without a password.
const (
	ActionStackGrant = "org.hearth.stack.grant" // auth_admin, no keep
	// GrantTTL is how long a grant lasts; setting it again renews it.
	GrantTTL = 90 * 24 * time.Hour
	// fileGrants holds every grant, at the top of the stack store. It is a
	// file, so List, which lists folders, never takes it for a stack.
	fileGrants = ".grants.json"
	maxGrants  = 256
)

// StackGrant is one grant.
type StackGrant struct {
	Name    string   `json:"name"`
	Verbs   []string `json:"verbs"`
	UID     uint32   `json:"uid"`
	Granted int64    `json:"granted"` // unix ms
	Until   int64    `json:"until"`   // unix ms
}

// StackGrantParams is stack.grant.set's params.
type StackGrantParams struct {
	Name  string `json:"name"`
	Start bool   `json:"start"` // also allow start; stop is always allowed
}

// StackGrantName is stack.grant.revoke's params.
type StackGrantName struct {
	Name string `json:"name"`
}

type grantStore struct {
	mu    sync.Mutex
	store StackStore
}

func (g *grantStore) read() (map[string]StackGrant, error) {
	out := map[string]StackGrant{}
	top, err := os.OpenRoot(g.store.dir())
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = top.Close() }()
	fi, err := top.Lstat(fileGrants)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
		return nil, errors.New("the grants file is not a small regular file")
	}
	b, err := top.ReadFile(fileGrants)
	if err != nil {
		return nil, err
	}
	var list []StackGrant
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("the grants file is damaged: %w", err)
	}
	for _, gr := range list {
		out[grantKey(gr.Name, gr.UID)] = gr
	}
	return out, nil
}

func (g *grantStore) write(m map[string]StackGrant) error {
	list := make([]StackGrant, 0, len(m))
	for _, gr := range m {
		list = append(list, gr)
	}
	sort.Slice(list, func(i, j int) bool { return grantKey(list[i].Name, list[i].UID) < grantKey(list[j].Name, list[j].UID) })
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(g.store.dir(), 0o700); err != nil {
		return err
	}
	top, err := os.OpenRoot(g.store.dir())
	if err != nil {
		return err
	}
	defer func() { _ = top.Close() }()
	return writeAtomic(top, fileGrants, b)
}

// drop removes every user's grant of a stack (stack.remove).
func (g *grantStore) drop(name string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	m, err := g.read()
	if err != nil {
		return err
	}
	n := len(m)
	for k, gr := range m {
		if gr.Name == name {
			delete(m, k)
		}
	}
	if len(m) == n {
		return nil
	}
	return g.write(m)
}

func grantKey(name string, uid uint32) string { return fmt.Sprintf("%s/%d", name, uid) }

// allows reports whether uid holds a live grant for every item.
func (g *grantStore) allows(uid uint32, items []StackBatchItem, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	m, err := g.read()
	if err != nil {
		return false
	}
	for _, it := range items {
		gr, ok := m[grantKey(it.Name, uid)]
		if !ok || now.UnixMilli() >= gr.Until || !contains(gr.Verbs, it.Verb) {
			return false
		}
	}
	return true
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// grantMethods are stack.grant.set (polkit, the dialog names the stack and
// what it may do), stack.grant.revoke and stack.grant.list. Revoking only
// takes power away and listing changes nothing, so neither asks: a
// password dialog on every look at the page would teach the owner to type
// it without reading.
func (s *stacks) grantMethods() []Method {
	return []Method{
		{Name: "stack.grant.set", Privileged: true, Action: ActionStackGrant,
			Check: func(_ context.Context, p json.RawMessage) error {
				_, err := s.checkGrant(p)
				return pre(err)
			},
			Authorize: func(p json.RawMessage) (string, map[string]string) {
				v, err := s.checkGrant(p)
				if err != nil {
					return "", nil
				}
				what := "stop"
				if v.Start {
					what = "stop or start"
				}
				return ActionStackGrant, map[string]string{"stack": v.Name, "allows": what,
					"polkit.message": fmt.Sprintf("Authentication is required to let Hearth %s the app %s by itself for 90 days", what, v.Name)}
			},
			Handle: s.setGrant},
		{Name: "stack.grant.revoke", Handle: s.revokeGrant},
		{Name: "stack.grant.list", Handle: s.listGrants},
	}
}

func (s *stacks) checkGrant(p json.RawMessage) (StackGrantParams, error) {
	var v StackGrantParams
	if err := decodeParams(p, &v); err != nil {
		return v, err
	}
	if _, _, _, err := s.stored(v.Name); err != nil {
		return v, err
	}
	return v, nil
}

func (s *stacks) setGrant(_ context.Context, c Caller, p json.RawMessage) (any, error) {
	v, err := s.checkGrant(p)
	if err != nil {
		return nil, err
	}
	verbs := []string{"stop"}
	if v.Start {
		verbs = append(verbs, "start")
	}
	s.unattended.mu.Lock()
	defer s.unattended.mu.Unlock()
	m, err := s.unattended.read()
	if err != nil {
		return nil, Errorf(CodeFailed, "%s", err.Error())
	}
	now := s.c.Now()
	// Grants of stacks that are gone, or lapsed, go now.
	for k, gr := range m {
		if _, _, _, err := s.stored(gr.Name); err != nil || now.UnixMilli() >= gr.Until {
			delete(m, k)
		}
	}
	if len(m) >= maxGrants {
		return nil, badParam("too many grants")
	}
	gr := StackGrant{Name: v.Name, Verbs: verbs, UID: c.UID, Granted: now.UnixMilli(), Until: now.Add(GrantTTL).UnixMilli()}
	m[grantKey(v.Name, c.UID)] = gr
	if err := s.unattended.write(m); err != nil {
		return nil, Errorf(CodeFailed, "the grant could not be written")
	}
	return gr, nil
}

func (s *stacks) revokeGrant(_ context.Context, c Caller, p json.RawMessage) (any, error) {
	var v StackGrantName
	if err := decodeParams(p, &v); err != nil {
		return nil, err
	}
	if !spec.ValidName(v.Name) {
		return nil, badParam("not a stack name")
	}
	s.unattended.mu.Lock()
	defer s.unattended.mu.Unlock()
	m, err := s.unattended.read()
	if err != nil {
		return nil, Errorf(CodeFailed, "%s", err.Error())
	}
	delete(m, grantKey(v.Name, c.UID))
	if err := s.unattended.write(m); err != nil {
		return nil, Errorf(CodeFailed, "the grant could not be removed")
	}
	return map[string]bool{"done": true}, nil
}

// listGrants returns the caller's live grants.
func (s *stacks) listGrants(_ context.Context, c Caller, p json.RawMessage) (any, error) {
	if err := decodeParams(p, &struct{}{}); err != nil {
		return nil, err
	}
	s.unattended.mu.Lock()
	defer s.unattended.mu.Unlock()
	m, err := s.unattended.read()
	if err != nil {
		return nil, Errorf(CodeFailed, "%s", err.Error())
	}
	now := s.c.Now().UnixMilli()
	out := []StackGrant{}
	for _, gr := range m {
		if gr.UID == c.UID && now < gr.Until {
			out = append(out, gr)
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].Name, out[j].Name) < 0 })
	return out, nil
}
