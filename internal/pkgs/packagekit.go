// Package pkgs installs, removes and updates packages through PackageKit
// on the system bus (SECURITY S9), simulating every change first
// (SECURITY S15). PackageKit asks polkit itself, so hearthd needs no root
// and no broker for this.
package pkgs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/mohammed-w-mosabeh/hearth/internal/dbusx"
)

const (
	pkService = "org.freedesktop.PackageKit"
	pkPath    = dbus.ObjectPath("/org/freedesktop/PackageKit")
	pkTx      = "org.freedesktop.PackageKit.Transaction"
)

// PackageKit enums (pk-enum.h). Filters and transaction flags are
// bitfields: 1 << the enum value.
const (
	filterInstalled    = uint64(1) << 2
	filterNotInstalled = uint64(1) << 3
	filterNewest       = uint64(1) << 16
	filterArch         = uint64(1) << 18

	flagOnlyTrusted = uint64(1) << 1
	flagSimulate    = uint64(1) << 2

	exitSuccess = 1
)

// infoNames is what a transaction does to a package (PkInfoEnum).
var infoNames = map[uint32]string{
	1: "installed", 2: "available", 3: "low", 4: "enhancement", 5: "normal", 6: "bugfix",
	7: "important", 8: "security", 9: "blocked", 10: "downloading", 11: "updating",
	12: "installing", 13: "removing", 14: "cleanup", 15: "obsoleting", 19: "reinstalling",
	20: "downgrading", 21: "preparing", 22: "decompressing", 23: "untrusted", 24: "trusted",
	25: "unavailable", 26: "critical",
}

// Change is one package a transaction touches.
type Change struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Arch    string `json:"arch"`
	Info    string `json:"info"` // installing, removing, updating, ...
	Summary string `json:"summary,omitempty"`
	ID      string `json:"-"` // PackageKit's "name;version;arch;data"
}

// Bus is a system bus connection (dbusx.Conn in production).
type Bus interface {
	dbusx.Bus
	Close() error
}

// Client runs PackageKit transactions.
type Client struct {
	Dial func(ctx context.Context) (Bus, error)
	// Wait bounds a transaction after it is started (a large upgrade
	// downloads for a while); default 30 min.
	Wait time.Duration
}

// TxError is a transaction that ended with a PackageKit error.
type TxError struct {
	Code    string // PackageKit's error name, e.g. "not-authorized"
	Details string
}

func (e *TxError) Error() string {
	if e.Details == "" {
		return e.Code
	}
	return e.Code + ": " + e.Details
}

// errorNames is PkErrorEnum, by value (as libpackagekit-glib2 1.3 names
// them).
var errorNames = []string{
	"unknown", "out-of-memory", "no-network", "not-supported", "internal-error", "gpg-failure",
	"package-id-invalid", "package-not-installed", "package-not-found", "package-already-installed",
	"package-download-failed", "group-not-found", "group-list-invalid", "dep-resolution-failed",
	"filter-invalid", "create-thread-failed", "transaction-error", "transaction-cancelled", "no-cache",
	"repo-not-found", "cannot-remove-system-package", "process-kill", "failed-initialization",
	"failed-finalise", "failed-config-parsing", "cannot-cancel", "cannot-get-lock",
	"no-packages-to-update", "cannot-write-repo-config", "local-install-failed", "bad-gpg-signature",
	"missing-gpg-signature", "cannot-install-source-package", "repo-configuration-error",
	"no-license-agreement", "file-conflicts", "package-conflicts", "repo-not-available",
	"invalid-package-file", "package-install-blocked", "package-corrupt",
	"all-packages-already-installed", "file-not-found", "no-more-mirrors-to-try",
	"no-distro-upgrade-data", "incompatible-architecture", "no-space-on-device",
	"media-change-required", "not-authorized", "update-not-found", "cannot-install-repo-unsigned",
	"cannot-update-repo-unsigned", "cannot-get-filelist", "cannot-get-requires",
	"cannot-disable-repository", "restricted-download", "package-failed-to-configure",
	"package-failed-to-build", "package-failed-to-install", "package-failed-to-remove",
	"failed-due-to-running-process", "package-database-changed", "provide-type-not-supported",
	"install-root-invalid", "cannot-fetch-sources", "cancelled-priority", "unfinished-transaction",
	"lock-required", "repo-already-set",
}

// ParseID splits PackageKit's "name;version;arch;data".
func ParseID(id string) (name, version, arch string, ok bool) {
	f := strings.Split(id, ";")
	if len(f) != 4 || f[0] == "" {
		return "", "", "", false
	}
	return f[0], f[1], f[2], true
}

