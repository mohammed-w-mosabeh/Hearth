package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mohammed-w-mosabeh/hearth/internal/pathcheck"
	"github.com/mohammed-w-mosabeh/hearth/internal/safeexec"
	"github.com/mohammed-w-mosabeh/hearth/internal/secret"
	"github.com/mohammed-w-mosabeh/hearth/internal/stacks/spec"
)

// Stack lifecycle (ROADMAP 9.4, SECURITY S12b): deploy, update, start,
// stop, restart, remove and status. Every privileged method checks its
// params, the spec and the bind paths before polkit, so a refusal raises no
// dialog; after polkit the paths are checked again and must resolve as
// before, the files are written and the unit runs; then each running
// container's mounts are compared with the check, and a stack whose mounts
// differ is stopped.
const (
	ActionStackDeploy  = "org.hearth.stack.deploy"  // deploy and update, auth_admin
	ActionStackControl = "org.hearth.stack.control" // start, stop, restart, auth_admin_keep
	ActionStackRemove  = "org.hearth.stack.remove"  // auth_admin
	// ActionStackDanger replaces deploy for a spec with danger flags, which
	// its dialog names: auth_admin, no keep, one prompt per deploy or update.
	ActionStackDanger = "org.hearth.stack.danger"

	// StackTimeout bounds a deploy or update, pulls included.
	StackTimeout = 30 * time.Minute
	// pathCacheTTL keeps a pre-polkit path answer while the dialog is open.
	pathCacheTTL = 15 * time.Minute
)

// StackContainer is one container of a stack, as stack.status shows it.
type StackContainer struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Service      string `json:"service"`
	State        string `json:"state"`
	Health       string `json:"health,omitempty"`
	Image        string `json:"image"`
	ImageID      string `json:"image_id"`
	Restart      string `json:"restart"`
	RestartCount int    `json:"restart_count"`
	StartedAt    string `json:"started_at,omitempty"`
	// Stack is the org.hearth.stack label: empty on a Compose project
	// Hearth does not manage.
	Stack string `json:"-"`
	PID   int    `json:"-"`
}

// StackEngine is what the stack methods need from the Docker Engine.
type StackEngine interface {
	// StackContainers lists every container of Compose project name.
	StackContainers(ctx context.Context, name string) ([]StackContainer, error)
	// PullTag pulls name@digest and tags it name:tag.
	PullTag(ctx context.Context, name, digest, tag string) error
}

// StackPaths asks hearth-pathcheck (PathChecker in production).
type StackPaths interface {
	Check(ctx context.Context, spec json.RawMessage) (pathcheck.Reply, error)
	Recheck(ctx context.Context, spec json.RawMessage, first pathcheck.Reply) (pathcheck.Reply, error)
}

// StackConfig configures StackMethods.
type StackConfig struct {
	Store  StackStore
	Paths  StackPaths
	Engine StackEngine
	// Run starts hearth-stack@<name>.service and waits for it; by default
	// through Bus, as the other oneshot units are.
	Run      func(ctx context.Context, unit string) error
	Bus      func(ctx context.Context) (SystemBus, error)
	ProcRoot string // default "/proc"
	Now      func() time.Time
	// Audit gets one entry per container that runs with danger flags.
	Audit Auditor

	// Backups (9.10). Measure asks the path check for sizes (by default
	// PathChecker's); Volumes reads named volumes (the Engine); Backups is
	// the archives' folder (backup.Dir); FreeSpace reads a filesystem's
	// free bytes (statfs).
	Measure   func(ctx context.Context, spec json.RawMessage) (pathcheck.Reply, error)
	Volumes   BackupVolumes
	Backups   string
	FreeSpace func(path string) (uint64, error)
	// Scheduled backups (9.11): UnitDir holds the timers (SystemUnitDir);
	// Analyze runs systemd-analyze calendar (safeexec.Run).
	UnitDir string
	Analyze Runner
}

// StackDeployParams is stack.deploy's params.
type StackDeployParams struct {
	Spec json.RawMessage `json:"spec"`
	// AckFlags must equal the spec's danger flags exactly.
	AckFlags []string `json:"ack_flags"`
}

// StackUpdateParams is stack.update's params: the stored spec the preview
// read, and for each service to update the digest the preview showed.
type StackUpdateParams struct {
	Name       string            `json:"name"`
	SpecSHA256 string            `json:"spec_sha256"`
	Pins       map[string]string `json:"pins"`
}

