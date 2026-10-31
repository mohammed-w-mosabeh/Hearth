package alerts

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func goodNTFY() NTFY {
	return NTFY{Enabled: true, Server: "https://ntfy.example.com", Topic: "hearth_alerts-1", Token: "tk_abc"}
}

func goodSMTP() SMTP {
	return SMTP{Enabled: true, Host: "mail.example.com", Port: 587, Security: "starttls", Username: "me", Password: "pw", From: "me@example.com", To: "you@example.com"}
}

func TestValidateChannels(t *testing.T) {
	if err := (ChannelConfig{}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (ChannelConfig{NTFY: goodNTFY(), SMTP: goodSMTP()}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []func(*NTFY){
		func(n *NTFY) { n.Server = "http://ntfy.sh" },
		func(n *NTFY) { n.Server = "ftp://ntfy.sh" },
		func(n *NTFY) { n.Server = "ntfy.sh" },
		func(n *NTFY) { n.Server = "https://user:pw@ntfy.sh" },
		func(n *NTFY) { n.Server = "https://ntfy.sh/path" },
		func(n *NTFY) { n.Server = "https://ntfy.sh?x=1" },
		func(n *NTFY) { n.Server = "https://ntfy.sh#x" },
		func(n *NTFY) { n.Server = "https://ntfy.sh\r\nX: y" },
		func(n *NTFY) { n.Server = "https://" },
		func(n *NTFY) { n.Topic = "" },
		func(n *NTFY) { n.Topic = "a/b" },
		func(n *NTFY) { n.Topic = "../x" },
		func(n *NTFY) { n.Topic = strings.Repeat("a", 65) },
		func(n *NTFY) { n.Token = "a b" },
		func(n *NTFY) { n.Token = "a\r\nX: y" },
	} {
		n := goodNTFY()
		f(&n)
		if (ChannelConfig{NTFY: n}).Validate() == nil {
			t.Errorf("accepted %+v", n)
		}
	}
	for _, f := range []func(*SMTP){
		func(s *SMTP) { s.Security = "none" },
		func(s *SMTP) { s.Security = "" },
		func(s *SMTP) { s.Port = 0 },
		func(s *SMTP) { s.Port = 70000 },
		func(s *SMTP) { s.Host = "mail example" },
		func(s *SMTP) { s.Host = "-x" },
		func(s *SMTP) { s.From = "me" },
		func(s *SMTP) { s.To = "a@b\r\nBcc: x@y" },
		func(s *SMTP) { s.To = "a@b,c@d" },
		func(s *SMTP) { s.To = "<a@b>" },
		func(s *SMTP) { s.Password = "" },
		func(s *SMTP) { s.Username = "a\nb" },
		func(s *SMTP) { s.Password = "a\nb" },
	} {
		s := goodSMTP()
		f(&s)
		if (ChannelConfig{SMTP: s}).Validate() == nil {
			t.Errorf("accepted %+v", s)
		}
	}
}

func TestSecretsStayIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "channels.json")
	c := &Channels{Path: path}
	var u ChannelUpdate
	u.NTFY.NTFY, u.SMTP = goodNTFY(), goodSMTP()
	if err := c.Update(u); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi.Mode(), err)
	}
	b, _ := json.Marshal(c.View())
	for _, s := range []string{"tk_abc", "hearth_alerts-1", `"pw"`} {
		if strings.Contains(string(b), s) {
			t.Fatalf("%s in %s", s, b)
		}
	}
	if !c.View().NTFY.HasToken || !c.View().SMTP.HasPassword || !c.View().NTFY.HasTopic {
		t.Fatalf("%s", b)
	}
	var log strings.Builder
	slog.New(slog.NewTextHandler(&log, nil)).Info("x", "cfg", c.config())
	if strings.Contains(log.String(), "tk_abc") {
		t.Fatal(log.String())
	}
	// A form sent back with empty secrets keeps them, reloaded from disk too.
	var again ChannelUpdate
	again.NTFY.NTFY = NTFY{Enabled: true, Server: "https://other.example.com"}
	again.SMTP = goodSMTP()
	again.SMTP.Password = ""
	if err := c.Update(again); err != nil {
		t.Fatal(err)
	}
	r := &Channels{Path: path}
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	got := r.config()
	if got.NTFY.Token != "tk_abc" || got.NTFY.Topic != "hearth_alerts-1" || got.SMTP.Password != "pw" || got.NTFY.Server != "https://other.example.com" {
		t.Fatalf("%+v", got)
	}
	again.NTFY.ClearToken = true
	again.SMTP.Username = ""
	if err := c.Update(again); err != nil {
		t.Fatal(err)
	}
	if got := c.config(); got.NTFY.Token != "" || got.SMTP.Password != "" {
		t.Fatalf("%+v", got)
	}
}

