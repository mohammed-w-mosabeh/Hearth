package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
)

const (
	requestTimeout = 15 * time.Second
	maxAnswer      = 32 << 20 // far more than any answer used here
	maxTokenFile   = 256
)

var errHelp = errors.New("help")

type options struct {
	json      bool
	tokenFile string
	command   string
}

// parseArgs takes the flags before or after the command.
func parseArgs(args []string) (options, error) {
	var o options
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help" || a == "help":
			return o, errHelp
		case a == "--json":
			o.json = true
		case a == "--token-file":
			i++
			if i == len(args) {
				return o, errors.New("--token-file needs a file")
			}
			o.tokenFile = args[i]
		case strings.HasPrefix(a, "--token-file="):
			o.tokenFile = strings.TrimPrefix(a, "--token-file=")
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("unknown option %q", a)
		case o.command != "":
			return o, fmt.Errorf("unexpected %q", a)
		case commands[a] == nil:
			return o, fmt.Errorf("unknown command %q", a)
		default:
			o.command = a
		}
	}
	if o.command == "" {
		return o, errors.New("no command")
	}
	return o, nil
}

// client talks to hearthd on 127.0.0.1 and nowhere else: no proxy, no
// redirects, so the token cannot be carried away from the machine.
type client struct {
	base  string
	token string
	http  *http.Client
}

// configDir applies the XDG rule hearthd uses.
func configDir(getenv func(string) string) (string, error) {
	if v := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(v) {
		return filepath.Join(filepath.Clean(v), "hearth"), nil
	}
	home := getenv("HOME")
	if !filepath.IsAbs(home) {
		return "", errors.New("$HOME is not set to an absolute path")
	}
	return filepath.Join(home, ".config", "hearth"), nil
}

func newClient(tokenFile string, getenv func(string) string) (*client, error) {
	dir, err := configDir(getenv)
	if err != nil {
		return nil, err
	}
	port, err := readPort(filepath.Join(dir, "config.json"))
	if err != nil {
		return nil, err
	}
	if tokenFile == "" {
		tokenFile = filepath.Join(dir, "tokens", "cli.token")
	}
	token, err := readToken(tokenFile)
	if err != nil {
		return nil, err
	}
	return &client{
		base:  "http://127.0.0.1:" + strconv.Itoa(port),
		token: token,
		http: &http.Client{
			Timeout:       requestTimeout,
			Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func readPort(path string) (int, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- the user's own config file
	if errors.Is(err, fs.ErrNotExist) {
		return 0, errors.New("hearthd has not been run yet for this user")
	}
	if err != nil {
		return 0, fmt.Errorf("read config: %w", err)
	}
	var cfg struct {
		Port int `json:"port"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil || cfg.Port < 1024 || cfg.Port > 65535 {
		return 0, fmt.Errorf("%s has no valid port", path)
	}
	return cfg.Port, nil
}

// readToken refuses a file others could read: like ssh with a key, it treats
// that as a mistake to fix rather than to use.
func readToken(path string) (string, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("no token at %s: create one on Hearth's Tokens page, named \"cli\"", path)
	}
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Size() > maxTokenFile {
		return "", fmt.Errorf("%s is not a token file", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s can be read by others: chmod 600 it", path)
	}
	b, err := os.ReadFile(path) // #nosec G304 -- a path the user chose or the default
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// get returns the body of a read-only GET.
func (c *client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if errors.Is(err, syscall.ECONNREFUSED) {
		return nil, errors.New("hearthd is not running: systemctl --user start hearthd")
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return nil, errors.New("hearthd did not answer in time")
	}
	if err != nil {
		return nil, errors.New("cannot reach hearthd")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer+1))
	if err != nil {
		return nil, errors.New("hearthd's answer was cut off")
	}
	if len(body) > maxAnswer {
		return nil, errors.New("hearthd's answer is too large")
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return body, nil
	case http.StatusUnauthorized:
		return nil, errors.New("the token was refused: it may have been revoked; create a new one on the Tokens page")
	case http.StatusForbidden:
		return nil, errors.New("hearthd refused this for a read-only token; is it the same version as this command?")
	}
	return nil, fmt.Errorf("hearthd answered %d", resp.StatusCode)
}

// clean makes a system string safe to print: container, unit and process
// names can hold anything, and a terminal obeys escape sequences. Controls
// and invisible format characters such as direction overrides become "?".
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return '?'
		}
		return r
	}, s)
}