// StackControlParams is stack.control's params.
type StackControlParams struct {
	Name string `json:"name"`
	Verb string `json:"verb"` // start, stop or restart
}

// StackRemoveParams is stack.remove's params.
type StackRemoveParams struct {
	Name string `json:"name"`
	// RemoveVolumes also deletes the stack's named volumes, and needs the
	// stack's name typed back in TypedName.
	RemoveVolumes bool   `json:"remove_volumes"`
	TypedName     string `json:"typed_name"`
}

// StackInfo is one stack in stack.status.
type StackInfo struct {
	Name       string           `json:"name"`
	SpecSHA256 string           `json:"spec_sha256"`
	Spec       json.RawMessage  `json:"spec"`  // environment values S19 masks are masked
	State      string           `json:"state"` // running, partial, stopped or absent
	Containers []StackContainer `json:"containers"`
	Secrets    []StackSecret    `json:"secrets"` // names only
}

// controlVerbs maps stack.control's verbs to the unit's. start is up
// without a pull, so a stack taken down comes back.
var controlVerbs = map[string]string{"start": "up", "stop": "stop", "restart": "restart"}

var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// pathCache holds pre-polkit path answers, so the check after polkit is
// compared with the one made before the dialog.
type pathCache struct {
	mu  sync.Mutex
	m   map[string]cachedPaths
	now func() time.Time
}

type cachedPaths struct {
	r  pathcheck.Reply
	at time.Time
}

func (c *pathCache) key(raw []byte) string {
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func (c *pathCache) put(raw []byte, r pathcheck.Reply) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.m {
		if c.now().Sub(v.at) > pathCacheTTL {
			delete(c.m, k)
		}
	}
	if len(c.m) >= 32 {
		clear(c.m)
	}
	c.m[c.key(raw)] = cachedPaths{r: r, at: c.now()}
}

func (c *pathCache) take(raw []byte) (pathcheck.Reply, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := c.key(raw)
	v, ok := c.m[k]
	delete(c.m, k)
	if !ok || c.now().Sub(v.at) > pathCacheTTL {
		return pathcheck.Reply{}, false
	}
	return v.r, true
}

type stackLocks struct {
	mu  sync.Mutex
	m   map[string]*sync.Mutex
	dir string // the lock files' folder: the stack store
}

// lock holds name's mutex, then its lock file, which a scheduled backup
// (another process) takes too.
func (l *stackLocks) lock(name string) func() {
	l.mu.Lock()
	m, ok := l.m[name]
	if !ok {
		m = &sync.Mutex{}
		l.m[name] = m
	}
	l.mu.Unlock()
	m.Lock()
	unlock := flock(l.dir, name)
	return func() {
		unlock()
		m.Unlock()
	}
}

type stacks struct {
	c      StackConfig
	cache  *pathCache
	locks  *stackLocks
	grants *restoreGrants
}

