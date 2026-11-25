package modules

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/keelbox/keelbox/internal/agentlimits"
	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	snap "github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/system"
	"github.com/keelbox/keelbox/internal/watch"
)

// Host is the operating-system layer: packages, identity, swap, automatic
// security updates, the two accounts, SSH hardening and the firewall.
type Host struct{}

func (Host) Name() string                { return "host" }
func (Host) Enabled(*config.Config) bool { return true }

// BasePackages are installed on every box. Hardware diagnostics from the
// original single-machine setup are left out: boxes are mostly VMs.
var BasePackages = []string{
	"ca-certificates", "curl", "gnupg", "jq", "git", "openssh-server",
	"build-essential", "pkg-config", "unzip", "zip", "xz-utils", "zstd",
	"tmux", "ncurses-term", "htop", "ripgrep", "fd-find", "ncdu",
	"bubblewrap", "socat", "file", "python3-pil", "unattended-upgrades", "ufw", "nftables", "locales", "acl",
}

const (
	sshdDropIn   = "/etc/ssh/sshd_config.d/01-keelbox.conf"
	sysctlFile   = "/etc/sysctl.d/60-keelbox.conf"
	uuLocalFile  = "/etc/apt/apt.conf.d/52keelbox-unattended-upgrades"
	uuPeriodic   = "/etc/apt/apt.conf.d/21keelbox-auto-upgrades"
	swapFile     = "/swapfile"
	adminSudoers = "/etc/sudoers.d/90-keelbox-admin"
	agentSudoers = "/etc/sudoers.d/91-keelbox-agent"
)

// sshd uses the FIRST value it reads for each keyword, and sshd_config
// includes sshd_config.d/*.conf in lexical order before its own settings.
// The drop-in is therefore 01-, so it wins over cloud-init's
// 50-cloud-init.conf (which sets PasswordAuthentication yes on many images).
//
// The agent account never receives a forwarded SSH agent: a human attaching
// with ssh -A would otherwise hand the agent their keys, including the one
// that logs in as the admin. sshd ends a Match block at the end of an
// included file, so the block cannot capture sshd_config's own settings.
func sshdConfig(agent string) string {
	return `# Managed by keelbox. Key-only SSH, no root login.
# Numbered 01- because sshd keeps the first value it reads for a keyword.
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitEmptyPasswords no
PubkeyAuthentication yes
AuthenticationMethods publickey
X11Forwarding no

# The agent never gets a forwarded SSH agent.
Match User ` + agent + `
	AllowAgentForwarding no
`
}

const sysctlConfig = `# Managed by keelbox.
# The default inotify limits are exhausted by file watchers over large
# node_modules trees.
fs.inotify.max_user_watches = 1048576
fs.inotify.max_user_instances = 1024
`

