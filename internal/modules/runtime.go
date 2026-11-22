package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/projects"
	"github.com/keelbox/keelbox/internal/system"
)

// Runtime is the toolchain: Docker Engine from Docker's apt repository,
// mise, and Node + pnpm through mise for the agent user.
type Runtime struct{}

func (Runtime) Name() string                { return "runtime" }
func (Runtime) Enabled(*config.Config) bool { return true }

const (
	dockerKeyring = "/etc/apt/keyrings/docker.asc"
	dockerSources = "/etc/apt/sources.list.d/docker.sources"
	dockerDaemon  = "/etc/docker/daemon.json"
	dockerBaseURL = "https://download.docker.com/linux/ubuntu"
	miseBin       = "/usr/local/bin/mise"
	ghKeyring     = "/etc/apt/keyrings/githubcli-archive-keyring.gpg"
	ghSources     = "/etc/apt/sources.list.d/github-cli.list"
)

// DockerConflicts are packages Docker documents as conflicting.
var DockerConflicts = []string{"docker.io", "docker-compose", "docker-compose-v2", "podman-docker", "containerd", "runc"}

// dockerLogOpts bound container logs; unbounded json-file logs fill disks.
var dockerLogOpts = map[string]any{
	"log-driver": "json-file",
	"log-opts":   map[string]any{"max-size": "10m", "max-file": "3"},
}

func (r Runtime) Steps(e *engine.Env) []engine.Step {
	var steps []engine.Step
	agent := e.AgentUser()
	if e.Cfg.Docker() {
		steps = append(steps,
			dockerConflictsStep(),
			aptKeyStep("docker-key", dockerBaseURL+"/gpg", dockerKeyring, e.M.Docker.GPGFingerprint),
			dockerSourceStep(e),
			dockerPackagesStep(),
			daemonJSONStep("docker-daemon-json", dockerDaemon, "root", func(ctx context.Context, e *engine.Env) error {
				// Only when the file changed: a restart stops running containers.
				if !system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", "docker.service")) {
					return nil
				}
				_, err := e.X.Run(ctx, system.C("systemctl", "restart", "docker.service"))
				return err
			}),
			dockerServiceStep(),
		)
		steps = append(steps, dockerGuardSteps(e.Cfg)...)
		steps = append(steps, rootlessSteps(e)...)
	}
	if e.Cfg.GH() {
		steps = append(steps,
			aptKeyStep("gh-key", "https://cli.github.com/packages/githubcli-archive-keyring.gpg", ghKeyring, e.M.GitHubCLI.GPGFingerprint),
			aptSourceStep("gh-repo", ghSources, fmt.Sprintf("# Managed by keelbox.\ndeb [arch=%s signed-by=%s] https://cli.github.com/packages stable main\n", e.Facts.Arch, ghKeyring)),
			engine.PackagesStep("gh-package", func(*engine.Env) []string { return []string{"gh"} }),
		)
	}
	steps = append(steps, gitIdentitySteps(e)...)
	steps = append(steps, shellPathStep(agent))
	steps = append(steps, environmentDStep(agent)...)
	if e.Cfg.Node() {
		steps = append(steps, miseBinaryStep(), miseConfigStep(e, agent), miseToolsStep(agent.Name))
	}
	return steps
}

// dockerConflictsStep removes packages Docker documents as conflicting,
// such as Ubuntu's docker.io. Removing them stops their containers, so on a
// box that already has them keelbox refuses unless the user passes --adopt.
func dockerConflictsStep() engine.Step {
	present := func(ctx context.Context, e *engine.Env) []string {
		have, _ := engine.InstalledPackages(ctx, e, DockerConflicts)
		var p []string
		for _, c := range DockerConflicts {
			if have[c] {
				p = append(p, c)
			}
		}
		return p
	}
	return engine.Step{
		Name: "docker-conflicts",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			if p := present(ctx, e); len(p) > 0 {
				return engine.Pending("remove %s (needs --adopt)", strings.Join(p, " ")), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			p := present(ctx, e)
			if !e.Adopt {
				return fmt.Errorf("this box already has %s, which conflicts with Docker Engine from Docker's repository; "+
					"keelbox would remove it, stopping its containers (images and volumes under /var/lib/docker are kept). "+
					"Re-run with --adopt to replace it, or set runtime.docker: false", strings.Join(p, " "))
			}
			// Stop the old daemon and its socket first. Removing the package
			// leaves docker.socket active with no listening socket, and
			// docker-ce's daemon then fails to start from it.
			_, _ = e.X.Run(ctx, system.C("systemctl", "stop", "docker.socket", "docker.service"))
			return engine.Apt(ctx, e, append([]string{"remove", "-y"}, p...)...)
		},
	}
}

