package modules

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// The agent runs its own rootless Docker daemon as a user service. Membership
// of the system daemon's docker group is root-equivalent, so the agent is
// never in it. Rootless containers run in a user namespace mapped onto the
// agent's subordinate IDs; their published ports are opened by the agent's
// rootlesskit process as ordinary sockets, so UFW filters them like any other
// listening port, and their outbound traffic is the agent's own (agentnet.go).

// RootlessPackages are what rootless Docker needs beyond the Engine.
var RootlessPackages = []string{"docker-ce-rootless-extras", "uidmap", "slirp4netns", "dbus-user-session"}

const rootlessUnit = "docker.service"

// DockerHost is the agent's rootless Docker socket, for DOCKER_HOST.
func DockerHost(uid int) string { return "unix:///run/user/" + strconv.Itoa(uid) + "/docker.sock" }

// agentDocker runs the docker CLI as the agent against its rootless daemon.
func agentDocker(e *engine.Env, args ...string) system.Cmd {
	c := system.As(e.Cfg.Users.Agent.Name, "docker", args...)
	c.Env = []string{"DOCKER_HOST=" + DockerHost(e.AgentUser().UID)}
	return c
}

// rootlessUnitText is the user unit dockerd-rootless-setuptool.sh would
// write, managed here so it is converged rather than generated once.
const rootlessUnitText = `# Managed by keelbox. The agent's own rootless Docker daemon.
[Unit]
Description=Docker Application Container Engine (rootless, keelbox)
Documentation=https://docs.docker.com/go/rootless/
Requires=dbus.socket

[Service]
Environment=PATH=/usr/bin:/sbin:/usr/sbin
ExecStart=/usr/bin/dockerd-rootless.sh
ExecReload=/bin/kill -s HUP $MAINPID
TimeoutSec=0
RestartSec=2
Restart=always
StartLimitBurst=3
StartLimitInterval=60s
LimitNOFILE=infinity
LimitNPROC=infinity
LimitCORE=infinity
TasksMax=infinity
Delegate=yes
Type=notify
NotifyAccess=all
KillMode=mixed

[Install]
WantedBy=default.target
`

func rootlessSteps(e *engine.Env) []engine.Step {
	u := e.AgentUser()
	return []engine.Step{
		engine.PackagesStep("docker-rootless-packages", func(*engine.Env) []string { return RootlessPackages }),
		groupMemberStep("agent-not-in-docker-group", u.Name, "docker", false),
		subIDStep(subUIDPath, u.Name),
		subIDStep("/etc/subgid", u.Name),
		engine.DirStep("agent-docker-config-dir", u.Home+"/.config/docker", 0o700, u.Name, u.Name),
		daemonJSONStep("agent-docker-daemon-json", u.Home+"/.config/docker/daemon.json", u.Name, func(ctx context.Context, e *engine.Env) error {
			return restartUserUnit(ctx, e, u, rootlessUnit, false)
		}),
		userUnitFile("agent-docker-unit", u, rootlessUnit, rootlessUnitText),
		userUnitStep("agent-docker-enabled", u, rootlessUnit),
	}
}

// subIDRange is the size of the subordinate ID range rootless Docker maps
// container users onto; 65536 covers every UID a container image uses.
const subIDRange = 65536

// subIDStep gives the user a subordinate ID range. useradd on Ubuntu
// allocates one for new accounts; an adopted account may have none. A new
// range starts after the highest one in the file, so ranges never overlap.
func subIDStep(path, user string) engine.Step {
	parse := func(e *engine.Env) (has bool, next int64, data []byte) {
		data, _ = e.X.ReadFile(path)
		next = 100000
		for _, l := range strings.Split(string(data), "\n") {
			f := strings.Split(strings.TrimSpace(l), ":")
			if len(f) != 3 {
				continue
			}
			if f[0] == user {
				has = true
			}
			start, err1 := strconv.ParseInt(f[1], 10, 64)
			n, err2 := strconv.ParseInt(f[2], 10, 64)
			if err1 == nil && err2 == nil && start+n > next {
				next = start + n
			}
		}
		return has, next, data
	}
	return engine.Step{
		Name: "agent-" + strings.TrimPrefix(path, "/etc/"),
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			if has, _, _ := parse(e); has {
				return engine.Converged(), nil
			}
			return engine.Pending("give %s a subordinate ID range in %s", user, path), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			_, next, data := parse(e)
			if len(data) > 0 && data[len(data)-1] != '\n' {
				data = append(data, '\n')
			}
			data = append(data, fmt.Sprintf("%s:%d:%d\n", user, next, subIDRange)...)
			if err := e.X.WriteFile(path, data, 0o644, "root", "root"); err != nil {
				return err
			}
			if path != subUIDPath {
				return nil
			}
			// The agent's network limits match its subordinate uids, and
			// their step ran before this range existed (agentuids.go).
			return refreshAgentNet(ctx, e)
		},
	}
}

// Published ports of the system (rootful) daemon are DNATed in the kernel
// before UFW's input rules see them, so a container started by root with
// -p 5432:5432 would be reachable from the internet. The guard is a chain of
// keelbox's own, jumped to first from Docker's DOCKER-USER chain, that drops
// new forwarded connections unless they arrive on the tailnet or a Docker
// bridge. Rules the user adds to DOCKER-USER are left alone.
const (
	dockerGuardScript = "/usr/local/lib/keelbox/docker-user-guard.sh"
	dockerGuardUnit   = "/etc/systemd/system/keelbox-docker-guard.service"
	dockerGuardName   = "keelbox-docker-guard.service"
)

