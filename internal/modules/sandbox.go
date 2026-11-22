package modules

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// The agent CLIs' own sandboxes (Codex's workspace-write mode, Claude
// Code's sandboxed commands) run commands under /usr/bin/bwrap with a new
// network namespace. bwrap is not setuid on Ubuntu: it needs an
// unprivileged user namespace with capabilities inside it. With
// kernel.apparmor_restrict_unprivileged_userns=1, the Ubuntu default since
// 24.04, only an AppArmor profile that allows userns gets those
// capabilities. Ubuntu 25.04 and later load one for /usr/bin/bwrap
// (bwrap-userns-restrict). 24.04 ships the same profile in its
// apparmor-profiles package without loading it, so bwrap fails with
// "loopback: Failed RTM_NEWADDR" and the agent's sandboxed commands cannot
// run. On such a box keelbox loads Ubuntu's own file (ADR 0072): the
// profile's text comes from Ubuntu's package on the box and is not part of
// keelbox.
const (
	bwrapProfilePath  = "/etc/apparmor.d/keelbox-bwrap"
	ubuntuBwrapPath   = "/etc/apparmor.d/bwrap-userns-restrict"
	ubuntuBwrapExtra  = "/usr/share/apparmor/extra-profiles/bwrap-userns-restrict"
	bwrapProfilePkg   = "apparmor-profiles"
	userNSRestriction = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"
)

// bwrapProfileMark opens the file keelbox writes. Check looks for it: a
// file without it, such as an older copy, is replaced by Ubuntu's.
const bwrapProfileMark = "# Managed by keelbox. Below is Ubuntu's own bwrap-userns-restrict profile,\n"

// bwrapProfileHeader says where the profile below it came from. bwrap
// itself may use a user namespace and capabilities in it, to set up the
// sandbox; everything it runs is stacked with unpriv_bwrap, which denies
// every capability, so bwrap gives the agent's programs no more than they
// had outside it. The profile names are Ubuntu's, so the distribution's
// file in /etc/apparmor.d, once there, replaces this one without a
// conflict.
func bwrapProfileHeader(version string) string {
	return bwrapProfileMark +
		"# copied on this box from Ubuntu's " + bwrapProfilePkg + " package (" + version + "),\n" +
		"# file " + ubuntuBwrapExtra + ",\n" +
		"# which Ubuntu 24.04 ships without loading. It lets /usr/bin/bwrap use an\n" +
		"# unprivileged user namespace under\n" +
		"# kernel.apparmor_restrict_unprivileged_userns=1, so the agent CLIs'\n" +
		"# sandboxes can start; programs bwrap runs get no capabilities. keelbox\n" +
		"# removes this file when Ubuntu's own is in /etc/apparmor.d.\n\n"
}

// bwrapProfileNeeded reports whether the box restricts unprivileged user
// namespaces through AppArmor and has no profile of Ubuntu's for bwrap.
func bwrapProfileNeeded(e *engine.Env) (bool, error) {
	if st, err := e.X.Stat(ubuntuBwrapPath); err != nil {
		return false, err
	} else if st.Exists {
		return false, nil
	}
	data, err := e.X.ReadFile(userNSRestriction)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(data)) == "1", nil
}

// ubuntuBwrapProfile returns Ubuntu's profile and the version of the
// package it is from. Where the package is installed the file is read in
// place. Otherwise apt downloads the package, with apt's own signature
// check, and the one file is read out of it: installing the package would
// also load its profiles for ping, syslogd, dnsmasq, Samba and others,
// which keelbox has no reason to add to a box.
func ubuntuBwrapProfile(ctx context.Context, e *engine.Env) (profile []byte, version string, err error) {
	missing := func(version string) error {
		return fmt.Errorf("the %s package (%s) from Ubuntu has no bwrap-userns-restrict profile, so Codex and Claude Code cannot run sandboxed commands on %s. Ubuntu added it in an update to 24.04: turn on the box's updates source (noble-updates) and run: keelbox apply %s", bwrapProfilePkg, version, e.Cfg.Box, e.Cfg.Box)
	}
	if st, err := e.X.Stat(ubuntuBwrapExtra); err != nil {
		return nil, "", err
	} else if st.Exists {
		version, err = system.Output(ctx, e.X, system.C("dpkg-query", "-W", "-f=${Version}", bwrapProfilePkg))
		if err != nil {
			return nil, "", err
		}
		profile, err = e.X.ReadFile(ubuntuBwrapExtra)
		return profile, version, err
	}
	if err := engine.AptUpdate(ctx, e); err != nil {
		return nil, "", err
	}
	dir, cleanup, err := tempDir(ctx, e, "")
	if err != nil {
		return nil, "", err
	}
	defer cleanup()
	download := system.C("apt-get", "-o", "DPkg::Lock::Timeout=600", "download", bwrapProfilePkg)
	download.Dir = dir
	if _, err := e.X.Run(ctx, download); err != nil {
		return nil, "", fmt.Errorf("download Ubuntu's %s package for its bwrap profile: %w. Check the box reaches its Ubuntu package mirror, then run: keelbox apply %s", bwrapProfilePkg, err, e.Cfg.Box)
	}
	deb, err := system.Output(ctx, e.X, system.C("find", dir, "-maxdepth", "1", "-type", "f", "-name", bwrapProfilePkg+"_*.deb"))
	if err != nil || deb == "" || strings.Contains(deb, "\n") {
		return nil, "", fmt.Errorf("apt downloaded no single %s package into %s (found %q): run keelbox apply %s again", bwrapProfilePkg, dir, deb, e.Cfg.Box)
	}
	if version, err = system.Output(ctx, e.X, system.C("dpkg-deb", "-f", deb, "Version")); err != nil {
		return nil, "", err
	}
	// Unpacked under the private directory, never installed.
	if _, err := e.X.Run(ctx, system.C("dpkg-deb", "-x", deb, dir+"/pkg")); err != nil {
		return nil, "", err
	}
	r, err := e.X.Run(ctx, system.C("cat", "--", dir+"/pkg"+ubuntuBwrapExtra))
	if err != nil {
		return nil, "", missing(version)
	}
	return []byte(r.Stdout), version, nil
}