// dockerServiceStep enables and starts the system daemon. Docker starts it
// through docker.socket; a socket unit left failed or without its listener
// (after a replaced docker.io, say) is reset and restarted first, or the
// daemon exits with "no sockets found via socket activation".
func dockerServiceStep() engine.Step {
	s := engine.ServiceStep("docker-service", "docker.service")
	s.Apply = func(ctx context.Context, e *engine.Env) error {
		_, _ = e.X.Run(ctx, system.C("systemctl", "reset-failed", "docker.socket", "docker.service"))
		_, _ = e.X.Run(ctx, system.C("systemctl", "restart", "docker.socket"))
		_, err := e.X.Run(ctx, system.C("systemctl", "enable", "--now", "docker.service"))
		return err
	}
	return s
}

func dockerSource(codename, arch string) string {
	return fmt.Sprintf("# Managed by keelbox.\nTypes: deb\nURIs: %s\nSuites: %s\nComponents: stable\nArchitectures: %s\nSigned-By: %s\n",
		dockerBaseURL, codename, arch, dockerKeyring)
}

// dockerSourceStep checks Docker publishes a suite for this release before
// writing the source, so a brand-new Ubuntu gives a clear error instead of
// an opaque apt 404.
func dockerSourceStep(e *engine.Env) engine.Step {
	s := aptSourceStep("docker-repo", dockerSources, dockerSource(e.Facts.Codename, e.Facts.Arch))
	apply := s.Apply
	s.Apply = func(ctx context.Context, e *engine.Env) error {
		url := fmt.Sprintf("%s/dists/%s/stable/binary-%s/Packages", dockerBaseURL, e.Facts.Codename, e.Facts.Arch)
		if !system.Succeeds(ctx, e.X, system.C("curl", "-fsSI", "--retry", "3", url)) {
			return fmt.Errorf("docker publishes no %q suite for %s yet (%s/dists/); use a supported Ubuntu release", e.Facts.Codename, e.Facts.Arch, dockerBaseURL)
		}
		return apply(ctx, e)
	}
	return s
}

var dockerPackages = []string{"docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"}

// dockerPackagesStep installs the newest Docker Engine in the manifest's
// major series. Engine and CLI are pinned to the same version.
func dockerPackagesStep() engine.Step {
	state := func(ctx context.Context, e *engine.Env) (missing []string, engineVer string) {
		have, _ := engine.InstalledPackages(ctx, e, dockerPackages)
		for _, p := range dockerPackages {
			if !have[p] {
				missing = append(missing, p)
			}
		}
		engineVer, _ = system.Output(ctx, e.X, system.C("dpkg-query", "-W", "-f=${Version}", "docker-ce"))
		return missing, engineVer
	}
	inSeries := func(e *engine.Env, v string) bool { return strings.HasPrefix(v, "5:"+e.M.Docker.Series+".") }
	return engine.Step{
		Name: "docker-packages",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			missing, v := state(ctx, e)
			if len(missing) > 0 {
				return engine.Pending("install %s (Docker %s series)", strings.Join(missing, " "), e.M.Docker.Series), nil
			}
			if !inSeries(e, v) {
				return engine.Pending("move docker-ce %s to the %s series", v, e.M.Docker.Series), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			if err := engine.AptUpdate(ctx, e); err != nil {
				return err
			}
			out, err := system.Output(ctx, e.X, system.C("apt-cache", "madison", "docker-ce"))
			if err != nil {
				return err
			}
			ver := ""
			for _, l := range strings.Split(out, "\n") {
				f := strings.Split(l, "|")
				if len(f) >= 2 {
					if v := strings.TrimSpace(f[1]); inSeries(e, v) {
						ver = v // madison lists newest first
						break
					}
				}
			}
			if ver == "" {
				return fmt.Errorf("no docker-ce %s.x package in the Docker repository for %s", e.M.Docker.Series, e.Facts.Codename)
			}
			return engine.Apt(ctx, e, "install", "-y", "--allow-downgrades",
				"docker-ce="+ver, "docker-ce-cli="+ver, "containerd.io", "docker-buildx-plugin", "docker-compose-plugin")
		},
	}
}