func TestBadFileIsNotEchoed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "channels.json")
	if err := os.WriteFile(path, []byte(`{"smtp":{"password":"hunter2`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := (&Channels{Path: path}).Load()
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatal(err)
	}
}

func pool(srv *httptest.Server) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(srv.Certificate())
	return p
}

func TestNTFY(t *testing.T) {
	var got struct {
		sync.Mutex
		body map[string]any
		auth string
		path string
	}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Lock()
		defer got.Unlock()
		got.auth, got.path = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got.body)
	}))
	defer srv.Close()
	c := &Channels{Roots: pool(srv)}
	n := NTFY{Server: srv.URL, Topic: "top", Token: "tk"}
	if err := c.sendNTFY(context.Background(), n, "Disk\r\nX-Evil: 1", "body", "critical"); err != nil {
		t.Fatal(err)
	}
	if got.auth != "Bearer tk" || got.path != "/" || got.body["topic"] != "top" || got.body["priority"] != float64(5) || strings.ContainsAny(got.body["title"].(string), "\r\n") {
		t.Fatalf("%+v", got.body)
	}
}

func TestNTFYRefusesRedirectUntrustedAndHidesTopic(t *testing.T) {
	var followed bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
	defer target.Close()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	roots := pool(srv)
	roots.AddCert(target.Certificate())
	c := &Channels{Roots: roots}
	n := NTFY{Server: srv.URL, Topic: "secret-topic", Token: "tk"}
	err := c.sendNTFY(context.Background(), n, "s", "b", "info")
	if err == nil || followed {
		t.Fatalf("redirect: %v followed=%v", err, followed)
	}
	// The default pool does not trust the test CA.
	err = (&Channels{}).sendNTFY(context.Background(), n, "s", "b", "info")
	if err == nil || strings.Contains(err.Error(), "secret-topic") || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("%v", err)
	}
}

// fakeMail is an SMTP server on a local port.
type fakeMail struct {
	ln       net.Listener
	cfg      *tls.Config
	starttls bool
	implicit bool

	mu   sync.Mutex
	cmds []string
	data string
}

func (f *fakeMail) serve(t *testing.T) {
	t.Helper()
	go func() {
		for {
			c, err := f.ln.Accept()
			if err != nil {
				return
			}
			go f.conn(c)
		}
	}()
}

