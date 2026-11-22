package provider

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func TestSSHAddrs(t *testing.T) {
	addrs := func(ss ...string) []netip.Addr {
		var out []netip.Addr
		for _, s := range ss {
			out = append(out, netip.MustParseAddr(s))
		}
		return out
	}
	many := addrs("192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4", "192.0.2.5", "192.0.2.6", "192.0.2.7", "192.0.2.8", "192.0.2.9")
	for _, tc := range []struct {
		name    string
		targets []Addr
		ssh     map[string]string // what ssh -G answers for a host
		dns     map[string][]netip.Addr
		want    string
		looked  string // the names resolved
	}{
		{name: "an address is used as it is", targets: []Addr{{"203.0.113.7", 22}}, ssh: map[string]string{"203.0.113.7": "ignored.example"}, want: "203.0.113.7:22"},
		{name: "an IPv6 address, another port", targets: []Addr{{"2001:db8::7", 2222}}, want: "[2001:db8::7]:2222"},
		{name: "a name resolves here, to every address", targets: []Addr{{"box.example", 22}}, ssh: map[string]string{"box.example": "box.example"},
			dns: map[string][]netip.Addr{"box.example": addrs("203.0.113.7", "2001:db8::7", "203.0.113.7")}, want: "203.0.113.7:22,[2001:db8::7]:22", looked: "box.example"},
		{name: "an ssh config alias with an address", targets: []Addr{{"devbox", 22}}, ssh: map[string]string{"devbox": "198.51.100.4"}, want: "198.51.100.4:22"},
		{name: "an ssh config alias with a name", targets: []Addr{{"devbox", 22}}, ssh: map[string]string{"devbox": "ec2-1.example"},
			dns: map[string][]netip.Addr{"ec2-1.example": addrs("198.51.100.4")}, want: "198.51.100.4:22", looked: "ec2-1.example"},
		{name: "ssh does not say: the name itself", targets: []Addr{{"box.example", 22}},
			dns: map[string][]netip.Addr{"box.example": addrs("203.0.113.7")}, want: "203.0.113.7:22", looked: "box.example"},
		{name: "a mapped address is unmapped, a scoped one left out", targets: []Addr{{"box.example", 22}},
			dns: map[string][]netip.Addr{"box.example": addrs("::ffff:203.0.113.7", "fe80::1%eth0")}, want: "203.0.113.7:22", looked: "box.example"},
		{name: "a name that does not resolve", targets: []Addr{{"nowhere.example", 22}}, want: "", looked: "nowhere.example"},
		{name: "at most eight", targets: []Addr{{"many.example", 22}, {"203.0.113.7", 22}}, dns: map[string][]netip.Addr{"many.example": many},
			want: "192.0.2.1:22,192.0.2.2:22,192.0.2.3:22,192.0.2.4:22,192.0.2.5:22,192.0.2.6:22,192.0.2.7:22,192.0.2.8:22", looked: "many.example"},
		{name: "no port, no host, nothing", targets: []Addr{{"203.0.113.7", 0}, {"", 22}, {"203.0.113.7", 70000}}, want: ""},
		// Over the tailnet: the address connected to first, then the box's
		// address outside it, from the config and the cloud's API.
		{name: "the tailnet address and the public ones", targets: []Addr{{"100.101.102.103", 22}, {"box.example", 2222}, {"203.0.113.7", 22}, {"100.101.102.103", 22}},
			dns: map[string][]netip.Addr{"box.example": addrs("203.0.113.7")}, want: "100.101.102.103:22,203.0.113.7:2222,203.0.113.7:22", looked: "box.example"},
	} {
		looked := ""
		got := sshAddrs(context.Background(), tc.targets,
			func(_ context.Context, host string, _ int) string { return tc.ssh[host] },
			func(_ context.Context, network, host string) ([]netip.Addr, error) {
				looked += host
				if network != "ip" {
					t.Errorf("%s: lookup network %q", tc.name, network)
				}
				if a, ok := tc.dns[host]; ok {
					return a, nil
				}
				return nil, errors.New("no such host")
			})
		if got != tc.want || looked != tc.looked {
			t.Errorf("%s: got %q (resolved %q), want %q (resolved %q)", tc.name, got, looked, tc.want, tc.looked)
		}
	}
}

func TestSSHConfigHostname(t *testing.T) {
	out := "host devbox\nuser admin\nhostname 198.51.100.4\nport 22\nhostkeyalias none\n"
	if h := sshConfigHostname(out); h != "198.51.100.4" {
		t.Errorf("hostname %q", h)
	}
	if h := sshConfigHostname("user admin\n"); h != "" {
		t.Errorf("no hostname line, got %q", h)
	}
}

// ssh -G is asked about the host in question, with the target's own
// options, and never connects.
func TestSSHHostnameAsksSSH(t *testing.T) {
	dir := t.TempDir()
	stub := `#!/bin/sh
for a in "$@"; do [ "$a" = -G ] && g=1; last=$a; done
[ -n "$g" ] || exit 9
printf 'user admin\nhostname resolved.%s\n' "${last#*@}"
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	s := &SSH{Host: "100.101.102.103", Port: 22, User: "admin", KnownHosts: filepath.Join(dir, "kh")}
	if h := s.sshHostname(context.Background(), "devbox", 2222); h != "resolved.devbox" {
		t.Errorf("hostname %q", h)
	}
}
