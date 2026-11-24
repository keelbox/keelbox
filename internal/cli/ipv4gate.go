package cli

import (
	"context"
	"io"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// githubProbe is fetched to learn whether the box reaches GitHub, where
// Caddy, mise and restic come from (releases) and the tmux plugins (git).
// github.com has no IPv6 address.
var githubProbe = system.Cmd{
	Name: "curl",
	Args: []string{"--proto", "=https", "--tlsv1.2", "-fsS", "-o", "/dev/null", "--head",
		"--max-time", "10", "--retry", "2", "--retry-all-errors", "https://github.com/"},
	Timeout: 45 * time.Second,
}

// ipv4Gate stops an apply before any change on a box that reaches the
// internet over IPv6 only: apply would otherwise stop part-way, at the
// first download from GitHub. A box with an IPv4 default route passes
// without a request, so an ordinary apply costs nothing and a GitHub outage
// is left to the step that downloads. Without one, the probe is a real
// request with the same curl the downloads use, so a box that reaches
// GitHub through NAT64 and DNS64, 464XLAT or a proxy passes; apply stops
// only when it fails. Without curl, which Ubuntu's images have, there is
// nothing to probe with and apply goes on.
func ipv4Gate(ctx context.Context, e *engine.Env, stderr io.Writer) bool {
	routes, err := system.Output(ctx, e.X, system.C("ip", "-4", "route", "show", "default"))
	if err != nil || routes != "" {
		return true
	}
	if st, err := e.X.Stat("/usr/bin/curl"); err != nil || !st.Exists {
		return true
	}
	if system.Succeeds(ctx, e.X, githubProbe) {
		return true
	}
	failf(stderr, "%s reaches the internet over IPv6 only, and github.com, where Caddy, mise, restic and the tmux plugins come from, has no IPv6 address. Nothing has changed. Give the server an IPv4 address in your provider's console (or route it through NAT64 with DNS64), then: keelbox apply %s\n",
		e.Cfg.Box, e.Cfg.Box)
	return false
}
