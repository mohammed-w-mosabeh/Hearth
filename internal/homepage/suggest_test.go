package homepage

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestParseProcAddr(t *testing.T) {
	for in, want := range map[string]string{
		"0100007F:1F90":                         "127.0.0.1 8080",
		"00000000:0050":                         "0.0.0.0 80",
		"00000000000000000000000001000000:0BB8": "::1 3000",
		"0000000000000000FFFF00000101A8C0:1F90": "192.168.1.1 8080",
	} {
		ip, port, ok := parseProcAddr(in)
		if got := ip.String() + " " + itoa(int64(port)); !ok || got != want {
			t.Errorf("%s: %s %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "zz:1", "0100007F", "0100007F:0000", "01:0050"} {
		if _, _, ok := parseProcAddr(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestPageTitle(t *testing.T) {
	for in, want := range map[string]string{
		`<html><head><TITLE> Jellyfin  &amp; co </TITLE></head>`: "Jellyfin & co",
		`<title>` + strings.Repeat("x", 200) + `</title>`:        strings.Repeat("x", 80),
		`no title`:                          "",
		"<title>a\x00b</title>":             "",
		`<title><script>x</script></title>`: "<script>x</script>", // plain text; the UI escapes it
	} {
		if got := pageTitle([]byte(in)); got != want {
			t.Errorf("%.40q: %q", in, got)
		}
	}
}

type dockerList string

func (d dockerList) Call(_ context.Context, method string, _, out any) error {
	return json.Unmarshal([]byte(d), out)
}

func TestSuggest(t *testing.T) {
	proc := t.TempDir()
	// Listeners: 8096 (wildcard, the user's "jellyfin"), 5432 (postgres,
	// never probed), 7380 (hearthd), 9000 (a published container port),
	// 4000 (an existing tile), 8443 on loopback, one not listening.
	tcp := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 00000000:1FA0 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 111 1\n" +
		"   1: 00000000:1538 00000000:0000 0A 00000000:00000000 00:00000000 00000000   999        0 222 1\n" +
		"   2: 0100007F:1CD4 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 333 1\n" +
		"   3: 00000000:2328 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 444 1\n" +
		"   4: 0100007F:0FA0 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 555 1\n" +
		"   5: 0100007F:20FB 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 666 1\n" +
		"   6: 0100007F:1F41 0100007F:9999 01 00000000:00000000 00:00000000 00000000  1000        0 777 1\n"
	_ = os.MkdirAll(filepath.Join(proc, "net"), 0o750)
	_ = os.WriteFile(filepath.Join(proc, "net", "tcp"), []byte(tcp), 0o600)
	_ = os.MkdirAll(filepath.Join(proc, "42", "fd"), 0o750)
	_ = os.WriteFile(filepath.Join(proc, "42", "comm"), []byte("jellyfin\n"), 0o600)
	_ = os.Symlink("socket:[111]", filepath.Join(proc, "42", "fd", "7"))

	var mu sync.Mutex
	var probed []string
	s := &Suggester{
		Docker: dockerList(`[{"Names":["/filestore"],"State":"running","Ports":[{"IP":"0.0.0.0","PublicPort":9000,"Type":"tcp"},
			{"IP":"::","PublicPort":9000,"Type":"tcp"},{"PrivatePort":9001,"Type":"tcp"}]},
			{"Names":["/old"],"State":"exited","Ports":[{"PublicPort":81,"Type":"tcp"}]}]`),
		ProcRoot: proc, SelfPort: 7380,
		Probe: func(_ context.Context, u string) (bool, string) {
			mu.Lock()
			probed = append(probed, u)
			mu.Unlock()
			switch u {
			case "http://127.0.0.1:8096/":
				return true, "Jellyfin"
			case "http://127.0.0.1:9000/":
				return true, "Files Console"
			}
			return false, ""
		},
	}
	got := s.Suggest(context.Background(), []string{"http://localhost:4000/app"})
	b, _ := json.Marshal(got)
	if len(got) != 2 || got[0].Source != "container" || got[0].Name != "filestore" || got[0].Title != "Files Console" ||
		got[1].Name != "Jellyfin" || got[1].Process != "jellyfin" || got[1].URL != "http://127.0.0.1:8096/" {
		t.Fatalf("%s", b)
	}
	for _, p := range probed {
		for _, never := range []string{":5432", ":7380", ":4000", ":81/", ":8001"} {
			if strings.Contains(p, never) {
				t.Errorf("probed %s", p)
			}
		}
	}
	if len(probed) != 3 { // filestore, jellyfin, 8443 (no HTTP answer)
		t.Errorf("probed %v", probed)
	}
}

func TestHTTPProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<title>Pi-hole</title>"))
	}))
	defer srv.Close()
	probe := HTTPProbe(nil)
	if ok, title := probe(context.Background(), srv.URL+"/"); !ok || title != "Pi-hole" {
		t.Fatalf("%v %q", ok, title)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0") // speaks nothing
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			_, _ = c.Write([]byte("SSH-2.0-OpenSSH\r\n"))
			_ = c.Close()
		}
	}()
	if ok, _ := probe(context.Background(), "http://"+ln.Addr().String()+"/"); ok {
		t.Fatal("non-HTTP accepted")
	}
}
