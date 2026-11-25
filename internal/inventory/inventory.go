// Package inventory looks at a machine before keelbox changes it: what
// listens on the network, the firewall, the system Docker daemon and its
// containers, the accounts, sshd's effective settings, and the tmux
// servers and agent CLIs already running. It only reads. The impact report
// (keelbox plan --impact) and the first-apply gate join it with the plan
// to say what apply would change that the owner may care about.
package inventory

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/keelbox/keelbox/internal/services"
	"github.com/keelbox/keelbox/internal/system"
)

// DockerSocket is the system Docker daemon's socket. The inventory asks
// that daemon by name, never the agent's rootless one.
const DockerSocket = "unix:///var/run/docker.sock"

// Inventory is what Collect found. Every part is best effort: a command
// that fails (not installed, not running) leaves its part empty.
type Inventory struct {
	Hostname  string     `json:"hostname"`
	Virt      string     `json:"virt,omitempty"` // systemd-detect-virt: kvm, lxc, ...; "" on hardware
	Listeners []Listener `json:"listeners,omitempty"`
	UFW       UFW        `json:"ufw"`
	Docker    Docker     `json:"docker"`
	Accounts  []Account  `json:"accounts,omitempty"`
	SSHD      SSHD       `json:"sshd"`
	Running   []Running  `json:"running,omitempty"`
	// LinkLocal is the sockets connected to a link-local address now.
	LinkLocal  []LinkLocalPeer `json:"link_local,omitempty"`
	containers map[string]string
}

// UFW is the firewall's state now.
type UFW struct {
	Active    bool   `json:"active"`
	DefaultIn string `json:"default_in,omitempty"` // deny, reject or allow
	Rules     int    `json:"rules"`                // rules UFW enforces now
	// Admits is the ports the user's own rules allow in ("8081/tcp",
	// "8000:8010/tcp" for a range), from
	// ufw show added, which lists them whether UFW is on or not. Rules by
	// application name (OpenSSH, Samba) are not resolved.
	Admits []string `json:"admits,omitempty"`
}

// Admit reports whether one of the user's rules allows port/proto in.
func (u UFW) Admit(port int, proto string) bool {
	for _, k := range u.Admits {
		ports, pr, _ := strings.Cut(k, "/")
		if pr != proto {
			continue
		}
		first, last, isRange := strings.Cut(ports, ":")
		if !isRange {
			last = first
		}
		if lo, hi := atoi(first), atoi(last); port >= lo && port <= hi {
			return true
		}
	}
	return false
}

// Blocks reports whether UFW drops inbound connections today unless a rule
// admits them. A listener is then already unreachable from the network
// unless an existing rule admits it, and keelbox keeps existing rules.
func (u UFW) Blocks() bool { return u.Active && (u.DefaultIn == "deny" || u.DefaultIn == "reject") }

// Docker is the system (rootful) Docker daemon.
type Docker struct {
	Running    bool        `json:"running"`
	Containers []Container `json:"containers,omitempty"`
}

// Container is one of the system daemon's containers.
type Container struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	// Restart is the restart policy: no, always, unless-stopped or
	// on-failure. A running container with "no" stays stopped when the
	// daemon restarts.
	Restart string      `json:"restart"`
	Ports   []Published `json:"ports,omitempty"`
	Image   string      `json:"image,omitempty"`
	ID      string      `json:"id,omitempty"` // docker ps's short ID
	// Service is the known service its image is (internal/services), or "".
	Service string `json:"service,omitempty"`
}

// ComesBack reports whether the container runs again after the daemon
// restarts.
func (c Container) ComesBack() bool {
	return c.Restart == "always" || c.Restart == "unless-stopped" || c.Restart == "on-failure"
}

// Published is a container port published on the host.
type Published struct {
	HostIP string `json:"host_ip"`
	Port   int    `json:"port"`
	Proto  string `json:"proto"`
}

// Account is a login account: root, or uid 1000 and up with a shell.
type Account struct {
	Name     string   `json:"name"`
	UID      int      `json:"uid"`
	Password bool     `json:"password"` // a usable password hash in /etc/shadow
	Groups   []string `json:"groups,omitempty"`
	Home     string   `json:"home,omitempty"`
	// Keys is how many public keys its authorized keys files hold, from
	// the files sshd reads (AuthorizedKeysFile), read as the account.
	Keys int `json:"keys,omitempty"`
}

// In reports whether the account is in group g.
func (a Account) In(g string) bool {
	for _, x := range a.Groups {
		if x == g {
			return true
		}
	}
	return false
}

