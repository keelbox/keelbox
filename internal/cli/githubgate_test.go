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

// A box never set up stops before any change when it cannot reach GitHub,
// whatever its routes: an IPv6-only box, or one with a private IPv4 address
// and no NAT. Any HTTP answer is a reachable GitHub. The route only picks
// the message, and the IPv4 one keeps curl's reason.
func TestGitHubGateOnABoxNeverSetUp(t *testing.T) {
	const route = "default via 10.0.0.1 dev eth0 proto dhcp"
	refused := system.Result{ExitCode: 7, Stderr: "curl: (7) Failed to connect to github.com port 443: Network is unreachable"}
	for _, tc := range []struct {
		name     string
		curl     bool
		probe    system.Result
		routes   string
		ipFails  bool
		code     int
		probed   bool
		wantText []string
	}{
		{name: "reaches GitHub", curl: true, routes: route, probed: true},
		{name: "IPv6 only behind NAT64", curl: true, routes: "", probed: true},
		// Without -f, curl exits 0 on any HTTP status: a 429 or 503 after
		// its retries, a 404 at once.
		{name: "GitHub answers 429", curl: true, probe: system.Result{Stderr: "Warning: Problem: HTTP error. Will retry in 1 second."}, routes: route, probed: true},
		{name: "IPv6 only", curl: true, probe: refused, routes: "", code: 1, probed: true,
			wantText: []string{"acme reaches the internet over IPv6 only", "Give the server IPv4 internet access"}},
		{name: "private IPv4 without NAT", curl: true, probe: system.Result{ExitCode: 28, Stderr: "Warning: Transient problem: timeout\ncurl: (28) Connection timed out after 10002 milliseconds"}, routes: route, code: 1, probed: true,
			wantText: []string{"acme cannot reach github.com (curl: (28) Connection timed out after 10002 milliseconds)"}},
		{name: "ip fails", curl: true, probe: refused, ipFails: true, code: 1, probed: true,
			wantText: []string{"acme cannot reach github.com (curl: (7) Failed to connect"}},
		{name: "no curl", curl: false, probe: refused, routes: "", probed: false},
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
			if got := githubGate(context.Background(), e, &stderr); got != tc.code {
				t.Fatalf("exit %d, want %d; stderr %q", got, tc.code, stderr.String())
			}
			if x.Ran("curl") != tc.probed {
				t.Errorf("probed = %v, want %v", x.Ran("curl"), tc.probed)
			}
			msg := stderr.String()
			if tc.code == 0 {
				if msg != "" {
					t.Errorf("a passing box printed %q", msg)
				}
				return
			}
			for _, want := range append(tc.wantText, "Nothing has changed", "then: keelbox apply acme") {
				if !strings.Contains(msg, want) {
					t.Errorf("message lacks %q: %q", want, msg)
				}
			}
		})
	}
}

// Only an apply that finished, in full or in part, ends the probing: a box
// that stopped half-way at a GitHub download is still checked, and one that
// was set up never is, so a GitHub outage cannot stop its applies.
func TestGitHubGateProbesUntilAnApplyFinishes(t *testing.T) {
	for _, tc := range []struct {
		results []string
		probed  bool
	}{
		{results: nil, probed: true},
		{results: []string{"failed"}, probed: true},
		{results: []string{"failed", "interrupted"}, probed: true},
		{results: []string{"failed", "partial"}, probed: false},
		{results: []string{"ok"}, probed: false},
		{results: []string{"ok", "failed"}, probed: false},
	} {
		x := system.NewFake()
		x.AddFile("/usr/bin/curl", "", 0o755)
		x.On("curl", system.Result{ExitCode: 7, Stderr: "curl: (7) Failed to connect"})
		x.OK("ip -4 route show default", "")
		for _, r := range tc.results {
			if err := logbook.Append(x, logbook.Record{Time: time.Now(), Release: "2026.11.4", Result: r, Keelbox: "v0.1.1"}); err != nil {
				t.Fatal(err)
			}
		}
		// A day-two record is not an apply.
		if err := logbook.Append(x, logbook.Record{Time: time.Now(), Kind: logbook.KindWatch, Release: "2026.11.4", Result: "ok", Keelbox: "v0.1.1"}); err != nil {
			t.Fatal(err)
		}
		e := &engine.Env{X: x, Cfg: &config.Config{Box: "acme"}}
		var stderr bytes.Buffer
		code := githubGate(context.Background(), e, &stderr)
		if x.Ran("curl") != tc.probed || (code != 0) != tc.probed {
			t.Errorf("after %v: probed %v, exit %d, want probed %v", tc.results, x.Ran("curl"), code, tc.probed)
		}
	}
}

// Ctrl-C or SIGTERM during the probe is an interrupted apply, not an
// unreachable GitHub.
func TestGitHubGateInterrupted(t *testing.T) {
	x := system.NewFake()
	x.AddFile("/usr/bin/curl", "", 0o755)
	ctx, cancel := context.WithCancel(context.Background())
	x.OnFunc("curl", func(c system.Cmd) (system.Result, error) {
		cancel()
		res := system.Result{ExitCode: -1, Stderr: "signal: killed"}
		return res, &system.ExitError{Cmd: c, Result: res}
	})
	x.OK("ip -4 route show default", "")
	e := &engine.Env{X: x, Cfg: &config.Config{Box: "acme"}}
	var stderr bytes.Buffer
	if code := githubGate(ctx, e, &stderr); code != exitInterrupted || strings.Contains(stderr.String(), "reach") {
		t.Errorf("exit %d, stderr %q; want %d and no network message", code, stderr.String(), exitInterrupted)
	}
}

// The probe asks GitHub over HTTPS with the downloads' TLS floor, bounded
// in time, and without -f, so an HTTP error status still counts as an
// answer.
func TestGitHubGateProbe(t *testing.T) {
	s := githubProbe.String()
	for _, want := range []string{"curl --proto =https --tlsv1.2", "--max-time 10", "https://github.com/"} {
		if !strings.Contains(s, want) {
			t.Errorf("probe %q lacks %q", s, want)
		}
	}
	for _, a := range githubProbe.Args {
		if a == "--fail" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "f")) {
			t.Errorf("probe fails on an HTTP status: %q", a)
		}
	}
	if githubProbe.Timeout == 0 || githubProbe.User != "" {
		t.Errorf("probe runs as root with a timeout: %+v", githubProbe)
	}
}

// runLocal's apply runs the gate after taking the lock and before the
// first-apply gate, so it stops before anything changes.
func TestApplyGatesGitHubFirst(t *testing.T) {
	src, err := os.ReadFile("local.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	lock, gate, first := strings.Index(s, "unlock, err := lock()"), strings.Index(s, "githubGate(ctx, env, stderr)"), strings.Index(s, "firstApplyGate(ctx, env,")
	if lock < 0 || gate < 0 || first < 0 || lock > gate || gate > first {
		t.Errorf("runLocal: lock at %d, githubGate at %d, firstApplyGate at %d; want them in that order", lock, gate, first)
	}
}
