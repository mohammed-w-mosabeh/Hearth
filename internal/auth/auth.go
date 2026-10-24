// Package auth implements the desktop login flow (SECURITY S2).
//
// hearthd writes a single-use launch token to a 0600 file. The launcher opens
// /auth?t=<token>; the server burns the token, writes a fresh one, and
// redirects to /#k=<session key>. The UI keeps the key in localStorage, which
// is isolated per port (cookies are not), and sends it as a bearer token on
// API calls or as a WebSocket subprotocol on upgrades.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// MaxSessions bounds live sessions; the oldest is evicted beyond this.
	MaxSessions = 16

	// Subprotocol is the WebSocket subprotocol the server echoes back.
	Subprotocol = "hearth.v1"
	// keyProtoPrefix carries the session key in Sec-WebSocket-Protocol,
	// because browsers cannot set Authorization on a WebSocket.
	keyProtoPrefix = "hearth.key."
)

var errBadToken = errors.New("auth: invalid launch token")

// Manager holds the current launch token and live sessions.
type Manager struct {
	tokenPath string

	mu       sync.Mutex
	launch   [sha256.Size]byte // hash of the current launch token
	sessions map[[sha256.Size]byte]time.Time
}

// NewManager rotates the launch token at tokenPath and returns a Manager with
// no sessions. Call it once per hearthd start.
func NewManager(tokenPath string) (*Manager, error) {
	if !filepath.IsAbs(tokenPath) {
		return nil, fmt.Errorf("auth: token path %q must be absolute", tokenPath)
	}
	m := &Manager{tokenPath: tokenPath, sessions: make(map[[sha256.Size]byte]time.Time)}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.rotateLocked(); err != nil {
		return nil, err
	}
	return m, nil
}

// TokenPath returns where the current launch token is stored.
func (m *Manager) TokenPath() string { return m.tokenPath }

// rotateLocked writes a fresh launch token. The old one stops working even if
// the write fails, so a failed rotation never leaves a used token valid.
func (m *Manager) rotateLocked() error {
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		return fmt.Errorf("auth: generate token: %w", err)
	}
	s := hex.EncodeToString(tok)
	if _, err := rand.Read(m.launch[:]); err != nil { // invalidate first
		return fmt.Errorf("auth: generate token: %w", err)
	}
	if err := writeSecret(m.tokenPath, s+"\n"); err != nil {
		return err
	}
	m.launch = sha256.Sum256([]byte(s))
	return nil
}

// Exchange burns a valid launch token and returns a new session key.
func (m *Manager) Exchange(token string) (string, error) {
	h := sha256.Sum256([]byte(token))
	m.mu.Lock()
	defer m.mu.Unlock()
	if subtle.ConstantTimeCompare(h[:], m.launch[:]) != 1 {
		return "", errBadToken
	}
	if err := m.rotateLocked(); err != nil {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: generate session: %w", err)
	}
	key := base64.RawURLEncoding.EncodeToString(raw)
	if len(m.sessions) >= MaxSessions {
		m.evictOldestLocked()
	}
	m.sessions[sha256.Sum256([]byte(key))] = time.Now()
	return key, nil
}

func (m *Manager) evictOldestLocked() {
	var oldest [sha256.Size]byte
	var t time.Time
	for k, c := range m.sessions {
		if t.IsZero() || c.Before(t) {
			oldest, t = k, c
		}
	}
	delete(m.sessions, oldest)
}

// Valid reports whether key belongs to a live session.
func (m *Manager) Valid(key string) bool {
	if key == "" {
		return false
	}
	h := sha256.Sum256([]byte(key))
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sessions[h]
	return ok
}

// HandleAuth serves GET /auth?t=<launch token>.
func (m *Manager) HandleAuth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	key, err := m.Exchange(r.URL.Query().Get("t"))
	if err != nil {
		http.Error(w, "invalid or used launch link; open Hearth from the launcher", http.StatusForbidden)
		return
	}
	http.Redirect(w, r, "/#k="+key, http.StatusSeeOther)
}

// Require rejects requests without a valid session key with 401.
func (m *Manager) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !m.Valid(KeyFromRequest(r)) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="hearth"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// KeyFromRequest extracts the session key from "Authorization: Bearer", or
// for WebSocket upgrades from a "hearth.key.<key>" subprotocol.
func KeyFromRequest(r *http.Request) string {
	if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(v)
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return ""
	}
	for _, h := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(h, ",") {
			if k, ok := strings.CutPrefix(strings.TrimSpace(p), keyProtoPrefix); ok {
				return k
			}
		}
	}
	return ""
}

// writeSecret atomically replaces path with content, mode 0600, in a 0700
// directory (SECURITY S20).
func writeSecret(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("auth: create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- directory needs the x bit; 0700 is owner-only
		return fmt.Errorf("auth: chmod %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return fmt.Errorf("auth: write token: %w", err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return fmt.Errorf("auth: write token: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("auth: write token: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("auth: write token: %w", err)
	}
	return nil
}
