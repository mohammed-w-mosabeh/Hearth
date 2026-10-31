package alerts

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Bounds of a remote notification (SECURITY S21d, as S21).
const (
	sendTimeout = 5 * time.Second
	smtpTimeout = 10 * time.Second
	maxReply    = 64 << 10
)

// NTFY is an ntfy server. The topic is as good as a password on a public
// server: anyone who knows it can read the alerts.
type NTFY struct {
	Enabled bool   `json:"enabled"`
	Server  string `json:"server"` // https://host[:port]
	Topic   string `json:"topic"`
	Token   string `json:"token,omitempty"`
}

// SMTP is a mail server. TLS is not optional.
type SMTP struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"` // "tls" or "starttls"
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	From     string `json:"from"`
	To       string `json:"to"`
}

// ChannelConfig is what is kept in the channels file.
type ChannelConfig struct {
	NTFY NTFY `json:"ntfy"`
	SMTP SMTP `json:"smtp"`
}

// LogValue keeps a config out of the logs whatever logs it.
func (ChannelConfig) LogValue() slog.Value { return slog.StringValue("(channels)") }

// ChannelView is the config as the UI sees it: no secret leaves hearthd.
type ChannelView struct {
	NTFY struct {
		Enabled  bool   `json:"enabled"`
		Server   string `json:"server"`
		HasTopic bool   `json:"has_topic"`
		HasToken bool   `json:"has_token"`
	} `json:"ntfy"`
	SMTP struct {
		Enabled     bool   `json:"enabled"`
		Host        string `json:"host"`
		Port        int    `json:"port"`
		Security    string `json:"security"`
		Username    string `json:"username"`
		HasPassword bool   `json:"has_password"`
		From        string `json:"from"`
		To          string `json:"to"`
	} `json:"smtp"`
}

// ChannelUpdate is PUT /api/v1/alerts/channels. A secret left empty keeps
// the saved one, since the form cannot show it.
type ChannelUpdate struct {
	NTFY struct {
		NTFY
		ClearToken bool `json:"clear_token,omitempty"`
	} `json:"ntfy"`
	SMTP SMTP `json:"smtp"`
}

var (
	ntfyTopicRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	smtpHostRe  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)
	mailLocalRe = regexp.MustCompile(`^[A-Za-z0-9.!#$%&'*+/=?^_{|}~-]{1,64}$`)
)

func printable(s string, limit int) bool {
	return s != "" && len(s) <= limit && !strings.ContainsFunc(s, func(r rune) bool {
		return r > unicode.MaxASCII || unicode.IsControl(r) || r == ' '
	})
}

func validAddress(s string) bool {
	local, host, ok := strings.Cut(s, "@")
	return ok && len(s) <= 254 && mailLocalRe.MatchString(local) && smtpHostRe.MatchString(host)
}

// validServer accepts https://host[:port] and nothing else: a path, query
// or user name would be a way to smuggle something past the form.
func validServer(s string) error {
	u, err := url.Parse(s)
	if err != nil || len(s) > 255 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(s, " \t\r\n?#") {
		return errors.New("the ntfy server must look like https://ntfy.sh: HTTPS, no path")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return errors.New("the ntfy server's port is not valid")
		}
	}
	return nil
}

// Validate checks every channel that holds something, enabled or not.
func (c ChannelConfig) Validate() error {
	n := c.NTFY
	if n.Enabled || n.Server != "" || n.Topic != "" || n.Token != "" {
		if err := validServer(n.Server); err != nil {
			return err
		}
		if !ntfyTopicRe.MatchString(n.Topic) {
			return errors.New("the ntfy topic is 1 to 64 letters, digits, - or _")
		}
		if n.Token != "" && !printable(n.Token, 256) {
			return errors.New("the ntfy token has characters a token cannot have")
		}
	}
	s := c.SMTP
	if s.Enabled || s.Host != "" || s.From != "" || s.To != "" || s.Username != "" || s.Password != "" {
		switch {
		case !smtpHostRe.MatchString(s.Host):
			return errors.New("the mail server's host name is not valid")
		case s.Port < 1 || s.Port > 65535:
			return errors.New("the mail server's port must be 1 to 65535")
		case s.Security != "tls" && s.Security != "starttls":
			return errors.New(`mail security must be "tls" or "starttls"`)
		case !validAddress(s.From) || !validAddress(s.To):
			return errors.New("the mail addresses must look like name@example.com")
		case s.Username != "" && !printable(s.Username, 254):
			return errors.New("the mail user name is not valid")
		case (s.Username == "") != (s.Password == ""):
			return errors.New("a mail user name and password go together")
		case len(s.Password) > 512 || strings.ContainsFunc(s.Password, unicode.IsControl):
			return errors.New("the mail password is not valid")
		}
	}
	return nil
}

