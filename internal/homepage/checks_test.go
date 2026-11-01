package homepage

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tlsServer serves https on 127.0.0.1 with a self-signed certificate
// ending at end, and returns the pool that trusts it.
func tlsServer(t *testing.T, end time.Time, code int) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"},
		NotBefore: end.Add(-90 * 24 * time.Hour), NotAfter: end,
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return srv, pool
}

func checkerTrusting(pool *x509.CertPool) *Checker {
	c := NewChecker(nil, nil, nil)
	c.client = healthClient(&http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}})
	return c
}

func TestCertNote(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	const day = 24 * time.Hour
	for left, want := range map[time.Duration]string{
		30 * day:       "",
		14 * day:       "",
		14*day - 1:     "its certificate expires in 13 days",
		2 * day:        "its certificate expires in 2 days",
		36 * time.Hour: "its certificate expires in 1 day",
		time.Hour:      "its certificate expires within a day",
		0:              "its certificate has expired",
		-3 * day:       "its certificate has expired",
	} {
		if got := certNote(now.Add(left), now); got != want {
			t.Errorf("%v: %q, want %q", left, got, want)
		}
	}
}

func TestCheckCertificate(t *testing.T) {
	ctx := context.Background()
	const day = 24 * time.Hour
	t.Run("far off", func(t *testing.T) {
		end := time.Now().Add(60 * day).Truncate(time.Second)
		srv, pool := tlsServer(t, end, 200)
		st := checkerTrusting(pool).Check(ctx, srv.URL)
		if st.State != "up" || st.Error != "" || st.CertExpires != end.UnixMilli() {
			t.Fatalf("%+v", st)
		}
	})
	t.Run("soon", func(t *testing.T) {
		srv, pool := tlsServer(t, time.Now().Add(5*day+time.Hour), 200)
		st := checkerTrusting(pool).Check(ctx, srv.URL)
		if st.State != "warn" || st.Error != "its certificate expires in 5 days" || st.Code != 200 {
			t.Fatalf("%+v", st)
		}
	})
	t.Run("a failing server stays down", func(t *testing.T) {
		srv, pool := tlsServer(t, time.Now().Add(5*day), 503)
		st := checkerTrusting(pool).Check(ctx, srv.URL)
		if st.State != "down" || st.CertExpires == 0 {
			t.Fatalf("%+v", st)
		}
	})
	t.Run("expired", func(t *testing.T) {
		end := time.Now().Add(-day).Truncate(time.Second)
		srv, pool := tlsServer(t, end, 200)
		st := checkerTrusting(pool).Check(ctx, srv.URL)
		if st.State != "warn" || st.Error != "its certificate has expired" || st.CertExpires != end.UnixMilli() {
			t.Fatalf("%+v", st)
		}
	})
	t.Run("untrusted, far off", func(t *testing.T) {
		end := time.Now().Add(60 * day).Truncate(time.Second)
		srv, _ := tlsServer(t, end, 200)
		st := NewChecker(nil, nil, nil).Check(ctx, srv.URL)
		if st.State != "warn" || st.Error != "its certificate is not trusted" || st.CertExpires != end.UnixMilli() {
			t.Fatalf("%+v", st)
		}
	})
	t.Run("untrusted, soon", func(t *testing.T) {
		srv, _ := tlsServer(t, time.Now().Add(3*day+time.Hour), 200)
		st := NewChecker(nil, nil, nil).Check(ctx, srv.URL)
		if st.State != "warn" || st.Error != "its certificate is not trusted and expires in 3 days" {
			t.Fatalf("%+v", st)
		}
	})
	t.Run("plain http has none", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer srv.Close()
		st := NewChecker(nil, nil, nil).Check(ctx, srv.URL)
		if st.State != "up" || st.CertExpires != 0 {
			t.Fatalf("%+v", st)
		}
	})
}

func TestCheckPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	port := l.Addr().(*net.TCPAddr).Port
	got := make(chan int, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		b, _ := io.ReadAll(conn)
		got <- len(b)
	}()
	c := NewChecker(nil, nil, nil)
	// The URL's own port and path play no part.
	st := c.CheckPort(context.Background(), "http://127.0.0.1:1/some/page", port)
	if st.State != "up" || st.Error != "" {
		t.Fatalf("%+v", st)
	}
	select {
	case n := <-got:
		if n != 0 {
			t.Fatalf("the check sent %d bytes", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connection stayed open")
	}
	_ = l.Close()
	if st := c.CheckPort(context.Background(), "http://127.0.0.1/", port); st.State != "down" || st.Error != "connection refused" {
		t.Fatalf("%+v", st)
	}
	if st := c.CheckPort(context.Background(), "ftp://127.0.0.1/", port); st.State != "down" || st.Error != "invalid address" {
		t.Fatalf("%+v", st)
	}
}

func TestTCPTarget(t *testing.T) {
	for _, tc := range []struct {
		url  string
		port int
		want string
	}{
		{"http://nas.lan:8080/x?y=1", 22, "nas.lan:22"},
		{"https://[::1]/", 5432, "[::1]:5432"},
		{"http://a/", 65535, "a:65535"},
		{"http://a/", 0, ""},
		{"http://a/", -1, ""},
		{"http://a/", 65536, ""},
		{"ftp://a/", 22, ""},
	} {
		got, err := tcpTarget(tc.url, tc.port)
		if got != tc.want || (err == nil) != (tc.want != "") {
			t.Errorf("%q %d: %q, %v", tc.url, tc.port, got, err)
		}
	}
}

func FuzzTCPTarget(f *testing.F) {
	for _, s := range []string{"http://127.0.0.1:8080/", "https://[::1]/", "http://u:p@h/", "http://h:99999/", "javascript:alert(1)"} {
		f.Add(s, 22)
		f.Add(s, 0)
		f.Add(s, 70000)
	}
	f.Fuzz(func(t *testing.T, s string, port int) {
		target, err := tcpTarget(s, port)
		if err != nil {
			return
		}
		if port < 1 || port > 65535 || ValidURL(s) != nil {
			t.Fatalf("accepted %q %d", s, port)
		}
		host, p, err := net.SplitHostPort(target)
		if err != nil || host == "" || p != strconv.Itoa(port) || strings.ContainsAny(host, " /\x00") {
			t.Fatalf("%q %d gave %q", s, port, target)
		}
	})
}
