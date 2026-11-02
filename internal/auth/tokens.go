package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Read-only tokens (SECURITY S2a) are for the hearth CLI and /metrics. Each is
// a 0600 file <dir>/<name>.token, and those files are the only record of them:
// nothing goes into the database, so no copy of it can carry a token or bring
// a revoked one back. In memory only hashes are kept, and no API returns a
// token: whoever wants one reads its file.
const (
	// MaxTokens bounds how many read-only tokens can exist.
	MaxTokens = 16

	tokenExt    = ".token"
	tokenPrefix = "hearth_ro_"
	// tokenMax bounds a file read at start well above a real token's size.
	tokenMax = 256
)

var (
	tokenNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	tokenRe     = regexp.MustCompile(`^hearth_ro_[A-Za-z0-9_-]{43}$`)

	// ErrTokenExists, ErrTokenLimit and ErrNoToken are the refusals a caller
	// can name.
	ErrTokenExists = errors.New("a token with that name already exists")
	ErrTokenLimit  = errors.New("too many tokens: revoke one first")
	ErrNoToken     = errors.New("no such token")
)

// ValidTokenName reports whether name can name a token; it also has to be
// safe as a file name, which the character set guarantees.
func ValidTokenName(name string) bool { return tokenNameRe.MatchString(name) }

// TokenInfo describes a token without revealing it.
type TokenInfo struct {
	Name    string `json:"name"`
	File    string `json:"file"`
	Created int64  `json:"created"` // Unix ms
}

// Tokens holds the read-only tokens. A nil *Tokens accepts nothing.
type Tokens struct {
	dir string

	mu     sync.Mutex
	hashes map[[sha256.Size]byte]string // to the token's name
}

// NewTokens loads the tokens in dir. A file that is not a private, regular
// token file is ignored and returned in skipped, for the log: a token anyone
// else could read is not a secret.
func NewTokens(dir string) (t *Tokens, skipped []string, err error) {
	if !filepath.IsAbs(dir) {
		return nil, nil, fmt.Errorf("auth: tokens dir %q must be absolute", dir)
	}
	t = &Tokens{dir: dir, hashes: map[[sha256.Size]byte]string{}}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return t, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("auth: read tokens: %w", err)
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), tokenExt)
		if !ok || !ValidTokenName(name) {
			continue
		}
		tok, err := readTokenFile(filepath.Join(dir, e.Name()))
		if err != nil {
			skipped = append(skipped, e.Name())
			continue
		}
		t.hashes[sha256.Sum256([]byte(tok))] = name
	}
	return t, skipped, nil
}

// readTokenFile opens path without following a link and returns the token.
func readTokenFile(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 || fi.Size() > tokenMax {
		return "", errors.New("not a private token file")
	}
	b, err := os.ReadFile(path) // #nosec G304 -- a validated name in hearthd's own 0700 directory
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if !tokenRe.MatchString(tok) {
		return "", errors.New("not a token")
	}
	return tok, nil
}

// Valid reports whether key is a live read-only token.
func (t *Tokens) Valid(key string) bool {
	if t == nil || !strings.HasPrefix(key, tokenPrefix) {
		return false
	}
	h := sha256.Sum256([]byte(key))
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.hashes[h]
	return ok
}

func (t *Tokens) path(name string) string { return filepath.Join(t.dir, name+tokenExt) }

// Create makes a token and writes its file; only the path is returned.
func (t *Tokens) Create(name string) (TokenInfo, error) {
	if !ValidTokenName(name) {
		return TokenInfo{}, errors.New("a name is 1 to 32 lowercase letters, digits or hyphens")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, n := range t.hashes {
		if n == name {
			return TokenInfo{}, ErrTokenExists
		}
	}
	if len(t.hashes) >= MaxTokens {
		return TokenInfo{}, ErrTokenLimit
	}
	path := t.path(name)
	// A file that did not load (wrong mode, not a token) must not be replaced
	// unseen.
	if _, err := os.Lstat(path); err == nil {
		return TokenInfo{}, ErrTokenExists
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return TokenInfo{}, fmt.Errorf("auth: generate token: %w", err)
	}
	tok := tokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	if err := writeSecret(path, tok+"\n"); err != nil {
		return TokenInfo{}, err
	}
	t.hashes[sha256.Sum256([]byte(tok))] = name
	return TokenInfo{Name: name, File: path, Created: time.Now().UnixMilli()}, nil
}

// Revoke ends a token at once and removes its file. The token stops working
// even if the file cannot be removed; the error then says so, since it would
// load again at the next start.
func (t *Tokens) Revoke(name string) error {
	if !ValidTokenName(name) {
		return ErrNoToken
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	found := false
	for h, n := range t.hashes {
		if n == name {
			delete(t.hashes, h)
			found = true
		}
	}
	if !found {
		return ErrNoToken
	}
	if err := os.Remove(t.path(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("auth: remove token file: %w", err)
	}
	return nil
}

// List returns the tokens by name.
func (t *Tokens) List() []TokenInfo {
	out := []TokenInfo{}
	if t == nil {
		return out
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, name := range t.hashes {
		info := TokenInfo{Name: name, File: t.path(name)}
		if fi, err := os.Lstat(info.File); err == nil {
			info.Created = fi.ModTime().UnixMilli()
		}
		out = append(out, info)
	}
	slices.SortFunc(out, func(a, b TokenInfo) int { return strings.Compare(a.Name, b.Name) })
	return out
}
