package homepage

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Suggestions (SPEC 6, ROADMAP 2.10): published container ports and
// local HTTP listeners, offered as pre-filled forms. Nothing is saved
// from here; the user saves the form. Only computed when asked.

// Suggestion is one service the user might want a tile for.
type Suggestion struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Source    string `json:"source"`              // "container" or "listener"
	Container string `json:"container,omitempty"` // for Source "container"
	Process   string `json:"process,omitempty"`   // the listener's program, when it is the user's
	Title     string `json:"title,omitempty"`     // the page's <title>
}

// DockerLister lists containers (broker.Client).
type DockerLister interface {
	Call(ctx context.Context, method string, params, out any) error
}

// Suggester finds suggestions.
type Suggester struct {
	Docker   DockerLister // nil: no container suggestions
	ProcRoot string       // default /proc
	SelfPort int          // hearthd's own port, never suggested
	// Probe GETs a URL and reports whether it speaks HTTP, and its title.
	Probe func(ctx context.Context, u string) (ok bool, title string)
}

// notHTTP are ports that are almost never a web page; they are not probed.
var notHTTP = map[int]bool{
	22: true, 25: true, 53: true, 110: true, 111: true, 139: true, 143: true, 445: true, 465: true,
	587: true, 631: true, 993: true, 995: true, 1716: true, 3306: true, 5353: true, 5432: true,
	6379: true, 11211: true, 27017: true,
}

// maxProbes bounds the ports probed at once and in all.
const (
	probeWorkers = 8
	maxProbes    = 64
)

// Suggest returns suggestions, minus those whose host:port a tile has.
func (s *Suggester) Suggest(ctx context.Context, tiles []string) []Suggestion {
	have := map[string]bool{}
	for _, u := range tiles {
		if p, err := url.Parse(u); err == nil {
			have[hostPort(p)] = true
		}
	}
	var cands []Suggestion
	seenPort := map[int]bool{}
	if s.Docker != nil {
		for _, c := range s.containers(ctx) {
			cands = append(cands, c)
			if p, err := url.Parse(c.URL); err == nil {
				n, _ := strconv.Atoi(p.Port())
				seenPort[n] = true
			}
		}
	}
	for _, l := range s.listeners() {
		if seenPort[l.port] || l.port == s.SelfPort || notHTTP[l.port] {
			continue
		}
		seenPort[l.port] = true
		sg := Suggestion{Name: l.process, Source: "listener", Process: l.process,
			URL: "http://" + net.JoinHostPort(l.host, strconv.Itoa(l.port)) + "/"}
		if sg.Name == "" {
			sg.Name = "Port " + strconv.Itoa(l.port)
		}
		cands = append(cands, sg)
	}
	var out []Suggestion
	for _, c := range cands {
		if p, err := url.Parse(c.URL); err == nil && !have[hostPort(p)] {
			out = append(out, c)
		}
	}
	if len(out) > maxProbes {
		out = out[:maxProbes]
	}
	return s.probeAll(ctx, out)
}

func hostPort(u *url.URL) string {
	port := u.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
	}
	host := u.Hostname()
	if host == "localhost" || host == "::1" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return host + ":" + port
}

