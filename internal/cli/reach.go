package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/modules"
)

// Reaching the box over the tailnet, and closing public SSH once that works
// (docs/adr/0021-close-public-ssh.md). The laptop remembers the tailnet
// address it reached the box at, next to its known_hosts, and connects
// there first; target.ssh.tailnet_host overrides it.

// boxRecord is what this laptop knows about reaching a box over the tailnet.
type boxRecord struct {
	TailnetHost string    `json:"tailnet_host"`
	TailnetPort int       `json:"tailnet_port"`
	TailnetName string    `json:"tailnet_name,omitempty"`
	Reached     time.Time `json:"reached"`
}

func recordPath(cfg *config.Config) string {
	return filepath.Join(filepath.Dir(knownHosts(cfg)), "boxes", cfg.Box+".json")
}

func loadRecord(cfg *config.Config) (boxRecord, bool) {
	data, err := os.ReadFile(recordPath(cfg))
	if err != nil {
		return boxRecord{}, false
	}
	var r boxRecord
	if json.Unmarshal(data, &r) != nil || r.TailnetHost == "" {
		return boxRecord{}, false
	}
	return r, true
}

func saveRecord(cfg *config.Config, r boxRecord) error {
	p := recordPath(cfg)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, append(data, '\n'), 0o600)
}

func forgetRecord(cfg *config.Config) { _ = os.Remove(recordPath(cfg)) }

// tailnetAddr is where to try the box over the tailnet: the configured
// target.ssh.tailnet_host, or the address this laptop last reached it at.
func tailnetAddr(cfg *config.Config) (addr, bool) {
	if h, p := cfg.TailnetHostPort(); h != "" {
		return addr{h, p}, true
	}
	if r, ok := loadRecord(cfg); ok {
		port := r.TailnetPort
		if port == 0 {
			port = cfg.Target.SSH.Port
		}
		return addr{r.TailnetHost, port}, true
	}
	return addr{}, false
}

func knownHostsName(a addr) string {
	if a.port == 0 || a.port == 22 {
		return a.host
	}
	return "[" + a.host + "]:" + strconv.Itoa(a.port)
}

// dialable reports whether something accepts TCP connections at a.
func dialable(ctx context.Context, a addr, timeout time.Duration) error {
	d := net.Dialer{Timeout: timeout}
	c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(a.host, strconv.Itoa(a.port)))
	if err != nil {
		return err
	}
	return c.Close()
}

// reach picks the address for this run: the tailnet address when one is
// known, its host keys are pinned (copied from the public address, so
// nothing new is trusted) and it accepts a connection; otherwise the public
// address.
func reach(ctx context.Context, cfg *config.Config, public addr, stderr io.Writer) addr {
	if !cfg.Tailscale() {
		return public
	}
	t, ok := tailnetAddr(cfg)
	if !ok || t == public {
		return public
	}
	s := sshTarget(cfg, t, options{}, io.Discard, io.Discard)
	if public.host != "" {
		_ = s.CopyPins(public.host, public.port)
	}
	if !s.IsKnown() && public.host != "" {
		return public // never ask the user to trust a key for the tailnet name
	}
	err := dialable(ctx, t, 8*time.Second)
	if err == nil {
		return t
	}
	if public.host == "" {
		return t
	}
	notef(stderr, "no answer over the tailnet at %s (%v); trying the public address %s", t, err, public)
	return public
}

// pickTailnetIP is the address to reach a box at on the tailnet: its first
// IPv4 Tailscale address, else its first one.
func pickTailnetIP(ips []string) string {
	for _, ip := range ips {
		if a, err := netip.ParseAddr(ip); err == nil && a.Is4() {
			return ip
		}
	}
	if len(ips) > 0 {
		return ips[0]
	}
	return ""
}

