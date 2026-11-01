package connectivity

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"syscall"
	"time"
)

// probeTimeout bounds one check.
const probeTimeout = 5 * time.Second

// ErrNoPing: this user may not open ping sockets (ping_group_range).
var ErrNoPing = errors.New("ping is not allowed for this user")

// TCPCheck connects and closes. Nothing is sent.
func TCPCheck(ctx context.Context, host string, port int) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var d net.Dialer
	start := time.Now()
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return 0, err
	}
	took := time.Since(start)
	_ = c.Close()
	return took, nil
}

// DNSCheck resolves name through the system's resolver.
func DNSCheck(ctx context.Context, name string) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	start := time.Now()
	_, err := net.DefaultResolver.LookupHost(ctx, name)
	return time.Since(start), err
}

// echoRequest builds an ICMP echo request; the kernel fills in the ID and
// the checksum of a ping socket.
func echoRequest(seq uint16) []byte {
	b := make([]byte, 16)
	b[0] = 8 // echo request
	binary.BigEndian.PutUint16(b[6:], seq)
	return b
}

// isEchoReply reports whether b answers seq.
func isEchoReply(b []byte, seq uint16) bool {
	return len(b) >= 8 && b[0] == 0 && binary.BigEndian.Uint16(b[6:]) == seq
}

// PingCheck sends one echo over an unprivileged ping socket (SOCK_DGRAM,
// IPv4). It never opens a raw socket; a user outside ping_group_range gets
// ErrNoPing.
func PingCheck(ctx context.Context, host string) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
	if err != nil {
		return 0, err
	}
	if len(addrs) == 0 {
		return 0, errors.New("no IPv4 address")
	}
	to := addrs[0].Unmap().As4()
	return ping(to, uint16(time.Now().UnixNano()&0xffff), probeTimeout) // #nosec G115 -- masked to 16 bits
}

func ping(to [4]byte, seq uint16, wait time.Duration) (time.Duration, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, syscall.IPPROTO_ICMP)
	if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		return 0, ErrNoPing
	}
	if err != nil {
		return 0, err
	}
	defer func() { _ = syscall.Close(fd) }()
	tv := syscall.NsecToTimeval(wait.Nanoseconds())
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		return 0, err
	}
	start := time.Now()
	if err := syscall.Sendto(fd, echoRequest(seq), 0, &syscall.SockaddrInet4{Addr: to}); err != nil {
		return 0, err
	}
	buf := make([]byte, 128)
	for time.Since(start) < wait {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			return 0, err
		}
		if isEchoReply(buf[:n], seq) {
			return time.Since(start), nil
		}
	}
	return 0, errors.New("no reply")
}

// isV4 reports whether s is an IPv4 address.
func isV4(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && a.Is4()
}