func (h Host) Steps(e *engine.Env) []engine.Step {
	c := e.Cfg
	steps := []engine.Step{
		engine.PackagesStep("packages", func(e *engine.Env) []string {
			return append(append([]string(nil), BasePackages...), e.Cfg.Host.Packages...)
		}),
		bwrapProfileStep(),
		hostnameStep(c.Host.Hostname),
		timezoneStep(c.Host.Timezone),
	}
	if c.Host.Locale != "C.UTF-8" {
		steps = append(steps, localeGenStep(c.Host.Locale))
	}
	steps = append(steps, localeDefaultStep(c.Host.Locale))
	if n, _ := c.SwapBytes(); n > 0 {
		steps = append(steps, swapStep(n))
	}
	steps = append(steps,
		engine.FileStep("sysctl", engine.File{Path: sysctlFile, Content: []byte(sysctlConfig), Mode: 0o644,
			OnChange: func(ctx context.Context, e *engine.Env) error {
				_, err := e.X.Run(ctx, system.C("sysctl", "--system"))
				return err
			}}),
		engine.FileStep("unattended-upgrades-periodic", engine.File{Path: uuPeriodic, Content: []byte(periodicConfig(c.UnattendedUpgrades())), Mode: 0o644}),
		engine.FileStep("unattended-upgrades-reboot", engine.File{Path: uuLocalFile, Content: []byte(rebootConfig(c.Host.AutoReboot)), Mode: 0o644}),
	)
	if c.UnattendedUpgrades() {
		steps = append(steps, engine.ServiceStep("unattended-upgrades-service", "unattended-upgrades.service"))
	}
	steps = append(steps, powerSleepSteps(c)...)

	admin, agent := c.Users.Admin, c.Users.Agent
	steps = append(steps,
		userStep("admin-user", admin.Name),
		groupMemberStep("admin-sudo-group", admin.Name, "sudo", true),
		engine.FileStep("admin-sudoers", engine.File{
			Path: adminSudoers, Mode: 0o440,
			Content:  []byte(fmt.Sprintf("# Managed by keelbox. The admin account has no password, so sudo cannot ask for one.\n%s ALL=(ALL:ALL) NOPASSWD:ALL\n", admin.Name)),
			Validate: visudo(adminSudoers),
		}),
		authorizedKeysDir("admin-ssh-dir", e.AdminUser()),
		authorizedKeysStep("admin-ssh-keys", e.AdminUser(), admin.SSHAuthorizedKeys),
		userStep("agent-user", agent.Name),
		authorizedKeysDir("agent-ssh-dir", e.AgentUser()),
		authorizedKeysStep("agent-ssh-keys", e.AgentUser(), agent.SSHAuthorizedKeys),
		lingerStep(agent.Name),
	)
	steps = append(steps, agentSchedulerSteps(agent.Name)...)
	steps = append(steps, agentLimitsSteps(c)...)
	if c.AgentSudo() {
		steps = append(steps, engine.FileStep("agent-sudoers", engine.File{
			Path: agentSudoers, Mode: 0o440,
			Content:  []byte(fmt.Sprintf("# Managed by keelbox: users.agent.permissions is full.\n%s ALL=(ALL:ALL) NOPASSWD:ALL\n", agent.Name)),
			Validate: visudo(agentSudoers),
		}))
	} else {
		steps = append(steps, absentStep("agent-no-sudoers", agentSudoers), groupMemberStep("agent-not-sudo", agent.Name, "sudo", false))
	}
	steps = append(steps, sshdStep(agent.Name), cloudInitKeepStep())
	steps = append(steps, rootNotExpiredStep(), bootstrapSudoersStep())
	if c.LockCloudUser() {
		steps = append(steps, cloudUserLockStep())
	}
	steps = append(steps, sshdTailnetOnlyStep())
	if c.FirewallEnabled() {
		steps = append(steps, firewallStep(), firewallAllowStep())
	}
	steps = append(steps, publicSSHStateStep())
	return append(steps, agentNetSteps(agent.Name)...)
}

func periodicConfig(on bool) string {
	v := "0"
	if on {
		v = "1"
	}
	return fmt.Sprintf("// Managed by keelbox.\nAPT::Periodic::Update-Package-Lists \"%s\";\nAPT::Periodic::Unattended-Upgrade \"%s\";\n", v, v)
}

func rebootConfig(when string) string {
	switch when {
	case config.AutoRebootNightly, config.AutoRebootWeekly:
		return "// Managed by keelbox. Agents may be mid-task: keelbox's watch run reboots for\n" +
			"// an update at night, once the box is idle (host.auto_reboot: " + when + ").\n" +
			"Unattended-Upgrade::Automatic-Reboot \"false\";\nUnattended-Upgrade::Automatic-Reboot-WithUsers \"false\";\n"
	case config.AutoRebootNever:
		return "// Managed by keelbox. Agents may be mid-task: never reboot without a human.\n" +
			"Unattended-Upgrade::Automatic-Reboot \"false\";\nUnattended-Upgrade::Automatic-Reboot-WithUsers \"false\";\n"
	}
	return fmt.Sprintf("// Managed by keelbox (host.auto_reboot).\nUnattended-Upgrade::Automatic-Reboot \"true\";\n"+
		"Unattended-Upgrade::Automatic-Reboot-WithUsers \"true\";\nUnattended-Upgrade::Automatic-Reboot-Time \"%s\";\n", when)
}

func visudo(path string) func(context.Context, *engine.Env) error {
	return func(ctx context.Context, e *engine.Env) error {
		_, err := e.X.Run(ctx, system.C("visudo", "-cqf", path))
		return err
	}
}

