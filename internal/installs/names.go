package installs

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits on one request.
const (
	// MaxNames is the most package names one request carries.
	MaxNames = 20
	// maxNameLen is the longest name taken; Ubuntu's longest are under it.
	maxNameLen = 128
	// WhyMax is the most characters kept of the agent's stated reason.
	WhyMax = 300
	// transactionMax is the most packages one request may install or
	// upgrade, counting what apt adds for it: a metapackage that brings a
	// desktop is the owner's to install.
	transactionMax = 300
)

// nameRe is a Debian package name and nothing else: lower-case letters,
// digits, '+', '.' and '-', starting with a letter or digit. A version
// (name=1.2), a release (name/noble), an architecture (name:arm64), a path,
// a URL and an option all hold a character outside it, or start with '-'.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)

// Refusal is why a request is not installed. Msg is for the agent and the
// owner and may quote, escaped, what the agent wrote. Code is a few fixed
// words for the logbook, which holds nothing the agent wrote besides
// checked package names.
type Refusal struct {
	Msg, Code string
}

func (r *Refusal) Error() string { return r.Msg }

func refuse(code, format string, args ...any) *Refusal {
	return &Refusal{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// quoted shows a string the agent wrote, cut short and with every
// character outside printable ASCII escaped, so a look-alike letter or a
// control character is seen for what it is.
func quoted(s string) string {
	if utf8.RuneCountInString(s) > 40 {
		s = string([]rune(s)[:40]) + "..."
	}
	return strconv.QuoteToASCII(s)
}

// CheckNames checks the shape of a request's package list and returns it
// without repeats. It looks at the names only: whether apt has them, and
// what installing them would do, is checked against the box (plan).
func CheckNames(names []string) ([]string, *Refusal) {
	if len(names) == 0 {
		return nil, refuse("no package named", "name at least one package: keelbox install NAME")
	}
	if len(names) > MaxNames {
		return nil, refuse("too many names", "%d package names in one request; the most is %d: ask for them in smaller groups", len(names), MaxNames)
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		switch {
		case strings.HasPrefix(n, "-"):
			return nil, refuse("an option, not a package name", "%s is an option, not a package name: keelbox install takes package names only", quoted(n))
		case len(n) > maxNameLen || !nameRe.MatchString(n):
			return nil, refuse("not a package name", "%s is not a package name: lower-case letters, digits, '+', '.' and '-' only, "+
				"with no version, release, architecture, path, URL or option", quoted(n))
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, n := range out {
		if fam, ok := Denied(n); ok {
			return nil, deniedRefusal(n, fam)
		}
	}
	return out, nil
}

func deniedRefusal(name string, fam DenyFamily) *Refusal {
	return refuse("on the deny-list ("+fam.Name+")", "%s is on keelbox's deny-list (%s): %s", name, fam.Name, fam.Why)
}

// DenyFamily is one group of packages a request may not touch, and why.
// A name ending in * matches as a prefix, one starting with * as a suffix.
type DenyFamily struct {
	Name, Why string
	Names     []string
}

// DenyList is every package the agent's requests may not install, may not
// upgrade and may not pull in as a dependency, in manual, auto and full
// alike, by family. Each would replace or reconfigure something keelbox
// sets up, or something reaching the box depends on. It is a list of names,
// not a property of packages: a package that is not on it can still bring a
// root service, a setuid program or a maintainer script
// (docs/security-model.md says what that leaves open).
var DenyList = []DenyFamily{
	{
		Name: "remote login",
		// keelbox's sshd settings (keys only, no root, no agent forwarding
		// for the agent, tailnet-only once public SSH closes) and the
		// agent network table are written for OpenSSH on port 22.
		Why:   "keelbox hardens the box's sshd and refuses the agent's connections to it; another login server would not carry those settings",
		Names: []string{"openssh-server*", "openssh-sftp-server", "ssh", "dropbear*", "tinysshd", "lsh-server", "shellinabox", "ttyd", "telnetd*", "inetutils-telnetd", "rsh-server", "rsh-redone-server"},
	},
	{
		Name: "firewall",
		// UFW's default deny, the DOCKER-USER guard and the agent's
		// nftables table are keelbox's; a second firewall front end loads
		// its own rules over them, or restores saved ones at boot.
		Why:   "keelbox manages UFW and the nftables table that limits the agent; another firewall tool would load its own rules",
		Names: []string{"ufw", "gufw", "nftables", "iptables", "iptables-persistent", "netfilter-persistent", "firewalld", "shorewall*", "ferm", "firehol*", "arno-iptables-firewall"},
	},
	{
		Name: "privilege tools",
		// The agent has no sudo in manual and auto. These run commands as
		// another account; each is one rule or one flaw away from root.
		Why:   "they run commands as another account, and the agent has no route to root in manual and auto",
		Names: []string{"sudo", "sudo-ldap", "sudo-rs", "doas", "opendoas", "pkexec", "policykit-1", "polkitd", "polkitd-pkla", "super", "calife", "userv", "chiark-really", "schroot", "pleaser"},
	},
	{
		Name: "Docker engine and containerd",
		// The system daemon's socket is root-equivalent and closed to the
		// agent, which has its own rootless daemon. Ubuntu's docker.io
		// would replace Docker's packages; a changed containerd or runc is
		// what every container on the box then runs on.
		Why:   "keelbox installs Docker's own packages and keeps the system daemon closed to the agent, which has a rootless one",
		Names: []string{"docker.io", "docker-ce*", "docker-engine*", "docker-compose-plugin", "docker-buildx-plugin", "moby-*", "containerd*", "runc"},
	},
	{
		Name: "Tailscale",
		// The tailnet is how the box is reached once public SSH is closed.
		Why:   "the tailnet is how the box is reached; keelbox installs Tailscale from its own repository",
		Names: []string{"tailscale*"},
	},
	{
		Name: "AppArmor and other security modules",
		// The bwrap profile keelbox loads, and Ubuntu's restriction on user
		// namespaces, are AppArmor's. Extra profile packages change what is
		// confined; another security module changes what the kernel enforces.
		Why:   "the profiles that confine the agent's sandboxes are AppArmor's, and keelbox loads one of them",
		Names: []string{"apparmor*", "selinux-*"},
	},
	{
		Name: "updates and apt",
		// Unattended security updates are what keeps an installed package
		// patched. A package that makes apt find a proxy by itself would
		// let a process on the box stand between apt and its sources.
		Why:   "unattended security updates and apt's own settings are what keep installed packages patched",
		Names: []string{"unattended-upgrades", "apt", "dpkg", "auto-apt-proxy", "squid-deb-proxy-client"},
	},
	{
		Name: "systemd and the system bus",
		// Every keelbox unit, the agent's slice and its limits, lingering
		// and the journal are systemd's. Its optional parts change how the
		// box resolves names, kills processes under memory pressure, keeps
		// accounts or boots.
		Why: "keelbox's units, the agent's limits and its sessions are systemd's",
		Names: []string{"systemd", "systemd-sysv", "systemd-resolved", "systemd-timesyncd", "systemd-networkd", "systemd-oomd", "systemd-homed",
			"systemd-userdbd", "systemd-container", "systemd-coredump", "systemd-cron", "systemd-cryptsetup", "systemd-repart", "systemd-journal-remote",
			"systemd-boot*", "systemd-standalone-*", "udev", "dbus", "dbus-daemon", "dbus-broker", "init", "sysvinit-core", "runit-init", "openrc", "elogind"},
	},
	{
		Name: "login stack",
		// A PAM or NSS module is added to the stacks sshd, su and sudo use
		// when its package is installed: it changes how every account is
		// authenticated and looked up.
		Why:   "PAM and NSS modules change how sshd, su and sudo authenticate and look up every account",
		Names: []string{"libpam-*", "libnss-*"},
	},
	{
		Name: "cloud guest agents",
		// They create accounts, install SSH keys or change sshd from the
		// provider's metadata. keelbox tells cloud-init to keep the host
		// keys and create no account, and locks the image's own account.
		Why:   "they create accounts and install SSH keys from the cloud's metadata, which keelbox has turned off or locked",
		Names: []string{"cloud-init*", "ec2-instance-connect", "google-guest-agent", "google-compute-engine*", "google-osconfig-agent", "walinuxagent"},
	},
	{
		Name: "network configuration",
		// A second network manager takes over the box's interfaces or its
		// resolver. A box that drops off its network with public SSH closed
		// is reached only from its provider's console.
		Why:   "another network manager can take the box off its network, and with public SSH closed that locks the owner out",
		Names: []string{"network-manager*", "netplan.io", "connman*", "ifupdown*", "resolvconf", "openresolv", "dhcpcd*", "wicd*"},
	},
	{
		Name: "kernel and boot",
		// A kernel, a boot loader, an initramfs builder or a module built
		// for the running kernel changes what the box boots and what runs
		// in the kernel, where none of keelbox's limits apply.
		Why: "they change what the box boots and what runs in the kernel",
		Names: []string{"linux-image*", "linux-modules*", "linux-generic*", "linux-virtual*", "linux-signed*", "grub*", "shim", "shim-signed",
			"dracut*", "initramfs-tools*", "kexec-tools", "dkms", "*-dkms"},
	},
	{
		Name: "schedulers",
		// keelbox closes cron and at to the agent so its jobs stay inside
		// its limits (ADR 0055). Another scheduler has its own allow and
		// deny files, which keelbox does not write.
		Why:   "keelbox closes cron and at to the agent so that its jobs stay inside its limits; another scheduler would not be closed",
		Names: []string{"cronie*", "bcron*", "fcron", "incron"},
	},
	{
		Name: "proxies and relays",
		// A proxy that starts when its package is installed, and takes
		// requests from loopback, connects for whoever asks. The table that
		// refuses connections to the box's SSH port and to link-local
		// addresses applies to every account but root (ADR 0076), so a
		// proxy under an account of its own is refused there as the agent
		// is; it reaches the internet and the tailnet, as any process can.
		// This list is the second line: for a relay whose sockets are
		// root's, which the table lets through, and for a box whose table
		// is not the one loaded. These are the ones known to start that way
		// with their packaged configuration; the list cannot be complete.
		Why: "a proxy connects for whoever asks from the box, and one whose connections are root's is outside the limits on the box's SSH port and link-local addresses",
		Names: []string{"tinyproxy*", "privoxy", "squid*", "dante-server", "3proxy", "polipo", "microsocks", "redsocks", "shadowsocks*", "sslh",
			"guacd", "apt-cacher-ng"},
	},
}

// Denied reports the deny-list family a package name is in.
func Denied(name string) (DenyFamily, bool) {
	for _, fam := range DenyList {
		for _, p := range fam.Names {
			switch {
			case strings.HasSuffix(p, "*"):
				if strings.HasPrefix(name, strings.TrimSuffix(p, "*")) {
					return fam, true
				}
			case strings.HasPrefix(p, "*"):
				if strings.HasSuffix(name, strings.TrimPrefix(p, "*")) {
					return fam, true
				}
			case p == name:
				return fam, true
			}
		}
	}
	return DenyFamily{}, false
}

// blankGlyphs print as nothing and count as printable: the braille blank
// and the Hangul fillers. A run of them would push the rest of a reason
// onto a line of its own, to be read as keelbox's.
var blankGlyphs = map[rune]bool{0x2800: true, 0x3164: true, 0x115F: true, 0x1160: true, 0xFFA0: true}

// CleanWhy is the agent's stated reason as one line of plain text: control
// characters and everything else that does not print are dropped, runs of
// spaces and of characters that print blank become one space, and at most
// WhyMax characters are kept. It is still the agent's text, and is shown as
// such.
func CleanWhy(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
		case unicode.IsSpace(r), blankGlyphs[r]:
			b.WriteByte(' ')
		case unicode.IsPrint(r):
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if utf8.RuneCountInString(out) > WhyMax {
		out = string([]rune(out)[:WhyMax])
	}
	return out
}