// transaction creates a transaction, runs method on it and collects the
// packages it reports until it finishes.
func (c *Client) transaction(ctx context.Context, interactive bool, method string, args ...any) ([]Change, error) {
	wait := c.Wait
	if wait == 0 {
		wait = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	bus, err := c.Dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("pkgs: system bus: %w", err)
	}
	defer func() { _ = bus.Close() }()
	var tx dbus.ObjectPath
	if err := bus.Call(ctx, pkService, pkPath, "org.freedesktop.PackageKit.CreateTransaction", []any{&tx}); err != nil {
		return nil, fmt.Errorf("pkgs: PackageKit is not available: %w", err)
	}
	// Watch before starting, so no signal is missed.
	sigs, err := bus.Watch(ctx, dbus.WithMatchObjectPath(tx), dbus.WithMatchInterface(pkTx))
	if err != nil {
		return nil, err
	}
	hints := []string{"locale=C.UTF-8", "background=false", "interactive=" + fmt.Sprint(interactive)}
	if err := bus.Call(ctx, pkService, tx, pkTx+".SetHints", nil, hints); err != nil {
		return nil, err
	}
	if err := bus.Call(ctx, pkService, tx, pkTx+"."+method, nil, args...); err != nil {
		return nil, err
	}
	var out []Change
	var txErr *TxError
	for {
		select {
		case <-ctx.Done():
			return nil, errors.New("pkgs: PackageKit did not finish in time")
		case s, ok := <-sigs:
			if !ok {
				return nil, errors.New("pkgs: lost the connection to PackageKit")
			}
			if s.Path != tx {
				continue
			}
			switch s.Name {
			case pkTx + ".Package":
				if len(s.Body) != 3 {
					continue
				}
				info, _ := s.Body[0].(uint32)
				id, _ := s.Body[1].(string)
				summary, _ := s.Body[2].(string)
				name, version, arch, ok := ParseID(id)
				if !ok {
					continue
				}
				in := infoNames[info]
				if in == "" {
					in = fmt.Sprint("info-", info)
				}
				out = append(out, Change{Name: name, Version: version, Arch: arch, Info: in, Summary: summary, ID: id})
			case pkTx + ".ErrorCode":
				if len(s.Body) == 2 {
					code, _ := s.Body[0].(uint32)
					details, _ := s.Body[1].(string)
					name := fmt.Sprint("error-", code)
					if int(code) < len(errorNames) {
						name = errorNames[code]
					}
					txErr = &TxError{Code: name, Details: details}
				}
			case pkTx + ".Finished":
				exit := uint32(0)
				if len(s.Body) >= 1 {
					exit, _ = s.Body[0].(uint32)
				}
				if txErr != nil {
					return out, txErr
				}
				if exit != exitSuccess {
					return out, &TxError{Code: "failed", Details: fmt.Sprint("exit ", exit)}
				}
				return out, nil
			}
		}
	}
}

// Resolve finds the PackageKit IDs of names: installed ones, or for
// install, the newest available for this machine's architecture.
func (c *Client) Resolve(ctx context.Context, installed bool, names []string) ([]Change, error) {
	filter := filterInstalled
	if !installed {
		filter = filterNotInstalled | filterNewest | filterArch
	}
	return c.transaction(ctx, false, "Resolve", filter, names)
}

// Updates lists the available updates.
func (c *Client) Updates(ctx context.Context) ([]Change, error) {
	return c.transaction(ctx, false, "GetUpdates", uint64(0))
}

// Op is a package operation.
type Op string

const (
	OpInstall Op = "install"
	OpRemove  Op = "remove"
	OpUpdate  Op = "update"
)

// Simulate reports what op on ids would change, without changing it.
func (c *Client) Simulate(ctx context.Context, op Op, ids []string) ([]Change, error) {
	return c.do(ctx, op, ids, flagSimulate)
}

// Apply runs op on ids for real. PackageKit asks polkit, which may show
// the user a password dialog.
func (c *Client) Apply(ctx context.Context, op Op, ids []string) error {
	_, err := c.do(ctx, op, ids, flagOnlyTrusted)
	return err
}

func (c *Client) do(ctx context.Context, op Op, ids []string, flags uint64) ([]Change, error) {
	interactive := flags&flagSimulate == 0
	switch op {
	case OpInstall:
		return c.transaction(ctx, interactive, "InstallPackages", flags, ids)
	case OpRemove:
		// allow_deps: packages that depend on these go too (the preview
		// lists them); autoremove: no, orphans stay until asked.
		return c.transaction(ctx, interactive, "RemovePackages", flags, ids, true, false)
	case OpUpdate:
		return c.transaction(ctx, interactive, "UpdatePackages", flags, ids)
	}
	return nil, fmt.Errorf("pkgs: unknown operation %q", op)
}