func hostnameStep(name string) engine.Step {
	return engine.Step{
		Name: "hostname",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			cur, _ := e.X.ReadFile("/etc/hostname")
			if strings.TrimSpace(string(cur)) != name {
				return engine.Pending("set hostname %s", name), nil
			}
			if !system.Succeeds(ctx, e.X, system.C("getent", "hosts", name)) {
				return engine.Pending("make %s resolvable in /etc/hosts", name), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			cur, _ := e.X.ReadFile("/etc/hostname")
			if strings.TrimSpace(string(cur)) != name {
				if _, err := e.X.Run(ctx, system.C("hostnamectl", "set-hostname", name)); err != nil {
					return err
				}
			}
			if !system.Succeeds(ctx, e.X, system.C("getent", "hosts", name)) {
				return engine.LineStep("hosts", "/etc/hosts", "127.0.1.1 "+name+" # keelbox", 0o644, "root").Apply(ctx, e)
			}
			return nil
		},
	}
}

func timezoneStep(tz string) engine.Step {
	return engine.Step{
		Name: "timezone",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			cur, err := system.Output(ctx, e.X, system.C("timedatectl", "show", "-p", "Timezone", "--value"))
			if err == nil && cur == tz {
				return engine.Converged(), nil
			}
			return engine.Pending("set timezone %s", tz), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			if st, _ := e.X.Stat("/usr/share/zoneinfo/" + tz); !st.Exists {
				return fmt.Errorf("unknown timezone %q (no /usr/share/zoneinfo/%s)", tz, tz)
			}
			_, err := e.X.Run(ctx, system.C("timedatectl", "set-timezone", tz))
			return err
		},
	}
}

// localeName normalises en_GB.UTF-8 to locale -a's en_GB.utf8.
func localeName(l string) string {
	l = strings.Replace(l, ".UTF-8", ".utf8", 1)
	return l
}

func localeGenStep(loc string) engine.Step {
	return engine.Step{
		Name: "locale-generated",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			out, _ := system.Output(ctx, e.X, system.C("locale", "-a"))
			for _, l := range strings.Fields(out) {
				if l == localeName(loc) {
					return engine.Converged(), nil
				}
			}
			return engine.Pending("generate locale %s", loc), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			_, err := e.X.Run(ctx, system.C("locale-gen", loc))
			return err
		},
	}
}

func localeDefaultStep(loc string) engine.Step {
	return engine.Step{
		Name: "locale-default",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			data, _ := e.X.ReadFile("/etc/default/locale")
			if engine.ParseEnvFile(string(data))["LANG"] == loc {
				return engine.Converged(), nil
			}
			return engine.Pending("set LANG=%s", loc), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			_, err := e.X.Run(ctx, system.C("update-locale", "LANG="+loc))
			return err
		},
	}
}

const swapFstab = swapFile + " none swap sw 0 0"

// swapStep creates /swapfile unless the machine already has swap of its own;
// existing swap (a partition or the installer's /swap.img) is never replaced.
func swapStep(size int64) engine.Step {
	state := func(ctx context.Context, e *engine.Env) (active []string, fstab bool) {
		out, _ := system.Output(ctx, e.X, system.C("swapon", "--show=NAME", "--noheadings"))
		active = strings.Fields(out)
		data, _ := e.X.ReadFile("/etc/fstab")
		for _, l := range strings.Split(string(data), "\n") {
			if f := strings.Fields(l); len(f) >= 3 && f[0] == swapFile && f[2] == "swap" {
				fstab = true
			}
		}
		return active, fstab
	}
	return engine.Step{
		Name: "swap",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			active, fstab := state(ctx, e)
			ours := contains(active, swapFile)
			switch {
			case len(active) > 0 && !ours:
				return engine.Converged(), nil // existing swap, left alone
			case ours && fstab:
				return engine.Converged(), nil
			case ours:
				return engine.Pending("add %s to /etc/fstab", swapFile), nil
			}
			return engine.Pending("create %s swap file", humanSize(size)), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			active, fstab := state(ctx, e)
			if !contains(active, swapFile) {
				st, err := e.X.Stat(swapFile)
				if err != nil {
					return err
				}
				if !st.Exists || st.Size != size {
					_ = e.X.Remove(swapFile)
					if _, err := e.X.Run(ctx, system.C("fallocate", "-l", strconv.FormatInt(size, 10), swapFile)); err != nil {
						return err
					}
				}
				for _, c := range []system.Cmd{
					system.C("chmod", "600", swapFile),
					system.C("mkswap", swapFile),
					system.C("swapon", swapFile),
				} {
					if _, err := e.X.Run(ctx, c); err != nil {
						return err
					}
				}
			}
			if !fstab {
				return engine.LineStep("fstab", "/etc/fstab", swapFstab, 0o644, "root").Apply(ctx, e)
			}
			return nil
		},
	}
}

