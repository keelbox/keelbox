package modules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/backup"
	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/manifest"
	"github.com/keelbox/keelbox/internal/previews"
	"github.com/keelbox/keelbox/internal/system"
)

// sim is a stateful fake Ubuntu box: enough of apt, systemd, users, ufw,
// sshd, mise, git and the installers for every module to converge. It lets
// tests prove apply is idempotent without root or a VM.
type sim struct {
	*system.Fake
	m         *manifest.Manifest
	pkgs      map[string]string // package -> version
	groups    map[string][]string
	units     map[string]bool // enabled+active
	ufw       map[string]bool
	ufwAdded  []string // keelbox allow rules, as ufw show added lists them
	tz        string
	swap      []string
	mise      map[string]bool
	gitHead   map[string]string
	gitConfig map[string]string // the account's git config, by account and key
	tmpN      int
	tailscale string
	// restic, when set, answers restic commands (backup tests).
	restic func(c system.Cmd) (system.Result, error)
	// listening is what ss reports.
	listening string
	// connected is what ss reports of connected sockets, with their owners.
	connected string
	// userEnv is the agent's user manager's environment: environment.d as
	// it was at the last daemon-reload (lingering started it before).
	userEnv string
	// managerXDG is whether the agent's running user manager started with
	// keelbox's XDG_CONFIG_DIRS, so reads AgentUserConfig's drop-ins.
	managerXDG bool
	// sliceHigh is MemoryHigh of the agent's user manager's slices.
	sliceHigh map[string]string
	// tmuxEnv is the agent's running tmux server's global environment;
	// nil while no server runs.
	tmuxEnv map[string]string
	// tsIface, when set, replaces the addresses on tailscale0 (by default
	// the Tailscale addresses while the tailnet runs).
	tsIface []string
	// localAddrs are the box's own addresses besides loopback and the
	// tailnet's: what ip route get calls local.
	localAddrs []string
	// nftLoaded is the agent table as the kernel has it: the rules file as
	// it was when keelbox-agent-net.service last started.
	nftLoaded string
	// noBwrapInPackage is an apparmor-profiles package from before Ubuntu
	// added the bwrap profile to it.
	noBwrapInPackage bool
	// installerFails makes the vendors' installers fail, as when their
	// host cannot be reached.
	installerFails bool
	// claudeSignedIn is what `claude auth status` reports for the agent.
	claudeSignedIn bool
	// rcUnanswered is Remote Control's first run with its questions not
	// answered yes: the server exits at once.
	rcUnanswered bool
	// rcFlaps is the same first run as systemd shows it on a real box: the
	// server reads as running for a moment after each start, then has
	// exited and waits to be started again. rcShows counts the readings.
	rcFlaps bool
	rcShows int
	// codexSignedIn is what `codex login status` reports.
	codexSignedIn bool
	// noPackage are names apt has no package for. conflicts maps a
	// package to the installed ones apt would remove to install it.
	noPackage map[string]bool
	conflicts map[string][]string
	// lxc makes the box an LXC container; nestingOff and noTun are its
	// host's settings: no new user namespace may mount /proc, and
	// /dev/net/tun does not open.
	lxc, nestingOff, noTun bool
	// guestName is what LXD's or Incus's guest API reports as the
	// container's local-hostname; "" when it does not answer.
	guestName string
}

// simSelf is the running keelbox, as the ssh provider stages it.
const simSelf = "/var/lib/keelbox/stage/keelbox"

const adminKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAdmin admin@laptop"

func newSim(t *testing.T) *sim {
	t.Helper()
	m, err := manifest.ForChannel("stable")
	if err != nil {
		t.Fatal(err)
	}
	s := &sim{Fake: system.NewFake(), m: m, pkgs: map[string]string{"openssh-server": "1", "locales": "1"},
		groups: map[string][]string{"sudo": nil}, units: map[string]bool{"ssh.socket": true},
		ufw: map[string]bool{}, tz: "Etc/UTC", mise: map[string]bool{}, gitHead: map[string]string{}, gitConfig: map[string]string{}, tailscale: "NeedsLogin"}
	s.AddFile("/etc/os-release", "ID=ubuntu\nVERSION_ID=\"26.04\"\nVERSION_CODENAME=resolute\n", 0o644)
	s.AddFile("/etc/hostname", "ubuntu\n", 0o644)
	s.AddFile("/etc/hosts", "127.0.0.1 localhost\n", 0o644)
	s.AddFile("/etc/fstab", "LABEL=root / ext4 defaults 0 1\n", 0o644)
	s.AddFile("/usr/share/zoneinfo/UTC", "", 0o644)
	s.AddFile("/usr/share/zoneinfo/Europe/London", "", 0o644)
	s.AddFile(simSelf, "keelbox binary", 0o755)
	// The cloud image's default user, as cloud-init leaves it.
	s.AddUser("ubuntu", 1000)
	// Ubuntu's own service accounts that the agent network table knows.
	// Nothing has installed chrony, so there is no _chrony.
	for name, uid := range map[string]int{"systemd-network": 998, "systemd-timesync": 997, "systemd-resolve": 992} {
		s.Users[name] = system.User{Name: name, UID: uid, GID: uid, Home: "/run/systemd"}
	}
	s.AddFile(cloudInitSudoers, "# Created by cloud-init\n\n# User rules for ubuntu\nubuntu ALL=(ALL) NOPASSWD:ALL\n", 0o440)
	s.AddFile("/etc/shadow", "root:*:20000:0:99999:7:::\nubuntu:!:20000:0:99999:7:::\n", 0o640)
	s.OnFunc("", s.run)
	return s
}

