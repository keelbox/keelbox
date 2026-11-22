package notify

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"time"

	"github.com/keelbox/keelbox/internal/procnet"
)

// A channel's address on this machine, loopback or one of its own
// addresses, is a port any local account can listen on while the owner's
// service there is down, restarting or not yet started. Before keelbox
// writes a byte of a request, with its URL, headers or token, to such an
// address, it finds the socket that accepted its connection in the
// kernel's socket tables and refuses to send unless root, a system account
// (UID below 1000, or nobody) or the box's admin owns it (ADR 0054). The
// check is made on the connection itself, after connect, so there is no
// window between check and use; a socket only root's own rules lead to (a
// system Docker container's port with the userland proxy off) is in
// another network namespace, not in these tables, and is root's doing.

// errNotSystemListener is the fixed phrase for a refused connection.
var errNotSystemListener = errors.New("the listener on this machine is not root's, a system account's or the admin's")

// procNetTCP are the socket tables read; a test points them elsewhere.
var procNetTCP = procnet.Tables

// localAddrs are this machine's addresses; a test replaces it.
var localAddrs = func() []netip.Addr {
	var out []netip.Addr
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil {
			out = append(out, p.Addr().Unmap())
		}
	}
	return out
}

// onThisMachine reports whether a is loopback or one of this machine's own
// addresses.
func onThisMachine(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsLoopback() || a.IsUnspecified() {
		return true
	}
	for _, l := range localAddrs() {
		if l == a {
			return true
		}
	}
	return false
}

// trustedListener makes the dial function of the channels' client: it
// connects, and for an address on this machine keeps the connection only
// when allowed accepts the UID that owns the accepting socket.
func trustedListener(allowed func(uid int) bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: Timeout}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		local, lok := addrPort(conn.LocalAddr())
		remote, rok := addrPort(conn.RemoteAddr())
		if !lok || !rok || !onThisMachine(remote.Addr()) {
			return conn, nil
		}
		uid, found := acceptingUID(local, remote)
		if found && !allowed(uid) {
			_ = conn.Close()
			return nil, errNotSystemListener
		}
		return conn, nil
	}
}

func addrPort(a net.Addr) (netip.AddrPort, bool) {
	t, ok := a.(*net.TCPAddr)
	if !ok {
		return netip.AddrPort{}, false
	}
	ap := t.AddrPort()
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), true
}

// acceptingUID finds the socket whose local end is remote and whose peer is
// local, the server's end of keelbox's connection, and returns the UID that
// owns it (the listener's: an accepted socket takes it from the listener).
// The kernel adds that socket as it handles the handshake's last packet, so
// it is looked for a few times over a short while before it counts as not
// on this machine.
func acceptingUID(local, remote netip.AddrPort) (int, bool) {
	for i := 0; i < 10; i++ {
		for _, path := range procNetTCP {
			f, err := os.Open(path)
			if err != nil {
				continue
			}
			uid, found := findSocket(f, remote, local)
			f.Close()
			if found {
				return uid, true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return 0, false
}

// findSocket reads a /proc/net/tcp or tcp6 table for the socket with local
// address lo and remote address rem, and returns its UID column.
func findSocket(r io.Reader, lo, rem netip.AddrPort) (int, bool) {
	for _, s := range procnet.Read(r) {
		if s.Local == lo && s.Remote == rem {
			return s.UID, true
		}
	}
	return 0, false
}

// systemUID is root's, a system account's (below 1000, Ubuntu's UID_MIN)
// or nobody's.
func systemUID(uid int) bool { return uid < 1000 || uid == 65534 }
