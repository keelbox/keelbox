package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// Access installs Tailscale from its signed apt repository. Joining the
// tailnet is the user's step (`sudo tailscale up`) unless the config names an
// auth key file; the key goes to tailscale as --auth-key=file:PATH, so it is
// never on a command line, in keelbox's output or in the logbook. Funnel,
// Serve and Tailscale SSH are never turned on.
type Access struct{}

func (Access) Name() string                  { return "access" }
func (Access) Enabled(c *config.Config) bool { return c.Tailscale() }

const (
	tailscaleKeyring = "/usr/share/keyrings/tailscale-archive-keyring.gpg"
	tailscaleSources = "/etc/apt/sources.list.d/tailscale.list"
)

func (a Access) Steps(e *engine.Env) []engine.Step {
	cn := e.Facts.Codename
	steps := []engine.Step{
		aptKeyStep("tailscale-key", fmt.Sprintf("https://pkgs.tailscale.com/stable/ubuntu/%s.noarmor.gpg", cn), tailscaleKeyring, e.M.Tailscale.GPGFingerprint),
		aptSourceStep("tailscale-repo", tailscaleSources, fmt.Sprintf("# Managed by keelbox.\ndeb [signed-by=%s] https://pkgs.tailscale.com/stable/ubuntu %s main\n", tailscaleKeyring, cn)),
		engine.PackagesStep("tailscale-package", func(*engine.Env) []string { return []string{"tailscale"} }),
		engine.ServiceStep("tailscaled", "tailscaled.service"),
	}
	if e.Cfg.Access.Tailscale.AuthKeyFile != "" {
		steps = append(steps, tailscaleUpStep())
	}
	return steps
}

func tailscaleState(ctx context.Context, e *engine.Env) string {
	st, _ := tailscaleStatus(ctx, e)
	return st
}

// tailscaleStatus is the backend state and, when the box's key expires,
// when (zero when key expiry is off).
func tailscaleStatus(ctx context.Context, e *engine.Env) (string, time.Time) {
	out, err := system.Output(ctx, e.X, system.C("tailscale", "status", "--json"))
	if err != nil && out == "" {
		return "", time.Time{}
	}
	var st struct {
		BackendState string
		Self         struct{ KeyExpiry *time.Time }
	}
	if json.Unmarshal([]byte(out), &st) != nil {
		return "", time.Time{}
	}
	var exp time.Time
	if st.Self.KeyExpiry != nil {
		exp = *st.Self.KeyExpiry
	}
	return st.BackendState, exp
}

// TailscaleMachines is the admin console page where key expiry is turned
// off for a machine.
const TailscaleMachines = "https://login.tailscale.com/admin/machines"

// keyWarnDays and keyFailDays are how long before the box's Tailscale key
// expires that doctor warns, and fails when the tailnet is the only way in.
const keyWarnDays, keyFailDays = 30, 7

// tailnetKeyFinding says when the box leaves the tailnet because its key
// expires (docs/guides/public-ssh.md#before-public-ssh-closes).
func tailnetKeyFinding(e *engine.Env, exp, now time.Time) engine.Finding {
	if exp.IsZero() {
		return ok("access", "tailnet-key", "key expiry off")
	}
	left := exp.Sub(now)
	days := int(left.Hours() / 24)
	when := exp.UTC().Format("2006-01-02")
	if days > keyWarnDays {
		return ok("access", "tailnet-key", "key expires "+when)
	}
	text := fmt.Sprintf("the box's Tailscale key expires %s, in %s, and the box then leaves the tailnet until someone signs it in again. "+
		"Turn key expiry off for it at %s (the box, then Disable key expiry)", when, plural(days, "day", "days"), TailscaleMachines)
	if days <= keyFailDays && (e.Cfg.PublicSSHMode() == config.PublicSSHClosed || PublicSSHClosed(e)) {
		return fail("access", "tailnet-key", text+"; with SSH from the internet closed, the tailnet is the only way in")
	}
	return warn("access", "tailnet-key", text)
}

// TailnetJoin says how to join the box to the tailnet, for where its reader
// is. A run that arrived over SSH is read on another computer: keelbox's own
// commands, which name the box. A run on the box itself gets tailscale's,
// and says it is for the box: a scheduled doctor's findings are read
// elsewhere too.
func TailnetJoin(e *engine.Env) string {
	box := e.Cfg.Box
	switch {
	case e.SSHConnection == "":
		return "join the tailnet on the box: sudo tailscale up --hostname=" + e.Cfg.Access.Tailscale.Hostname
	case e.Cfg.Access.Tailscale.AuthKeyFile != "":
		return "join the tailnet with the auth key file: keelbox apply " + box
	}
	return "join the tailnet: keelbox login " + box + " tailscale, then keelbox apply " + box
}

// TailscaleRunning reports whether this machine is connected to a tailnet.
func TailscaleRunning(ctx context.Context, e *engine.Env) bool {
	return tailscaleState(ctx, e) == "Running"
}

func tailscaleUpStep() engine.Step {
	return engine.Step{
		Name: "tailscale-up",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			if TailscaleRunning(ctx, e) {
				return engine.Converged(), nil
			}
			return engine.Pending("join the tailnet with the auth key file"), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			path := e.Cfg.Access.Tailscale.AuthKeyFile
			st, err := e.X.Stat(path)
			if err != nil {
				return err
			}
			if !st.Exists {
				return fmt.Errorf("access.tailscale.auth_key_file %s does not exist on the box", path)
			}
			if st.Mode&0o077 != 0 {
				return fmt.Errorf("access.tailscale.auth_key_file %s is readable by others (mode %o); chmod 600 it", path, st.Mode)
			}
			_, err = e.X.Run(ctx, system.C("tailscale", "up", "--auth-key=file:"+path, "--hostname="+e.Cfg.Access.Tailscale.Hostname))
			return err
		},
	}
}

func (Access) Verify(ctx context.Context, e *engine.Env) []engine.Finding {
	var out []engine.Finding
	if system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", "tailscaled.service")) {
		out = append(out, ok("access", "tailscaled", "tailscaled active"))
	} else {
		out = append(out, fail("access", "tailscaled", "tailscaled not active"))
	}
	switch st, exp := tailscaleStatus(ctx, e); st {
	case "Running":
		out = append(out, ok("access", "tailnet", "connected"), tailnetKeyFinding(e, exp, time.Now()))
	case "NeedsLogin", "NoState", "Stopped", "":
		lvl := warn
		if e.Cfg.Access.Tailscale.AuthKeyFile != "" || e.Cfg.PublicSSHMode() == config.PublicSSHClosed || PublicSSHClosed(e) {
			lvl = fail
		}
		out = append(out, flaky(lvl("access", "tailnet", "not connected; "+TailnetJoin(e))))
	default:
		out = append(out, flaky(warn("access", "tailnet", "tailscale state "+st)))
	}
	return out
}
