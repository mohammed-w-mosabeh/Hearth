package netinfo

import (
	"net/netip"
	"sort"
	"strconv"
	"syscall"
)

// Address scopes of a local address.
const (
	ScopeLoopback = "loopback" // 127.0.0.0/8, ::1: this machine only
	ScopeAddress  = "address"  // one address: the networks it is on
	ScopeAll      = "all"      // 0.0.0.0 or ::: every network (exposed)
)

// Endpoint is a listening socket or a connection.
type Endpoint struct {
	Proto   string `json:"proto"`
	Address string `json:"address"` // local address
	Port    uint16 `json:"port"`
	Scope   string `json:"scope"`
	Remote  string `json:"remote,omitempty"` // "addr:port" of a connection's peer
	UID     uint32 `json:"uid"`
	User    string `json:"user,omitempty"`
	Owner   *Owner `json:"owner,omitempty"`
	// TCP bytes so far (connections only).
	Sent     uint64 `json:"sent,omitempty"`
	Received uint64 `json:"received,omitempty"`
}

// Sockets is one read of the sockets.
type Sockets struct {
	Listeners   []Endpoint `json:"listeners"`
	Connections []Endpoint `json:"connections"`
	// Truncated is set when there were more connections than are sent.
	Truncated bool `json:"truncated,omitempty"`
}

// maxConnections bounds the connections sent to the UI.
const maxConnections = 2000

// Dumper lists sockets (Dump in production).
type Dumper func(proto uint8, states uint32) ([]Socket, error)

// Read lists the listening sockets (TCP LISTEN, unconnected bound UDP) and
// connections (TCP ESTABLISHED, connected UDP), with their owners.
func (r *Resolver) Read(dump Dumper, users map[int]string) (Sockets, error) {
	tcp, err := dump(syscall.IPPROTO_TCP, 1<<stateListen|1<<stateEstablished)
	if err != nil {
		return Sockets{}, err
	}
	udp, err := dump(syscall.IPPROTO_UDP, 1<<stateClose|1<<stateEstablished)
	if err != nil {
		return Sockets{}, err
	}
	all := append(tcp, udp...)
	want := map[uint32]bool{}
	for _, s := range all {
		if s.Inode != 0 {
			want[s.Inode] = true
		}
	}
	fd := r.fdOwners(want)
	walked := false
	procs := map[int]procRef{}
	out := Sockets{Listeners: []Endpoint{}, Connections: []Endpoint{}}
	for _, s := range all {
		listener := s.State == stateListen || (s.Proto == "udp" && s.State == stateClose)
		if !listener && s.State != stateEstablished {
			continue
		}
		if s.Local.Port() == 0 {
			continue // an unbound UDP socket
		}
		if !listener && len(out.Connections) >= maxConnections {
			out.Truncated = true
			continue
		}
		e := Endpoint{Proto: s.Proto, Address: s.Local.Addr().String(), Port: s.Local.Port(),
			Scope: scopeOf(s.Local.Addr()), UID: s.UID, User: users[int(s.UID)]}
		if e.User == "" {
			e.User = strconv.FormatUint(uint64(s.UID), 10)
		}
		if s.Inode != 0 {
			e.Owner = r.owner(s, fd, &walked, procs)
		}
		if listener {
			out.Listeners = append(out.Listeners, e)
			continue
		}
		e.Remote = s.Remote.String()
		e.Sent, e.Received = s.Sent, s.Received
		out.Connections = append(out.Connections, e)
	}
	sort.SliceStable(out.Listeners, func(i, j int) bool { return less(out.Listeners[i], out.Listeners[j]) })
	sort.SliceStable(out.Connections, func(i, j int) bool { return less(out.Connections[i], out.Connections[j]) })
	return out, nil
}

func less(a, b Endpoint) bool {
	if a.Port != b.Port {
		return a.Port < b.Port
	}
	if a.Proto != b.Proto {
		return a.Proto < b.Proto
	}
	return a.Address < b.Address
}

// scopeOf says who can reach a local address.
func scopeOf(a netip.Addr) string {
	switch {
	case a.IsUnspecified():
		return ScopeAll
	case a.IsLoopback():
		return ScopeLoopback
	default:
		return ScopeAddress
	}
}