// vendorCommand answers an agent CLI the vendor's installer put in the
// agent's home: --version, Codex's sandbox self-test and the sign-in
// checks, which exit as the sim says.
func (s *sim) vendorCommand(c system.Cmd) (system.Result, error) {
	if !s.has(c.Name) {
		return failed(c)
	}
	cli := path.Base(c.Name)
	switch strings.Join(c.Args, " ") {
	case "--version":
		if cli == "codex" {
			return res("codex-cli 1.0.0\n")
		}
		return res("1.0.0 (Claude Code)\n")
	case "sandbox -- /usr/bin/true":
		if cli == "codex" && s.bwrapWorks() {
			return res("")
		}
		return failed(c)
	case "auth status", "login status":
		if (cli == "codex" && !s.codexSignedIn) || (cli == "claude" && !s.claudeSignedIn) {
			return failed(c)
		}
		return res("")
	}
	return res("")
}

// What Ubuntu 24.04's apparmor-profiles package holds, as far as the sim
// goes: its version and the profile for bwrap.
const (
	simAppArmorProfiles   = "4.0.1really4.0.1-0ubuntu0.24.04.9"
	simUbuntuBwrapProfile = "abi <abi/4.0>,\n\nprofile bwrap /usr/bin/bwrap flags=(attach_disconnected) {\n  allow userns,\n}\n\nprofile unpriv_bwrap flags=(attach_disconnected) {\n  audit deny capability,\n}\n"
)

// Tests do not wait to see Remote Control stay up; the ones about that set
// their own times.
func init() { rcSteady = 0 }

// bwrapWorks is whether bwrap can set up a network namespace for an
// unprivileged account: with AppArmor's user namespace restriction on, only
// under a profile for /usr/bin/bwrap (Ubuntu's in /etc/apparmor.d, or the
// one keelbox loads from Ubuntu's package).
func (s *sim) bwrapWorks() bool {
	if strings.TrimSpace(s.content(userNSRestriction)) != "1" {
		return true
	}
	return s.has(ubuntuBwrapPath) || s.has(bwrapProfilePath)
}

// move renames a file or a directory tree, as mv -T does.
func (s *sim) move(src, dst string) bool {
	moved := false
	for k, f := range s.Files {
		if k == src || strings.HasPrefix(k, src+"/") {
			delete(s.Files, k)
			s.Files[dst+strings.TrimPrefix(k, src)] = f
			moved = true
		}
	}
	return moved
}

// has and content look at the fake's files directly, as the box's own
// programs would, without the access checks keelbox's file calls go through.
func (s *sim) has(p string) bool { _, ok := s.Files[p]; return ok }

func (s *sim) content(p string) string {
	if f, ok := s.Files[p]; ok {
		return string(f.Data)
	}
	return ""
}

func res(out string) (system.Result, error) { return system.Result{Stdout: out}, nil }

func failed(c system.Cmd) (system.Result, error) {
	r := system.Result{ExitCode: 1}
	return r, &system.ExitError{Cmd: c, Result: r}
}

func (s *sim) userGroups(u string) []string {
	var g []string
	for name, members := range s.groups {
		if name == u || contains(members, u) {
			g = append(g, name)
		}
	}
	sort.Strings(g)
	return g
}