// dockerGuardScriptText is the guard for a config: host.firewall.allow
// adds an exception per allowed port and network before the drop, matched
// on the published port (conntrack's original destination), since the
// rule sees the connection after Docker's NAT.
func dockerGuardScriptText(c *config.Config) string {
	v4, v6 := dockerGuardAllow(c.AllowRules())
	allow := ""
	if len(v4)+len(v6) > 0 {
		allow = "\t# host.firewall.allow\n\tif [ \"$ipt\" = iptables ]; then\n"
		for _, l := range v4 {
			allow += "\t\t" + l + "\n"
		}
		if len(v4) == 0 {
			allow += "\t\t:\n"
		}
		allow += "\telse\n"
		for _, l := range v6 {
			allow += "\t\t" + l + "\n"
		}
		if len(v6) == 0 {
			allow += "\t\t:\n"
		}
		allow += "\tfi\n"
	}
	return `#!/bin/sh
# Managed by keelbox. Drops new connections forwarded to system Docker
# containers unless they arrive on the tailnet or a Docker bridge, or are
# to a port host.firewall.allow keeps open.
set -eu
for ipt in iptables ip6tables; do
	$ipt -n -L DOCKER-USER >/dev/null 2>&1 || continue
	$ipt -N KEELBOX-DOCKER 2>/dev/null || $ipt -F KEELBOX-DOCKER
	$ipt -A KEELBOX-DOCKER -m conntrack --ctstate RELATED,ESTABLISHED -j RETURN
	$ipt -A KEELBOX-DOCKER -i tailscale0 -j RETURN
	$ipt -A KEELBOX-DOCKER -i docker0 -j RETURN
	$ipt -A KEELBOX-DOCKER -i br-+ -j RETURN
` + allow + `	$ipt -A KEELBOX-DOCKER -m conntrack --ctstate NEW -j DROP
	$ipt -C DOCKER-USER -j KEELBOX-DOCKER 2>/dev/null || $ipt -I DOCKER-USER 1 -j KEELBOX-DOCKER
done
`
}

// PartOf and WantedBy tie the guard to docker.service: Docker recreates its
// chains on start, and the guard runs again right after.
const dockerGuardUnitText = `# Managed by keelbox.
[Unit]
Description=keelbox: drop inbound connections to system Docker containers from public interfaces
After=docker.service
PartOf=docker.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=` + dockerGuardScript + `

[Install]
WantedBy=docker.service
`

func dockerGuardSteps(c *config.Config) []engine.Step {
	rerun := func(ctx context.Context, e *engine.Env) error {
		if !system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", dockerGuardName)) {
			return nil // the service step starts it
		}
		_, err := e.X.Run(ctx, system.C("systemctl", "restart", dockerGuardName))
		return err
	}
	return []engine.Step{
		engine.FileStep("docker-guard-script", engine.File{Path: dockerGuardScript, Content: []byte(dockerGuardScriptText(c)), Mode: 0o755, OnChange: rerun}),
		engine.FileStep("docker-guard-unit", engine.File{Path: dockerGuardUnit, Content: []byte(dockerGuardUnitText), Mode: 0o644,
			OnChange: func(ctx context.Context, e *engine.Env) error {
				_, err := e.X.Run(ctx, system.C("systemctl", "daemon-reload"))
				return err
			}}),
		engine.ServiceStep("docker-guard-service", dockerGuardName),
	}
}

func verifyDocker(ctx context.Context, e *engine.Env) []engine.Finding {
	var out []engine.Finding
	agent := e.Cfg.Users.Agent.Name
	v, err := system.Output(ctx, e.X, agentDocker(e, "version", "--format", "{{.Server.Version}}"))
	switch {
	case err != nil:
		out = append(out, fail("runtime", "docker-rootless", agent+"'s rootless Docker is not answering: "+err.Error()))
	case !strings.HasPrefix(v, e.M.Docker.Series+"."):
		out = append(out, warn("runtime", "docker-rootless", fmt.Sprintf("rootless Docker %s is outside the release's %s series", v, e.M.Docker.Series)))
	default:
		out = append(out, ok("runtime", "docker-rootless", "rootless Docker "+v+" for "+agent))
	}
	sys := system.As(agent, "docker", "-H", "unix:///var/run/docker.sock", "version")
	if system.Succeeds(ctx, e.X, sys) {
		out = append(out, fail("runtime", "docker-system-socket", agent+" can use the system Docker daemon, which is root-equivalent"))
	} else {
		out = append(out, ok("runtime", "docker-system-socket", agent+" cannot use the system Docker daemon"))
	}
	guard, err := system.Output(ctx, e.X, system.C("iptables", "-n", "-L", "DOCKER-USER"))
	if err != nil || !strings.Contains(guard, "KEELBOX-DOCKER") {
		out = append(out, fail("runtime", "docker-inbound-guard", "DOCKER-USER does not jump to KEELBOX-DOCKER; system containers' published ports may be public"))
	} else {
		out = append(out, ok("runtime", "docker-inbound-guard", "system containers' published ports reachable only on the tailnet"))
	}
	for _, plugin := range []string{"compose", "buildx"} {
		if pv, err := system.Output(ctx, e.X, agentDocker(e, plugin, "version")); err != nil {
			out = append(out, fail("runtime", "docker-"+plugin, "docker "+plugin+" unavailable"))
		} else {
			out = append(out, ok("runtime", "docker-"+plugin, firstLine(pv)))
		}
	}
	return out
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}
