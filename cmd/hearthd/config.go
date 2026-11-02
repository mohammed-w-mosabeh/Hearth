package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
)

// preferredPort is tried on first run before falling back to a free one.
const preferredPort = 7380

// Config is $XDG_CONFIG_HOME/hearth/config.json.
type Config struct {
	Port int `json:"port"`
}

// Paths are the XDG locations hearthd uses.
type Paths struct {
	ConfigDir string // $XDG_CONFIG_HOME/hearth
	DataDir   string // $XDG_DATA_HOME/hearth
}

func (p Paths) ConfigFile() string { return filepath.Join(p.ConfigDir, "config.json") }
func (p Paths) TokenFile() string  { return filepath.Join(p.ConfigDir, "token") }
func (p Paths) TokensDir() string  { return filepath.Join(p.ConfigDir, "tokens") }
func (p Paths) DBFile() string     { return filepath.Join(p.DataDir, "hearth.db") }

// resolvePaths applies the XDG Base Directory rules: a variable is used only
// if it is set to an absolute path, otherwise the default under $HOME.
func resolvePaths(getenv func(string) string) (Paths, error) {
	home := getenv("HOME")
	if !filepath.IsAbs(home) {
		return Paths{}, errors.New("hearthd: $HOME is not set to an absolute path")
	}
	xdg := func(name, def string) string {
		if v := getenv(name); filepath.IsAbs(v) {
			return filepath.Clean(v)
		}
		return filepath.Join(home, def)
	}
	return Paths{
		ConfigDir: filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "hearth"),
		DataDir:   filepath.Join(xdg("XDG_DATA_HOME", ".local/share"), "hearth"),
	}, nil
}

// loadConfig reads the config file. firstRun is true when it did not exist,
// in which case a port is chosen and the file is written.
func loadConfig(ctx context.Context, p Paths) (cfg Config, firstRun bool, err error) {
	b, err := os.ReadFile(p.ConfigFile())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		cfg = Config{Port: pickPort(ctx)}
		if err := writeConfig(p, cfg); err != nil {
			return Config{}, false, err
		}
		return cfg, true, nil
	case err != nil:
		return Config{}, false, fmt.Errorf("hearthd: read config: %w", err)
	}
	cfg, err = parseConfig(b)
	if err != nil {
		return Config{}, false, fmt.Errorf("hearthd: %s: %w", p.ConfigFile(), err)
	}
	return cfg, false, nil
}

func parseConfig(b []byte) (Config, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("invalid config: %w", err)
	}
	if dec.More() {
		return Config{}, errors.New("invalid config: trailing data")
	}
	if cfg.Port < 1024 || cfg.Port > 65535 {
		return Config{}, fmt.Errorf("invalid config: port %d not in 1024-65535", cfg.Port)
	}
	return cfg, nil
}

// pickPort returns preferredPort if it is free on 127.0.0.1, else a free
// ephemeral port.
func pickPort(ctx context.Context) int {
	var lc net.ListenConfig
	for _, port := range []int{preferredPort, 0} {
		ln, err := lc.Listen(ctx, "tcp4", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			continue
		}
		got := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		return got
	}
	return preferredPort
}

func writeConfig(p Paths, cfg Config) error {
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		return fmt.Errorf("hearthd: create %s: %w", p.ConfigDir, err)
	}
	if err := os.Chmod(p.ConfigDir, 0o700); err != nil { // #nosec G302 -- directory needs the x bit; 0700 is owner-only
		return fmt.Errorf("hearthd: chmod %s: %w", p.ConfigDir, err)
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p.ConfigFile(), append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("hearthd: write config: %w", err)
	}
	return nil
}
