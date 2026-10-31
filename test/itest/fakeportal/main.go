// Command fakeportal stands in for xdg-desktop-portal in make itest. It
// answers OpenURI like a browser would: it fetches the link, writes the
// status to -log, and sends Response with -response.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

const request = dbus.ObjectPath("/org/freedesktop/portal/desktop/request/itest/1")

type portal struct {
	conn     *dbus.Conn
	logPath  string
	response uint32
}

func (p *portal) OpenURI(_, uri string, _ map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		status := "error"
		if p.response == 0 {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
			if err == nil {
				if resp, err := client.Do(req); err == nil { // #nosec G704 -- a test tool, on the loopback
					_ = resp.Body.Close()
					status = strconv.Itoa(resp.StatusCode)
				}
			}
		} else {
			status = "refused"
		}
		if f, err := os.OpenFile(p.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil { // #nosec G304 -- a test tool's flag
			_, _ = fmt.Fprintln(f, status)
			_ = f.Close()
		}
		_ = p.conn.Emit(request, "org.freedesktop.portal.Request.Response", p.response, map[string]dbus.Variant{})
	}()
	return request, nil
}

func main() {
	logPath := flag.String("log", "", "where to write what the link answered")
	response := flag.Uint("response", 0, "the portal's Response code")
	flag.Parse()
	if err := run(*logPath, uint32(*response)); err != nil { // #nosec G115 -- a test tool's flag
		log.Fatal(err)
	}
}

func run(logPath string, response uint32) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Export(&portal{conn: conn, logPath: logPath, response: response},
		"/org/freedesktop/portal/desktop", "org.freedesktop.portal.OpenURI"); err != nil {
		return err
	}
	reply, err := conn.RequestName("org.freedesktop.portal.Desktop", dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return errors.New("the portal's name is taken")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	<-ctx.Done()
	return nil
}