// closeOverTailnet runs after an apply that reached the box over its public
// address. If the box is on the tailnet and this machine reaches its sshd
// there as the admin, it applies again over the tailnet: that apply is the
// proof the box needs to close public SSH. Otherwise public SSH stays open
// and it says why. A failure to reach the tailnet is not an error.
func closeOverTailnet(ctx context.Context, cfg *config.Config, public addr, o options, stdout, stderr io.Writer) (int, addr) {
	if !cfg.Tailscale() || cfg.PublicSSHMode() == config.PublicSSHOpen {
		return 0, public
	}
	// Reached over the tailnet already (target.ssh.host is the tailnet
	// name, say): that apply was the proof.
	if st, err := boxPublicSSH(ctx, cfg, public); err == nil && st.State == "closed" {
		return 0, public
	}
	admin := o
	admin.sshUser, admin.askSudo = "", false
	pub := sshTarget(cfg, public, admin, io.Discard, io.Discard)
	out, err := pub.Output(ctx, "tailscale status --json 2>/dev/null || true")
	if err != nil {
		say(stdout, o.style, o.style.Bang(), fmt.Sprintf("public SSH stays open: could not read the box's tailnet state: %v", err))
		return 0, public
	}
	self := modules.ParseTailnetSelf(out)
	if self.BackendState != "Running" {
		if o.upLast {
			return 0, public // the Tailscale sign-in was skipped; Ready says so
		}
		next := fmt.Sprintf(" Run keelbox login %s, then keelbox apply %s.", cfg.Box, cfg.Box)
		if o.inUp {
			next = " keelbox up closes it after the Tailscale sign-in."
		}
		say(stdout, o.style, o.style.Bang(), "public SSH stays open: the box is not on the tailnet yet."+next)
		return 0, public
	}
	t, ok := addr{}, false
	if h, p := cfg.TailnetHostPort(); h != "" {
		t, ok = addr{h, p}, true
	} else if ip := pickTailnetIP(self.IPs); ip != "" {
		t, ok = addr{ip, cfg.Target.SSH.Port}, true
	}
	if !ok {
		say(stdout, o.style, o.style.Bang(), "public SSH stays open: the box reports no tailnet address")
		return 0, public
	}
	ts := sshTarget(cfg, t, admin, stdout, stderr)
	if err := ts.CopyPins(public.host, public.port); err != nil {
		fail(stderr, err)
		return 1, public
	}
	// Right after joining, the tailnet path can take a few seconds.
	var derr error
	for i := 0; i < 4; i++ {
		if derr = dialable(ctx, t, 5*time.Second); derr == nil {
			break
		}
		select {
		case <-ctx.Done():
			return 1, public
		case <-time.After(time.Second):
		}
	}
	if derr == nil {
		derr = ts.WaitReachable(ctx, 30*time.Second)
	}
	if derr != nil {
		say(stdout, o.style, o.style.Bang(), fmt.Sprintf("public SSH stays open: the box is on the tailnet at %s, but this machine cannot reach its sshd there (%v).", t, derr))
		fmt.Fprintln(stdout, "  Join this machine to the same tailnet, or set target.ssh.tailnet_host; host.firewall.public_ssh: open skips this check.")
		return 0, public
	}
	if err := saveRecord(cfg, boxRecord{TailnetHost: t.host, TailnetPort: t.port, TailnetName: self.DNSName, Reached: time.Now().UTC()}); err != nil {
		notef(stderr, "warning: could not record the tailnet address: %v", err)
	}
	fmt.Fprintln(stdout)
	say(stdout, o.style, o.style.Tick(), fmt.Sprintf("reached sshd over the tailnet at %s; applying over the tailnet, which closes public SSH", t))
	return runSSH(ctx, "apply", cfg, t, admin, stdout, stderr), t
}

// boxPublicSSH reads the box's recorded public SSH state over a.
func boxPublicSSH(ctx context.Context, cfg *config.Config, a addr) (modules.PublicSSHStatus, error) {
	out, err := sshTarget(cfg, a, options{}, io.Discard, io.Discard).Output(ctx, "cat "+modules.PublicSSHStatusPath+" 2>/dev/null || true")
	if err != nil {
		return modules.PublicSSHStatus{}, err
	}
	var st modules.PublicSSHStatus
	if strings.TrimSpace(out) == "" {
		return modules.PublicSSHStatus{State: "open"}, nil
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		return modules.PublicSSHStatus{}, fmt.Errorf("read %s: %w", modules.PublicSSHStatusPath, err)
	}
	return st, nil
}
