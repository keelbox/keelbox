package modules

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// Public SSH closes once the laptop has proven it reaches sshd over the
// tailnet (docs/adr/0021-close-public-ssh.md). The proof is the run itself:
// an apply whose SSH session arrived at one of the box's Tailscale
// addresses on tailscale0, as the admin. The box records it, and keelbox
// then refuses SSH on every other address twice: UFW drops the public
// 22/tcp rule, and sshd refuses any login whose local address is not on
// the tailnet or loopback.
const (
	PublicSSHStatusPath = "/var/lib/keelbox/public-ssh.json"
	sshdTailnetOnly     = "/etc/ssh/sshd_config.d/02-keelbox-tailnet-only.conf"
)

// sshdTailnetOnlyConfig refuses every login unless the connection arrived at
// a tailnet or loopback address. It names the address ranges, not the box's
// own addresses, so a change of Tailscale address cannot lock the box.
const sshdTailnetOnlyConfig = `# Managed by keelbox: public SSH is closed (host.firewall.public_ssh).
# keelbox reached sshd over the tailnet, so logins are refused unless they
# arrive at a tailnet (100.64.0.0/10, fd7a:115c:a1e0::/48) or loopback address.
# Recovery when the tailnet is gone: https://keelbox.dev/docs/guides/public-ssh.
Match LocalAddress *,!100.64.0.0/10,!fd7a:115c:a1e0::/48,!127.0.0.0/8,!::1
	DenyUsers *
`

// sshdTailnetLANConfig is the same for public_ssh: lan, which also lets in
// logins from the LAN's private ranges (config.LANRanges), whatever address
// they arrive at: a port forwarded from the internet keeps the client's
// public address, so it is still refused.
var sshdTailnetLANConfig = `# Managed by keelbox: public SSH is closed to the internet (host.firewall.public_ssh: lan).
# keelbox reached sshd over the tailnet, so logins are refused unless they
# arrive at a tailnet (100.64.0.0/10, fd7a:115c:a1e0::/48) or loopback address,
# or come from the LAN (` + strings.Join(config.LANRanges, ", ") + `).
# Recovery when the tailnet is gone: https://keelbox.dev/docs/guides/public-ssh.
Match LocalAddress *,!100.64.0.0/10,!fd7a:115c:a1e0::/48,!127.0.0.0/8,!::1 Address *,!` + strings.Join(config.LANRanges, ",!") + `
	DenyUsers *
`

func sshdClosedConfig(c *config.Config) string {
	if c.PublicSSHMode() == config.PublicSSHLAN {
		return sshdTailnetLANConfig
	}
	return sshdTailnetOnlyConfig
}

// PublicSSHProof is how keelbox knows the admin reaches sshd over the tailnet.
type PublicSSHProof struct {
	Address string    `json:"address"` // the box's tailnet address the session arrived at
	Client  string    `json:"client"`  // the laptop's address, as sshd saw it
	User    string    `json:"user"`
	Time    time.Time `json:"time"`
}

// PublicSSHStatus is the recorded state, readable by anyone on the box
// (nothing in it is secret); the laptop reads it to set the Hetzner Cloud
// Firewall.
type PublicSSHStatus struct {
	State  string          `json:"state"` // open or closed
	Reason string          `json:"reason"`
	Proof  *PublicSSHProof `json:"proof,omitempty"`
}

// ReadPublicSSHStatus reads the recorded state.
func ReadPublicSSHStatus(x system.FS) (PublicSSHStatus, bool) {
	data, err := x.ReadFile(PublicSSHStatusPath)
	if err != nil {
		return PublicSSHStatus{}, false
	}
	var st PublicSSHStatus
	if json.Unmarshal(data, &st) != nil {
		return PublicSSHStatus{}, false
	}
	return st, true
}

// sshPlan is what this run does with public SSH.
type sshPlan struct {
	closed bool
	// deferred is set when the config asks for closed but nothing proves
	// the tailnet: SSH stays open and the step waits.
	deferred string
	reason   string
	proof    *PublicSSHProof
}

func (p sshPlan) state() string {
	if p.closed {
		return "closed"
	}
	return "open"
}

// tailnetFacts are the box's Tailscale state and the addresses on tailscale0.
type tailnetFacts struct {
	running bool
	ips     []string // Tailscale's own addresses for the box
	iface   []string // addresses on tailscale0
}

