// Package netinfo reads this machine's sockets, interfaces, NetworkManager
// state and firewall status (SPEC 11, ROADMAP 3.5). Everything is read as
// the user: sockets come from netlink sock_diag, which lists every
// socket of the network namespace with its owner uid, inode and cgroup.
// Other users' sockets are attributed to their systemd unit or container
// through that cgroup; only the user's own processes are matched to a
// pid (through /proc/<pid>/fd). The broker is not involved.
package netinfo

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"syscall"
	"time"
)

// Socket states (include/net/tcp_states.h).
const (
	stateEstablished = 1
	stateClose       = 7 // an unconnected UDP socket
	stateListen      = 10
)

// sock_diag attributes (include/uapi/linux/inet_diag.h).
const (
	inetDiagInfo     = 2  // struct tcp_info
	inetDiagCgroupID = 21 // u64, Linux 5.7+
)

// Socket is one TCP or UDP socket.
type Socket struct {
	Proto  string         // "tcp" or "udp"
	State  int            // stateListen, stateEstablished, stateClose
	Local  netip.AddrPort //
	Remote netip.AddrPort // zero for a listener
	UID    uint32
	Inode  uint32
	// Cgroup is the cgroup v2 id (the inode of its directory), 0 when the
	// kernel does not report it.
	Cgroup uint64
	// TCP byte counters (tcp_info bytes_acked, bytes_received).
	Sent, Received uint64
}

// maxSockets bounds one dump.
const maxSockets = 20000

// sizeofReq is struct inet_diag_req_v2; sizeofMsg is struct inet_diag_msg.
const (
	sizeofSockID = 48
	sizeofReq    = 8 + sizeofSockID
	sizeofMsg    = 4 + sizeofSockID + 20
)

// Dump lists the sockets of one protocol (syscall.IPPROTO_TCP or
// IPPROTO_UDP) in the given states, over IPv4 and IPv6.
func Dump(proto uint8, states uint32) ([]Socket, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, syscall.NETLINK_INET_DIAG)
	if err != nil {
		return nil, fmt.Errorf("netinfo: sock_diag: %w", err)
	}
	defer func() { _ = syscall.Close(fd) }()
	tv := syscall.NsecToTimeval((2 * time.Second).Nanoseconds())
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv); err != nil {
		return nil, fmt.Errorf("netinfo: sock_diag: %w", err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil, fmt.Errorf("netinfo: sock_diag: %w", err)
	}
	var out []Socket
	buf := make([]byte, 32<<10)
	for i, family := range []uint8{syscall.AF_INET, syscall.AF_INET6} {
		seq := uint32(i + 1) // #nosec G115 -- 1 or 2
		if err := syscall.Sendto(fd, request(family, proto, states, seq), 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
			return nil, fmt.Errorf("netinfo: sock_diag: %w", err)
		}
		for done := false; !done; {
			n, _, err := syscall.Recvfrom(fd, buf, 0)
			if err != nil {
				return nil, fmt.Errorf("netinfo: sock_diag: %w", err)
			}
			msgs, err := syscall.ParseNetlinkMessage(buf[:n])
			if err != nil {
				return nil, fmt.Errorf("netinfo: sock_diag: %w", err)
			}
			for _, m := range msgs {
				if m.Header.Seq != seq {
					continue
				}
				switch m.Header.Type {
				case syscall.NLMSG_DONE:
					done = true
				case syscall.NLMSG_ERROR:
					return nil, errors.New("netinfo: sock_diag: the kernel refused the request")
				default:
					if s, ok := parseMsg(m.Data, proto); ok && len(out) < maxSockets {
						out = append(out, s)
					}
				}
			}
		}
	}
	return out, nil
}

// request is a netlink message with an inet_diag_req_v2 dumping every
// socket of family and proto in states; tcp_info is asked for TCP.
func request(family, proto uint8, states, seq uint32) []byte {
	b := make([]byte, syscall.NLMSG_HDRLEN+sizeofReq)
	ne := binary.NativeEndian
	ne.PutUint32(b[0:], uint32(len(b))) // #nosec G115 -- 72 bytes
	ne.PutUint16(b[4:], 20)             // SOCK_DIAG_BY_FAMILY
	ne.PutUint16(b[6:], syscall.NLM_F_REQUEST|syscall.NLM_F_DUMP)
	ne.PutUint32(b[8:], seq)
	r := b[syscall.NLMSG_HDRLEN:]
	r[0], r[1] = family, proto
	if proto == syscall.IPPROTO_TCP {
		r[2] = 1 << (inetDiagInfo - 1)
	}
	ne.PutUint32(r[4:], states)
	return b
}

// parseMsg reads one inet_diag_msg and its attributes.
func parseMsg(b []byte, proto uint8) (Socket, bool) {
	if len(b) < sizeofMsg {
		return Socket{}, false
	}
	ne := binary.NativeEndian
	m := [sizeofMsg]byte(b[:sizeofMsg])
	s := Socket{Proto: "tcp", State: int(m[1])}
	if proto == syscall.IPPROTO_UDP {
		s.Proto = "udp"
	}
	id := m[4 : 4+sizeofSockID]
	sport, dport := binary.BigEndian.Uint16(id[0:2]), binary.BigEndian.Uint16(id[2:4])
	var src, dst netip.Addr
	if m[0] == syscall.AF_INET {
		src, dst = netip.AddrFrom4([4]byte(id[4:8])), netip.AddrFrom4([4]byte(id[20:24]))
	} else {
		src, dst = netip.AddrFrom16([16]byte(id[4:20])).Unmap(), netip.AddrFrom16([16]byte(id[20:36])).Unmap()
	}
	s.Local, s.Remote = netip.AddrPortFrom(src, sport), netip.AddrPortFrom(dst, dport)
	rest := m[4+sizeofSockID:] // expires, rqueue, wqueue, uid, inode
	s.UID, s.Inode = ne.Uint32(rest[12:16]), ne.Uint32(rest[16:20])
	for a := b[sizeofMsg:]; len(a) >= 4; { // #nosec G602 -- len(b) >= sizeofMsg checked above
		l := int(ne.Uint16(a[0:]))
		typ := ne.Uint16(a[2:]) &^ (1<<15 | 1<<14) // nested / byte-order flags
		if l < 4 || l > len(a) {
			break
		}
		v := a[4:l]
		switch {
		case typ == inetDiagCgroupID && len(v) >= 8:
			s.Cgroup = ne.Uint64(v)
		case typ == inetDiagInfo && len(v) >= 136:
			s.Sent, s.Received = ne.Uint64(v[120:]), ne.Uint64(v[128:])
		}
		next := (l + 3) &^ 3
		if next > len(a) {
			break
		}
		a = a[next:]
	}
	return s, true
}
