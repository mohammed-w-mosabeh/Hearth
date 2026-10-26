// Package broker is Hearth's privileged helper protocol (ARCHITECTURE,
// SECURITY S11): the server that runs as root in hearth-broker, and the
// client hearthd uses.
//
// hearth-broker is started by its socket (/run/hearth/broker.sock, mode
// 0660, group hearth) and exits when idle. Each connection is checked with
// SO_PEERCRED: the caller's uid must be a member of the hearth group.
// Requests are JSON objects, one per line; every method is in a static
// allowlist, privileged ones are authorized by polkit for the calling
// process, and every call (refused ones too) is audited. There is no way
// to run an arbitrary command.
package broker

import (
	"encoding/json"
	"errors"
	"fmt"
)

// SocketPath is where hearth-broker.socket listens.
const SocketPath = "/run/hearth/broker.sock"

// Group is the group whose members may use the broker.
const Group = "hearth"

// ActionManage is the polkit action for privileged methods.
const ActionManage = "org.hearth.broker.manage"

// MaxLine bounds one request or response line.
const MaxLine = 64 << 10

// Request is one call.
type Request struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response answers a Request with the same ID.
type Response struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error codes: JSON-RPC's own, then the broker's.
const (
	CodeParse        = -32700
	CodeInvalid      = -32600
	CodeNoMethod     = -32601
	CodeBadParams    = -32602
	CodeInternal     = -32603
	CodeNotMember    = 1 // the caller is not in the hearth group
	CodeUnauthorized = 2 // polkit did not authorize the call
	CodeFailed       = 3 // the method ran and failed
)

// Error is a call's failure.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("broker: %s (%d)", e.Message, e.Code) }

// Errorf returns an *Error with code.
func Errorf(code int, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// Is lets errors.Is match on the code: errors.Is(err, ErrUnauthorized).
func (e *Error) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Code == e.Code
}

// Sentinels for errors.Is.
var (
	ErrNotMember    = &Error{Code: CodeNotMember, Message: "not a member of group " + Group}
	ErrUnauthorized = &Error{Code: CodeUnauthorized, Message: "not authorized"}
	ErrNoMethod     = &Error{Code: CodeNoMethod, Message: "unknown method"}
)

// PingResult is broker.ping's result.
type PingResult struct {
	Version string   `json:"version"`
	UID     uint32   `json:"uid"` // the caller, as the broker sees it
	Methods []string `json:"methods"`
}