// Channels holds the remote channels. The config, secrets included, is
// its own 0600 file and not in the database, so a database backup or copy
// can never carry it (SECURITY S19, S20).
type Channels struct {
	Path string
	Log  *slog.Logger
	// Roots replaces the system's CAs in tests.
	Roots *x509.CertPool

	mu  sync.Mutex
	cfg ChannelConfig
}

// Load reads the file; no file is an empty config.
func (c *Channels) Load() error {
	b, err := os.ReadFile(c.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var cfg ChannelConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return errors.New("alerts: the channels file is not valid") // json errors can quote it
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("alerts: the channels file: %w", err)
	}
	c.mu.Lock()
	c.cfg = cfg
	c.mu.Unlock()
	return nil
}

func (c *Channels) config() ChannelConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// View is the config without its secrets.
func (c *Channels) View() ChannelView {
	cfg := c.config()
	var v ChannelView
	v.NTFY.Enabled, v.NTFY.Server = cfg.NTFY.Enabled, cfg.NTFY.Server
	v.NTFY.HasTopic, v.NTFY.HasToken = cfg.NTFY.Topic != "", cfg.NTFY.Token != ""
	s := cfg.SMTP
	v.SMTP.Enabled, v.SMTP.Host, v.SMTP.Port, v.SMTP.Security = s.Enabled, s.Host, s.Port, s.Security
	v.SMTP.Username, v.SMTP.HasPassword, v.SMTP.From, v.SMTP.To = s.Username, s.Password != "", s.From, s.To
	return v
}

// merge applies an update: an empty secret keeps the saved one, and a mail
// server without a user name has no password.
func merge(old ChannelConfig, u ChannelUpdate) ChannelConfig {
	cfg := ChannelConfig{NTFY: u.NTFY.NTFY, SMTP: u.SMTP}
	if cfg.NTFY.Topic == "" {
		cfg.NTFY.Topic = old.NTFY.Topic
	}
	if cfg.NTFY.Token == "" && !u.NTFY.ClearToken {
		cfg.NTFY.Token = old.NTFY.Token
	}
	if cfg.SMTP.Username == "" {
		cfg.SMTP.Password = ""
	} else if cfg.SMTP.Password == "" {
		cfg.SMTP.Password = old.SMTP.Password
	}
	return cfg
}

// Update validates and saves an update.
func (c *Channels) Update(u ChannelUpdate) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cfg := merge(c.cfg, u)
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := writeSecret(c.Path, cfg); err != nil {
		return err
	}
	c.cfg = cfg
	return nil
}

// writeSecret replaces path atomically; the temporary file is 0600 from
// its creation, so the secrets are never readable by anyone else.
func writeSecret(path string, cfg ChannelConfig) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".channels-*")
	if err != nil {
		return errors.New("alerts: the channels file could not be written")
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return errors.New("alerts: the channels file could not be written")
	}
	return nil
}

// clean makes a string safe to put in a mail header or a message.
func clean(s string, limit int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	if r := []rune(s); len(r) > limit {
		s = string(r[:limit])
	}
	return s
}

func ntfyPriority(severity string) int {
	switch severity {
	case "critical":
		return 5
	case "warning":
		return 4
	}
	return 3
}

// sendNTFY publishes as JSON to the server's root, so nothing the system
// names can end up in a header. No redirects: a POST with a token goes to
// the host the user typed or nowhere. Errors never carry the URL, which
// holds the topic.
func (c *Channels) sendNTFY(ctx context.Context, n NTFY, summary, body, severity string) error {
	payload, err := json.Marshal(map[string]any{"topic": n.Topic, "title": clean(summary, 200), "message": clean(body, 1000), "priority": ntfyPriority(severity)})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(n.Server, "/")+"/", bytes.NewReader(payload))
	if err != nil {
		return errors.New("ntfy: bad server")
	}
	req.Header.Set("Content-Type", "application/json")
	if n.Token != "" {
		req.Header.Set("Authorization", "Bearer "+n.Token)
	}
	client := http.Client{
		Transport: &http.Transport{
			Proxy: nil, DisableKeepAlives: true, ForceAttemptHTTP2: false,
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: c.Roots},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) && ue.Timeout() {
			return errors.New("ntfy: the server did not answer in time")
		}
		return errors.New("ntfy: the server could not be reached over HTTPS")
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReply))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("ntfy: the server answered %d", resp.StatusCode)
	}
	return nil
}