// mergeDaemonJSON overlays keelbox's keys on an existing daemon.json,
// keeping unrelated settings.
func mergeDaemonJSON(path string, existing []byte) ([]byte, bool, error) {
	cur := map[string]any{}
	if len(strings.TrimSpace(string(existing))) > 0 {
		if err := json.Unmarshal(existing, &cur); err != nil {
			return nil, false, fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
	}
	changed := false
	for k, v := range dockerLogOpts {
		if !reflect.DeepEqual(normalise(cur[k]), normalise(v)) {
			cur[k] = v
			changed = true
		}
	}
	out, err := json.MarshalIndent(cur, "", "  ")
	return append(out, '\n'), changed, err
}

func normalise(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// daemonJSONStep bounds container logs in a daemon.json, keeping other keys,
// and runs onChange only when the file actually changed. The file is read
// and written as owner.
func daemonJSONStep(name, path, owner string, onChange func(context.Context, *engine.Env) error) engine.Step {
	user := owner
	if user == "root" {
		user = ""
	}
	return engine.Step{
		Name: name,
		User: user,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			data, _ := e.FS(owner).ReadFile(path)
			_, changed, err := mergeDaemonJSON(path, data)
			if err != nil {
				return engine.Status{}, err
			}
			if changed {
				return engine.Pending("bound container logs in %s (json-file, 10m x 3)", path), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			data, _ := e.FS(owner).ReadFile(path)
			out, _, err := mergeDaemonJSON(path, data)
			if err != nil {
				return err
			}
			if err := e.FS(owner).WriteFile(path, out, 0o644, owner, owner); err != nil {
				return err
			}
			return onChange(ctx, e)
		},
	}
}

// ShellPath is the managed .bashrc block. It sits at the top of the file,
// before Ubuntu's "not interactive, return" guard, because bash reads
// .bashrc for non-interactive SSH commands too: `ssh box node -v` then finds
// mise's shims and the agent's rootless Docker. Per-user, unlike a
// system-wide /etc/environment PATH, so no user-writable directory lands on
// the admin's PATH.
const ShellPath = `# PATH for interactive and non-interactive shells (ssh box 'node -v').
case ":$PATH:" in
  *":$HOME/.local/share/mise/shims:"*) ;;
  *) export PATH="$HOME/.local/share/mise/shims:$HOME/.local/bin:$HOME/.local/share/pnpm:$PATH" ;;
esac
export PNPM_HOME="$HOME/.local/share/pnpm"
# This user's own rootless Docker daemon.
export DOCKER_HOST="unix:///run/user/$UID/docker.sock"`

func shellPathStep(u system.User) engine.Step {
	return engine.BlockStep("agent-shell-path", engine.Block{Path: u.Home + "/.bashrc", ID: "path", Content: ShellPath, Top: true, Mode: 0o644, Owner: u.Name})
}

// EnvironmentD is the agent's environment.d file, relative to its home. It
// sorts after Ubuntu's 99-environment.conf, which sets PATH from
// /etc/environment and would replace an earlier file's PATH, and before
// snapd's 990-snapd.conf, which appends /snap/bin to it.
const EnvironmentD = ".config/environment.d/99-keelbox.conf"

// oldEnvironmentD is where releases before 0.2 wrote it, where Ubuntu's
// 99-environment.conf overrode its PATH.
const oldEnvironmentD = ".config/environment.d/10-keelbox.conf"

// agentEnvPath is the PATH of the agent's user services and tmux: its own
// tools, then the system's.
func agentEnvPath(u system.User) string {
	return u.Home + "/.local/share/mise/shims:" + u.Home + "/.local/bin:" + u.Home + "/.local/share/pnpm:/usr/local/bin:/usr/bin:/bin"
}

// agentEnvironment is the environment keelbox gives the agent's user
// services and tmux, by name, for user u.
func agentEnvironment(u system.User, uid int) [][2]string {
	return [][2]string{
		{"PATH", agentEnvPath(u)},
		{"PNPM_HOME", u.Home + "/.local/share/pnpm"},
		{"DOCKER_HOST", fmt.Sprintf("unix:///run/user/%d/docker.sock", uid)},
	}
}

// environmentDStep gives systemd user services (the tmux server, rootless
// Docker, the Remote Control unit) the same PATH and DOCKER_HOST as the
// agent's shell. The agent's user manager reads environment.d when it
// starts and on a daemon-reload; agentEnvironmentLiveStep makes sure the
// running one has.
func environmentDStep(u system.User) []engine.Step {
	content := fmt.Sprintf("# Managed by keelbox.\nPATH=%s\nPNPM_HOME=%s/.local/share/pnpm\nDOCKER_HOST=unix://${XDG_RUNTIME_DIR}/docker.sock\n",
		agentEnvPath(u), u.Home)
	old := u.Home + "/" + oldEnvironmentD
	return []engine.Step{
		engine.FileStep("agent-environment-d", engine.File{Path: u.Home + "/" + EnvironmentD, Content: []byte(content), Mode: 0o644, Owner: u.Name}),
		{
			Name: "agent-environment-d-old",
			User: u.Name,
			Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
				if st, _ := e.FS(u.Name).Stat(old); st.Exists {
					return engine.Pending("remove %s (now %s)", old, EnvironmentD), nil
				}
				return engine.Converged(), nil
			},
			Apply: func(ctx context.Context, e *engine.Env) error { return e.FS(u.Name).Remove(old) },
		},
		agentEnvironmentLiveStep(u),
	}
}

// agentEnvironmentLiveStep gives the running agent's user manager and tmux
// server keelbox's environment. The user manager starts with lingering,
// before apply writes environment.d, and keeps the environment it started
// with: a daemon-reload re-reads it, for the services it starts next. A
// tmux server already running keeps its own: set-environment -g gives
// sessions and windows made from now on the new one, and ends none.
func agentEnvironmentLiveStep(u system.User) engine.Step {
	managerStale := func(ctx context.Context, e *engine.Env) bool {
		out, err := system.Output(ctx, e.X, userctl(u.Name, "show-environment"))
		return err == nil && !strings.HasPrefix(envValue(out, "PATH"), agentEnvPath(u))
	}
	tmuxStale := func(ctx context.Context, e *engine.Env) bool {
		out, err := system.Output(ctx, e.X, system.As(u.Name, "tmux", "show-environment", "-g", "PATH"))
		return err == nil && !strings.HasPrefix(envValue(out, "PATH"), agentEnvPath(u))
	}
	managerUp := func(ctx context.Context, e *engine.Env) (int, bool) {
		usr, err := e.X.LookupUser(u.Name)
		if err != nil {
			return 0, false
		}
		return usr.UID, system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", fmt.Sprintf("user@%d.service", usr.UID)))
	}
	return engine.Step{
		Name: "agent-environment-live",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			var stale []string
			if _, up := managerUp(ctx, e); up && managerStale(ctx, e) {
				stale = append(stale, "user manager")
			}
			if tmuxStale(ctx, e) {
				stale = append(stale, "running tmux server")
			}
			if len(stale) == 0 {
				return engine.Converged(), nil
			}
			return engine.Pending("give %s's %s keelbox's PATH", u.Name, strings.Join(stale, " and ")), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			uid, up := managerUp(ctx, e)
			if up && managerStale(ctx, e) {
				if _, err := e.X.Run(ctx, userctl(u.Name, "daemon-reload")); err != nil {
					return err
				}
			}
			if tmuxStale(ctx, e) {
				for _, kv := range agentEnvironment(u, uid) {
					if _, err := e.X.Run(ctx, system.As(u.Name, "tmux", "set-environment", "-g", kv[0], kv[1])); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
}

// envValue is NAME's value in NAME=value lines (systemctl show-environment,
// tmux show-environment).
func envValue(out, name string) string {
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), name+"="); ok {
			return v
		}
	}
	return ""
}

// miseBinaryStep installs the manifest's mise release, checksum-verified
// against the manifest, root-owned in /usr/local/bin so versions move only
// with keelbox's release manifest.
func miseBinaryStep() engine.Step {
	return engine.Step{
		Name: "mise",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			out, err := system.Output(ctx, e.X, system.C(miseBin, "--version"))
			if err != nil {
				return engine.Pending("install mise %s", e.M.Mise.Version), nil
			}
			if !strings.HasPrefix(out, e.M.Mise.Version+" ") && out != e.M.Mise.Version {
				return engine.Pending("move mise to %s (have %s)", e.M.Mise.Version, firstField(out)), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			want := e.M.Mise.SHA256[e.Facts.Arch]
			if want == "" {
				return fmt.Errorf("release %s has no mise checksum for %s", e.M.Release, e.Facts.Arch)
			}
			dir, cleanup, err := tempDir(ctx, e, "")
			if err != nil {
				return err
			}
			defer cleanup()
			tarball := dir + "/mise.tar.gz"
			if _, err := e.X.Run(ctx, curl(e.M.MiseURL(e.Facts.Arch), tarball)); err != nil {
				return err
			}
			sum, err := system.Output(ctx, e.X, system.C("sha256sum", tarball))
			if err != nil {
				return err
			}
			if got := firstField(sum); got != want {
				return fmt.Errorf("mise tarball checksum %s does not match the release manifest (%s); refusing to install", got, want)
			}
			for _, c := range []system.Cmd{
				system.C("tar", "-xzf", tarball, "-C", dir),
				system.C("install", "-m", "0755", "-o", "root", "-g", "root", dir+"/mise/bin/mise", miseBin),
			} {
				if _, err := e.X.Run(ctx, c); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}

func miseConfig(e *engine.Env) string {
	return fmt.Sprintf(`# Managed by keelbox from release %s. Pin per-project versions in each
# repository's mise.toml instead of editing this file.
[tools]
node = "%s"
pnpm = "%s"

[settings]
# Shims, not shell activation: they also work in non-interactive shells.
not_found_auto_install = false
`, e.M.Release, e.M.Node, e.M.Pnpm)
}

func miseConfigStep(e *engine.Env, u system.User) engine.Step {
	return engine.FileStep("mise-config", miseFile(e, u))
}

func miseFile(e *engine.Env, u system.User) engine.File {
	return engine.File{Path: u.Home + "/.config/mise/config.toml", Content: []byte(miseConfig(e)), Mode: 0o644, Owner: u.Name}
}

// miseToolsStep installs the pinned Node and pnpm for the agent user.
func miseToolsStep(user string) engine.Step {
	missing := func(ctx context.Context, e *engine.Env) []string {
		var m []string
		for _, t := range []string{"node@" + e.M.Node, "pnpm@" + e.M.Pnpm} {
			if !system.Succeeds(ctx, e.X, system.As(e.Cfg.Users.Agent.Name, miseBin, "where", t)) {
				m = append(m, t)
			}
		}
		return m
	}
	return engine.Step{
		Name: "mise-tools",
		User: user,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			if m := missing(ctx, e); len(m) > 0 {
				return engine.Pending("install %s for %s", strings.Join(m, " "), e.Cfg.Users.Agent.Name), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			_, err := e.X.Run(ctx, system.As(e.Cfg.Users.Agent.Name, miseBin, "install", "--yes"))
			if err != nil {
				return err
			}
			_, err = e.X.Run(ctx, system.As(e.Cfg.Users.Agent.Name, miseBin, "reshim"))
			return err
		},
	}
}

func (Runtime) Verify(ctx context.Context, e *engine.Env) []engine.Finding {
	var out []engine.Finding
	if e.Cfg.Docker() {
		out = append(out, verifyDocker(ctx, e)...)
	}
	if e.Cfg.GH() {
		if v, err := versionOf(ctx, e, "gh"); err != nil {
			out = append(out, fail("runtime", "gh", "gh not on the agent's PATH"))
		} else {
			out = append(out, ok("runtime", "gh", v))
		}
	}
	if e.Cfg.Node() {
		for _, t := range []struct{ bin, want string }{{"node", "v" + e.M.Node}, {"pnpm", e.M.Pnpm}} {
			v, err := versionOf(ctx, e, e.AgentTool(t.bin))
			switch {
			case err != nil:
				out = append(out, fail("runtime", t.bin, t.bin+" not on the agent's PATH"))
			case v != t.want:
				out = append(out, warn("runtime", t.bin, fmt.Sprintf("%s %s, release pins %s", t.bin, v, t.want)))
			default:
				out = append(out, ok("runtime", t.bin, t.bin+" "+v))
			}
		}
	}
	out = append(out, verifyGitIdentity(ctx, e))
	if f, found := verifyProjects(e.Cfg.Box, projects.Probe(ctx, e.X, e.Cfg)); found {
		out = append(out, f)
	}
	return out
}
