package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// Apply stops before any change only when GitHub cannot be reached and the
// box has no IPv4 route: an IPv6-only box. A box that reaches GitHub some
// other way (NAT64, a proxy) passes, and so does one with IPv4 whose probe
// failed for another reason; the download step reports that.
func TestIPv4GateStopsOnlyAnIPv6OnlyBox(t *testing.T) {
	const route = "default via 10.0.0.1 dev eth0 proto dhcp"
	for _, tc := range []struct {
		name         string
		curl, reach  bool
		routes       string
		pass, probed bool
	}{
		{name: "IPv4 box", curl: true, reach: true, routes: route, pass: true, probed: true},
		{name: "IPv6 only behind NAT64", curl: true, reach: true, routes: "", pass: true, probed: true},
		{name: "IPv6 only", curl: true, reach: false, routes: "", pass: false, probed: true},
		{name: "IPv4 box, GitHub down", curl: true, reach: false, routes: route, pass: true, probed: true},
		{name: "no curl", curl: false, reach: false, routes: "", pass: true, probed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := system.NewFake()
			if tc.curl {
				x.AddFile("/usr/bin/curl", "", 0o755)
			}
			if tc.reach {
				x.OK("curl", "")
			} else {
				x.On("curl", system.Result{ExitCode: 7, Stderr: "curl: (7) Failed to connect to github.com port 443: Network is unreachable"})
			}
			x.OK("ip -4 route show default", tc.routes)
			e := &engine.Env{X: x, Cfg: &config.Config{Box: "acme"}}
			var stderr bytes.Buffer
			if got := ipv4Gate(context.Background(), e, &stderr); got != tc.pass {
				t.Fatalf("pass = %v, want %v; stderr %q", got, tc.pass, stderr.String())
			}
			if x.Ran("curl") != tc.probed {
				t.Errorf("probed = %v, want %v", x.Ran("curl"), tc.probed)
			}
			if tc.pass {
				if stderr.Len() != 0 {
					t.Errorf("a passing box printed %q", stderr.String())
				}
				return
			}
			msg := stderr.String()
			for _, want := range []string{"acme reaches the internet over IPv6 only", "Nothing has changed", "keelbox apply acme"} {
				if !strings.Contains(msg, want) {
					t.Errorf("message lacks %q: %q", want, msg)
				}
			}
		})
	}
}

// The probe is a request to GitHub over HTTPS with the downloads' own TLS
// floor and a bound on its time, so an apply never waits long on it.
func TestIPv4GateProbe(t *testing.T) {
	s := githubProbe.String()
	for _, want := range []string{"curl --proto =https --tlsv1.2", "--max-time 10", "https://github.com/"} {
		if !strings.Contains(s, want) {
			t.Errorf("probe %q lacks %q", s, want)
		}
	}
	if githubProbe.Timeout == 0 || githubProbe.User != "" {
		t.Errorf("probe runs as root with a timeout: %+v", githubProbe)
	}
}