func (f *fakeMail) conn(c net.Conn) {
	defer func() { _ = c.Close() }()
	if f.implicit {
		c = tls.Server(c, f.cfg)
	}
	r := bufio.NewReader(c)
	say := func(s string) { _, _ = io.WriteString(c, s+"\r\n") }
	say("220 fake")
	secure := f.implicit
	for {
		l, err := r.ReadString('\n')
		if err != nil {
			return
		}
		l = strings.TrimSpace(l)
		f.mu.Lock()
		f.cmds = append(f.cmds, strings.ToUpper(strings.Fields(l + " x")[0]))
		f.mu.Unlock()
		switch strings.ToUpper(strings.Fields(l + " x")[0]) {
		case "EHLO":
			if f.starttls && !secure {
				say("250-fake\r\n250 STARTTLS")
			} else {
				say("250-fake\r\n250 AUTH PLAIN")
			}
		case "STARTTLS":
			say("220 go")
			tc := tls.Server(c, f.cfg)
			if tc.Handshake() != nil {
				return
			}
			c, r, secure = tc, bufio.NewReader(tc), true
		case "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.data = b.String()
			f.mu.Unlock()
			say("250 ok")
		case "AUTH":
			say("235 ok")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func mailServer(t *testing.T, starttls, implicit bool) (*fakeMail, *x509.CertPool, int) {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.StartTLS()
	t.Cleanup(srv.Close)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	f := &fakeMail{ln: ln, cfg: srv.TLS, starttls: starttls, implicit: implicit}
	f.serve(t)
	return f, pool(srv), ln.Addr().(*net.TCPAddr).Port
}

func mailTo(port int, security string) SMTP {
	return SMTP{Host: "127.0.0.1", Port: port, Security: security, Username: "me", Password: "pw", From: "me@example.com", To: "you@example.com"}
}

func TestSMTPStartTLS(t *testing.T) {
	f, roots, port := mailServer(t, true, false)
	c := &Channels{Roots: roots}
	if err := c.sendSMTP(context.Background(), mailTo(port, "starttls"), "Disk almost full\r\nBcc: evil@example.com", "Line\r\n.\r\nMAIL FROM:<x>"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	head, _, _ := strings.Cut(f.data, "\r\n\r\n")
	if strings.Contains(head, "\r\nBcc:") || !strings.Contains(head, "Subject: [Hearth] Disk almost full") {
		t.Fatalf("%q", head)
	}
	cmds := strings.Join(f.cmds, " ")
	if strings.Index(cmds, "STARTTLS") > strings.Index(cmds, "AUTH") || !strings.Contains(cmds, "DATA") {
		t.Fatal(cmds)
	}
}

func TestSMTPImplicitTLS(t *testing.T) {
	f, roots, port := mailServer(t, false, true)
	if err := (&Channels{Roots: roots}).sendSMTP(context.Background(), mailTo(port, "tls"), "s", "b"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(strings.Join(f.cmds, " "), "AUTH") {
		t.Fatal(f.cmds)
	}
}

// Without TLS nothing goes out: no name, no password, no address.
func TestSMTPNoTLSNoMail(t *testing.T) {
	f, roots, port := mailServer(t, false, false)
	err := (&Channels{Roots: roots}).sendSMTP(context.Background(), mailTo(port, "starttls"), "s", "b")
	if err == nil || !strings.Contains(err.Error(), "does not offer TLS") {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cmds {
		if c == "AUTH" || c == "MAIL" || c == "RCPT" || c == "DATA" {
			t.Fatalf("sent %s", c)
		}
	}
}

func TestSMTPBadCertificateNoAuth(t *testing.T) {
	f, _, port := mailServer(t, true, false)
	if err := (&Channels{}).sendSMTP(context.Background(), mailTo(port, "starttls"), "s", "b"); err == nil {
		t.Fatal("an untrusted certificate was accepted")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Contains(strings.Join(f.cmds, " "), "AUTH") {
		t.Fatal(f.cmds)
	}
}

func TestFanoutKeepsGoing(t *testing.T) {
	ok := &recordNotifier{}
	err := Fanout{failing{}, ok}.Notify(context.Background(), "s", "b", "info")
	if err == nil || ok.n != 1 {
		t.Fatal(err, ok.n)
	}
}

type failing struct{}

func (failing) Notify(context.Context, string, string, string) error { return errors.New("no") }

type recordNotifier struct{ n int }

func (r *recordNotifier) Notify(context.Context, string, string, string) error {
	r.n++
	return nil
}

func TestChannelsAPI(t *testing.T) {
	c := &Channels{Path: filepath.Join(t.TempDir(), "c.json")}
	a := &API{Store: &memStore{}, Channels: c}
	put := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.saveChannels(w, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body)))
		return w
	}
	if w := put(`{"ntfy":{"enabled":true,"server":"http://x.example","topic":"t"}}`); w.Code != http.StatusBadRequest {
		t.Fatal(w.Code)
	}
	if w := put(`{"nope":1}`); w.Code != http.StatusBadRequest {
		t.Fatal(w.Code)
	}
	w := put(`{"ntfy":{"enabled":true,"server":"https://x.example","topic":"my-topic","token":"tk_secret"}}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), "tk_secret") || strings.Contains(w.Body.String(), "my-topic") {
		t.Fatal(w.Code, w.Body)
	}
	ms := a.Store.(*memStore)
	b, _ := json.Marshal(ms.audits)
	if strings.Contains(string(b), "tk_secret") || strings.Contains(string(b), "my-topic") {
		t.Fatalf("%s", b)
	}
	r := httptest.NewRecorder()
	a.test(r, httptest.NewRequest(http.MethodPost, "/?channel="+url.QueryEscape("carrier-pigeon"), nil))
	if r.Code != http.StatusBadRequest {
		t.Fatal(r.Code)
	}
}

func FuzzChannelConfig(f *testing.F) {
	f.Add("https://ntfy.sh", "topic", "me@example.com", "mail.example.com", "subject", 587)
	f.Add("https://u:p@x/", "a/b", "a@b\r\nBcc: c@d", "-", "s\r\nBcc: x@y", 0)
	f.Fuzz(func(t *testing.T, server, topic, addr, host, subject string, port int) {
		n := ChannelConfig{NTFY: NTFY{Server: server, Topic: topic}}
		if n.Validate() == nil {
			u, err := url.Parse(server)
			if err != nil || u.Scheme != "https" || u.User != nil {
				t.Fatalf("accepted %q", server)
			}
		}
		s := SMTP{Host: host, Port: port, Security: "tls", From: addr, To: addr}
		if (ChannelConfig{SMTP: s}).Validate() != nil {
			s = SMTP{From: "a@b.c", To: "d@e.f"}
		} else if strings.ContainsAny(addr+host, "\r\n ") {
			t.Fatalf("accepted %q %q", addr, host)
		}
		head, _, _ := strings.Cut(string(message(s, subject, "b", time.Unix(0, 0))), "\r\n\r\n")
		if n := strings.Count(head, "\r\n"); n != 6 {
			t.Fatalf("%d header line breaks for %q: %q", n, subject, head)
		}
	})
}