// newStacks fills c's defaults.
func newStacks(c StackConfig) *stacks {
	if c.ProcRoot == "" {
		c.ProcRoot = "/proc"
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Run == nil {
		c.Run = func(ctx context.Context, unit string) error { return runOneshot(ctx, c.Bus, 0, "stack", unit) }
	}
	if c.Measure == nil {
		c.Measure = PathChecker{}.Measure
	}
	if c.FreeSpace == nil {
		c.FreeSpace = FreeSpace
	}
	if c.Analyze == nil {
		c.Analyze = safeexec.Run
	}
	return &stacks{c: c, cache: &pathCache{m: map[string]cachedPaths{}, now: c.Now},
		locks:  &stackLocks{m: map[string]*sync.Mutex{}, dir: c.Store.dir()},
		grants: &restoreGrants{m: map[string]restoreGrant{}, now: c.Now}}
}

// StackLifeMethods are stack.status, stack.deploy, stack.update,
// stack.control, stack.batch, stack.remove, stack.secrets.set and
// stack.secrets.reveal.
func StackLifeMethods(c StackConfig) []Method {
	s := newStacks(c)
	return append(append(append(s.secretMethods(), s.importMethod(), s.scheduleMethod(), s.batchMethod()), s.backupMethods()...), []Method{
		{Name: "stack.status", Handle: s.status},
		{Name: "stack.deploy", Privileged: true, Action: ActionStackDeploy, Timeout: StackTimeout,
			Check: func(ctx context.Context, p json.RawMessage) error {
				_, err := s.checkDeploy(ctx, p, true)
				return pre(err)
			},
			Handle: s.deploy, Authorize: s.authorizeDeploy},
		{Name: "stack.update", Privileged: true, Action: ActionStackDeploy, Timeout: StackTimeout,
			Check: func(ctx context.Context, p json.RawMessage) error {
				_, _, err := s.checkUpdate(ctx, p, true)
				return pre(err)
			},
			Handle: s.update, Authorize: s.authorizeUpdate},
		{Name: "stack.control", Privileged: true, Action: ActionStackControl, Timeout: 10 * time.Minute,
			Check: func(ctx context.Context, p json.RawMessage) error {
				_, _, err := s.checkControl(ctx, p, true)
				return pre(err)
			},
			Handle: s.control},
		{Name: "stack.remove", Privileged: true, Action: ActionStackRemove, Timeout: 10 * time.Minute,
			Check:  func(ctx context.Context, p json.RawMessage) error { _, err := s.checkRemove(p); return pre(err) },
			Handle: s.remove},
	}...)
}

// --- checks, run before polkit and again after it ---

// pre makes a missing stack a refusal before polkit: only CodeBadParams
// stops a call there, and no dialog should ask about a stack that is not.
func pre(err error) error {
	var be *Error
	if errors.As(err, &be) && be.Code == CodeNotFound {
		return badParam("%s", be.Message)
	}
	return err
}

// paths asks the path check for raw. Before polkit (pre) the answer is
// cached; after it, the cached answer is the first and a new check must
// resolve the same.
func (s *stacks) paths(ctx context.Context, raw []byte, pre bool) (pathcheck.Reply, error) {
	if pre {
		r, err := s.c.Paths.Check(ctx, raw)
		if err != nil {
			return r, Errorf(CodeFailed, "%s", err.Error())
		}
		if !r.OK {
			return r, badParam("refused: %s", strings.Join(r.Refused, "; "))
		}
		s.cache.put(raw, r)
		return r, nil
	}
	first, ok := s.cache.take(raw)
	if !ok {
		var err error
		if first, err = s.c.Paths.Check(ctx, raw); err != nil {
			return first, Errorf(CodeFailed, "%s", err.Error())
		}
	}
	if !first.OK {
		return first, Errorf(CodeRefused, "refused: %s", strings.Join(first.Refused, "; "))
	}
	r, err := s.c.Paths.Recheck(ctx, raw, first)
	if err != nil {
		return r, Errorf(CodeFailed, "%s", err.Error())
	}
	if !r.OK {
		return r, Errorf(CodeRefused, "refused: %s", strings.Join(r.Refused, "; "))
	}
	return r, nil
}

// foreign refuses a name a Compose project outside Hearth already uses:
// deploying would take its containers over; stack.import does that, after
// its own confirmation. Once the stack is in the store the name is Hearth's,
// so a deploy can finish an import whose up failed half way.
func (s *stacks) foreign(ctx context.Context, name string) error {
	if _, err := s.c.Store.Read(name, FileSpec); err == nil {
		return nil
	}
	list, err := s.c.Engine.StackContainers(ctx, name)
	if err != nil {
		return err
	}
	for _, c := range list {
		if c.Stack != name {
			return badParam("a Compose project named %s runs outside Hearth; import it instead", name)
		}
	}
	return nil
}

type deployCheck struct {
	spec spec.Spec
	raw  []byte
}

func (s *stacks) checkDeploy(ctx context.Context, p json.RawMessage, pre bool) (deployCheck, error) {
	var v StackDeployParams
	if err := decodeParams(p, &v); err != nil {
		return deployCheck{}, err
	}
	if len(v.Spec) == 0 {
		return deployCheck{}, badParam("no spec")
	}
	sp, rep, err := spec.Parse(v.Spec)
	if err != nil {
		return deployCheck{}, badParam("%s", err.Error())
	}
	if !rep.OK() {
		return deployCheck{}, badParam("refused: %s", strings.Join(rep.Refused, "; "))
	}
	ack := slices.Clone(v.AckFlags)
	sort.Strings(ack)
	if !slices.Equal(ack, rep.Flags) {
		return deployCheck{}, badParam("the acknowledged flags are not the spec's: %s", strings.Join(rep.Flags, ", "))
	}
	if pre {
		if _, err := s.paths(ctx, v.Spec, true); err != nil {
			return deployCheck{}, err
		}
	}
	// Last: an Engine that cannot be asked must not skip the checks above.
	if err := s.foreign(ctx, sp.Name); err != nil {
		return deployCheck{}, err
	}
	return deployCheck{spec: sp, raw: v.Spec}, nil
}

// stored reads a stack's checked spec and its hash.
func (s *stacks) stored(name string) (spec.Spec, []byte, string, error) {
	if !spec.ValidName(name) {
		return spec.Spec{}, nil, "", badParam("not a stack name")
	}
	raw, err := s.c.Store.Read(name, FileSpec)
	if errors.Is(err, os.ErrNotExist) {
		return spec.Spec{}, nil, "", Errorf(CodeNotFound, "no such stack")
	}
	if err != nil {
		return spec.Spec{}, nil, "", Errorf(CodeFailed, "the stack could not be read")
	}
	sp, rep, err := spec.Parse(raw)
	if err != nil || !rep.OK() {
		return spec.Spec{}, nil, "", Errorf(CodeFailed, "the stored spec no longer passes the check; deploy it again")
	}
	h := sha256.Sum256(raw)
	return sp, raw, hex.EncodeToString(h[:]), nil
}

func (s *stacks) checkUpdate(ctx context.Context, p json.RawMessage, pre bool) (StackUpdateParams, []byte, error) {
	var v StackUpdateParams
	if err := decodeParams(p, &v); err != nil {
		return v, nil, err
	}
	sp, raw, sum, err := s.stored(v.Name)
	if err != nil {
		return v, nil, err
	}
	if v.SpecSHA256 != sum {
		return v, nil, badParam("the stack changed since the preview")
	}
	if len(v.Pins) == 0 {
		return v, nil, badParam("nothing to update")
	}
	for svc, d := range v.Pins {
		def, ok := sp.Services[svc]
		switch {
		case !ok:
			return v, nil, badParam("no service %q", clipName(svc))
		case !digestRe.MatchString(d):
			return v, nil, badParam("%s: not a sha256 digest", svc)
		case def.PullPolicy == "never":
			return v, nil, badParam("%s: a local image is not updated", svc)
		}
		if _, _, dg, ok := ParseImageRef(def.Image); !ok || dg != "" {
			return v, nil, badParam("%s: its image is pinned by digest in the spec", svc)
		}
	}
	if pre {
		if _, err := s.paths(ctx, raw, true); err != nil {
			return v, nil, err
		}
	}
	return v, raw, nil
}

func (s *stacks) checkControl(ctx context.Context, p json.RawMessage, pre bool) (StackControlParams, []byte, error) {
	var v StackControlParams
	if err := decodeParams(p, &v); err != nil {
		return v, nil, err
	}
	if _, ok := controlVerbs[v.Verb]; !ok {
		return v, nil, badParam("verb must be start, stop or restart")
	}
	_, raw, _, err := s.stored(v.Name)
	if err != nil {
		return v, nil, err
	}
	// Start and restart mount the binds again: Docker resolves them anew.
	if pre && v.Verb != "stop" {
		if _, err := s.paths(ctx, raw, true); err != nil {
			return v, nil, err
		}
	}
	return v, raw, nil
}

func (s *stacks) checkRemove(p json.RawMessage) (StackRemoveParams, error) {
	var v StackRemoveParams
	if err := decodeParams(p, &v); err != nil {
		return v, err
	}
	if _, _, _, err := s.stored(v.Name); err != nil {
		return v, err
	}
	if v.RemoveVolumes && v.TypedName != v.Name {
		return v, badParam("type the stack's name to remove its volumes")
	}
	if !v.RemoveVolumes && v.TypedName != "" {
		return v, badParam("typed_name is only for removing volumes")
	}
	return v, nil
}

// --- handlers, after polkit ---

func (s *stacks) deploy(ctx context.Context, c Caller, p json.RawMessage) (any, error) {
	chk, err := s.checkDeploy(ctx, p, false)
	if err != nil {
		return nil, err
	}
	defer s.locks.lock(chk.spec.Name)()
	if err := s.foreign(ctx, chk.spec.Name); err != nil {
		return nil, err
	}
	paths, err := s.paths(ctx, chk.raw, false)
	if err != nil {
		return nil, err
	}
	if _, err := s.c.Store.Prepare(chk.raw, "up"); err != nil {
		return nil, Errorf(CodeFailed, "the stack's files could not be written")
	}
	res, err := s.runAndVerify(ctx, chk.spec.Name, "up", paths)
	if err == nil {
		s.auditDanger(ctx, c, "stack.deploy", chk.spec)
	}
	return res, err
}

func (s *stacks) update(ctx context.Context, c Caller, p json.RawMessage) (any, error) {
	v, _, err := s.checkUpdate(ctx, p, false)
	if err != nil {
		return nil, err
	}
	defer s.locks.lock(v.Name)()
	sp, raw, _, err := s.stored(v.Name) // read again under the lock
	if err != nil {
		return nil, err
	}
	paths, err := s.paths(ctx, raw, false)
	if err != nil {
		return nil, err
	}
	services := make([]string, 0, len(v.Pins))
	for svc := range v.Pins {
		services = append(services, svc)
	}
	sort.Strings(services)
	for _, svc := range services {
		name, tag, _, _ := ParseImageRef(sp.Services[svc].Image)
		if tag == "" {
			tag = "latest"
		}
		if err := s.c.Engine.PullTag(ctx, name, v.Pins[svc], tag); err != nil {
			return nil, err
		}
	}
	if err := s.c.Store.SetVerb(v.Name, "up"); err != nil {
		return nil, Errorf(CodeFailed, "the stack's files could not be written")
	}
	res, err := s.runAndVerify(ctx, v.Name, "up", paths)
	if err == nil {
		s.auditDanger(ctx, c, "stack.update", sp)
	}
	return res, err
}

// --- danger flags (ROADMAP 9.6) ---

// dangerAction is the action and dialog details for deploying sp: deploy
// when it has no flag, danger naming every flag when it has.
func dangerAction(sp spec.Spec, verb string) (string, map[string]string) {
	rep := spec.Check(sp)
	if len(rep.Flags) == 0 {
		return ActionStackDeploy, nil
	}
	flags := strings.Join(rep.Flags, ", ")
	return ActionStackDanger, map[string]string{
		"polkit.message": fmt.Sprintf("Hearth wants to %s the app %s with danger flags: %s", verb, sp.Name, flags),
		"app":            sp.Name,
		"flags":          flags,
	}
}

func (s *stacks) authorizeDeploy(p json.RawMessage) (string, map[string]string) {
	var v StackDeployParams
	if err := decodeParams(p, &v); err != nil {
		return ActionStackDanger, nil // Check refused it already; never the milder action
	}
	sp, _, err := spec.Parse(v.Spec)
	if err != nil {
		return ActionStackDanger, nil
	}
	return dangerAction(sp, "deploy")
}

func (s *stacks) authorizeUpdate(p json.RawMessage) (string, map[string]string) {
	var v StackUpdateParams
	if err := decodeParams(p, &v); err != nil {
		return ActionStackDanger, nil
	}
	sp, _, _, err := s.stored(v.Name)
	if err != nil {
		return ActionStackDanger, nil
	}
	return dangerAction(sp, "update")
}

// auditDanger writes one entry per container that runs with danger
// flags, naming them (SECURITY S12b).
func (s *stacks) auditDanger(ctx context.Context, c Caller, method string, sp spec.Spec) {
	rep := spec.Check(sp)
	if len(rep.ServiceFlags) == 0 || s.c.Audit == nil {
		return
	}
	list, err := s.c.Engine.StackContainers(ctx, sp.Name)
	if err != nil {
		list = nil
	}
	seen := map[string]bool{}
	now := s.c.Now()
	record := func(container, service string) {
		s.c.Audit.Record(AuditRecord{Time: now, TS: now.UnixMilli(), UID: c.UID, PID: c.PID, Method: method + ".danger",
			Result: fmt.Sprintf("ok: %s/%s container %s runs with %s", sp.Name, service, container, strings.Join(rep.ServiceFlags[service], ", "))})
	}
	for _, k := range list {
		if k.Stack == sp.Name && len(rep.ServiceFlags[k.Service]) > 0 {
			record(k.Name, k.Service)
			seen[k.Service] = true
		}
	}
	// A flagged service without a container still gets its line.
	svcs := make([]string, 0, len(rep.ServiceFlags))
	for svc := range rep.ServiceFlags {
		svcs = append(svcs, svc)
	}
	sort.Strings(svcs)
	for _, svc := range svcs {
		if !seen[svc] {
			record("(none)", svc)
		}
	}
}

func (s *stacks) control(ctx context.Context, _ Caller, p json.RawMessage) (any, error) {
	v, raw, err := s.checkControl(ctx, p, false)
	if err != nil {
		return nil, err
	}
	defer s.locks.lock(v.Name)()
	verb := controlVerbs[v.Verb]
	if v.Verb == "stop" {
		return s.run(ctx, v.Name, verb)
	}
	paths, err := s.paths(ctx, raw, false)
	if err != nil {
		return nil, err
	}
	return s.runAndVerify(ctx, v.Name, verb, paths)
}

func (s *stacks) remove(ctx context.Context, _ Caller, p json.RawMessage) (any, error) {
	v, err := s.checkRemove(p)
	if err != nil {
		return nil, err
	}
	defer s.locks.lock(v.Name)()
	verb := "down"
	if v.RemoveVolumes {
		verb = "down-volumes"
	}
	if _, err := s.run(ctx, v.Name, verb); err != nil {
		return nil, err
	}
	// Its schedule goes with it; archives stay.
	if err := s.unschedule(ctx, v.Name); err != nil {
		return nil, Errorf(CodeFailed, "the containers are gone, but the backup schedule could not be removed")
	}
	if err := s.c.Store.Remove(v.Name); err != nil {
		return nil, Errorf(CodeFailed, "the containers are gone, but the stack's files could not be removed")
	}
	return map[string]bool{"done": true}, nil
}

func (s *stacks) run(ctx context.Context, name, verb string) (any, error) {
	if err := s.c.Store.SetVerb(name, verb); err != nil {
		return nil, Errorf(CodeFailed, "the stack's files could not be written")
	}
	if err := s.c.Run(ctx, StackUnit(name)); err != nil {
		return nil, err
	}
	return map[string]bool{"done": true}, nil
}

// runAndVerify runs verb, then compares every running container's mounts
// with the path check; on a difference it stops the stack and refuses.
func (s *stacks) runAndVerify(ctx context.Context, name, verb string, paths pathcheck.Reply) (any, error) {
	if _, err := s.run(ctx, name, verb); err != nil {
		return nil, err
	}
	list, err := s.c.Engine.StackContainers(ctx, name)
	if err != nil {
		return nil, s.stopAfter(ctx, name, "its containers could not be listed to check their mounts")
	}
	var bad []string
	for _, c := range list {
		if c.PID <= 0 {
			continue
		}
		b, err := VerifyLive(s.c.ProcRoot, c.PID, c.Service, paths)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // it exited in between
			}
			return nil, s.stopAfter(ctx, name, "the mounts of "+c.Service+" could not be read")
		}
		bad = append(bad, b...)
	}
	if len(bad) > 0 {
		return nil, s.stopAfter(ctx, name, "its mounts are not the checked ones: "+strings.Join(bad, "; "))
	}
	return map[string]bool{"done": true}, nil
}