func humanSize(n int64) string {
	if n%(1<<30) == 0 {
		return fmt.Sprintf("%dG", n>>30)
	}
	return fmt.Sprintf("%dM", n>>20)
}

func userStep(step, name string) engine.Step {
	return engine.Step{
		Name: step,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			_, err := e.X.LookupUser(name)
			if errors.Is(err, system.ErrNoUser) {
				return engine.Pending("create user %s", name), nil
			}
			return engine.Converged(), err
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			// New accounts get no password (useradd leaves it locked), so the
			// only way in is an SSH key.
			args := []string{"--create-home", "--shell", "/bin/bash"}
			if system.Succeeds(ctx, e.X, system.C("getent", "group", name)) {
				args = append(args, "--gid", name)
			} else {
				args = append(args, "--user-group")
			}
			_, err := e.X.Run(ctx, system.C("useradd", append(args, name)...))
			return err
		},
	}
}

// UserGroups lists a user's groups.
func UserGroups(ctx context.Context, x system.Executor, user string) ([]string, error) {
	out, err := system.Output(ctx, x, system.C("id", "-nG", user))
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

func groupMemberStep(step, user, group string, want bool) engine.Step {
	return engine.Step{
		Name: step,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			groups, err := UserGroups(ctx, e.X, user)
			if err != nil {
				return engine.Pending("user %s missing", user), nil
			}
			if contains(groups, group) == want {
				return engine.Converged(), nil
			}
			if want {
				return engine.Pending("add %s to group %s", user, group), nil
			}
			return engine.Pending("remove %s from group %s", user, group), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			var err error
			if want {
				_, err = e.X.Run(ctx, system.C("usermod", "-aG", group, user))
			} else {
				_, err = e.X.Run(ctx, system.C("gpasswd", "-d", user, group))
			}
			return err
		},
	}
}

func absentStep(step, path string) engine.Step {
	return engine.Step{
		Name: step,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			st, err := e.X.Stat(path)
			if err != nil || !st.Exists {
				return engine.Converged(), err
			}
			return engine.Pending("remove %s", path), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error { return e.X.Remove(path) },
	}
}

func authorizedKeysDir(step string, u system.User) engine.Step {
	return engine.DirStep(step, u.Home+"/.ssh", 0o700, u.Name, u.Name)
}

// authorizedKeysStep manages a block in authorized_keys; keys the user
// added by hand outside the block are kept.
func authorizedKeysStep(step string, u system.User, keys []string) engine.Step {
	return engine.BlockStep(step, engine.Block{
		Path: u.Home + "/.ssh/authorized_keys", ID: "keys", Content: strings.Join(keys, "\n"),
		Mode: 0o600, Owner: u.Name,
	})
}

// sshdStep hardens sshd with a drop-in. It refuses to apply unless the admin
// account already has a key, validates with sshd -t and rolls back on
// failure, and reloads rather than restarts so open sessions survive. It
// never enables ssh.service: on Ubuntu 24.04+ sshd is socket-activated and
// starting the service tears down ssh.socket's listener.
func sshdStep(agent string) engine.Step {
	f := engine.File{
		Path: sshdDropIn, Content: []byte(sshdConfig(agent)), Mode: 0o644,
		Validate: func(ctx context.Context, e *engine.Env) error {
			_ = e.X.MkdirAll("/run/sshd", 0o755, "root", "root")
			_, err := e.X.Run(ctx, system.C("sshd", "-t"))
			return err
		},
		// Reload acts only on a running unit; with socket activation a
		// stopped ssh.service reads the new config when the next connection
		// starts it.
		OnChange: reloadSSHD,
	}
	s := engine.FileStep("sshd-hardening", f)
	apply := s.Apply
	s.Apply = func(ctx context.Context, e *engine.Env) error {
		admin := e.AdminUser()
		data, err := e.FS(admin.Name).ReadFile(admin.Home + "/.ssh/authorized_keys")
		if err != nil || !hasKey(string(data)) {
			return fmt.Errorf("refusing to disable SSH passwords: %s has no authorized SSH key yet", admin.Name)
		}
		return apply(ctx, e)
	}
	return s
}

