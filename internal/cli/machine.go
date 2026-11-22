package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/inventory"
	"github.com/keelbox/keelbox/internal/manifest"
)

// keelbox up looks at the machine before changing it: this machine
// directly, a server over SSH as the account the user logs in as. It runs
// one read-only script and reads key=value lines back, so it can say
// before anything changes that the machine is not Ubuntu, that sudo needs
// a password, that it is a desktop, and which services the firewall would
// cut off from the network.

// probeScript prints what keelbox up needs to know; it changes nothing.
// sudo -n is tried for the process names of other accounts' sockets and
// for Docker; without it the script still answers, with less detail.
const probeScript = `. /etc/os-release 2>/dev/null; echo "os=$ID $VERSION_ID"
echo "hostname=$(hostname 2>/dev/null)"
echo "user=${SUDO_USER:-$(id -un)}"
echo "timezone=$(timedatectl show -p Timezone --value 2>/dev/null || cat /etc/timezone 2>/dev/null)"
echo "locale=$(sed -n 's/^LANG=//p' /etc/default/locale 2>/dev/null | tr -d '"')"
if [ "$(id -u)" = 0 ] || sudo -n true 2>/dev/null; then echo sudo=yes
elif id -nG 2>/dev/null | tr ' ' '\n' | grep -qxE 'sudo|admin|wheel' || sudo -n -v 2>&1 | grep -qiE 'password|authentication'; then echo sudo=password
else echo sudo=no; fi
s=; [ "$(id -u)" = 0 ] || s="sudo -n"
echo "sessions=$(ls /usr/share/xsessions /usr/share/wayland-sessions 2>/dev/null | grep -c '\.desktop$')"
echo "cloud_users=$($s cat /etc/sudoers.d/90-cloud-init-users 2>/dev/null | awk '$1 !~ /^[#%]/ && NF > 1 {printf "%s ", $1}')"
systemctl is-active --quiet display-manager 2>/dev/null && echo "display_manager=$(basename "$(readlink -f /etc/systemd/system/display-manager.service 2>/dev/null)" .service)"
{ $s ss -Hltunp 2>/dev/null || ss -Hltun 2>/dev/null; } | sed 's/^/listen=/'
command -v docker >/dev/null 2>&1 && $s docker ps --format '{{.Names}} {{.Ports}}' 2>/dev/null | sed 's/^/container=/'
true
`

// machineFacts is what the probe found.
type machineFacts struct {
	OSID, OSVersion          string
	Hostname, User           string
	Timezone, Locale         string
	Sudo                     bool
	SudoPassword             bool     // sudo works once given the account's password
	CloudUsers               []string // accounts cloud-init gave passwordless sudo
	Desktop                  string   // why it looks like a desktop, or ""
	Listeners                []listener
	Sessions                 int // desktop sessions installed
	DispMgr                  string
	containers               map[string]string // "8096/tcp" -> container name
	rawListen, rawContainers []string
}

// listener is one service listening on a non-loopback address.
type listener = inventory.Listener

// summary is what the check found, in a line: the check passed.
func (f machineFacts) summary() string {
	s := "Ubuntu " + f.OSVersion + ", sudo works"
	if !f.Sudo && f.SudoPassword {
		s = "Ubuntu " + f.OSVersion + ", sudo with " + f.User + "'s password"
	}
	if f.Hostname != "" {
		s = f.Hostname + ", " + s
	}
	switch n := len(f.Listeners); n {
	case 0:
		s += ", nothing else listening"
	default:
		s += ", " + plural(n, "service", "services") + " listening"
	}
	return s
}