func (s *sim) run(c system.Cmd) (system.Result, error) {
	a := c.Args
	arg := func(i int) string {
		if i < len(a) {
			return a[i]
		}
		return ""
	}
	switch c.Name {
	case "systemd-detect-virt":
		if s.lxc {
			return res("lxc\n")
		}
		r := system.Result{Stdout: "none\n", ExitCode: 1}
		return r, &system.ExitError{Cmd: c, Result: r}
	case "unshare":
		if s.nestingOff && strings.Contains(strings.Join(a, " "), "--mount-proc") {
			r := system.Result{Stderr: "unshare: mount /proc failed: Operation not permitted\n", ExitCode: 1}
			return r, &system.ExitError{Cmd: c, Result: r}
		}
		return res("")
	case "dpkg":
		return res("amd64\n")
	case "nproc":
		return res("4\n")
	case "apparmor_parser":
		return res("")
	case "dpkg-deb":
		// The package apt downloaded: its version, or its files unpacked.
		if arg(0) == "-f" {
			return res(simAppArmorProfiles + "\n")
		}
		if !s.noBwrapInPackage {
			s.AddFile(arg(2)+ubuntuBwrapExtra, simUbuntuBwrapProfile, 0o644)
		}
		return res("")
	case "cat":
		data, err := s.ReadFile(a[len(a)-1])
		if err != nil {
			return failed(c)
		}
		return res(string(data))
	case "bwrap":
		if s.bwrapWorks() {
			return res("")
		}
		r := system.Result{ExitCode: 1, Stderr: "bwrap: loopback: Failed RTM_NEWADDR: Operation not permitted\n"}
		return r, &system.ExitError{Cmd: c, Result: r}
	case "dpkg-query":
		if arg(1) == "-f=${Version}" {
			if v, ok := s.pkgs[arg(2)]; ok {
				return res(v)
			}
			return failed(c)
		}
		var b strings.Builder
		for _, p := range a[2:] {
			if _, ok := s.pkgs[p]; ok {
				fmt.Fprintf(&b, "%s ii \n", p)
			}
		}
		return res(b.String())
	case "apt-get":
		var rest []string
		for i := 0; i < len(a); i++ {
			if a[i] == "-o" {
				i++
				continue
			}
			if !strings.HasPrefix(a[i], "-") {
				rest = append(rest, a[i])
			}
		}
		switch rest[0] {
		case "download":
			s.AddFile(c.Dir+"/"+rest[1]+"_"+simAppArmorProfiles+"_all.deb", "deb", 0o644)
		case "install":
			// What installing them would remove; with --no-remove apt
			// stops there. -s only says what it would do.
			var remv []string
			for _, p := range rest[1:] {
				for _, r := range s.conflicts[p] {
					if _, ok := s.pkgs[r]; ok {
						remv = append(remv, r)
					}
				}
			}
			if len(remv) > 0 && contains(a, "--no-remove") {
				r := system.Result{ExitCode: 100, Stdout: "The following packages will be REMOVED:\n  " + strings.Join(remv, " ") + "\n",
					Stderr: "E: Packages need to be removed but remove is disabled.\n"}
				return r, &system.ExitError{Cmd: c, Result: r}
			}
			if contains(a, "-s") {
				var out strings.Builder
				for _, r := range remv {
					fmt.Fprintf(&out, "Remv %s [1]\n", r)
				}
				for _, p := range rest[1:] {
					fmt.Fprintf(&out, "Inst %s (1 Ubuntu:26.04/resolute [amd64])\n", p)
				}
				return res(out.String())
			}
			for _, r := range remv {
				delete(s.pkgs, r)
			}
			for _, p := range rest[1:] {
				name, ver, _ := strings.Cut(p, "=")
				if ver == "" {
					ver = "1"
				}
				s.pkgs[name] = ver
				switch name {
				case "docker-ce":
					if _, ok := s.groups["docker"]; !ok {
						s.groups["docker"] = nil
					}
					s.units["docker.service"] = true
				case "tailscale":
					s.units["tailscaled.service"] = true
				}
			}
		case "remove":
			for _, p := range rest[1:] {
				delete(s.pkgs, p)
			}
		}
		return res("")
	case "apt-cache":
		if arg(0) == "policy" {
			var out strings.Builder
			for _, p := range a[1:] {
				if p == "--" || s.noPackage[p] {
					continue
				}
				inst := "(none)"
				if v, ok := s.pkgs[p]; ok {
					inst = v
				}
				fmt.Fprintf(&out, "%s:\n  Installed: %s\n  Candidate: 1\n  Version table:\n", p, inst)
			}
			return res(out.String())
		}
		return res(" docker-ce | 5:29.9.3-1~ubuntu.26.04~resolute | https://download.docker.com/linux/ubuntu resolute/stable amd64 Packages\n" +
			" docker-ce | 5:28.5.0-1~ubuntu.26.04~resolute | https://download.docker.com/linux/ubuntu resolute/stable amd64 Packages\n")
	case "getent":
		if arg(0) == "hosts" {
			h, _ := s.ReadFile("/etc/hosts")
			for _, l := range strings.Split(string(h), "\n") {
				if contains(strings.Fields(l), arg(1)) {
					return res(l)
				}
			}
			return failed(c)
		}
		if _, ok := s.groups[arg(1)]; ok {
			return res(arg(1))
		}
		return failed(c)
	case "hostnamectl":
		s.AddFile("/etc/hostname", arg(1)+"\n", 0o644)
		return res("")
	case "timedatectl":
		if arg(0) == "show" {
			return res(s.tz + "\n")
		}
		s.tz = arg(1)
		return res("")
	case "locale":
		return res("C\nC.utf8\nPOSIX\n")
	case "update-locale":
		s.AddFile("/etc/default/locale", arg(0)+"\n", 0o644)
		return res("")
	case "swapon":
		if arg(0) == "--show=NAME" {
			return res(strings.Join(s.swap, "\n"))
		}
		s.swap = append(s.swap, arg(0))
		return res("")
	case "fallocate":
		var n int64
		fmt.Sscan(arg(1), &n)
		s.Files[arg(2)] = &system.FakeFile{Size: n, Mode: 0o644, Owner: "root", Group: "root"}
		return res("")
	case "chmod":
		if m, err := strconv.ParseUint(arg(0), 8, 32); err == nil && s.has(arg(1)) {
			s.Files[arg(1)].Mode = fs.FileMode(m)
		}
		return res("")
	case "tmux":
		switch arg(0) {
		case "show-environment":
			if s.tmuxEnv == nil {
				return failed(c) // no server running
			}
			if v, ok := s.tmuxEnv[arg(2)]; ok {
				return res(arg(2) + "=" + v + "\n")
			}
			return res("-" + arg(2) + "\n")
		case "set-environment":
			if s.tmuxEnv == nil {
				return failed(c)
			}
			s.tmuxEnv[arg(2)] = arg(3)
		case "has-session":
			if s.tmuxEnv == nil {
				return failed(c) // no server running
			}
		}
		return res("")
	case "chown", "sync", "mkswap", "sysctl", "visudo":
		return res("")
	case "rm":
		p := a[len(a)-1]
		for k := range s.Files {
			if k == p || strings.HasPrefix(k, p+"/") {
				delete(s.Files, k)
			}
		}
		return res("")
	case "mv":
		src, dst := a[len(a)-2], a[len(a)-1]
		if st, _ := s.Stat(dst); st.Exists && st.IsDir && !strings.Contains(arg(0), "f") {
			return failed(c)
		}
		if !s.move(src, dst) {
			return failed(c)
		}
		return res("")
	case "userdel":
		delete(s.Users, arg(0))
		return res("")
	case "useradd":
		name := a[len(a)-1]
		uid := 1000 // the next one free, as useradd picks for a login account
		for _, u := range s.Users {
			if u.UID >= uid {
				uid = u.UID + 1
			}
		}
		s.AddUser(name, uid)
		s.groups[name] = nil
		shadow, _ := s.ReadFile("/etc/shadow")
		s.AddFile("/etc/shadow", string(shadow)+name+":!:20000:0:99999:7:::\n", 0o640)
		return res("")
	case "id":
		if _, err := s.LookupUser(arg(1)); err != nil {
			return failed(c)
		}
		return res(strings.Join(s.userGroups(arg(1)), " "))
	case "usermod":
		if arg(0) == "--lock" {
			shadow, _ := s.ReadFile("/etc/shadow")
			name := a[len(a)-1]
			out := strings.Replace(string(shadow), name+":!:20000:0:99999:7:::", name+":!:20000:0:99999:7::1:", 1)
			s.AddFile("/etc/shadow", out, 0o640)
			return res("")
		}
		s.groups[arg(1)] = append(s.groups[arg(1)], arg(2))
		return res("")
	case "chage":
		if arg(0) == "--expiredate" && arg(1) == "-1" {
			shadow, _ := s.ReadFile("/etc/shadow")
			var lines []string
			for _, l := range strings.Split(strings.TrimSuffix(string(shadow), "\n"), "\n") {
				if f := strings.Split(l, ":"); len(f) >= 8 && f[0] == arg(2) {
					f[7] = ""
					l = strings.Join(f, ":")
				}
				lines = append(lines, l)
			}
			s.AddFile("/etc/shadow", strings.Join(lines, "\n")+"\n", 0o640)
			return res("")
		}
		return failed(c)
	case "gpasswd":
		var keep []string
		for _, m := range s.groups[arg(2)] {
			if m != arg(1) {
				keep = append(keep, m)
			}
		}
		s.groups[arg(2)] = keep
		return res("")
	case "systemctl":
		if arg(0) == "--user" {
			unit := a[len(a)-1]
			switch arg(3) {
			case "set-property":
				// set-property --runtime SLICE MemoryHigh=X
				if s.sliceHigh == nil {
					s.sliceHigh = map[string]string{}
				}
				v := strings.TrimPrefix(a[len(a)-1], "MemoryHigh=")
				s.sliceHigh[a[len(a)-2]] = v
				uid := s.Users["agent"].UID
				s.AddFile(fmt.Sprintf("/run/user/%d/systemd/user.control/%s.d/50-MemoryHigh.conf", uid, a[len(a)-2]), "[Slice]\nMemoryHigh="+v+"\n", 0o644)
				return res("")
			case "daemon-reload":
				if s.managerXDG {
					s.sliceHigh = map[string]string{}
					for _, sl := range agentWorkSlices {
						if f, ok := s.Files[workSliceDropIn(sl)]; ok {
							s.sliceHigh[sl] = strings.TrimSpace(strings.SplitN(string(f.Data), "MemoryHigh=", 2)[1])
						}
					}
				}
				s.userEnv = "PATH=/usr/local/bin:/usr/bin:/bin\n"
				if f, ok := s.Files["/home/agent/"+EnvironmentD]; ok {
					s.userEnv = string(f.Data)
				}
			case "show-environment":
				if s.userEnv == "" {
					return res("PATH=/usr/local/bin:/usr/bin:/bin\n")
				}
				return res(s.userEnv)
			case "enable":
				s.Files["/home/agent/.config/systemd/user/default.target.wants/"+unit] = &system.FakeFile{Target: "../" + unit, Owner: "agent", Group: "agent"}
				// The Remote Control unit is skipped by its ExecCondition
				// until Claude Code is signed in.
				s.units["user:"+unit] = unit != RemoteControlUnit || s.claudeSignedIn
			case "restart":
				s.units["user:"+unit] = unit != RemoteControlUnit || s.claudeSignedIn && !s.rcUnanswered
			case "show":
				if strings.Contains(c.String(), "-p MemoryHigh") {
					if v, ok := s.sliceHigh[unit]; ok {
						return res(v + "\n")
					}
					return res("infinity\n")
				}
				if unit == RemoteControlUnit && s.rcFlaps && s.claudeSignedIn {
					if s.rcShows++; s.rcShows == 1 {
						return res("ActiveState=active\nSubState=running\nResult=success\nNRestarts=0\n")
					}
					return res("ActiveState=activating\nSubState=auto-restart\nResult=exit-code\nNRestarts=1\n")
				}
				if s.units["user:"+unit] {
					return res("ActiveState=active\nSubState=running\nResult=success\nNRestarts=0\n")
				}
				return res("ActiveState=inactive\nSubState=dead\nResult=exec-condition\nNRestarts=0\n")
			case "is-active":
				if !s.units["user:"+unit] {
					return failed(c)
				}
			}
			return res("")
		}
		unit := a[len(a)-1]
		if strings.HasPrefix(unit, "user@") {
			return res("") // lingering runs every user manager
		}
		switch arg(0) {
		case "mask", "unmask":
			for _, u := range a[1:] {
				s.units["masked:"+u] = arg(0) == "mask"
			}
		case "is-enabled":
			if s.units["masked:"+unit] {
				r := system.Result{ExitCode: 1, Stdout: "masked\n"}
				return r, &system.ExitError{Cmd: c, Result: r}
			}
			if !s.units[unit] && !s.units["enabled:"+unit] {
				return failed(c)
			}
		case "is-active":
			if !s.units[unit] {
				return failed(c)
			}
		case "enable":
			s.units[unit], s.units["enabled:"+unit] = true, true
			if unit == agentNetName && contains(a, "--now") {
				s.nftLoaded = s.content(agentNetRules)
			}
		case "start", "restart":
			s.units[unit] = true
			if unit == agentNetName {
				s.nftLoaded = s.content(agentNetRules)
			}
		case "stop":
			s.units[unit] = false
		case "show":
			return res("0.0.0.0/0 ::/0\n")
		}
		return res("")
	case "nft":
		if arg(0) == "list" {
			if !s.units["keelbox-agent-net.service"] {
				return failed(c)
			}
			// As nft lists it: without the script's own first lines, and
			// with the counters' values.
			_, table, _ := strings.Cut(s.nftLoaded, "delete table inet keelbox_agent\n")
			return res(strings.ReplaceAll(table, " counter ", " counter packets 0 bytes 0 "))
		}
		return res("")
	case "sshd":
		if arg(0) == "-T" && arg(1) == "-C" {
			data, _ := s.ReadFile(sshdDropIn)
			out := "allowagentforwarding yes\n"
			if strings.Contains(string(data), "AllowAgentForwarding no") {
				out = "allowagentforwarding no\n"
			}
			lanClient := strings.Contains(s.content(sshdTailnetOnly), " Address *,!10.0.0.0/8") &&
				(strings.Contains(arg(2), "addr=192.168.") || strings.Contains(arg(2), "addr=10."))
			if s.has(sshdTailnetOnly) && strings.Contains(arg(2), "laddr=") && !strings.Contains(arg(2), "laddr=100.") && !lanClient {
				out += "denyusers *\n"
			}
			return res(out)
		}
		if arg(0) == "-T" {
			if st, _ := s.Stat(sshdDropIn); st.Exists {
				return res("passwordauthentication no\nkbdinteractiveauthentication no\npermitrootlogin no\n")
			}
			return res("passwordauthentication yes\nkbdinteractiveauthentication no\npermitrootlogin prohibit-password\n")
		}
		return res("")
	case "ufw":
		if arg(0) == "status" {
			if !s.ufw["enable"] {
				return res("Status: inactive\n")
			}
			out := "Status: active\nDefault: deny (incoming), allow (outgoing), deny (routed)\n\nTo Action From\n"
			if s.ufw["ssh"] {
				out += "22/tcp                     ALLOW IN    Anywhere                   # keelbox ssh\n"
			}
			if s.ufw["tailscale0"] {
				out += "Anywhere on tailscale0     ALLOW IN    Anywhere                   # keelbox tailnet\n"
			}
			return res(out)
		}
		if arg(0) == "show" && arg(1) == "added" {
			out := "Added user rules (see 'ufw status' for running firewall):\n"
			if s.ufw["ssh"] {
				out += "ufw allow 22/tcp comment 'keelbox ssh'\n"
			}
			for _, r := range s.ufwAdded {
				out += r + "\n"
			}
			return res(out)
		}
		switch {
		case arg(0) == "allow" && arg(1) == "from":
			rule := "ufw " + strings.Join(a[:len(a)-2], " ") + " comment '" + a[len(a)-1] + "'"
			s.ufwAdded = append(s.ufwAdded, rule)
		case arg(0) == "delete" && arg(2) == "from":
			prefix := "ufw " + strings.Join(a[1:], " ") + " comment "
			var keep []string
			for _, r := range s.ufwAdded {
				if !strings.HasPrefix(r, prefix) {
					keep = append(keep, r)
				}
			}
			s.ufwAdded = keep
		case arg(0) == "--force":
			s.ufw["enable"] = true
		case arg(0) == "allow" && arg(1) == "22/tcp":
			s.ufw["ssh"] = true
		case arg(0) == "allow" && arg(3) == "tailscale0":
			s.ufw["tailscale0"] = true
		case arg(0) == "delete":
			s.ufw["ssh"] = false
		}
		return res("")
	case "mktemp":
		s.tmpN++
		d := fmt.Sprintf("/tmp/keelbox.%d", s.tmpN)
		return res(d + "\n")
	case "curl":
		if strings.Contains(strings.Join(a, " "), "--unix-socket") {
			if s.guestName == "" {
				return failed(c)
			}
			return res("instance-id: 1\nlocal-hostname: " + s.guestName + "\n")
		}
		if arg(0) == "-fsSI" {
			return res("HTTP/2 200\n")
		}
		out, url := a[len(a)-2], a[len(a)-1]
		s.AddFile(out, "URL:"+url, 0o644)
		return res("")
	case "gpg":
		data, err := s.ReadFile(a[len(a)-1])
		if err != nil {
			return failed(c)
		}
		fpr := s.m.Tailscale.GPGFingerprint
		switch {
		case strings.Contains(string(data), "docker"):
			fpr = s.m.Docker.GPGFingerprint
		case strings.Contains(string(data), "cli.github.com"):
			fpr = s.m.GitHubCLI.GPGFingerprint
		}
		return res("pub:-:4096:1:X:::\nfpr:::::::::" + fpr + ":\n")
	case "install":
		src, dst := a[len(a)-2], a[len(a)-1]
		data, _ := s.ReadFile(src)
		s.AddFile(dst, string(data), 0o755)
		return res("")
	case "sha256sum":
		if arg(0) == "--" {
			data, err := s.ReadFile(arg(1))
			if err != nil {
				return failed(c)
			}
			sum := sha256.Sum256(data)
			out := hex.EncodeToString(sum[:])
			return res(out + "  " + arg(1) + "\n")
		}
		if strings.Contains(arg(0), "restic") {
			return res(s.m.Restic.SHA256["amd64"] + "  " + arg(0) + "\n")
		}
		return res(s.m.Mise.SHA256["amd64"] + "  " + arg(0) + "\n")
	case "sha512sum":
		return res(s.m.Caddy.SHA512["amd64"] + "  " + arg(0) + "\n")
	case "tar":
		if arg(4) == "caddy" {
			s.AddFile(arg(3)+"/caddy", "caddy", 0o755)
			return res("")
		}
		s.AddFile(arg(3)+"/mise/bin/mise", "mise", 0o755)
		return res("")
	case "bunzip2":
		s.AddFile(strings.TrimSuffix(arg(0), ".bz2"), "restic", 0o644)
		return res("")
	case previews.CaddyBin:
		if !s.has(previews.CaddyBin) {
			return failed(c)
		}
		return res("v" + s.m.Caddy.Version + " h1:simulated\n")
	case backup.ResticBin, "/bin/bash":
		if c.Name == backup.ResticBin && arg(0) == "version" {
			if !s.has(backup.ResticBin) {
				return failed(c)
			}
			return res("restic " + s.m.Restic.Version + " compiled with go1.25.0 on linux/amd64\n")
		}
		if s.restic != nil {
			return s.restic(c)
		}
		return failed(c)
	case "ss":
		if arg(0) == "-Htunpe" {
			return res(s.connected)
		}
		return res(s.listening)
	case "find":
		if contains(a, "-perm") {
			var bad []string
			for p, f := range s.Files {
				for _, root := range a[:len(a)-15] {
					if (p == root || strings.HasPrefix(p, root+"/")) && f.Target == "" && (f.Owner != "root" || f.Mode&0o022 != 0) {
						bad = append(bad, p)
					}
				}
			}
			sort.Strings(bad)
			return res(strings.Join(bad, "\n"))
		}
		for p := range s.Files {
			if strings.HasPrefix(p, arg(0)+"/") {
				return res(p + "\n")
			}
		}
		return res("")
	case "systemd-analyze":
		return res("")
	case miseBin:
		switch arg(0) {
		case "--version":
			if st, _ := s.Stat(miseBin); !st.Exists {
				return failed(c)
			}
			return res(s.m.Mise.Version + " linux-x64 (2026-11-01)\n")
		case "where":
			if !s.mise[arg(1)] {
				return failed(c)
			}
		case "install":
			s.mise["node@"+s.m.Node], s.mise["pnpm@"+s.m.Pnpm] = true, true
		}
		return res("")
	case "git":
		if arg(0) == "config" {
			kv := a[1:]
			if len(kv) > 0 && kv[0] == "--global" {
				kv = kv[1:]
			}
			if len(kv) == 2 && kv[0] == "--get" {
				v, ok := s.gitConfig[c.User+" "+kv[1]]
				if !ok {
					return failed(c)
				}
				return res(v + "\n")
			}
			if len(kv) == 2 {
				s.gitConfig[c.User+" "+kv[0]] = kv[1]
				return res("")
			}
			return failed(c)
		}
		switch {
		case arg(0) == "clone":
			s.gitHead[arg(3)] = "upstream-head"
			s.Files[arg(3)+"/.git"] = &system.FakeFile{Dir: true, Mode: 0o755, Owner: c.User, Group: c.User}
		case arg(2) == "rev-parse":
			h, ok := s.gitHead[arg(1)]
			if !ok {
				return failed(c)
			}
			return res(h + "\n")
		case strings.Contains(strings.Join(a, " "), "checkout"):
			s.gitHead[arg(1)] = a[len(a)-1]
		}
		return res("")
	case "ip":
		if arg(1) == "route" && arg(2) == "get" {
			local := append([]string{"127.0.0.1", "::1", "10.0.0.5"}, s.localAddrs...)
			if s.tailscale == "Running" {
				local = append(local, "100.101.102.103", "fd7a:115c:a1e0::1")
			}
			if contains(local, arg(3)) {
				return res("local " + arg(3) + " dev lo table local src " + arg(3) + " uid 0 \\    cache <local> \n")
			}
			return res(arg(3) + " via 10.0.0.1 dev eth0 src 10.0.0.5 uid 0 \\    cache \n")
		}
		if s.tailscale != "Running" && s.tsIface == nil {
			return failed(c)
		}
		addrs := s.tsIface
		if addrs == nil {
			addrs = []string{"100.101.102.103", "fd7a:115c:a1e0::1"}
		}
		var info []string
		for _, a := range addrs {
			info = append(info, `{"local":"`+a+`"}`)
		}
		return res(`[{"ifname":"tailscale0","addr_info":[` + strings.Join(info, ",") + `]}]`)
	case "bash", "sh":
		if arg(1) == tunProbe.Args[1] {
			if s.noTun {
				return failed(c)
			}
			return res("")
		}
		if s.installerFails {
			return failed(c)
		}
		data, _ := s.ReadFile(arg(0))
		u, _ := s.LookupUser(c.User)
		bin := "codex"
		if strings.Contains(string(data), "claude.ai") {
			bin = "claude"
		}
		s.Files[u.Home+"/.local/bin/"+bin] = &system.FakeFile{Target: "versions/" + bin, Owner: c.User, Group: c.User}
		s.Files[u.Home+"/.local/bin/versions/"+bin] = &system.FakeFile{Data: []byte(bin), Mode: 0o755, Owner: c.User, Group: c.User}
		return res("")
	case "loginctl":
		s.AddFile("/var/lib/systemd/linger/"+arg(1), "", 0o644)
		return res("")
	case "tailscale":
		if arg(0) == "status" {
			if s.tailscale == "Running" {
				return res(`{"BackendState":"Running","CertDomains":["testbox.tail1234.ts.net"],"Self":{"DNSName":"testbox.tail1234.ts.net.","TailscaleIPs":["100.101.102.103","fd7a:115c:a1e0::1"]}}`)
			}
			return res(`{"BackendState":"` + s.tailscale + `"}`)
		}
		if arg(0) == "cert" {
			s.AddFile(arg(2), "CERT for "+arg(5), 0o644)
			s.AddFile(arg(4), "KEY for "+arg(5), 0o600)
			return res("")
		}
		if arg(0) == "up" {
			s.tailscale = "Running"
		}
		return res("")
	case "test":
		return res("")
	case "docker":
		// The system socket answers docker group members; the rootless one
		// answers its owner once the user unit runs.
		if arg(0) == "-H" {
			if !contains(s.userGroups(c.User), "docker") {
				return failed(c)
			}
			return res("29.9.3\n")
		}
		if _, ok := s.pkgs["docker-ce-rootless-extras"]; !ok || !s.units["user:docker.service"] || !strings.Contains(strings.Join(c.Env, " "), "DOCKER_HOST=unix:///run/user/") {
			return failed(c)
		}
		return res("29.9.3\n")
	case "iptables":
		if !s.units["keelbox-docker-guard.service"] {
			return failed(c)
		}
		return res("Chain DOCKER-USER (1 references)\nKEELBOX-DOCKER  all  --  0.0.0.0/0  0.0.0.0/0\n")
	case "node":
		if !s.mise["node@"+s.m.Node] {
			return failed(c)
		}
		return res("v" + s.m.Node + "\n")
	case "pnpm":
		if !s.mise["pnpm@"+s.m.Pnpm] {
			return failed(c)
		}
		return res(s.m.Pnpm + "\n")
	case "gh":
		if _, ok := s.pkgs["gh"]; !ok {
			return failed(c)
		}
		return res("gh version 2.103.3 (2026-10-30)\n")
	case "claude", "codex":
		u, _ := s.LookupUser(c.User)
		c.Name = u.Home + "/.local/bin/" + c.Name
		return s.vendorCommand(c)
	}
	if u, err := s.LookupUser(c.User); err == nil && c.User != "" && strings.HasPrefix(c.Name, u.Home+"/.local/bin/") {
		// A vendor-installed CLI, found on the agent's PATH.
		return s.vendorCommand(c)
	}
	return failed(c)
}

func testConfig(t *testing.T, extra string) *config.Config {
	t.Helper()
	y := fmt.Sprintf("version: 1\nbox: testbox\nusers:\n  admin:\n    ssh_authorized_keys: [%q]\n%s", adminKey, extra)
	c, err := config.Parse([]byte(y), "test.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func simEnv(t *testing.T, s *sim, c *config.Config) *engine.Env {
	t.Helper()
	facts, err := engine.GatherFacts(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.CheckPlatform(facts, s.m); err != nil {
		t.Fatal(err)
	}
	return &engine.Env{X: s, Cfg: c, M: s.m, Facts: facts, AptStale: true, Self: simSelf}
}
