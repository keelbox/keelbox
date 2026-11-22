package notify

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseProcNetTCP(t *testing.T) {
	table := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:270F 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1002        0 31337 1 0000000000000000 100 0 0 10 0
   1: 0100007F:270F 0100007F:D431 01 00000000:00000000 00:00000000 00000000  1002        0 31338 1 0000000000000000 20 4 30 10 -1
`
	lo := netip.MustParseAddrPort("127.0.0.1:9999")
	rem := netip.MustParseAddrPort("127.0.0.1:54321")
	if uid, ok := findSocket(strings.NewReader(table), lo, rem); !ok || uid != 1002 {
		t.Errorf("v4: %d %v", uid, ok)
	}
	if _, ok := findSocket(strings.NewReader(table), lo, netip.MustParseAddrPort("127.0.0.1:1")); ok {
		t.Error("found a connection that is not there")
	}
	// A dual-stack listener's accepted socket, in tcp6 with v4-mapped
	// addresses.
	table6 := `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0000000000000000FFFF00000100007F:270F 0000000000000000FFFF00000100007F:D431 01 00000000:00000000 00:00000000 00000000     0        0 4242 1 0000000000000000 20 4 30 10 -1
`
	if uid, ok := findSocket(strings.NewReader(table6), lo, rem); !ok || uid != 0 {
		t.Errorf("v4-mapped: %d %v", uid, ok)
	}
}

// With the owner's service on a loopback port down, another account
// listens there first. keelbox finds that account's socket behind its
// connection and sends nothing: no URL, no header, no token reaches the
// listener.
func TestNoSendToAnotherAccountsLoopbackListener(t *testing.T) {
	if systemUID(os.Getuid()) {
		t.Skip("the test's own listener would be a system account's")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
				buf := make([]byte, 4096)
				n, _ := io.ReadAtLeast(c, buf, 1)
				got <- string(buf[:n])
				if n > 0 {
					_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"))
				}
			}()
		}
	}()
	u, _ := url.Parse("http://" + ln.Addr().String() + "/hooks/SECRET-PATH")
	hdr := http.Header{"Authorization": {"Bearer SEKRET-HEADER-123"}}
	p := poster{channel: "hook", kind: "webhook", client: newClient(systemUID)}
	err = p.post(context.Background(), u, "application/json", []byte(`{"text":"x"}`), hdr)
	var se *SendError
	if !errors.As(err, &se) || !strings.Contains(se.Problem, "not root's") || !se.Retryable() || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("send to another account's listener: %v", err)
	}
	select {
	case b := <-got:
		if b != "" {
			t.Fatalf("the listener received %q", b)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("no connection seen")
	}
	// The same listener, accepted as the admin's: the request goes out.
	p.client = newClient(func(uid int) bool { return uid == os.Getuid() })
	if err := p.post(context.Background(), u, "application/json", []byte(`{}`), hdr); err != nil {
		t.Fatalf("allowed listener: %v", err)
	}
	if b := <-got; !strings.Contains(b, "SEKRET-HEADER-123") {
		t.Errorf("allowed listener got %q", b)
	}
}