// probeAll keeps the suggestions that answer HTTP, named by their title.
func (s *Suggester) probeAll(ctx context.Context, in []Suggestion) []Suggestion {
	keep := make([]bool, len(in))
	sem := make(chan struct{}, probeWorkers)
	var wg sync.WaitGroup
	for i := range in {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ok, title := s.Probe(ctx, in[i].URL)
			keep[i] = ok
			if title != "" {
				in[i].Title = title
				if in[i].Source == "listener" {
					in[i].Name = title
				}
			}
		}()
	}
	wg.Wait()
	out := []Suggestion{}
	for i, sg := range in {
		if keep[i] {
			out = append(out, sg)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source == "container"
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// containers are the running containers' published TCP ports.
func (s *Suggester) containers(ctx context.Context) []Suggestion {
	var list []struct {
		Names  []string
		State  string
		Labels map[string]string
		Ports  []struct {
			IP         string
			PublicPort int
			Type       string
		}
	}
	if err := s.Docker.Call(ctx, "docker.containers.list", map[string]bool{"all": false}, &list); err != nil {
		return nil
	}
	var out []Suggestion
	for _, c := range list {
		if c.State != "running" || len(c.Names) == 0 {
			continue
		}
		name := strings.TrimPrefix(c.Names[0], "/")
		seen := map[int]bool{}
		for _, p := range c.Ports {
			if p.Type != "tcp" || p.PublicPort == 0 || seen[p.PublicPort] {
				continue
			}
			seen[p.PublicPort] = true
			host := "127.0.0.1"
			if ip := net.ParseIP(p.IP); ip != nil && !ip.IsUnspecified() {
				host = ip.String()
			}
			out = append(out, Suggestion{
				Name: name, Source: "container", Container: name,
				URL: "http://" + net.JoinHostPort(host, strconv.Itoa(p.PublicPort)) + "/",
			})
		}
	}
	return out
}

type listener struct {
	host    string
	port    int
	process string
}

// listeners reads the TCP sockets in LISTEN state from /proc/net/tcp
// and tcp6. Wildcard and loopback listeners are reached on 127.0.0.1
// (or ::1); others on their address.
func (s *Suggester) listeners() []listener {
	root := s.ProcRoot
	if root == "" {
		root = "/proc"
	}
	inodes := map[string]string{} // socket inode -> program, for the user's processes
	if dirs, err := os.ReadDir(root); err == nil {
		for _, d := range dirs {
			if _, err := strconv.Atoi(d.Name()); err != nil {
				continue
			}
			fds, err := os.ReadDir(filepath.Join(root, d.Name(), "fd"))
			if err != nil {
				continue // another user's process
			}
			comm, _ := os.ReadFile(filepath.Join(root, d.Name(), "comm")) // #nosec G304 -- fixed /proc layout, numeric pid
			for _, fd := range fds {
				link, err := os.Readlink(filepath.Join(root, d.Name(), "fd", fd.Name()))
				if ino, ok := strings.CutPrefix(link, "socket:["); err == nil && ok {
					inodes[strings.TrimSuffix(ino, "]")] = strings.TrimSpace(string(comm))
				}
			}
		}
	}
	var out []listener
	seen := map[string]bool{}
	for _, f := range []string{"net/tcp", "net/tcp6"} {
		file, err := os.Open(filepath.Join(root, f)) // #nosec G304 -- fixed /proc paths
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(file)
		sc.Scan() // header
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 10 || fields[3] != "0A" { // 0A: LISTEN
				continue
			}
			ip, port, ok := parseProcAddr(fields[1])
			if !ok {
				continue
			}
			host := ip.String()
			switch {
			case ip.IsUnspecified() || ip.IsLoopback():
				host = "127.0.0.1"
				if ip.To4() == nil && ip.IsLoopback() {
					host = "::1"
				}
			case ip.IsLinkLocalUnicast():
				continue // needs a zone; not worth a tile
			}
			k := host + ":" + strconv.Itoa(port)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, listener{host: host, port: port, process: inodes[fields[9]]})
		}
		_ = file.Close()
	}
	return out
}

// parseProcAddr reads "0100007F:1F90" (IPv4) or a 32-hex-digit IPv6
// address: 32-bit words in host (little-endian) order.
func parseProcAddr(s string) (net.IP, int, bool) {
	a, p, ok := strings.Cut(s, ":")
	if !ok {
		return nil, 0, false
	}
	port, err := strconv.ParseUint(p, 16, 16)
	if err != nil || port == 0 {
		return nil, 0, false
	}
	b, err := hex.DecodeString(a)
	if err != nil || (len(b) != 4 && len(b) != 16) {
		return nil, 0, false
	}
	for i := 0; i < len(b); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	return net.IP(b), int(port), true
}

var titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// HTTPProbe is the default Probe: one GET, 1.5 s, no redirects followed,
// at most 64 KiB read; any HTTP answer counts.
func HTTPProbe(client *http.Client) func(ctx context.Context, u string) (bool, string) {
	if client == nil {
		client = &http.Client{
			Timeout:       1500 * time.Millisecond,
			Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 1500 * time.Millisecond},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return func(ctx context.Context, u string) (bool, string) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return false, ""
		}
		req.Header.Set("User-Agent", "Hearth suggestions")
		resp, err := client.Do(req)
		if err != nil {
			return false, ""
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxHealthBody))
		return true, pageTitle(b)
	}
}

// pageTitle is an HTML page's <title> as plain text, at most 80 runes.
func pageTitle(b []byte) string {
	m := titleRe.FindSubmatch(b)
	if m == nil {
		return ""
	}
	s := strings.Join(strings.Fields(html.UnescapeString(string(m[1]))), " ")
	s = strings.ToValidUTF8(s, "")
	if utf8.RuneCountInString(s) > 80 {
		s = string([]rune(s)[:80])
	}
	if !textOK(s, 1, 80) {
		return ""
	}
	return s
}

// HandleSuggestions serves GET /api/v1/homepage/suggestions.
func (a *API) HandleSuggestions(s *Suggester) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		tiles, err := a.DB.Tiles(ctx)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "tiles could not be read")
			return
		}
		urls := make([]string, 0, len(tiles))
		for _, t := range tiles {
			urls = append(urls, t.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"suggestions": s.Suggest(ctx, urls)})
	}
}
