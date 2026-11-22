package modules

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/previews"
	"github.com/keelbox/keelbox/internal/system"
)

// Previews installs Caddy and the root service that publishes the agent's
// loopback dev servers on the tailnet (docs/adr/0019-previews.md). Apply
// installs the pieces; what is served follows the agent's own file, which
// the service reads as the agent whenever it changes.
type Previews struct{}

func (Previews) Name() string                  { return "previews" }
func (Previews) Enabled(c *config.Config) bool { return c.PreviewsEnabled() }

const unitDir = "/etc/systemd/system/"

func caddyUnit() string {
	return `# Managed by keelbox. Serves the agent's previews on the tailnet only.
[Unit]
Description=keelbox previews (Caddy, tailnet addresses only)
After=network-online.target tailscaled.service
Wants=network-online.target
ConditionPathExists=` + previews.CaddyConfig + `

[Service]
User=` + previews.ServiceUser + `
Group=` + previews.ServiceUser + `
ExecStart=` + previews.CaddyBin + ` run --config ` + previews.CaddyConfig + `
Environment=HOME=` + previews.StateDir + ` XDG_CONFIG_HOME=` + previews.StateDir + `/config XDG_DATA_HOME=` + previews.StateDir + `/data
StateDirectory=keelbox-preview
StateDirectoryMode=0700
Restart=on-failure
RestartSec=5
# Caddy may talk only to loopback (the dev servers) and the tailnet.
IPAddressDeny=any
IPAddressAllow=localhost 100.64.0.0/10 fd7a:115c:a1e0::/48
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
CapabilityBoundingSet=
AmbientCapabilities=
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictNamespaces=yes
RestrictRealtime=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
`
}

const syncUnitText = `# Managed by keelbox. Renders the agent's previews for Caddy.
[Unit]
Description=keelbox previews: publish the agent's previews
After=tailscaled.service

[Service]
Type=oneshot
ExecStart=` + BinaryPath + ` previews-sync --local ` + ConfigPath + `
`

func pathUnit(agentHome string) string {
	return `# Managed by keelbox. Runs the previews service when the agent's previews file changes.
[Unit]
Description=keelbox previews: watch the agent's previews file

[Path]
PathChanged=` + agentHome + "/" + previews.AgentFile + `
Unit=` + previews.SyncUnit + `

[Install]
WantedBy=paths.target
`
}

const timerUnitText = `# Managed by keelbox. Re-publishes previews when the tailnet or certificate changes.
[Unit]
Description=keelbox previews: periodic refresh

[Timer]
OnBootSec=1min
OnUnitActiveSec=10min
Unit=` + previews.SyncUnit + `

[Install]
WantedBy=timers.target
`

func (Previews) Steps(e *engine.Env) []engine.Step {
	agent := e.AgentUser()
	return []engine.Step{
		caddyBinaryStep(),
		systemUserStep("preview-user", previews.ServiceUser),
		engine.DirStep("previews-dir", previews.Dir, 0o755, "root", "root"),
		engine.DirStep("previews-tls-dir", previews.TLSDir, 0o750, "root", previews.ServiceUser),
		engine.DirStep("agent-previews-dir", agent.Home+"/.config/keelbox", 0, agent.Name, agent.Name),
		unitFileStep("caddy-unit", previews.CaddyUnit, caddyUnit(), true),
		unitFileStep("previews-service-unit", previews.SyncUnit, syncUnitText, false),
		unitFileStep("previews-path-unit", previews.PathUnit, pathUnit(agent.Home), false),
		unitFileStep("previews-timer-unit", previews.TimerUnit, timerUnitText, false),
		unitEnabledStep("caddy-enabled", previews.CaddyUnit),
		engine.ServiceStep("previews-path", previews.PathUnit),
		engine.ServiceStep("previews-timer", previews.TimerUnit),
		previewsSyncStep(),
	}
}

// unitFileStep writes a system unit; a change reloads systemd, and when
// restart is set restarts the unit if it is running.
func unitFileStep(step, unit, content string, restart bool) engine.Step {
	return engine.FileStep(step, engine.File{Path: unitDir + unit, Content: []byte(content), Mode: 0o644,
		OnChange: func(ctx context.Context, e *engine.Env) error {
			if _, err := e.X.Run(ctx, system.C("systemctl", "daemon-reload")); err != nil {
				return err
			}
			if restart && system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", unit)) {
				_, err := e.X.Run(ctx, system.C("systemctl", "restart", unit))
				return err
			}
			return nil
		}})
}

