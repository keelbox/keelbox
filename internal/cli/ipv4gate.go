package cli

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// githubProbe asks github.com for its headers, with the TLS floor of the
// downloads. Caddy, mise and restic come from GitHub's releases and the
// tmux plugins from github.com, which has no IPv6 address. There is no -f:
// any HTTP answer, a 429 included, shows the box reaches GitHub; only a
// failure to connect (DNS, refused, unreachable, timeout, TLS) counts.
var githubProbe = system.Cmd{
	Name: "curl",
	Args: []string{"--proto", "=https", "--tlsv1.2", "-sS", "-o", "/dev/null", "--head",
		"--max-time", "10", "--retry", "2", "--retry-all-errors", "https://github.com/"},
	Timeout: 45 * time.Second,
}

// curlHTTPError is curl's exit code for an HTTP error status under -f:
// GitHub answered, so it is reachable.
const curlHTTPError = 22

// ipv4Gate stops the first apply, before any change, on a box that cannot
// reach GitHub: an IPv6-only server, or one with a private IPv4 address and
// no NAT. Such an apply would otherwise harden sshd, enable UFW and install
// Tailscale and Docker, then stop at the first download from GitHub. The
// probe is a real request, so a box that reaches GitHub through NAT64 and
// DNS64, 464XLAT or a transparent proxy passes; the apply unit does not
// read https_proxy. The IPv4 default route only picks the message. Later
// applies never probe: the box was set up, and a GitHub outage must not
// stop an apply that may need nothing from it. Without curl, which
// Ubuntu's server and cloud images have, there is nothing to probe with
// and apply goes on.
//
// runLocal calls it before firstApplyGate and before any change
// (TestApplyGatesIPv4First pins the order).
func ipv4Gate(ctx context.Context, e *engine.Env, stderr io.Writer) bool {
	if !neverApplied(e) {
		return true
	}
	if st, err := e.X.Stat("/usr/bin/curl"); err != nil || !st.Exists {
		return true
	}
	_, err := e.X.Run(ctx, githubProbe)
	var exit *system.ExitError
	if err == nil || (errors.As(err, &exit) && exit.Result.ExitCode == curlHTTPError) {
		return true
	}
	box := e.Cfg.Box
	if routes, rerr := system.Output(ctx, e.X, system.C("ip", "-4", "route", "show", "default")); rerr == nil && routes == "" {
		failf(stderr, "%s reaches the internet over IPv6 only, and github.com, where Caddy, mise, restic and the tmux plugins come from, has no IPv6 address. Nothing has changed. Give the server IPv4 internet access (in your provider's console, or on its network), or route it through NAT64 with DNS64, then: keelbox apply %s\n", box, box)
		return false
	}
	failf(stderr, "%s cannot reach github.com, where Caddy, mise, restic and the tmux plugins come from: its network may block it (a private IPv4 address without NAT, or a firewall), or GitHub may be down. Nothing has changed. Give the server internet access to github.com on port 443, then: keelbox apply %s\n", box, box)
	return false
}