// message builds the mail. Addresses are validated, and the subject is
// cleaned of line breaks and encoded, so nothing can add a header.
func message(s SMTP, summary, body string, now time.Time) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\n", s.From, s.To,
		mime.QEncoding.Encode("utf-8", "[Hearth] "+clean(summary, 200)), now.Format(time.RFC1123Z))
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	w := quotedprintable.NewWriter(&b)
	_, _ = w.Write([]byte(clean(body, 2000) + "\r\n"))
	_ = w.Close()
	return b.Bytes()
}

// sendSMTP sends one mail, over TLS or not at all: a server that offers no
// STARTTLS gets no address and no password. Certificates are verified
// against the host name. Errors name the step, never what the server said.
func (c *Channels) sendSMTP(ctx context.Context, s SMTP, summary, body string) error {
	ctx, cancel := context.WithTimeout(ctx, smtpTimeout)
	defer cancel()
	raw, err := (&net.Dialer{Timeout: sendTimeout}).DialContext(ctx, "tcp", net.JoinHostPort(s.Host, strconv.Itoa(s.Port)))
	if err != nil {
		return errors.New("mail: the server could not be reached")
	}
	defer func() { _ = raw.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(dl)
	}
	cfg := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12, RootCAs: c.Roots}
	conn := raw
	if s.Security == "tls" {
		tc := tls.Client(raw, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return errors.New("mail: TLS failed: the certificate may not match the host")
		}
		conn = tc
	}
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return errors.New("mail: the server did not greet")
	}
	defer func() { _ = client.Close() }()
	if s.Security == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("mail: the server does not offer TLS, so nothing was sent")
		}
		if err := client.StartTLS(cfg); err != nil {
			return errors.New("mail: TLS failed: the certificate may not match the host")
		}
	}
	if s.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return errors.New("mail: the server refused the user name or password")
		}
	}
	if err := client.Mail(s.From); err != nil {
		return errors.New("mail: the server refused the sender")
	}
	if err := client.Rcpt(s.To); err != nil {
		return errors.New("mail: the server refused the recipient")
	}
	w, err := client.Data()
	if err != nil {
		return errors.New("mail: the server refused the message")
	}
	if _, err := w.Write(message(s, summary, body, time.Now())); err != nil {
		return errors.New("mail: the message could not be sent")
	}
	if err := w.Close(); err != nil {
		return errors.New("mail: the server refused the message")
	}
	_ = client.Quit()
	return nil
}

// Send sends through one channel, enabled or not (for the test button).
func (c *Channels) Send(ctx context.Context, channel, summary, body, severity string) error {
	cfg := c.config()
	switch channel {
	case "ntfy":
		if cfg.NTFY.Server == "" {
			return errors.New("ntfy is not set up")
		}
		return c.sendNTFY(ctx, cfg.NTFY, summary, body, severity)
	case "smtp":
		if cfg.SMTP.Host == "" {
			return errors.New("mail is not set up")
		}
		return c.sendSMTP(ctx, cfg.SMTP, summary, body)
	}
	return errors.New("unknown channel")
}

// Notify implements Notifier: every enabled channel, at the same time.
func (c *Channels) Notify(ctx context.Context, summary, body, severity string) error {
	cfg := c.config()
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	send := func(channel string) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.Send(ctx, channel, summary, body, severity); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	if cfg.NTFY.Enabled {
		send("ntfy")
	}
	if cfg.SMTP.Enabled {
		send("smtp")
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Fanout sends one notification to several notifiers; one failing keeps
// none of the others from it.
type Fanout []Notifier

// Notify implements Notifier.
func (f Fanout) Notify(ctx context.Context, summary, body, severity string) error {
	errs := make([]error, len(f))
	var wg sync.WaitGroup
	for i, n := range f {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = n.Notify(ctx, summary, body, severity)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}