func hasKey(authorized string) bool {
	for _, l := range strings.Split(authorized, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			return true
		}
	}
	return false
}

// ufwRules are the rules keelbox converges, as `ufw status` prints them.
type ufwState struct {
	active, denyIn, allowOut, ssh, tailnet bool
}

func readUFW(ctx context.Context, e *engine.Env) (ufwState, error) {
	out, err := system.Output(ctx, e.X, system.C("ufw", "status", "verbose"))
	if err != nil {
		return ufwState{}, nil // not installed yet, or inactive
	}
	var s ufwState
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		f := strings.Fields(l)
		switch {
		case l == "Status: active":
			s.active = true
		case strings.HasPrefix(l, "Default:"):
			s.denyIn = strings.Contains(l, "deny (incoming)")
			s.allowOut = strings.Contains(l, "allow (outgoing)")
		case len(f) >= 4 && f[0] == "22/tcp" && f[1] == "ALLOW" && f[2] == "IN" && f[3] == "Anywhere":
			s.ssh = true
		case strings.Contains(l, "on tailscale0") && strings.Contains(l, "ALLOW IN"):
			s.tailnet = true
		}
	}
	return s, nil
}

// firewallStep enables UFW: deny inbound, allow outbound, allow SSH unless
// public SSH is closed (planPublicSSH), and everything on tailscale0.
// Existing rules are kept. The system Docker daemon's port forwarding
// bypasses UFW; the DOCKER-USER guard in runtime covers it. Rootless ports
// are ordinary sockets.
func firewallStep() engine.Step {
	want := func(e *engine.Env, s ufwState, closed bool) []string {
		var p []string
		if !s.denyIn {
			p = append(p, "default deny incoming")
		}
		if !s.allowOut {
			p = append(p, "default allow outgoing")
		}
		if !closed && !s.ssh {
			p = append(p, "allow 22/tcp")
		}
		if closed && s.ssh {
			p = append(p, "remove public 22/tcp")
		}
		if e.Cfg.Tailscale() && !s.tailnet {
			p = append(p, "allow in on tailscale0")
		}
		if !s.active {
			p = append(p, "enable")
		}
		return p
	}
	return engine.Step{
		Name: "firewall",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			s, err := readUFW(ctx, e)
			if err != nil {
				return engine.Status{}, err
			}
			if p := want(e, s, planPublicSSH(ctx, e).closed); len(p) > 0 {
				return engine.Pending("ufw: %s", strings.Join(p, ", ")), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			s, err := readUFW(ctx, e)
			if err != nil {
				return err
			}
			closed := planPublicSSH(ctx, e).closed
			cmds := [][]string{{"default", "deny", "incoming"}, {"default", "allow", "outgoing"}}
			// The tailnet rule goes first, so closing never leaves a moment
			// with no way in.
			if e.Cfg.Tailscale() {
				cmds = append(cmds, []string{"allow", "in", "on", "tailscale0", "comment", "keelbox tailnet"})
			}
			if !closed {
				cmds = append(cmds, []string{"allow", "22/tcp", "comment", "keelbox ssh"})
			} else if s.ssh {
				cmds = append(cmds, []string{"delete", "allow", "22/tcp"})
			}
			cmds = append(cmds, []string{"--force", "enable"})
			for _, args := range cmds {
				if _, err := e.X.Run(ctx, system.C("ufw", args...)); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func (Host) Verify(ctx context.Context, e *engine.Env) []engine.Finding {
	var out []engine.Finding
	add := func(check string, ok bool, detail string, fail engine.Level) {
		lvl := engine.LevelOK
		if !ok {
			lvl = fail
		}
		out = append(out, engine.Finding{Module: "host", Check: check, Level: lvl, Detail: detail})
	}
	// Effective sshd settings, not just our file: catches an earlier drop-in
	// or Match block overriding them.
	eff, err := system.Output(ctx, e.X, system.C("sshd", "-T"))
	if err != nil {
		add("sshd-effective", false, "sshd -T failed: "+err.Error(), engine.LevelFail)
	} else {
		kv := map[string]string{}
		for _, l := range strings.Split(eff, "\n") {
			k, v, _ := strings.Cut(strings.TrimSpace(l), " ")
			kv[k] = v
		}
		add("ssh-password-auth", kv["passwordauthentication"] == "no", "passwordauthentication "+kv["passwordauthentication"], engine.LevelFail)
		add("ssh-kbd-interactive", kv["kbdinteractiveauthentication"] == "no", "kbdinteractiveauthentication "+kv["kbdinteractiveauthentication"], engine.LevelFail)
		add("ssh-root-login", kv["permitrootlogin"] == "no", "permitrootlogin "+kv["permitrootlogin"], engine.LevelFail)
	}
	out = append(out, verifyAgentNet(ctx, e), verifyBootstrapSudoers(e))
	out = append(out, verifyContainer(ctx, e)...)
	out = append(out, verifyLinkLocalAccounts(e)...)
	out = append(out, verifyResolver(e)...)
	out = append(out, verifyAgentSchedulers(ctx, e)...)
	if eff, err := system.Output(ctx, e.X, system.C("sshd", "-T", "-C", "user="+e.Cfg.Users.Agent.Name+",host=localhost,addr=127.0.0.1")); err == nil {
		add("ssh-agent-forwarding", strings.Contains(eff, "allowagentforwarding no"), "no agent forwarding for "+e.Cfg.Users.Agent.Name, engine.LevelFail)
	}
	listening := system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", "ssh.socket")) ||
		system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", "ssh.service"))
	add("ssh-listening", listening, "ssh.socket or ssh.service active", engine.LevelFail)
	if e.Cfg.FirewallEnabled() {
		s, _ := readUFW(ctx, e)
		add("firewall-active", s.active && s.denyIn, "ufw active, default deny incoming", engine.LevelFail)
	}
	out = append(out, verifyPublicSSH(ctx, e)...)
	if e.Cfg.UnattendedUpgrades() {
		ok := system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", "unattended-upgrades.service"))
		add("unattended-upgrades", ok, "unattended-upgrades.service active", engine.LevelFail)
	}
	if watch.RebootNeeded(e.X) {
		add("reboot-required", false, "an installed update needs a reboot: "+watch.RebootFix(e.Cfg), engine.LevelWarn)
	}
	if _, sec, known := watch.PendingUpdates(ctx, e.X); known && sec > 0 {
		add("security-updates", false, fmt.Sprintf("%d security update(s) waiting; unattended-upgrades installs them daily. Now: keelbox ssh %s --admin, then sudo unattended-upgrade", sec, e.Cfg.Box), engine.LevelWarn)
	}
	out = append(out, verifySize(ctx, e)...)
	out = append(out, verifyAgentLimits(ctx, e)...)
	out = append(out, verifyIOWeight(ctx, e)...)
	return append(out, verifyPowerSleep(ctx, e)...)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Below these a dev server, a test run and a headless browser together
// run the box out of memory. 4 GB machines report a
// little less than 4 GiB.
const (
	minCPUs   = 2
	minMemory = 3584 << 20
)

// SmallHost says why a machine is below the recommended size, "" when it
// is not.
func SmallHost(cpus int, memory int64) string {
	var low []string
	if cpus > 0 && cpus < minCPUs {
		low = append(low, fmt.Sprintf("%d CPU", cpus))
	}
	if memory > 0 && memory < minMemory {
		low = append(low, agentlimits.Size(memory)+" memory")
	}
	if len(low) == 0 {
		return ""
	}
	return strings.Join(low, " and ") + ", below the 2 CPUs and 4 GB a dev server, tests and a headless browser need together"
}

func verifySize(ctx context.Context, e *engine.Env) []engine.Finding {
	h := snap.ReadHost(ctx, e.X)
	if h == nil {
		return nil
	}
	if why := SmallHost(h.CPUs, h.Memory); why != "" {
		return []engine.Finding{warn("host", "size", why+": use a larger machine; for a cloud server keelbox created: keelbox resize "+e.Cfg.Box)}
	}
	return []engine.Finding{ok("host", "size", fmt.Sprintf("%d CPUs, %s memory", h.CPUs, agentlimits.Size(h.Memory)))}
}
