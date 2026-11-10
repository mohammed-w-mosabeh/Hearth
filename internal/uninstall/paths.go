// Package uninstall builds and runs "Uninstall completely" (SECURITY S16k,
// docs/plan/uninstall.md): an item resolved to its owner, one plan of
// everything that goes with it, and an undo for 7 days.
package uninstall

import "github.com/mohammed-w-mosabeh/hearth/internal/uninstall/rules"

// The path rules live in package rules, shared with the broker.
var (
	SystemRoots = rules.SystemRoots
	UserRoots   = rules.UserRoots
	CheckSystem = rules.CheckSystem
	CheckUser   = rules.CheckUser
)
