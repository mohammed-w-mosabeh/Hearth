package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"sync"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
)

// Polkit asks polkitd whether the calling process may perform an action,
// letting it show the desktop's authentication dialog.
type Polkit struct {
	Dial func(ctx context.Context) (*dbusx.Conn, error)
}

// subject is polkit's (sa{sv}) Subject.
type subject struct {
	Kind    string
	Details map[string]dbus.Variant
}

// authResult is CheckAuthorization's (bba{ss}).
type authResult struct {
	IsAuthorized bool
	IsChallenge  bool
	Details      map[string]string
}

// pkAllowUserInteraction is CheckAuthorizationFlags.AllowUserInteraction.
const pkAllowUserInteraction = uint32(1)

// Check implements Authority. The subject is the caller's process (pid
// with its start time, so a reused pid does not match); without a start
// time it refuses.
func (p Polkit) Check(ctx context.Context, c Caller, action string) (bool, error) {
	return p.check(ctx, c, action, nil, true)
}

// CheckDetails implements DetailedAuthority: polkit shows details in its
// dialog, and since the broker runs as root it honours polkit.message.
func (p Polkit) CheckDetails(ctx context.Context, c Caller, action string, details map[string]string) (bool, error) {
	return p.check(ctx, c, action, details, true)
}

// CheckQuiet implements QuietAuthority: no user interaction is allowed, so
// polkit answers without a dialog and a challenge is a refusal.
func (p Polkit) CheckQuiet(ctx context.Context, c Caller, action string) (bool, error) {
	return p.check(ctx, c, action, nil, false)
}

func (p Polkit) check(ctx context.Context, c Caller, action string, details map[string]string, interactive bool) (bool, error) {
	if details == nil {
		details = map[string]string{}
	}
	if c.PID <= 0 || c.Start == 0 {
		return false, errors.New("caller process unknown")
	}
	conn, err := p.Dial(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = conn.Close() }()
	subj := subject{Kind: "unix-process", Details: map[string]dbus.Variant{
		"pid":        dbus.MakeVariant(uint32(c.PID)), // #nosec G115 -- a positive pid
		"start-time": dbus.MakeVariant(c.Start),
		"uid":        dbus.MakeVariant(int32(c.UID)), // #nosec G115 -- uids fit in int32
	}}
	var res authResult
	const dest, path, method = "org.freedesktop.PolicyKit1", "/org/freedesktop/PolicyKit1/Authority", "org.freedesktop.PolicyKit1.Authority.CheckAuthorization"
	if interactive {
		err = conn.CallInteractive(ctx, dest, path, method, []any{&res}, subj, action, details, pkAllowUserInteraction, "")
	} else {
		err = conn.Call(ctx, dest, path, method, []any{&res}, subj, action, details, uint32(0), "")
	}
	if err != nil {
		return false, err
	}
	return res.IsAuthorized, nil
}

// GroupMember reports whether uid is a member of group (primary or
// supplementary), from the user and group databases.
func GroupMember(group string) func(uid uint32) bool {
	return func(uid uint32) bool {
		g, err := user.LookupGroup(group)
		if err != nil {
			return false
		}
		u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
		if err != nil {
			return false
		}
		ids, err := u.GroupIds()
		if err != nil {
			return false
		}
		for _, id := range ids {
			if id == g.Gid {
				return true
			}
		}
		return u.Gid == g.Gid
	}
}

// FileAudit appends audit records as JSON lines to Path (mode 0600). When
// the file passes MaxBytes it is moved to Path+".1" (replacing it).
type FileAudit struct {
	Path     string
	MaxBytes int64

	mu sync.Mutex
}

// Record implements Auditor. A record that cannot be written is reported
// on stderr (the journal), never dropped silently.
func (a *FileAudit) Record(r AuditRecord) {
	a.mu.Lock()
	defer a.mu.Unlock()
	line, err := json.Marshal(r)
	if err != nil {
		return
	}
	if fi, err := os.Stat(a.Path); err == nil && a.MaxBytes > 0 && fi.Size() >= a.MaxBytes {
		_ = os.Rename(a.Path, a.Path+".1")
	}
	f, err := os.OpenFile(a.Path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err == nil {
		_, err = f.Write(append(line, '\n'))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "hearth-broker: audit write failed: %v: %s\n", err, line)
	}
}

func noParams(p json.RawMessage) error {
	if len(p) > 0 && string(p) != "null" && string(p) != "{}" {
		return Errorf(CodeBadParams, "this method takes no params")
	}
	return nil
}

// BaseMethods are the methods every broker has.
//   - broker.ping: who the broker sees, its version and methods
//   - broker.authorize: privileged and does nothing else, so hearthd can
//     check the polkit path (and the user can authorize once, with
//     auth_admin_keep) before a real privileged action
func BaseMethods(version string, names func() []string) []Method {
	return []Method{
		{Name: "broker.ping", Handle: func(_ context.Context, c Caller, p json.RawMessage) (any, error) {
			if err := noParams(p); err != nil {
				return nil, err
			}
			return PingResult{Version: version, UID: c.UID, Methods: names()}, nil
		}},
		{Name: "broker.authorize", Privileged: true, Check: func(_ context.Context, p json.RawMessage) error { return noParams(p) }, Handle: func(_ context.Context, _ Caller, p json.RawMessage) (any, error) {
			if err := noParams(p); err != nil {
				return nil, err
			}
			return map[string]bool{"authorized": true}, nil
		}},
	}
}
