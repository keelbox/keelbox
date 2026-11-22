// Package procnet reads the kernel's TCP socket tables, /proc/net/tcp and
// /proc/net/tcp6, which every account may read.
package procnet

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// Tables are the socket tables, IPv4 and IPv6; a test points them elsewhere.
var Tables = []string{"/proc/net/tcp", "/proc/net/tcp6"}

// Socket is one row of a table.
type Socket struct {
	Local, Remote netip.AddrPort
	Listen        bool // state 0A
	UID           int
}

// Read parses one table. Rows it cannot parse are skipped.
func Read(r io.Reader) []Socket {
	var out []Socket
	sc := bufio.NewScanner(r)
	sc.Scan() // the header
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 8 {
			continue
		}
		l, ok1 := ParseAddr(f[1])
		rem, ok2 := ParseAddr(f[2])
		uid, err := strconv.Atoi(f[7])
		if !ok1 || !ok2 || err != nil {
			continue
		}
		out = append(out, Socket{Local: l, Remote: rem, Listen: f[3] == "0A", UID: uid})
	}
	return out
}

// All reads every table; a table that cannot be opened is skipped.
func All() []Socket {
	var out []Socket
	for _, p := range Tables {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		out = append(out, Read(f)...)
		f.Close()
	}
	return out
}

// LoopbackListener reports whether a socket listens on port on a loopback
// address (127.0.0.0/8, ::1) or on every address, IPv4 or IPv6.
func LoopbackListener(socks []Socket, port int) bool {
	for _, s := range socks {
		a := s.Local.Addr()
		if s.Listen && int(s.Local.Port()) == port && (a.IsLoopback() || a.IsUnspecified()) {
			return true
		}
	}
	return false
}

// ParseAddr reads an address as the tables write it: the IP in
// hexadecimal, as 32-bit words in the machine's byte order, then the port.
// IPv4-mapped IPv6 addresses come back as IPv4.
func ParseAddr(s string) (netip.AddrPort, bool) {
	ipHex, portHex, ok := strings.Cut(s, ":")
	if !ok {
		return netip.AddrPort{}, false
	}
	raw, err1 := hex.DecodeString(ipHex)
	port, err2 := strconv.ParseUint(portHex, 16, 16)
	if err1 != nil || err2 != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.AddrPort{}, false
	}
	for i := 0; i < len(raw); i += 4 {
		w := binary.NativeEndian.Uint32(raw[i:])
		binary.BigEndian.PutUint32(raw[i:], w)
	}
	ip, ok := netip.AddrFromSlice(raw)
	if !ok {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(ip.Unmap(), uint16(port)), true
}
