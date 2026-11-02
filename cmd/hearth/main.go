// Command hearth is the command line face of hearthd: read-only, through a
// read-only token (SECURITY S2a) on 127.0.0.1. Create the token on the Tokens
// page, named "cli" for the default file.
//
// Usage:
//
//	hearth [--json] [--token-file FILE] status|ports|alerts|units|containers
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

const usage = `usage: hearth [--json] [--token-file FILE] <command>

commands:
  status       CPU, memory, disks and open alerts
  ports        listening sockets and who owns them
  alerts       recent alerts, newest first
  units        loaded systemd units
  containers   Docker containers

--json prints what hearthd answered, for scripts.
The token is read from ~/.config/hearth/tokens/cli.token unless --token-file
says otherwise. Create it on Hearth's Tokens page, named "cli".
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run returns the exit code: 0 done, 1 failed, 2 bad usage.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args)
	if errors.Is(err, errHelp) {
		_, _ = io.WriteString(stdout, usage)
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hearth: %v\n\n%s", err, usage)
		return 2
	}
	c, err := newClient(opts.tokenFile, getenv)
	if err == nil {
		err = commands[opts.command](ctx, c, opts.json, stdout)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hearth: %s\n", clean(err.Error()))
		return 1
	}
	return 0
}
