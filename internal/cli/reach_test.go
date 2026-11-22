package cli

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
)

func reachConfig(t *testing.T, extra string) *config.Config {
	t.Helper()
	kh := filepath.Join(t.TempDir(), "known_hosts")
	c, err := config.Parse([]byte(`version: 1
box: acme
target:
  ssh:
    host: 192.0.2.10
    known_hosts: `+kh+`
`+extra+`users:
  admin:
    ssh_authorized_keys: ["ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"]
`), "t")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBoxRecordLivesBesideKnownHosts(t *testing.T) {
	c := reachConfig(t, "")
	if _, ok := tailnetAddr(c); ok {
		t.Fatal("tailnet address before any record")
	}
	if err := saveRecord(c, boxRecord{TailnetHost: "100.101.102.103", TailnetPort: 22, TailnetName: "acme.tail1234.ts.net"}); err != nil {
		t.Fatal(err)
	}
	p := recordPath(c)
	if filepath.Dir(filepath.Dir(p)) != filepath.Dir(config.ExpandHome(c.Target.SSH.KnownHosts)) {
		t.Errorf("record at %s", p)
	}
	if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("record mode: %v %v", st, err)
	}
	if a, ok := tailnetAddr(c); !ok || a != (addr{"100.101.102.103", 22}) {
		t.Errorf("tailnet address %v %v", a, ok)
	}
	forgetRecord(c)
	if _, ok := tailnetAddr(c); ok {
		t.Error("record kept after forget")
	}
}

func TestTailnetHostOverridesTheRecord(t *testing.T) {
	c := reachConfig(t, "    tailnet_host: 127.0.0.1:2222\n")
	_ = saveRecord(c, boxRecord{TailnetHost: "100.101.102.103"})
	if a, ok := tailnetAddr(c); !ok || a != (addr{"127.0.0.1", 2222}) {
		t.Errorf("got %v", a)
	}
	if a := (addr{"127.0.0.1", 2222}); knownHostsName(a) != "[127.0.0.1]:2222" || a.String() != "127.0.0.1:2222" {
		t.Errorf("names %s %s", knownHostsName(a), a)
	}
}

func TestReachFallsBackToThePublicAddress(t *testing.T) {
	// A listener that closes at once stands in for a reachable tailnet sshd.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	c := reachConfig(t, "    tailnet_host: 127.0.0.1:"+strconv.Itoa(port)+"\n")
	public := addr{"192.0.2.10", 22}
	// No host keys pinned for either name: never trust a new key for the
	// tailnet name, use the public address.
	if got := reach(context.Background(), c, public, io.Discard); got != public {
		t.Errorf("unpinned tailnet address used: %v", got)
	}
	off := false
	c.Access.Tailscale.Enabled = &off
	if got := reach(context.Background(), c, public, io.Discard); got != public {
		t.Errorf("tailnet used with tailscale off: %v", got)
	}
}

func TestPickTailnetIP(t *testing.T) {
	if got := pickTailnetIP([]string{"fd7a:115c:a1e0::1", "100.101.102.103"}); got != "100.101.102.103" {
		t.Errorf("got %s", got)
	}
	if got := pickTailnetIP([]string{"fd7a:115c:a1e0::1"}); got != "fd7a:115c:a1e0::1" {
		t.Errorf("got %s", got)
	}
	if pickTailnetIP(nil) != "" {
		t.Error("address from nothing")
	}
}
