package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/system"
)

// The first apply stops before any change when the box cannot reach
// GitHub, whatever its routes: an IPv6-only box, or one with a private IPv4
// address and no NAT. Any HTTP answer is a reachable GitHub. The route only
// picks the message.
func TestIPv4GateOnTheFirstApply(t *testing.T) {
	const route = "default via 10.0.0.1 dev eth0 proto dhcp"
	refused := system.Result{ExitCode: 7, Stderr: "curl: (7) Failed to connect to github.com port 443: Network is unreachable"}
	for _, tc := range []struct {
		name     string
		curl     bool
		probe    system.Result
		routes   string
		ipFails  bool
		pass     bool
		probed   bool
		wantText string
	}{
		{name: "reaches GitHub", curl: true, routes: route, pass: true, probed: true},
		{name: "IPv6 only behind NAT64", curl: true, routes: "", pass: true, probed: true},
		{name: "GitHub answers 429", curl: true, probe: system.Result{ExitCode: 22, Stderr: "curl: (22) The requested URL returned error: 429"}, routes: route, pass: true, probed: true},
		{name: "IPv6 only", curl: true, probe: refused, routes: "", probed: true, wantText: "acme reaches the internet over IPv6 only"},
		{name: "private IPv4 without NAT", curl: true, probe: system.Result{ExitCode: 28, Stderr: "curl: (28) Connection timed out"}, routes: route, probed: true, wantText: "acme cannot reach github.com"},
		{name: "ip fails", curl: true, probe: refused, ipFails: true, probed: true, wantText: "acme cannot reach github.com"},
		{name: "no curl", curl: false, probe: refused, routes: "", pass: true, probed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := system.NewFake()
			if tc.curl {
				x.AddFile("/usr/bin/curl", "", 0o755)
			}
			x.On("curl", tc.probe)
			if tc.ipFails {
				x.Fail("ip -4 route")
			} else {
				x.OK("ip -4 route show default", tc.routes)
			}
			e := &engine.Env{X: x, Cfg: &config.Config{Box: "acme"}}
			var stderr bytes.Buffer
			if got := ipv4Gate(context.Background(), e, &stderr); got != tc.pass {
				t.Fatalf("pass = %v, want %v; stderr %q", got, tc.pass, stderr.String())
			}
			if x.Ran("curl") != tc.probed {
				t.Errorf("probed = %v, want %v", x.Ran("curl"), tc.probed)
			}
			msg := stderr.String()
			if tc.pass {
				if msg != "" {
					t.Errorf("a passing box printed %q", msg)
				}
				return
			}
			for _, want := range []string{tc.wantText, "Nothing has changed", "then: keelbox apply acme"} {
				if !strings.Contains(msg, want) {
					t.Errorf("message lacks %q: %q", want, msg)
				}
			}
		})
	}
}

// A box applied before is never probed: a GitHub outage must not stop an
// apply that may need nothing from it.
func TestIPv4GateSkipsABoxAppliedBefore(t *testing.T) {
	x := system.NewFake()
	x.AddFile("/usr/bin/curl", "", 0o755)
	x.On("curl", system.Result{ExitCode: 7})
	x.OK("ip -4 route show default", "")
	e := &engine.Env{X: x, Cfg: &config.Config{Box: "acme"}}
	if err := logbook.Append(x, logbook.Record{Time: time.Now(), Release: "2026.11.4", Result: "ok", Keelbox: "v0.1.1"}); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if !ipv4Gate(context.Background(), e, &stderr) || x.Ran("curl") {
		t.Errorf("an applied box was probed or stopped: %q", stderr.String())
	}
}

// The probe asks GitHub over HTTPS with the downloads' TLS floor, bounded
// in time, and without -f, so an HTTP error status still counts as an
// answer.
func TestIPv4GateProbe(t *testing.T) {
	s := githubProbe.String()
	for _, want := range []string{"curl --proto =https --tlsv1.2", "--max-time 10", "https://github.com/"} {
		if !strings.Contains(s, want) {
			t.Errorf("probe %q lacks %q", s, want)
		}
	}
	for _, a := range githubProbe.Args {
		if a == "-f" || a == "--fail" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "f")) {
			t.Errorf("probe fails on an HTTP status: %q", a)
		}
	}
	if githubProbe.Timeout == 0 || githubProbe.User != "" {
		t.Errorf("probe runs as root with a timeout: %+v", githubProbe)
	}
}

// runLocal's apply runs the gate after taking the lock and before the
// first-apply gate, so it stops before anything changes.
func TestApplyGatesIPv4First(t *testing.T) {
	src, err := os.ReadFile("local.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	lock, gate, first := strings.Index(s, "unlock, err := lock()"), strings.Index(s, "ipv4Gate(ctx, env, stderr)"), strings.Index(s, "firstApplyGate(ctx, env,")
	if lock < 0 || gate < 0 || first < 0 || lock > gate || gate > first {
		t.Errorf("runLocal: lock at %d, ipv4Gate at %d, firstApplyGate at %d; want them in that order", lock, gate, first)
	}
}