func (t tailnetFacts) isTailnetAddr(a string) bool {
	addr, err := netip.ParseAddr(a)
	if err != nil {
		return false
	}
	a = addr.Unmap().String()
	return contains(t.ips, a) && contains(t.iface, a)
}

// TailnetSelf is the box's own entry in `tailscale status --json`.
type TailnetSelf struct {
	BackendState string
	DNSName      string
	IPs          []string
}

// ReadTailnetSelf asks tailscaled for the box's state and addresses.
func ReadTailnetSelf(ctx context.Context, x system.Executor) TailnetSelf {
	out, _ := system.Output(ctx, x, system.C("tailscale", "status", "--json"))
	return ParseTailnetSelf(out)
}

// ParseTailnetSelf reads `tailscale status --json`.
func ParseTailnetSelf(out string) TailnetSelf {
	var st struct {
		BackendState string
		Self         struct {
			DNSName      string
			TailscaleIPs []string
		}
	}
	if json.Unmarshal([]byte(out), &st) != nil {
		return TailnetSelf{}
	}
	return TailnetSelf{BackendState: st.BackendState, DNSName: strings.TrimSuffix(st.Self.DNSName, "."), IPs: st.Self.TailscaleIPs}
}

func readTailnetFacts(ctx context.Context, e *engine.Env) tailnetFacts {
	self := ReadTailnetSelf(ctx, e.X)
	f := tailnetFacts{running: self.BackendState == "Running", ips: self.IPs}
	out, err := system.Output(ctx, e.X, system.C("ip", "-j", "addr", "show", "dev", "tailscale0"))
	if err != nil {
		return f
	}
	var links []struct {
		AddrInfo []struct {
			Local string `json:"local"`
		} `json:"addr_info"`
	}
	if json.Unmarshal([]byte(out), &links) == nil {
		for _, l := range links {
			for _, a := range l.AddrInfo {
				f.iface = append(f.iface, a.Local)
			}
		}
	}
	return f
}

// parseSSHConnection splits $SSH_CONNECTION into the client and the local
// (server) address.
func parseSSHConnection(s string) (client, server string, ok bool) {
	f := strings.Fields(s)
	if len(f) != 4 {
		return "", "", false
	}
	c, err1 := netip.ParseAddr(f[0])
	sv, err2 := netip.ParseAddr(f[2])
	if err1 != nil || err2 != nil {
		return "", "", false
	}
	return c.Unmap().String(), sv.Unmap().String(), true
}

// planPublicSSH decides, from the config, the tailnet, the recorded proof
// and the session this run came over, whether public SSH is closed.
func planPublicSSH(ctx context.Context, e *engine.Env) sshPlan {
	mode := e.Cfg.PublicSSHMode()
	if mode == config.PublicSSHOpen {
		return sshPlan{reason: "host.firewall.public_ssh is open"}
	}
	if !e.Cfg.Tailscale() {
		return sshPlan{reason: "access.tailscale is disabled"}
	}
	rec, _ := ReadPublicSSHStatus(e.X)
	return decidePublicSSH(mode, readTailnetFacts(ctx, e), rec.Proof, e.SSHConnection, e.Facts.InvokingUser, e.Cfg.Users.Admin.Name, time.Now().UTC())
}

