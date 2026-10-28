package netinfo

import (
	"context"
	"log/slog"
	"net/netip"
	"reflect"
	"syscall"
	"testing"
	"time"
)

func TestExposedWatcher(t *testing.T) {
	r, cg := fakeSystem(t)
	ap := netip.MustParseAddrPort
	tcp := []Socket{
		{Proto: "tcp", State: stateListen, Local: ap("[::]:22"), Inode: 21, Cgroup: cg["ssh"]},
		{Proto: "tcp", State: stateListen, Local: ap("127.0.0.1:631"), Inode: 22},
	}
	udp := []Socket{
		{Proto: "udp", State: stateClose, Local: ap("0.0.0.0:41134"), UID: 1000, Inode: 31}, // ephemeral: a client
	}
	dump := func(proto uint8, _ uint32) ([]Socket, error) {
		if proto == syscall.IPPROTO_UDP {
			return udp, nil
		}
		return tcp, nil
	}
	var saved []string
	var stored bool
	var observed []map[string]float64
	w := &ExposedWatcher{Resolver: r, Dump: dump, Users: func() map[int]string { return map[int]string{1000: "me", 0: "root"} }, Log: slog.Default(),
		Load:    func(context.Context) ([]string, bool, error) { return saved, stored, nil },
		Save:    func(_ context.Context, k []string) error { saved, stored = k, true; return nil },
		Observe: func(_ context.Context, _ time.Time, v map[string]float64) { observed = append(observed, v) },
	}
	ctx := context.Background()
	// First run ever: ssh is recorded, nothing reported.
	known := w.poll(ctx, nil, time.Now())
	if !reflect.DeepEqual(saved, []string{"TCP 22 (ssh)"}) || len(observed[0]) != 0 {
		t.Fatalf("first run: saved %v observed %v", saved, observed)
	}
	// A new exposed listener is reported once, then resolves.
	tcp = append(tcp, Socket{Proto: "tcp", State: stateListen, Local: ap("0.0.0.0:8080"), UID: 1000, Inode: 11, Cgroup: cg["app"]})
	udp = append(udp, Socket{Proto: "udp", State: stateClose, Local: ap("0.0.0.0:5353"), UID: 1000, Inode: 32})
	known = w.poll(ctx, known, time.Now())
	want := map[string]float64{"TCP 8080 (Example Chat)": 1, "UDP 5353 (me)": 1}
	if got := observed[1]; !reflect.DeepEqual(got, want) {
		t.Errorf("new: %v", got)
	}
	known = w.poll(ctx, known, time.Now())
	if len(observed[2]) != 0 {
		t.Errorf("reported twice: %v", observed[2])
	}
	// After a restart (known set from storage) nothing is reported again.
	w.poll(ctx, nil, time.Now())
	if len(observed[3]) != 0 || len(saved) != 3 {
		t.Errorf("after restart: %v, saved %v", observed[3], saved)
	}
	_ = known
}