func (s *stacks) stopAfter(ctx context.Context, name, why string) error {
	if _, err := s.run(context.WithoutCancel(ctx), name, "stop"); err != nil {
		return Errorf(CodeFailed, "%s, and stopping it failed: %v", why, err)
	}
	return Errorf(CodeRefused, "the app was stopped: %s", why)
}

// --- status ---

type statusParams struct {
	Name string `json:"name"`
}

func (s *stacks) status(ctx context.Context, _ Caller, p json.RawMessage) (any, error) {
	var v statusParams
	if err := decodeParams(p, &v); err != nil {
		return nil, err
	}
	names := []string{v.Name}
	if v.Name == "" {
		var err error
		if names, err = s.c.Store.List(); err != nil {
			return nil, Errorf(CodeFailed, "the stacks could not be listed")
		}
	}
	out := []StackInfo{}
	for _, name := range names {
		sp, _, sum, err := s.stored(name)
		if err != nil {
			if v.Name != "" {
				return nil, err
			}
			continue
		}
		masked, err := json.Marshal(maskSpec(sp))
		if err != nil {
			return nil, Errorf(CodeFailed, "the spec could not be shown")
		}
		list, err := s.c.Engine.StackContainers(ctx, name)
		if err != nil {
			return nil, err
		}
		mine := []StackContainer{}
		for _, c := range list {
			if c.Stack == name {
				mine = append(mine, c)
			}
		}
		out = append(out, StackInfo{Name: name, SpecSHA256: sum, Spec: masked, State: stackState(sp, mine), Containers: mine,
			Secrets: s.secretList(name, sp)})
	}
	return out, nil
}