// plural is "1 change" or "3 changes".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func parseProbe(out string) machineFacts {
	f := machineFacts{containers: map[string]string{}}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimRight(line, "\r"), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "os":
			f.OSID, f.OSVersion, _ = strings.Cut(v, " ")
		case "hostname":
			f.Hostname = v
		case "user":
			f.User = v
		case "timezone":
			f.Timezone = v
		case "locale":
			f.Locale = v
		case "sudo":
			f.Sudo, f.SudoPassword = v == "yes", v == "password"
		case "cloud_users":
			f.CloudUsers = strings.Fields(v)
		case "sessions":
			f.Sessions, _ = strconv.Atoi(v)
		case "display_manager":
			f.DispMgr = v
		case "listen":
			f.rawListen = append(f.rawListen, v)
		case "container":
			f.rawContainers = append(f.rawContainers, v)
		}
	}
	for _, c := range f.rawContainers {
		name, ports, _ := strings.Cut(c, " ")
		for _, m := range dockerPortRe.FindAllStringSubmatch(ports, -1) {
			f.containers[m[1]+"/"+m[2]] = name
		}
	}
	f.Listeners = inventory.ParseListeners(f.rawListen, f.containers)
	switch {
	case f.DispMgr != "":
		f.Desktop = "a display manager (" + f.DispMgr + ") is running"
	case f.Sessions > 0:
		// Ubuntu Server boots to graphical.target too, so the target
		// says nothing; an installed desktop session does.
		f.Desktop = "a desktop is installed"
	}
	return f
}

// dockerPortRe matches a published port in docker ps: 0.0.0.0:8096->8096/tcp.
var dockerPortRe = regexp.MustCompile(`:(\d+)->\d+/(tcp|udp)`)

// supported says whether keelbox can set the machine up, and if not why.
func (f machineFacts) supported() error {
	m, err := manifest.ForChannel("stable")
	if err != nil {
		return err
	}
	if f.OSID != "ubuntu" {
		os := strings.TrimSpace(f.OSID + " " + f.OSVersion)
		if os == "" {
			os = "not Linux"
		}
		return fmt.Errorf("keelbox sets up Ubuntu %s, and this is %s", strings.Join(m.OS.Ubuntu, " or "), os)
	}
	if !contains(m.OS.Ubuntu, f.OSVersion) {
		return fmt.Errorf("keelbox sets up Ubuntu %s, and this is Ubuntu %s; upgrade it (sudo do-release-upgrade) or install a supported release", strings.Join(m.OS.Ubuntu, " or "), f.OSVersion)
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// pickPorts reads an answer to "which stay open on the LAN": all, none,
// or service names and port numbers (with /udp where both exist) from the
// list. A port of a service picks the service.
func pickPorts(answer string, cs []lanChoice) ([]lanChoice, error) {
	a := strings.ToLower(strings.TrimSpace(answer))
	switch a {
	case "all", "":
		return cs, nil
	case "none", "no", "-":
		return nil, nil
	}
	var out []lanChoice
	picked := map[string]bool{}
	for _, w := range strings.FieldsFunc(a, func(r rune) bool { return r == ',' || r == ' ' }) {
		found := false
		for _, c := range cs {
			match := w == c.Key
			for _, k := range c.Ports {
				port, _, _ := strings.Cut(k, "/")
				match = match || w == port || w == k
			}
			if match {
				found = true
				if !picked[c.Key] {
					picked[c.Key] = true
					out = append(out, c)
				}
			}
		}
		if !found {
			var have []string
			for _, c := range cs {
				have = append(have, c.Key)
			}
			return nil, fmt.Errorf("%s is not one of those listed (%s); use all, none, or names and numbers such as %s", w, strings.Join(have, ", "), exampleChoice(cs))
		}
	}
	return out, nil
}

// exampleChoice is a choice to name in a hint: a port number, or a service.
func exampleChoice(cs []lanChoice) string {
	for _, c := range cs {
		if c.Service == "" {
			return strconv.Itoa(c.l.Port)
		}
	}
	return cs[0].Key
}

// keepRules are the host.firewall.allow entries for the chosen services
// and ports.
func keepRules(cs []lanChoice) []config.Allow {
	var out []config.Allow
	var ports []listener
	for _, c := range cs {
		if c.Service != "" {
			out = append(out, c.keep())
		} else {
			ports = append(ports, c.l)
		}
	}
	return append(out, allowRules(ports)...)
}

// allowRules are the host.firewall.allow entries for the chosen listeners.
func allowRules(ls []listener) []config.Allow {
	var out []config.Allow
	names := map[string]int{}
	for _, l := range ls {
		name := l.Name
		if names[name]++; names[name] > 1 {
			name = fmt.Sprintf("%s-%d", name, l.Port)
		}
		a := config.Allow{Name: name, Port: config.Port(l.Port)}
		if l.Proto == "udp" {
			a.Proto = "udp"
		}
		out = append(out, a)
	}
	return out
}
