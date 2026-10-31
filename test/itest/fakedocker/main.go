// Command fakedocker is a stand-in Docker Engine for `make itest` (a real
// dockerd would need a privileged container). It listens on
// /run/docker.sock (root only, mode 0600), answers a few Engine API
// requests and logs every request line to /run/fakedocker.log, so the
// checks can see exactly what hearth-broker sent.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

const (
	sock    = "/run/docker.sock"
	logPath = "/run/fakedocker.log"
)

var answers = map[string]struct {
	status int
	body   string
}{
	"GET /v1.44/version":         {200, `{"Version":"fake","ApiVersion":"1.44"}`},
	"GET /v1.44/containers/json": {200, `[{"Id":"` + id + `","Names":["/web"],"State":"exited"}]`},
	"GET /v1.44/containers/web/json": {200, `{"Id":"` + id + `","Name":"/web",
		"Config":{"Tty":false,"Env":["PATH=/bin","DB_PASSWORD=hunter2"]},
		"HostConfig":{"Privileged":true,"Binds":["/etc:/host-etc"]}}`},
	// Running and privileged with / mounted: a shell here is host root
	// (SECURITY S12a), so exec must be refused before any exec request.
	"GET /v1.44/containers/risky/json": {200, `{"Id":"` + id + `","Name":"/risky",
		"State":{"Running":true,"Paused":false},
		"HostConfig":{"Privileged":true,"Binds":["/:/host"]}}`},
	"POST /v1.44/containers/web/start": {204, ""},
	"DELETE /v1.44/containers/web":     {204, ""},
	// The rest of a Docker page snapshot.
	"POST /v1.44/containers/web/update": {200, `{"Warnings":[]}`},
	"GET /v1.44/images/json":            {200, `[]`},
	"GET /v1.44/networks":               {200, `[]`},
	"GET /v1.44/volumes":                {200, `{"Volumes":[]}`},
	"GET /v1.44/system/df":              {200, `{"Volumes":[{"Name":"orphan","UsageData":{"RefCount":0,"Size":2048}},{"Name":"busy","UsageData":{"RefCount":1,"Size":9}}]}`},
	"DELETE /v1.44/volumes/orphan":      {204, ""},
	"DELETE /v1.44/volumes/busy":        {409, `{"message":"volume is in use"}`},
}

// hostileFlag, when it exists, adds a container whose name, image, status
// and compose project are HTML (the e2e XSS test): a hostile daemon or
// labels can say anything, so the UI must show it as text.
const hostileFlag = "/run/fakedocker.hostile"

// Payload is what the e2e test looks for.
const payload = `<img src=x onerror=\"window.__xss=1\">`

var hostile = `{"Id":"fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210","Names":["/` + payload + `"],` +
	`"Image":"` + payload + `","State":"running","Status":"` + payload + `",` +
	`"Labels":{"com.docker.compose.project":"` + payload + `","com.docker.compose.service":"` + payload + `"}}`

const id = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func main() {
	_ = os.Remove(sock)
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", sock)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		log.Fatal(err)
	}
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644) // #nosec G302 G304 -- test-only log in the container
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		_, _ = fmt.Fprintf(f, "%s %s\n", r.Method, r.URL.RequestURI())
		if key == "GET /v1.44/containers/json" {
			if _, err := os.Stat(hostileFlag); err == nil {
				_, _ = w.Write([]byte(`[{"Id":"` + id + `","Names":["/web"],"State":"exited"},` + hostile + `]`))
				return
			}
		}
		a, ok := answers[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"page not found"}`))
			return
		}
		w.WriteHeader(a.status)
		_, _ = w.Write([]byte(a.body))
	})}
	log.Fatal(srv.Serve(ln))
}
