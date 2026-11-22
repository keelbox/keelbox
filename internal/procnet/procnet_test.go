package procnet

import (
	"net/netip"
	"strings"
	"testing"
)

const tcp4 = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1002        0 31337 1 0000000000000000 100 0 0 10 0
   1: 0100007F:0BB8 0100007F:D431 01 00000000:00000000 00:00000000 00000000  1002        0 31338 1 0000000000000000 20 4 30 10 -1
   2: 0A00000A:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1002        0 31339 1 0000000000000000 100 0 0 10 0
`

// Vite binds localhost, which on Ubuntu cloud images is [::1] first.
const tcp6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:1435 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1002        0 4242 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000000000000:1F41 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1002        0 4243 1 0000000000000000 100 0 0 10 0
`

func TestRead(t *testing.T) {
	socks := append(Read(strings.NewReader(tcp4)), Read(strings.NewReader(tcp6))...)
	if len(socks) != 5 {
		t.Fatalf("%d sockets: %+v", len(socks), socks)
	}
	if s := socks[0]; s.Local != netip.MustParseAddrPort("127.0.0.1:3000") || !s.Listen || s.UID != 1002 {
		t.Errorf("v4 listener: %+v", s)
	}
	if s := socks[1]; s.Listen {
		t.Errorf("an established connection read as a listener: %+v", s)
	}
	if s := socks[3]; s.Local != netip.MustParseAddrPort("[::1]:5173") || !s.Listen {
		t.Errorf("v6 loopback listener: %+v", s)
	}
	for port, want := range map[int]bool{
		3000:  true,  // 127.0.0.1
		5173:  true,  // [::1] only
		8001:  true,  // [::], every address
		8080:  false, // 10.0.0.10 only: not loopback
		54321: false,
	} {
		if got := LoopbackListener(socks, port); got != want {
			t.Errorf("port %d: listening %v, want %v", port, got, want)
		}
	}
}