// SSHD is sshd's effective configuration (sshd -T).
type SSHD struct {
	PasswordAuth    bool   `json:"password_auth"`
	PermitRootLogin string `json:"permit_root_login,omitempty"`
	// PubkeyAuth is pubkeyauthentication, yes unless sshd says no.
	PubkeyAuth bool `json:"pubkey_auth"`
	// AuthorizedKeysFiles is authorizedkeysfile: paths with %h, %u and %%,
	// relative to the home directory unless absolute.
	AuthorizedKeysFiles []string `json:"authorized_keys_files,omitempty"`
}

// KeyFiles are the account's authorized keys files, as sshd reads them.
func (d SSHD) KeyFiles(a Account) []string {
	files := d.AuthorizedKeysFiles
	if len(files) == 0 {
		files = []string{".ssh/authorized_keys", ".ssh/authorized_keys2"}
	}
	var out []string
	for _, f := range files {
		if f == "none" {
			continue
		}
		f = strings.NewReplacer("%%", "%", "%h", a.Home, "%u", a.Name).Replace(f)
		if !strings.HasPrefix(f, "/") {
			if a.Home == "" {
				continue
			}
			f = strings.TrimSuffix(a.Home, "/") + "/" + f
		}
		out = append(out, f)
	}
	return out
}

// CountKeys counts the public key lines in an authorized_keys file.
func CountKeys(data string) int {
	n := 0
	for _, l := range strings.Split(data, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") && (strings.Contains(l, "ssh-") || strings.Contains(l, "ecdsa-sha2-") || strings.Contains(l, "sk-")) {
			n++
		}
	}
	return n
}

// Running is a program keelbox cares about, running as a user now.
type Running struct {
	User    string `json:"user"`
	Program string `json:"program"` // tmux, claude or codex
	Count   int    `json:"count"`
}

// Account returns the named account, if it exists.
func (inv *Inventory) Account(name string) (Account, bool) {
	for _, a := range inv.Accounts {
		if a.Name == name {
			return a, true
		}
	}
	return Account{}, false
}

// Collect reads the machine. It runs as root and changes nothing.
func Collect(ctx context.Context, x system.Executor) *Inventory {
	inv := &Inventory{}
	out := func(name string, args ...string) string {
		s, err := system.Output(ctx, x, system.C(name, args...))
		if err != nil {
			return ""
		}
		return s
	}
	if data, err := x.ReadFile("/etc/hostname"); err == nil {
		inv.Hostname = strings.TrimSpace(string(data))
	}
	// systemd-detect-virt prints "none" and exits 1 on hardware.
	if v := strings.TrimSpace(out("systemd-detect-virt")); v != "none" {
		inv.Virt = v
	}
	inv.UFW = ParseUFW(out("ufw", "status", "verbose"))
	inv.UFW.Admits = ParseUFWAdded(out("ufw", "show", "added"))
	if ps := out("docker", "-H", DockerSocket, "ps", "-a", "--format", "{{.Names}}\t{{.State}}\t{{.Ports}}\t{{.Image}}\t{{.ID}}"); ps != "" || system.Succeeds(ctx, x, system.C("docker", "-H", DockerSocket, "version", "--format", "{{.Server.Version}}")) {
		inv.Docker.Running = true
		var names []string
		for _, l := range strings.Split(ps, "\n") {
			if n, _, ok := strings.Cut(l, "\t"); ok {
				names = append(names, n)
			}
		}
		var policies string
		if len(names) > 0 {
			policies = out("docker", append([]string{"-H", DockerSocket, "inspect", "--format", "{{.Name}} {{.HostConfig.RestartPolicy.Name}}"}, names...)...)
		}
		inv.Docker.Containers = ParseContainers(ps, policies)
	}
	inv.containers = map[string]string{}
	for _, c := range inv.Docker.Containers {
		for _, p := range c.Ports {
			inv.containers[portKey(p.Port, p.Proto)] = c.Name
		}
	}
	inv.Listeners = ParseListeners(strings.Split(out("ss", "-Hltunp"), "\n"), inv.containers)
	inv.recognise(x)
	shadow, _ := x.ReadFile("/etc/shadow")
	passwd := out("getent", "passwd")
	inv.Accounts = ParseAccounts(passwd, string(shadow), out("getent", "group"))
	inv.LinkLocal = ParseLinkLocalPeers(strings.Split(out("ss", "-Htunpe"), "\n"), passwd)
	sshd, _ := system.SSHDConfig(ctx, x)
	inv.SSHD = ParseSSHD(sshd)
	// Whether a password login is all an account has: its keys, read as the
	// account itself, since a home is the account's to change (ADR 0015).
	for i, a := range inv.Accounts {
		if a.UID == 0 || !a.Password {
			continue
		}
		for _, f := range inv.SSHD.KeyFiles(a) {
			if data, err := x.UserFS(a.Name).ReadFile(f); err == nil {
				inv.Accounts[i].Keys += CountKeys(string(data))
			}
		}
	}
	inv.Running = ParseRunning(out("ps", "-eo", "user:32=,comm="))
	return inv
}