// unitEnabledStep enables a unit at boot without starting it now.
func unitEnabledStep(step, unit string) engine.Step {
	return engine.CommandStep(step, "enable "+unit, system.C("systemctl", "is-enabled", "--quiet", unit), system.C("systemctl", "enable", unit))
}

// systemUserStep creates a locked system account with no home and no shell.
func systemUserStep(step, name string) engine.Step {
	return engine.Step{
		Name: step,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			if _, err := e.X.LookupUser(name); err == nil {
				return engine.Converged(), nil
			}
			return engine.Pending("create system user %s", name), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			_, err := e.X.Run(ctx, system.C("useradd", "--system", "--user-group", "--no-create-home",
				"--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", name))
			return err
		},
	}
}

// caddyBinaryStep installs the manifest's Caddy release, checked against the
// manifest's SHA-512, root-owned in /usr/local/bin.
func caddyBinaryStep() engine.Step {
	return engine.Step{
		Name: "caddy",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			out, err := system.Output(ctx, e.X, system.C(previews.CaddyBin, "version"))
			if err != nil {
				return engine.Pending("install caddy %s", e.M.Caddy.Version), nil
			}
			if !strings.HasPrefix(out, "v"+e.M.Caddy.Version+" ") && out != "v"+e.M.Caddy.Version {
				return engine.Pending("move caddy to %s (have %s)", e.M.Caddy.Version, firstField(out)), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			want := e.M.Caddy.SHA512[e.Facts.Arch]
			if want == "" {
				return fmt.Errorf("release %s has no caddy checksum for %s", e.M.Release, e.Facts.Arch)
			}
			dir, cleanup, err := tempDir(ctx, e, "")
			if err != nil {
				return err
			}
			defer cleanup()
			tarball := dir + "/caddy.tar.gz"
			if _, err := e.X.Run(ctx, curl(e.M.CaddyURL(e.Facts.Arch), tarball)); err != nil {
				return err
			}
			sum, err := system.Output(ctx, e.X, system.C("sha512sum", tarball))
			if err != nil {
				return err
			}
			if got := firstField(sum); got != want {
				return fmt.Errorf("caddy tarball checksum %s does not match the release manifest (%s); refusing to install", got, want)
			}
			for _, c := range []system.Cmd{
				system.C("tar", "-xzf", tarball, "-C", dir, "caddy"),
				system.C("install", "-m", "0755", "-o", "root", "-g", "root", dir+"/caddy", previews.CaddyBin),
			} {
				if _, err := e.X.Run(ctx, c); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// previewsSyncStep publishes the agent's current previews once, so the
// status file exists and matches the config as soon as apply finishes. The
// service keeps it current afterwards.
func previewsSyncStep() engine.Step {
	return engine.Step{
		Name: "previews-sync",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			st, ok := previews.ReadStatus(e)
			lo, hi := e.Cfg.PreviewPorts()
			switch {
			case !ok:
				return engine.Pending("publish the agent's previews"), nil
			case st.Agent != e.Cfg.Users.Agent.Name || st.Ports != [2]int{lo, hi} || !slices.Equal(st.Skip, e.Cfg.PreviewSkipped()):
				return engine.Pending("publish the agent's previews for the new config"), nil
			case st.Render < previews.Render:
				return engine.Pending("publish the agent's previews with this keelbox's proxy settings"), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			_, err := previews.Sync(ctx, e)
			return err
		},
	}
}

func (Previews) Verify(ctx context.Context, e *engine.Env) []engine.Finding {
	const mod = "previews"
	var out []engine.Finding
	st, found := previews.ReadStatus(e)
	switch {
	case !found:
		out = append(out, fail(mod, "service", "no status from "+previews.SyncUnit+" (systemctl status "+previews.SyncUnit+")"))
		return out
	case !st.Tailnet.Connected:
		out = append(out, warn(mod, "tailnet", "previews need the tailnet; the box is not connected"))
	case !st.Tailnet.HTTPS:
		out = append(out, warn(mod, "tailnet", "HTTPS certificates are off in the tailnet: turn them on in the Tailscale admin console (DNS page)"))
	default:
		out = append(out, ok(mod, "tailnet", "certificates for "+st.Tailnet.Name))
	}
	if st.Tailnet.Connected && st.Tailnet.HTTPS && st.Error != "" {
		out = append(out, fail(mod, "service", st.Error))
	} else if age := time.Since(st.Updated); age > 30*time.Minute {
		out = append(out, warn(mod, "service", fmt.Sprintf("last ran %s ago; the timer runs it every 10 minutes (systemctl status %s)", age.Round(time.Minute), previews.TimerUnit)))
	} else {
		out = append(out, ok(mod, "service", fmt.Sprintf("%d preview(s) published", len(st.Previews))))
	}
	for _, p := range st.Problems {
		out = append(out, warn(mod, "agent-file", p))
	}

	// Caddy must listen on tailnet addresses and nowhere else.
	ss, err := system.Output(ctx, e.X, system.C("ss", "-Hltnp"))
	if err != nil {
		out = append(out, fail(mod, "binding", "ss failed: "+err.Error()))
	} else {
		var bad, good []string
		for ap, proc := range previews.Listeners(ss) {
			if !strings.Contains(proc, `"caddy"`) {
				continue
			}
			if previews.InTailnet(ap.Addr()) {
				good = append(good, ap.String())
			} else {
				bad = append(bad, ap.String())
			}
		}
		switch {
		case len(bad) > 0:
			out = append(out, fail(mod, "binding", "Caddy listens off the tailnet: "+strings.Join(bad, ", ")))
		case len(good) > 0:
			out = append(out, ok(mod, "binding", "Caddy listens on tailnet addresses only"))
		default:
			out = append(out, ok(mod, "binding", "Caddy is not listening (nothing to serve)"))
		}
	}
	if deny, _ := system.Output(ctx, e.X, system.C("systemctl", "show", "-p", "IPAddressDeny", "--value", previews.CaddyUnit)); deny == "" {
		out = append(out, fail(mod, "caddy-sandbox", previews.CaddyUnit+" has no IPAddressDeny: run keelbox apply"))
	}
	out = append(out, verifyPreviewFirewall(ctx, e))
	return out
}

// verifyPreviewFirewall checks that no UFW rule admits the preview ports on
// any interface but tailscale0. The tailnet rule admits them there.
func verifyPreviewFirewall(ctx context.Context, e *engine.Env) engine.Finding {
	const mod = "previews"
	if !e.Cfg.FirewallEnabled() {
		return warn(mod, "firewall", "host.firewall.enabled is false: only Caddy's tailnet-only binding keeps previews off other interfaces")
	}
	s, _ := readUFW(ctx, e)
	if !s.active || !s.denyIn {
		return fail(mod, "firewall", "ufw is not active with default deny incoming")
	}
	out, _ := system.Output(ctx, e.X, system.C("ufw", "status"))
	lo, hi := e.Cfg.PreviewPorts()
	skip := e.Cfg.PreviewSkipped()
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 || !strings.Contains(l, "ALLOW") || strings.Contains(l, "on tailscale0") {
			continue
		}
		if portRuleOverlaps(f[0], lo, hi, skip) {
			return fail(mod, "firewall", "a ufw rule admits preview ports off the tailnet: "+strings.TrimSpace(l))
		}
	}
	detail := fmt.Sprintf("ports %d-%d admitted only on tailscale0", lo, hi)
	if note := e.Cfg.PreviewSkipNote(); note != "" {
		detail += "; " + note
	}
	return ok(mod, "firewall", detail)
}

// portRuleOverlaps reports whether a ufw "To" column such as 8401/tcp,
// 8400:8500/tcp or Anywhere admits a TCP port in lo-hi that previews use:
// one not in skip, which host.firewall.allow keeps open on purpose.
func portRuleOverlaps(to string, lo, hi int, skip []int) bool {
	if to == "Anywhere" || to == "Anywhere (v6)" {
		return true
	}
	spec, proto, _ := strings.Cut(to, "/")
	if proto == "udp" {
		return false // previews are HTTPS: TCP only
	}
	for _, part := range strings.Split(spec, ",") {
		a, b, isRange := strings.Cut(part, ":")
		var x, y int
		if _, err := fmt.Sscan(a, &x); err != nil {
			continue
		}
		y = x
		if isRange {
			if _, err := fmt.Sscan(b, &y); err != nil {
				continue
			}
		}
		for q := max(x, lo); q <= min(y, hi); q++ {
			if !slices.Contains(skip, q) {
				return true
			}
		}
	}
	return false
}

// PreviewsSummary is one line for keelbox status.
func PreviewsSummary(e *engine.Env) string {
	st, ok := previews.ReadStatus(e)
	switch {
	case !ok:
		return "not set up yet (keelbox apply)"
	case st.Error != "":
		if !st.Tailnet.Connected {
			return "not on the tailnet"
		}
		if !st.Tailnet.HTTPS {
			return "tailnet HTTPS certificates off"
		}
		return "error, see keelbox doctor"
	}
	var urls []string
	for _, p := range st.Previews {
		urls = append(urls, p.Name+" "+p.URL)
	}
	if len(urls) == 0 {
		return "none"
	}
	return strings.Join(urls, ", ")
}
