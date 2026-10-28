// termprobe drives one Terminal page shell for the integration test:
// it opens GET /api/v1/terminal as the browser does (session key in the
// subprotocol, same Origin), types -send, and prints everything the shell
// writes plus the control messages, until the shell exits or -timeout.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/coder/websocket"
)

func main() {
	url := flag.String("url", "", "ws://127.0.0.1:<port>/api/v1/terminal?...")
	origin := flag.String("origin", "", "Origin header")
	key := flag.String("key", "", "session key")
	send := flag.String("send", "", "text typed once the shell is ready")
	timeout := flag.Duration("timeout", 15*time.Second, "give up after")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, *url, &websocket.DialOptions{
		Subprotocols: []string{"hearth.v1", "hearth.key." + *key},
		HTTPHeader:   http.Header{"Origin": {*origin}},
	})
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		fmt.Printf("refused: %d\n", status)
		os.Exit(1)
	}
	defer func() { _ = conn.CloseNow() }()
	sent := false
	for {
		typ, msg, err := conn.Read(ctx)
		if err != nil {
			fmt.Printf("\nend: %v\n", websocket.CloseStatus(err))
			return
		}
		if typ == websocket.MessageText {
			fmt.Printf("\ncontrol: %s\n", msg)
			continue
		}
		fmt.Print(string(msg))
		if !sent { // the prompt is there
			sent = true
			if err := conn.Write(ctx, websocket.MessageBinary, []byte(*send)); err != nil {
				fmt.Printf("\nsend: %v\n", err)
				return
			}
		}
	}
}