// bwrapProfileStep loads Ubuntu's profile where it is needed, and removes
// keelbox's file once Ubuntu's own is in /etc/apparmor.d. It is a soft
// step: it needs the box's Ubuntu mirror, nothing later depends on it, and
// a failure must not hold back the sshd, firewall and agent network steps
// after it. Doctor's sandbox checks fail until it passes.
func bwrapProfileStep() engine.Step {
	ours := func(e *engine.Env) (exists, current bool, err error) {
		st, err := e.X.Stat(bwrapProfilePath)
		if err != nil || !st.Exists {
			return false, false, err
		}
		data, err := e.X.ReadFile(bwrapProfilePath)
		return true, strings.HasPrefix(string(data), bwrapProfileMark), err
	}
	return engine.Step{
		Name: "bwrap-userns",
		Soft: true,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			need, err := bwrapProfileNeeded(e)
			if err != nil {
				return engine.Status{}, err
			}
			has, current, err := ours(e)
			switch {
			case err != nil:
				return engine.Status{}, err
			case need && current, !need && !has:
				return engine.Converged(), nil
			case need && has:
				return engine.Pending("replace %s with Ubuntu's own bwrap profile", bwrapProfilePath), nil
			case need:
				return engine.Pending("load Ubuntu's bwrap profile as %s", bwrapProfilePath), nil
			}
			return engine.Pending("remove %s (Ubuntu's %s covers bwrap)", bwrapProfilePath, ubuntuBwrapPath), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			need, err := bwrapProfileNeeded(e)
			if err != nil {
				return err
			}
			if need {
				profile, version, err := ubuntuBwrapProfile(ctx, e)
				if err != nil {
					return err
				}
				file := engine.FileStep("bwrap-userns", engine.File{Path: bwrapProfilePath, Mode: 0o644,
					Content: append([]byte(bwrapProfileHeader(version)), profile...),
					// Loading is the check that the parser takes the
					// profile; a file it refuses is rolled back.
					Validate: func(ctx context.Context, e *engine.Env) error {
						_, err := e.X.Run(ctx, system.C("apparmor_parser", "--replace", "--write-cache", bwrapProfilePath))
						return err
					}})
				return file.Apply(ctx, e)
			}
			// The profile names are Ubuntu's: reload its file rather than
			// unload the profiles, which would leave bwrap without one.
			if err := e.X.Remove(bwrapProfilePath); err != nil {
				return err
			}
			if st, _ := e.X.Stat(ubuntuBwrapPath); st.Exists {
				_, err = e.X.Run(ctx, system.C("apparmor_parser", "--replace", "--write-cache", ubuntuBwrapPath))
			}
			return err
		},
	}
}

// sandboxFix is what a failing sandbox check tells the user to do.
func sandboxFix(e *engine.Env) string {
	return "run keelbox apply " + e.Cfg.Box + "; if it still fails, check `sudo dmesg | grep apparmor` and https://keelbox.dev/docs/guides/troubleshooting#agent-sandboxes"
}

// verifySandbox runs bwrap as the agent with a new network namespace, as
// the CLIs' sandboxes do: what failed on 24.04 without the profile.
func verifySandbox(ctx context.Context, e *engine.Env) engine.Finding {
	c := system.As(e.Cfg.Users.Agent.Name, "bwrap", "--ro-bind", "/", "/", "--dev", "/dev", "--unshare-net", "--", "/usr/bin/true")
	c.Timeout = 30 * time.Second
	if r, err := e.X.Run(ctx, c); err != nil {
		why := firstLineOf(r.Stderr)
		if why == "" {
			why = err.Error()
		}
		return fail("agents", "sandbox", "bwrap cannot start a sandbox for "+e.Cfg.Users.Agent.Name+" ("+why+"), so Codex and Claude Code cannot run sandboxed commands: "+sandboxFix(e))
	}
	return ok("agents", "sandbox", "bwrap starts a sandbox with its own network for "+e.Cfg.Users.Agent.Name)
}

// verifyCodexSandbox is Codex's own self-test: `codex sandbox` runs a
// command the way Codex runs the agent's, with the bwrap Codex picks.
func verifyCodexSandbox(ctx context.Context, e *engine.Env) engine.Finding {
	bin := e.AgentTool("codex")
	c := system.As(e.Cfg.Users.Agent.Name, bin, "sandbox", "--", "/usr/bin/true")
	c.Timeout = time.Minute
	if r, err := e.X.Run(ctx, c); err != nil {
		why := firstLineOf(r.Stderr)
		if why == "" {
			why = err.Error()
		}
		return fail("agents", "codex-sandbox", "codex sandbox fails for "+e.Cfg.Users.Agent.Name+" ("+why+"): Codex cannot run commands or write files in workspace-write mode; "+sandboxFix(e))
	}
	return ok("agents", "codex-sandbox", "codex sandbox runs a command")
}

// firstLineOf is the first line of a command's output, cut to 120 bytes.
func firstLineOf(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if len(l) > 120 {
		l = l[:120]
	}
	return l
}