// maskSpec masks what S19 masks in a spec's plain environment.
func maskSpec(sp spec.Spec) spec.Spec {
	services := make(map[string]spec.Service, len(sp.Services))
	for name, svc := range sp.Services {
		if len(svc.Environment) > 0 {
			env := make(map[string]string, len(svc.Environment))
			for k, val := range svc.Environment {
				if secret.Name(k) || secret.Value(val) {
					val = secret.Mask
				}
				env[k] = val
			}
			svc.Environment = env
		}
		services[name] = svc
	}
	sp.Services = services
	return sp
}

// stackState: running when every service runs, stopped when none does,
// absent when it has no container at all.
func stackState(sp spec.Spec, list []StackContainer) string {
	if len(list) == 0 {
		return "absent"
	}
	running := map[string]bool{}
	for _, c := range list {
		if c.State == "running" {
			running[c.Service] = true
		}
	}
	switch {
	case len(running) == 0:
		return "stopped"
	case len(running) >= len(sp.Services):
		return "running"
	}
	return "partial"
}

func clipName(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.ToValidUTF8(s, "?")
}

// --- the Engine ---

// StackContainers lists project's containers with what status shows,
// reading only those fields from inspect: no environment leaves here.
func (d *Docker) StackContainers(ctx context.Context, project string) ([]StackContainer, error) {
	if !spec.ValidName(project) {
		return nil, badParam("not a stack name")
	}
	var list []struct {
		ID string `json:"Id"`
	}
	q := url.Values{"all": {"1"}, "filters": {filtersQ(map[string][]string{"label": {"com.docker.compose.project=" + project}})}}
	if err := d.getInto(ctx, "/containers/json", q, &list); err != nil {
		return nil, err
	}
	out := make([]StackContainer, 0, len(list))
	for _, c := range list {
		var in struct {
			ID     string `json:"Id"`
			Name   string
			Image  string
			Config struct {
				Image  string
				Labels map[string]string
			}
			State struct {
				Status    string
				Pid       int
				StartedAt string
				Health    *struct{ Status string }
			}
			HostConfig struct {
				RestartPolicy struct{ Name string }
			}
			RestartCount int
		}
		if !ValidContainer(c.ID) {
			continue
		}
		if err := d.getInto(ctx, "/containers/"+esc(c.ID)+"/json", nil, &in); err != nil {
			var be *Error
			if errors.As(err, &be) && be.Code == CodeNotFound {
				continue // removed in between
			}
			return nil, err
		}
		if in.Config.Labels["com.docker.compose.project"] != project {
			continue
		}
		sc := StackContainer{
			ID: in.ID, Name: strings.TrimPrefix(in.Name, "/"), Service: in.Config.Labels["com.docker.compose.service"],
			State: in.State.Status, Image: in.Config.Image, ImageID: in.Image, Restart: in.HostConfig.RestartPolicy.Name,
			RestartCount: in.RestartCount, StartedAt: in.State.StartedAt, Stack: in.Config.Labels[spec.LabelStack],
		}
		if in.State.Health != nil {
			sc.Health = in.State.Health.Status
		}
		if in.State.Status == "running" {
			sc.PID = in.State.Pid
		}
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service+out[i].Name < out[j].Service+out[j].Name })
	return out, nil
}

// PullTag pulls name@digest from its registry, without credentials, and
// tags it name:tag, so the stack's next up uses exactly that image.
func (d *Docker) PullTag(ctx context.Context, name, digest, tag string) error {
	ref := name + "@" + digest
	if n, _, dg, ok := ParseImageRef(ref); !ok || n != name || dg != digest || !digestRe.MatchString(digest) {
		return badParam("not an image digest")
	}
	if n, tg, _, ok := ParseImageRef(name + ":" + tag); !ok || n != name || tg != tag {
		return badParam("not an image tag")
	}
	params, err := json.Marshal(map[string]string{"ref": ref})
	if err != nil {
		return err
	}
	if _, err := d.imagePull(ctx, params); err != nil {
		return err
	}
	if _, err := d.do(ctx, http.MethodPost, "/images/"+ref+"/tag", url.Values{"repo": {name}, "tag": {tag}}); err != nil {
		return fmt.Errorf("tagging %s: %w", ref, err)
	}
	return nil
}