// decidePublicSSH is planPublicSSH's rule, without the box:
//   - A run whose session arrived at the box's tailnet address, as the
//     admin, proves the tailnet: public SSH closes (auto and closed).
//   - A run that arrived at any other address shows the laptop is not
//     using the tailnet: auto opens public SSH again and forgets the proof;
//     closed keeps an earlier proof that still holds, and otherwise waits.
//   - A run on the box itself keeps an earlier proof while the tailnet is
//     up and the address is still the box's. With the tailnet down, auto
//     opens public SSH (so the box stays reachable) and closed stays closed.
func decidePublicSSH(mode config.PublicSSHMode, t tailnetFacts, rec *PublicSSHProof, sshConn, invoking, admin string, now time.Time) sshPlan {
	recValid := rec != nil && t.running && t.isTailnetAddr(rec.Address)
	keep := func(reason string) sshPlan { return sshPlan{closed: true, reason: reason, proof: rec} }
	wait := func(reason string) sshPlan {
		return sshPlan{deferred: reason, reason: reason, proof: rec}
	}
	client, server, over := parseSSHConnection(sshConn)
	if over && t.running && t.isTailnetAddr(server) && invoking == admin {
		return sshPlan{closed: true, reason: fmt.Sprintf("%s reached sshd over the tailnet at %s", admin, server),
			proof: &PublicSSHProof{Address: server, Client: client, User: admin, Time: now}}
	}
	if over && t.running && !t.isTailnetAddr(server) {
		switch {
		case mode == config.PublicSSHClosed && recValid:
			return keep("host.firewall.public_ssh is closed and the tailnet proof of " + rec.Time.Format("2006-01-02") + " still holds")
		case mode == config.PublicSSHClosed:
			return wait("host.firewall.public_ssh is closed, but nothing proves the tailnet yet: run keelbox apply from a machine on the tailnet")
		}
		return sshPlan{reason: "this run reached sshd at " + server + ", not over the tailnet, so public SSH stays open"}
	}
	switch {
	case recValid:
		return keep(fmt.Sprintf("%s reached sshd over the tailnet at %s on %s", rec.User, rec.Address, rec.Time.Format("2006-01-02")))
	case mode == config.PublicSSHClosed && rec != nil && !t.running:
		return keep("host.firewall.public_ssh is closed; the tailnet is down, so the box is reachable only from its provider's console")
	case mode == config.PublicSSHClosed:
		return wait("host.firewall.public_ssh is closed, but keelbox has not reached sshd over the tailnet yet: run keelbox apply from a machine on the tailnet")
	case !t.running:
		return sshPlan{reason: "the box is not on the tailnet, so public SSH stays open (keelbox login, then keelbox apply)"}
	}
	return sshPlan{reason: "keelbox has not reached sshd over the tailnet yet, so public SSH stays open: run keelbox apply from a machine on the tailnet"}
}

// reloadSSHD reloads a running sshd. With socket activation and no daemon
// running, the next connection reads the new config by itself.
func reloadSSHD(ctx context.Context, e *engine.Env) error {
	for _, unit := range []string{"ssh.service", "sshd.service"} {
		if system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", unit)) {
			_, err := e.X.Run(ctx, system.C("systemctl", "reload", unit))
			return err
		}
	}
	return nil
}

// sshdTailnetOnlyStep writes the sshd drop-in while public SSH is closed and
// removes it otherwise.
func sshdTailnetOnlyStep() engine.Step {
	present := func(e *engine.Env) engine.Step {
		return engine.FileStep("sshd-tailnet-only", engine.File{
			Path: sshdTailnetOnly, Content: []byte(sshdClosedConfig(e.Cfg)), Mode: 0o644,
			Validate: func(ctx context.Context, e *engine.Env) error {
				_ = e.X.MkdirAll("/run/sshd", 0o755, "root", "root")
				_, err := e.X.Run(ctx, system.C("sshd", "-t"))
				return err
			},
			OnChange: reloadSSHD,
		})
	}
	return engine.Step{
		Name: "sshd-tailnet-only",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			if planPublicSSH(ctx, e).closed {
				return present(e).Check(ctx, e)
			}
			st, err := e.X.Stat(sshdTailnetOnly)
			if err != nil || !st.Exists {
				return engine.Converged(), err
			}
			return engine.Pending("remove %s: public SSH opens again", sshdTailnetOnly), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			if planPublicSSH(ctx, e).closed {
				return present(e).Apply(ctx, e)
			}
			if err := e.X.Remove(sshdTailnetOnly); err != nil {
				return err
			}
			return reloadSSHD(ctx, e)
		},
	}
}

