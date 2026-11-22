//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// TestAgentSandboxes: the Codex and Claude Code sandboxes start for the
// agent under AppArmor's user namespace restriction, on 24.04 through
// Ubuntu's bwrap profile as keelbox loads it from Ubuntu's package and on
// 26.04 as Ubuntu loads it itself; programs bwrap runs get no capabilities
// (ADR 0059, ADR 0072). Run it on both:
// KEELBOX_E2E_IMAGE=24.04 and the default 26.04.
func TestAgentSandboxes(t *testing.T) {
	ensureKey(t)
	build(t)
	cfg, host := agentsBox(t)

	must(t, host, "agent", "sysctl -n kernel.apparmor_restrict_unprivileged_userns", "1")
	release := strings.TrimSpace(must(t, host, "agent", ". /etc/os-release && echo $VERSION_ID"))
	ours := strings.TrimSpace(must(t, host, "admin", "test -e /etc/apparmor.d/keelbox-bwrap && echo ours; test -e /etc/apparmor.d/bwrap-userns-restrict && echo ubuntu; true"))
	switch {
	case release == "24.04" && ours != "ours":
		t.Fatalf("24.04 without keelbox's bwrap profile: %q", ours)
	case release != "24.04" && ours != "ubuntu":
		t.Fatalf("%s: want Ubuntu's bwrap profile and not keelbox's, have %q", release, ours)
	}
	must(t, host, "admin", "sudo aa-status", "bwrap", "unpriv_bwrap")
	if ours == "ours" {
		// Ubuntu's own text under keelbox's header, from a package that
		// was downloaded and not installed.
		must(t, host, "admin", "head -n 3 /etc/apparmor.d/keelbox-bwrap", "Ubuntu's own bwrap-userns-restrict profile", "apparmor-profiles package (", "/usr/share/apparmor/extra-profiles/bwrap-userns-restrict")
		must(t, host, "admin", "sudo apparmor_parser --preprocess /etc/apparmor.d/keelbox-bwrap", "profile bwrap /usr/bin/bwrap", "audit deny capability")
		if out, err := sshRun(t, host, "admin", nil, "dpkg-query -W -f='${db:Status-Abbrev}' apparmor-profiles 2>/dev/null | grep -q ^ii"); err == nil {
			t.Fatalf("apparmor-profiles is installed, with its other profiles:\n%s", out)
		}
	}
	logf(t, "PASS Ubuntu %s: bwrap profile %s, restriction kept on", release, ours)

	if out, err := kb(t, "doctor", cfg); err != nil || !strings.Contains(out, "sandbox") || strings.Contains(out, "FAIL") {
		t.Fatalf("doctor: %v\n%s", err, out)
	}

	// Codex's own sandbox, as Codex runs the agent's commands: workspace-
	// write runs a command and writes in the workspace, and nowhere else.
	must(t, host, "agent", `mkdir -p ~/projects/sbx && cd ~/projects/sbx && `+
		`codex sandbox -c 'sandbox_mode="workspace-write"' -- sh -c 'echo made-in-sandbox > made.txt && uname -s' && cat made.txt`,
		"Linux", "made-in-sandbox")
	if out, err := sshRun(t, host, "agent", nil, `cd ~/projects/sbx && codex sandbox -c 'sandbox_mode="workspace-write"' -- sh -c 'echo x > ~/outside.txt' 2>&1; test ! -e ~/outside.txt`); err != nil {
		t.Fatalf("codex sandbox wrote outside the workspace: %v\n%s", err, out)
	}
	logf(t, "PASS codex sandbox (workspace-write): ran a command, wrote in the workspace, refused outside it")

	// Programs bwrap starts have no capabilities and cannot map a user
	// namespace of their own.
	must(t, host, "agent", "bwrap --ro-bind / / --dev /dev --proc /proc --unshare-net -- grep CapEff /proc/self/status", "0000000000000000")
	if out, err := sshRun(t, host, "agent", nil, "bwrap --ro-bind / / --dev /dev --proc /proc --unshare-net -- unshare -Urn true 2>&1"); err == nil {
		t.Fatalf("a program inside bwrap mapped a user namespace:\n%s", out)
	}
	if out, err := sshRun(t, host, "agent", nil, "unshare -Urn true 2>&1"); err == nil {
		t.Fatalf("the agent maps a capable user namespace outside bwrap:\n%s", out)
	}
	logf(t, "PASS inside bwrap: no capabilities, no user namespace of its own; outside: restriction holds")

	// Claude Code's sandbox is Anthropic's sandbox runtime (bwrap with a
	// socat network bridge); a live Claude Code turn needs a sign-in. Its
	// Unix socket filter makes a nested user namespace inside bwrap, which
	// needs capabilities that Ubuntu's profile strips from what bwrap runs:
	// with sandbox.network.allowAllUnixSockets it runs without that filter.
	must(t, host, "agent", "command -v socat")
	srt := `cd ~/projects/sbx && npx -y @anthropic-ai/sandbox-runtime@0.0.81 `
	settings := `printf '%s' '{"network":{"allowedDomains":[],"deniedDomains":[],"allowAllUnixSockets":true},"filesystem":{"denyRead":[],"allowWrite":["."],"denyWrite":[]}}' > /tmp/srt.json && `
	must(t, host, "agent", settings+srt+`-s /tmp/srt.json -c 'echo srt-in-sandbox > srt.txt && cat srt.txt' 2>&1`, "srt-in-sandbox")
	logf(t, "PASS sandbox runtime (Claude Code's sandbox) with allowAllUnixSockets: ran a command and wrote in the workspace")
	if out, err := sshRun(t, host, "agent", nil, srt+`-c 'echo default-ok' 2>&1`); err == nil && strings.Contains(out, "default-ok") {
		logf(t, "PASS sandbox runtime with its Unix socket filter also runs here")
	} else if strings.Contains(out, "setgroups") {
		logf(t, "KNOWN sandbox runtime's Unix socket filter fails under Ubuntu's bwrap profile: %s", firstLine(out))
	} else {
		t.Errorf("sandbox runtime with its defaults failed another way: %v\n%s", err, out)
	}
}
