package launcher

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

const (
	token = "0123456789abcdef"
	link  = "http://127.0.0.1:7380/auth?t=" + token
)

type fakeBus struct {
	callErr  error
	response *uint32
	opened   []string
	notified []string
	sigs     chan *dbus.Signal
}

func (b *fakeBus) Close() error { return nil }
func (b *fakeBus) Watch(context.Context, ...dbus.MatchOption) (<-chan *dbus.Signal, error) {
	b.sigs = make(chan *dbus.Signal, 2)
	return b.sigs, nil
}
func (b *fakeBus) Call(_ context.Context, _ string, _ dbus.ObjectPath, method string, out []any, args ...any) error {
	if strings.HasSuffix(method, ".Notify") {
		b.notified = append(b.notified, args[3].(string)+" "+args[4].(string))
		return nil
	}
	if b.callErr != nil {
		return b.callErr
	}
	b.opened = append(b.opened, args[1].(string))
	*out[0].(*dbus.ObjectPath) = "/req/1"
	if b.response != nil {
		b.sigs <- &dbus.Signal{Path: "/req/other", Body: []any{uint32(2)}} // another request's
		b.sigs <- &dbus.Signal{Path: "/req/1", Body: []any{*b.response}}
	}
	return nil
}

type starts struct {
	names []string
	errs  map[string]error
}

func (s *starts) start(name string, args ...string) error {
	s.names = append(s.names, name)
	if slices.Contains(args, link) {
		return s.errs[name]
	}
	return errors.New("no link in the arguments")
}

func code(c uint32) *uint32 { return &c }

func config(b *fakeBus, s *starts) Config {
	return Config{Session: func(context.Context) (Bus, error) { return b, nil }, Start: s.start}
}

func TestPortal(t *testing.T) {
	for _, c := range []uint32{responseOK, responseCancelled} {
		b, s := &fakeBus{response: code(c)}, &starts{}
		if err := Open(context.Background(), config(b, s), link); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(b.opened, []string{link}) || len(s.names) != 0 {
			t.Fatalf("%v %v", b.opened, s.names)
		}
	}
}

func TestFallbacks(t *testing.T) {
	for name, b := range map[string]*fakeBus{
		"no portal":     {callErr: errors.New("ServiceUnknown")},
		"portal failed": {response: code(2)},
	} {
		s := &starts{}
		if err := Open(context.Background(), config(b, s), link); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(s.names, []string{"gio-open"}) {
			t.Fatalf("%s: %v", name, s.names)
		}
	}
	s := &starts{errs: map[string]error{"gio-open": errors.New("no gio")}}
	if err := Open(context.Background(), config(&fakeBus{callErr: errors.New("x")}, s), link); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.names, []string{"gio-open", "xdg-open"}) {
		t.Fatalf("%v", s.names)
	}
}

func TestNoBusStillFallsBack(t *testing.T) {
	s := &starts{}
	cfg := config(nil, s)
	cfg.Session = func(context.Context) (Bus, error) { return nil, errors.New("no bus") }
	if err := Open(context.Background(), cfg, link); err != nil || len(s.names) != 1 {
		t.Fatalf("%v %v", err, s.names)
	}
}

func TestFailureNotifiesWithoutTheToken(t *testing.T) {
	b := &fakeBus{callErr: errors.New("refused " + link)}
	s := &starts{errs: map[string]error{"gio-open": errors.New("t=" + token), "xdg-open": errors.New("no")}}
	err := Open(context.Background(), config(b, s), link)
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("%v", err)
	}
	if len(b.notified) != 1 || strings.Contains(b.notified[0], token) || !strings.Contains(b.notified[0], "http://127.0.0.1:7380/") {
		t.Fatalf("%v", b.notified)
	}
}

func TestPortalOnlyStartsNothing(t *testing.T) {
	b, s := &fakeBus{callErr: errors.New("no portal")}, &starts{}
	cfg := config(b, s)
	cfg.PortalOnly = true
	if err := Open(context.Background(), cfg, link); err == nil || len(s.names) != 0 || len(b.notified) != 1 {
		t.Fatalf("%v %v %v", err, s.names, b.notified)
	}
}