// ParseUFW reads ufw status verbose.
func ParseUFW(s string) UFW {
	var u UFW
	rules := false
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case l == "Status: active":
			u.Active = true
		case strings.HasPrefix(l, "Default:"):
			for _, p := range strings.Split(strings.TrimPrefix(l, "Default:"), ",") {
				if f := strings.Fields(p); len(f) == 2 && f[1] == "(incoming)" {
					u.DefaultIn = f[0]
				}
			}
		case strings.HasPrefix(l, "--"):
			rules = true
		case rules && l != "":
			u.Rules++
		}
	}
	return u
}

var addedPortRe = regexp.MustCompile(`^ufw allow (?:in )?(?:from \S+ to any port )?(\d+(?::\d+)?)(?:/(tcp|udp)| proto (tcp|udp))?(?: comment .*)?$`)

// ParseUFWAdded reads ufw show added for the ports allow rules admit.
func ParseUFWAdded(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range strings.Split(s, "\n") {
		m := addedPortRe.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil {
			continue
		}
		protos := []string{firstNonEmpty(m[2], m[3])}
		if protos[0] == "" {
			protos = []string{"tcp", "udp"}
		}
		for _, p := range protos {
			if k := m[1] + "/" + p; !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	return out
}

// ParseContainers reads docker ps lines (name, state, ports, image, ID,
// tab-separated; image and ID may be missing) and docker inspect lines
// ("/name policy").
func ParseContainers(ps, policies string) []Container {
	restart := map[string]string{}
	for _, l := range strings.Split(policies, "\n") {
		if n, p, ok := strings.Cut(strings.TrimSpace(l), " "); ok {
			restart[strings.TrimPrefix(n, "/")] = p
		}
	}
	var out []Container
	for _, l := range strings.Split(ps, "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 2 || f[0] == "" {
			continue
		}
		c := Container{Name: f[0], Running: f[1] == "running", Restart: restart[f[0]]}
		if c.Restart == "" {
			c.Restart = "no"
		}
		if len(f) > 2 {
			c.Ports = parsePorts(f[2])
		}
		if len(f) > 4 {
			c.Image, c.ID = f[3], f[4]
		}
		if s, ok := services.Match("", "", c.Image); ok {
			c.Service = s.Name
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// parsePorts reads docker ps's ports column: 0.0.0.0:8080->80/tcp,
// [::]:8080->80/tcp, 127.0.0.1:5432->5432/tcp, 80/tcp (exposed, not
// published).
func parsePorts(s string) []Published {
	var out []Published
	seen := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		hostPart, ctr, ok := strings.Cut(p, "->")
		if !ok {
			continue
		}
		_, proto, _ := strings.Cut(ctr, "/")
		host, port, ok := splitHostPort(hostPart)
		if !ok {
			continue
		}
		n := atoi(port)
		if n == 0 {
			continue
		}
		pub := Published{HostIP: host, Port: n, Proto: firstNonEmpty(proto, "tcp")}
		if k := pub.HostIP + " " + portKey(n, pub.Proto); !seen[k] {
			seen[k] = true
			out = append(out, pub)
		}
	}
	return out
}

// ParseAccounts reads getent passwd, /etc/shadow and getent group: root
// and the accounts people log in as (uid 1000 to 59999 with a shell).
func ParseAccounts(passwd, shadow, group string) []Account {
	pw := map[string]bool{}
	for _, l := range strings.Split(shadow, "\n") {
		f := strings.Split(l, ":")
		if len(f) >= 2 {
			h := f[1]
			pw[f[0]] = h != "" && !strings.HasPrefix(h, "!") && !strings.HasPrefix(h, "*")
		}
	}
	groups := map[string][]string{}
	for _, l := range strings.Split(group, "\n") {
		f := strings.Split(l, ":")
		if len(f) < 4 || f[3] == "" {
			continue
		}
		for _, m := range strings.Split(f[3], ",") {
			groups[m] = append(groups[m], f[0])
		}
	}
	var out []Account
	for _, l := range strings.Split(passwd, "\n") {
		f := strings.Split(l, ":")
		if len(f) < 7 {
			continue
		}
		uid := atoi(f[2])
		shell := f[6]
		login := !strings.HasSuffix(shell, "nologin") && !strings.HasSuffix(shell, "/false") && shell != ""
		person := uid >= 1000 && uid < 60000 && login
		if f[2] != "0" && !person {
			continue
		}
		g := groups[f[0]]
		sort.Strings(g)
		out = append(out, Account{Name: f[0], UID: uid, Password: pw[f[0]], Groups: g, Home: f[5]})
	}
	return out
}

// ParseSSHD reads sshd -T.
func ParseSSHD(s string) SSHD {
	d := SSHD{PubkeyAuth: true}
	for _, l := range strings.Split(s, "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(l), " ")
		switch k {
		case "passwordauthentication":
			d.PasswordAuth = v == "yes"
		case "permitrootlogin":
			d.PermitRootLogin = v
		case "pubkeyauthentication":
			d.PubkeyAuth = v != "no"
		case "authorizedkeysfile":
			d.AuthorizedKeysFiles = strings.Fields(v)
		}
	}
	return d
}

// ParseRunning reads ps -eo user,comm for tmux servers and agent CLIs.
func ParseRunning(s string) []Running {
	count := map[[2]string]int{}
	for _, l := range strings.Split(s, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		prog := ""
		switch comm := strings.Join(f[1:], " "); comm {
		case "tmux: server":
			prog = "tmux"
		case "claude", "codex":
			prog = comm
		}
		if prog != "" {
			count[[2]string{f[0], prog}]++
		}
	}
	var out []Running
	for k, n := range count {
		out = append(out, Running{User: k[0], Program: k[1], Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].User != out[j].User {
			return out[i].User < out[j].User
		}
		return out[i].Program < out[j].Program
	})
	return out
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
		if n > 1<<20 {
			return 0
		}
	}
	return n
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// recognise names the known services among the listeners: by process name,
// by the systemd unit the process runs in, or by the image of its
// container, published or on the host network. The unit and container come
// from /proc/PID/cgroup and the owner from /proc/PID/status, which root can
// read. Any account can name a process jellyfin or a user unit
// jellyfin.service, so only a process root or a system account runs, a
// unit of the system manager's and a container of the system Docker's are
// taken for a service; another account's process is shown with its UID.
func (inv *Inventory) recognise(x system.Executor) {
	byID := map[string]Container{}
	byName := map[string]Container{}
	for _, c := range inv.Docker.Containers {
		if c.ID != "" {
			byID[c.ID] = c
		}
		byName[c.Name] = c
	}
	for i := range inv.Listeners {
		l := &inv.Listeners[i]
		if c, ok := byName[l.Container]; ok && l.Container != "" {
			l.Image = c.Image
		} else if l.PID > 0 {
			l.UID = processUID(x, l.PID)
			data, err := x.ReadFile(fmt.Sprintf("/proc/%d/cgroup", l.PID))
			if err == nil {
				unit, id := CgroupUnit(string(data))
				l.Unit = unit
				for short, c := range byID {
					if id != "" && strings.HasPrefix(id, short) {
						l.Container, l.HostNetwork, l.Image = c.Name, true, c.Image
						l.Unit = ""
					}
				}
			}
		}
		proc := l.Process
		if l.PID > 0 && (l.UID < 0 || l.UID >= 1000) {
			proc = ""
		}
		l.Service = ""
		if s, ok := services.Match(proc, l.Unit, l.Image); ok {
			l.Service = s.Name
		}
		l.Label, l.Name = describeService(*l)
	}
}

// processUID reads the real UID of a process from /proc/PID/status, or -1.
func processUID(x system.Executor, pid int) int {
	data, err := x.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return -1
	}
	for _, l := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(l, "Uid:"); ok {
			if f := strings.Fields(rest); len(f) > 0 {
				if uid, err := strconv.Atoi(f[0]); err == nil {
					return uid
				}
			}
		}
	}
	return -1
}

var dockerScopeRe = regexp.MustCompile(`^docker-([0-9a-f]{12,64})\.scope$`)

// CgroupUnit reads /proc/PID/cgroup (cgroup v2: "0::/system.slice/x.service")
// for the system service the process runs in, or the system Docker
// container ID when it runs in one (docker-ID.scope). Only system.slice
// counts: under user.slice an account's own manager names its units, and an
// account can call one jellyfin.service or docker-ID.scope.
func CgroupUnit(s string) (unit, container string) {
	for _, l := range strings.Split(s, "\n") {
		path, ok := strings.CutPrefix(strings.TrimSpace(l), "0::")
		if !ok || !strings.HasPrefix(path, "/system.slice/") {
			continue
		}
		parts := strings.Split(path, "/")
		for i := len(parts) - 1; i >= 0; i-- {
			p := parts[i]
			if m := dockerScopeRe.FindStringSubmatch(p); m != nil {
				return "", m[1]
			}
			if strings.HasSuffix(p, ".service") {
				return p, ""
			}
		}
	}
	return "", ""
}
