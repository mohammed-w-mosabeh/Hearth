// Package unattended marks work done with nobody at the screen: an
// automation's run (SECURITY S15d). Code that could ask polkit for a
// password checks the mark and refuses instead, so no dialog can come
// from such work, whatever an action declares.
package unattended

import (
	"context"
	"errors"
)

type key struct{}

// With marks ctx as unattended.
func With(ctx context.Context) context.Context { return context.WithValue(ctx, key{}, true) }

// Is reports whether ctx is unattended.
func Is(ctx context.Context) bool { v, _ := ctx.Value(key{}).(bool); return v }

// ErrWouldPrompt is the refusal: the work would have asked for a password.
var ErrWouldPrompt = errors.New("this would ask for a password, and nobody started it: it waits for your approval")
