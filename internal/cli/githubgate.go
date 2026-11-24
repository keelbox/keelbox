package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/plain"
	"github.com/keelbox/keelbox/internal/system"
)

// githubProbe asks github.com for its headers, with the TLS floor of the
// downloads. Caddy, mise and restic come from GitHub's releases and the
// tmux plugins from github.com, which has no IPv6 address. Without -f any
// HTTP answer exits 0: a 404 at once, a 429 or 503 after the retries. Only
// a failure to connect (DNS, refused, unreachable, timeout, TLS) exits
// non-zero.
var githubProbe = system.Cmd{
	Name: "curl",
	Args: []string{"--proto", "=https", "--tlsv1.2", "-sS", "-o", "/dev/null", "--head",
		"--max-time", "10", "--retry", "2", "--retry-all-errors", "https://github.com/"},
	Timeout: 45 * time.Second,
}

// githubGate stops an apply, before any change, on a box that has never
// been set up and cannot reach GitHub: an IPv6-only server, or one with a
// private IPv4 address and no NAT. Such an apply would otherwise harden
// sshd, enable UFW and install Tailscale and Docker, then stop at the first
// download from GitHub. It returns 0 to go on, or the exit code.
//
// The probe is a real request, so a box that reaches GitHub through NAT64
// and DNS64, 464XLAT or a transparent proxy passes; the apply unit does not
// read https_proxy. The IPv4 default route only picks the message. Only a
// box with no converged apply in its logbook is probed, so one that
// half-applied before this check existed is probed too; a box that was set
// up never is, and a GitHub outage cannot stop an apply that may need
// nothing from it. Without curl, which Ubuntu's server and cloud images
// have, there is nothing to probe with and apply goes on.
//
// runLocal calls it after taking the lock and before firstApplyGate, before
// any change (TestApplyGatesGitHubFirst pins the order).
func githubGate(ctx context.Context, e *engine.Env, stderr io.Writer) int {
	if !neverConverged(e) {
		return 0
	}
	if st, err := e.X.Stat("/usr/bin/curl"); err != nil || !st.Exists {
		return 0
	}
	_, err := e.X.Run(ctx, githubProbe)
	if ctx.Err() != nil {
		return exitInterrupted
	}
	if err == nil {
		return 0
	}
	box := e.Cfg.Box
	if routes, rerr := system.Output(ctx, e.X, system.C("ip", "-4", "route", "show", "default")); rerr == nil && routes == "" {
		failf(stderr, "%s reaches the internet over IPv6 only, and github.com, where Caddy, mise, restic and the tmux plugins come from, has no IPv6 address. Nothing has changed. Give the server IPv4 internet access (in your provider's console, or on its network), or route it through NAT64 with DNS64, then: keelbox apply %s\n", box, box)
		return 1
	}
	failf(stderr, "%s cannot reach github.com (%s), where Caddy, mise, restic and the tmux plugins come from: its network may block it (a private IPv4 address without NAT, or a firewall), or GitHub may be down. Nothing has changed. Give the server internet access to github.com on port 443, then: keelbox apply %s\n", box, probeCause(err), box)
	return 1
}

// probeCause is the last line curl wrote to stderr, which says why the
// request failed, or the error itself.
func probeCause(err error) string {
	var exit *system.ExitError
	if errors.As(err, &exit) {
		lines := strings.Split(strings.TrimSpace(exit.Result.Stderr), "\n")
		if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
			return plain.Text(last)
		}
	}
	return plain.Text(err.Error())
}

// neverConverged reports whether the logbook holds no apply that finished,
// in full or in part (logbook.LastApplied). A failed or interrupted apply
// does not count: a box that stopped half-way at a download from GitHub is
// still checked. A logbook that cannot be read counts as set up.
func neverConverged(e *engine.Env) bool {
	last, err := logbook.LastApplied(e.X)
	return err == nil && last == nil
}