// publicSSHStateStep records the decision, with its proof, for doctor, the
// laptop (which sets the Hetzner Cloud Firewall from it) and later runs on
// the box. It defers while the config asks for closed without a proof.
func publicSSHStateStep() engine.Step {
	same := func(cur PublicSSHStatus, ok bool, p sshPlan) bool {
		if !ok || cur.State != p.state() || (cur.Proof == nil) != (p.proof == nil) {
			return false
		}
		return cur.Proof == nil || (cur.Proof.Address == p.proof.Address && cur.Proof.User == p.proof.User)
	}
	return engine.Step{
		Name: "public-ssh-state",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			p := planPublicSSH(ctx, e)
			cur, ok := ReadPublicSSHStatus(e.X)
			switch {
			case !same(cur, ok, p):
				return engine.Pending("record public SSH %s: %s", p.state(), p.reason), nil
			case p.deferred != "":
				return engine.Pending("close public SSH: %s", p.deferred), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			p := planPublicSSH(ctx, e)
			if cur, ok := ReadPublicSSHStatus(e.X); !same(cur, ok, p) {
				data, err := json.MarshalIndent(PublicSSHStatus{State: p.state(), Reason: p.reason, Proof: p.proof}, "", "  ")
				if err != nil {
					return err
				}
				if err := e.X.MkdirAll("/var/lib/keelbox", 0o755, "root", "root"); err != nil {
					return err
				}
				if err := e.X.WriteFile(PublicSSHStatusPath, append(data, '\n'), 0o644, "root", "root"); err != nil {
					return err
				}
			}
			if p.deferred != "" {
				return engine.Defer("%s", p.deferred)
			}
			return nil
		},
	}
}

// PublicSSHClosed reports whether the box has closed public SSH, as recorded.
func PublicSSHClosed(e *engine.Env) bool {
	st, ok := ReadPublicSSHStatus(e.X)
	return ok && st.State == "closed"
}

// verifyPublicSSH reports the state, and when closed checks UFW and sshd
// enforce it.
func verifyPublicSSH(ctx context.Context, e *engine.Env) []engine.Finding {
	p := planPublicSSH(ctx, e)
	mode := e.Cfg.PublicSSHMode()
	if !p.closed {
		switch {
		case p.deferred != "":
			return []engine.Finding{fail("host", "public-ssh", "open: "+p.deferred)}
		case mode == config.PublicSSHOpen || !e.Cfg.Tailscale():
			return []engine.Finding{ok("host", "public-ssh", "open (LAN and internet): "+p.reason)}
		}
		return []engine.Finding{warn("host", "public-ssh", "open: "+p.reason)}
	}
	lan := mode == config.PublicSSHLAN
	closed := "closed: "
	if lan {
		closed = "closed to the internet, open to the LAN: "
	}
	out := []engine.Finding{ok("host", "public-ssh", closed+p.reason)}
	if e.Cfg.FirewallEnabled() {
		s, _ := readUFW(ctx, e)
		if s.ssh {
			out = append(out, fail("host", "public-ssh-firewall", "ufw still admits 22/tcp from anywhere; run keelbox apply"))
		} else {
			out = append(out, ok("host", "public-ssh-firewall", map[bool]string{false: "ufw admits SSH only on tailscale0", true: "ufw admits SSH only on tailscale0 and from the LAN"}[lan]))
		}
	}
	admin := e.Cfg.Users.Admin.Name
	public, _ := system.SSHDConfig(ctx, e.X, "-C", "user="+admin+",host=keelbox-check,addr=192.0.2.1,laddr=192.0.2.2,lport=22")
	tailnet, _ := system.SSHDConfig(ctx, e.X, "-C", "user="+admin+",host=keelbox-check,addr=100.64.0.1,laddr="+p.proof.Address+",lport=22")
	fromLAN := ""
	if lan {
		fromLAN, _ = system.SSHDConfig(ctx, e.X, "-C", "user="+admin+",host=keelbox-check,addr=192.168.1.10,laddr=192.0.2.2,lport=22")
	}
	switch {
	case lan && strings.Contains(fromLAN, "denyusers *"):
		out = append(out, fail("host", "public-ssh-sshd", "sshd refuses logins from the LAN, which host.firewall.public_ssh: lan keeps; run keelbox apply"))
	case strings.Contains(public, "denyusers *") && !strings.Contains(tailnet, "denyusers *"):
		out = append(out, ok("host", "public-ssh-sshd", map[bool]string{false: "sshd refuses logins that do not arrive over the tailnet or loopback", true: "sshd refuses logins that do not arrive over the tailnet or loopback, or come from the LAN"}[lan]))
	default:
		out = append(out, fail("host", "public-ssh-sshd", "sshd's effective config does not refuse logins on public addresses; run keelbox apply"))
	}
	return out
}
