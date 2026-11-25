package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/keelbox/keelbox/internal/ui"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/services"
)

// keelbox up on hardware people already have: this machine, or a server
// over SSH. Before anything changes it checks the machine (Ubuntu, sudo),
// says what keelbox will change there, and offers to keep the services it
// finds listening reachable on the LAN.

// localLikely reports whether keelbox up was probably started on the box
// itself: run as root, or on Ubuntu with no graphical session.
var localLikely = func() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if os.Geteuid() == 0 {
		return true
	}
	data, err := os.ReadFile("/etc/os-release")
	if err != nil || engine.ParseEnvFile(string(data))["ID"] != "ubuntu" {
		return false
	}
	return os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("XDG_CURRENT_DESKTOP") == ""
}

// localWhy says why this machine is taken to be the box, for the question
// that preselects it; tests replace it.
var localWhy = func() string {
	if os.Geteuid() == 0 {
		return "keelbox up runs as root here"
	}
	return "this is Ubuntu without a desktop session"
}

// prepareLocal checks this machine before any question about it: it must
// be Ubuntu, sudo must work (asked for now, once, not halfway through the
// apply), and the probe says what is running here.
func (w *wizard) prepareLocal(ctx context.Context, a *answers) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("this machine runs %s, and keelbox sets up Ubuntu. Set up a server from here: keelbox up --provider ssh; or on the Ubuntu machine itself: curl -fsSL https://get.keelbox.dev | sh -s -- up", runtime.GOOS)
	}
	if os.Geteuid() != 0 {
		if w.p.Interactive() {
			w.hint("keelbox sets up this machine with sudo; sudo may ask for your password now.")
		}
		if !w.run.sudo(ctx, w.p.Interactive(), w.out) {
			if !w.p.Interactive() {
				return w.needFlag("sudo needs a password here", "sudo", `sudo "$(command -v keelbox)" up `+firstNonEmpty(w.box, "NAME")+" --provider local --yes")
			}
			return errors.New("sudo did not work, so keelbox cannot change this machine; check you are in the sudo group (groups), or run: sudo \"$(command -v keelbox)\" up")
		}
	}
	f, err := w.run.inspect(ctx, nil, "", w.out)
	if err != nil {
		return fmt.Errorf("look at this machine: %w", err)
	}
	if err := f.supported(); err != nil {
		return err
	}
	if !f.Sudo {
		return errors.New(`sudo does not work without a password after asking for it; run: sudo "$(command -v keelbox)" up`)
	}
	if w.p.Interactive() {
		w.ok(f.summary())
	}
	a.facts = &f
	return nil
}

// inspectServer connects to the server as the account the user named,
// before anything changes: it pins the host key (asking on the terminal
// when it is not given), and checks Ubuntu and passwordless sudo, which
// keelbox's prompt-free SSH needs. A key the server does not take yet is
// offered to be put there (installKey), and the check runs again.
func (w *wizard) inspectServer(ctx context.Context, a *answers) error {
	s := a.starter
	if s.SSHHost == "" {
		return nil
	}
	if s.Box == "" {
		s.Box = "devbox" // named after the check, from the server's hostname
	}
	out, err := s.Render()
	if err != nil {
		return err
	}
	cfg, err := config.Parse([]byte(out), s.Box+".yaml")
	if err != nil {
		return err
	}
	user := firstNonEmpty(w.o.sshUser, "root")
	var f machineFacts
	for tries := 0; ; tries++ {
		if w.p.Interactive() {
			if w.st.Live {
				say(w.out, w.st, w.st.Pointer(), fmt.Sprintf("checking %s as %s (nothing changes yet)...", s.SSHHost, user))
			} else {
				fmt.Fprintf(w.out, "Checking %s as %s (nothing changes yet)...\n", s.SSHHost, user)
			}
		}
		f, err = w.run.inspect(ctx, cfg, user, w.out)
		// On a terminal, the account the server's image names is taken: the
		// summary shows it before anything changes.
		var acct *wrongAccount
		if errors.As(err, &acct) && w.p.Interactive() && tries < 3 {
			w.hint(fmt.Sprintf("%s. Checking as %s instead.", acct.Error(), acct.want))
			user, w.o.sshUser = acct.want, acct.want
			w.remember("ssh-user", user)
			continue
		}
		var refused *keyRefused
		if err == nil || !errors.As(err, &refused) {
			break
		}
		if !w.p.Interactive() || tries == 3 {
			return &needsYouError{refused.Error() + "\n" + w.keyInstructions(a, refused)}
		}
		if err := w.installKey(ctx, a, cfg, refused); err != nil {
			return err
		}
	}
	if err != nil {
		return w.connectFix(err)
	}
	if err := f.supported(); err != nil {
		return err
	}
	switch {
	case !f.Sudo && !f.SudoPassword:
		return fmt.Errorf("%s cannot use sudo on %s, and keelbox needs root there once, to set the box up. Log in as an account that can (keelbox up --ssh-user root), or add %s to the sudo group there: sudo usermod -aG sudo %s", user, s.SSHHost, user, user)
	case !f.Sudo && !w.p.Interactive():
		// sudo asks on a terminal, and there is none to give it.
		return &needsYouError{fmt.Sprintf("%s's sudo on %s asks for a password, which sudo reads on a terminal. Run keelbox up on one: keelbox up %s\n"+
			"sudo asks there, over SSH, and keelbox never sees the password", user, s.SSHHost, firstNonEmpty(w.box, s.Box))}
	}
	if w.p.Interactive() {
		w.ok(f.summary())
		if !f.Sudo {
			text := fmt.Sprintf("sudo on %s asks for %s's password when keelbox needs root there: once to look (read only), once for the first apply. "+
				"sudo reads it on this terminal itself; keelbox never sees or keeps it. After that keelbox uses its own admin account.", s.SSHHost, user)
			if w.st.Live {
				w.hint(text)
			} else {
				fmt.Fprintln(w.out, "  "+text)
			}
		}
	}
	a.facts = &f
	keepMachineSettings(a)
	return nil
}

// compareHostKey is where the user reads a server's host key to compare.
const compareHostKey = "the provider's console, or ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub on the server"

// connectFix ends an error from the first connection that another flag
// resolves with the command to run: the account the server's image names,
// or the server's host key. Before the box's config is written there is no
// target.ssh.host_key to set, so the fix is a flag. A fingerprint is never
// filled in from what the server offered: comparing it is the user's step.
func (w *wizard) connectFix(err error) error {
	var acct *wrongAccount
	var hk *provider.HostKeyError
	switch {
	case errors.As(err, &acct):
		return fmt.Errorf("%w. Run it as that account: %s", err, w.upCommand(map[string]string{"ssh-user": acct.want}))
	case errors.As(err, &hk) && hk.Want != "":
		// A key that is not the one asked for is not offered as the answer.
		return fmt.Errorf("%s does not offer the host key %s given for it; it offers %s. keelbox did not connect. Read the server's key in %s, and pass that one: %s",
			hk.Host, hk.Want, hk.Fingerprints(), compareHostKey, w.upCommand(map[string]string{"host-key": hostKeyPlaceholder}))
	case errors.As(err, &hk):
		// What it offers is listed, not filled in: nobody has compared it
		// yet, and a command that runs as printed would skip that step.
		return fmt.Errorf("%s is not a host keelbox has connected to, and no terminal is attached to confirm its host key. It offers %s. Read the server's key in %s; if it is one of these, pass it: %s",
			hk.Host, hk.Fingerprints(), compareHostKey, w.upCommand(map[string]string{"host-key": hostKeyPlaceholder}))
	}
	return err
}

// keyLine is the command that adds the key on the server, run as the
// account keelbox logs in as, from the provider's console or any session.
func keyLine(pub string) string {
	return "mkdir -p ~/.ssh && chmod 700 ~/.ssh && echo '" + strings.ReplaceAll(pub, "'", "") + "' >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys"
}

// keyInstructions are the two ways to put the key on the server.
func (w *wizard) keyInstructions(a *answers, r *keyRefused) string {
	pub := firstNonEmpty(a.pubKey, "<your public key>")
	path := "~/.ssh/id_ed25519.pub"
	if len(a.starter.KeyFiles) > 0 {
		path = a.starter.KeyFiles[0]
	}
	return fmt.Sprintf("Put your key there in one of two ways:\n"+
		"  with a password or another key that logs in now: ssh-copy-id -i %s %s\n"+
		"  in the provider's console (or any session on the server), as %s:\n    %s",
		path, r.dest, r.user, keyLine(pub))
}

// installKey puts the user's public key on the server: with ssh-copy-id,
// which logs in with whatever works now (a password, another key), or by
// the user in the provider's console, from the line keelbox prints.
func (w *wizard) installKey(ctx context.Context, a *answers, cfg *config.Config, r *keyRefused) error {
	path := "~/.ssh/id_ed25519.pub"
	if len(a.starter.KeyFiles) > 0 {
		path = a.starter.KeyFiles[0]
	}
	fmt.Fprintln(w.out)
	w.hint(fmt.Sprintf("%s does not take your key %s for %s yet; keelbox logs in with it, so it has to be there first.", r.host, path, r.user))
	choices := []prompt.Choice{
		{Key: "copy", Label: "Copy it with ssh-copy-id", Detail: "logs in with a password or another key that works now"},
		{Key: "console", Label: "Add it in the provider's console", Detail: "keelbox prints the line to paste"},
		{Key: "stop", Label: "Stop here", Detail: "your answers are kept; keelbox up carries on later"},
	}
	if !haveCopyID() {
		choices = choices[1:]
	}
	i, err := w.p.Choose("How should the key get there?", choices, 0)
	if err != nil {
		return err
	}
	switch choices[i].Key {
	case "stop":
		return errStopHere
	case "copy":
		if err := w.run.copyID(ctx, cfg, r.user, config.ExpandHome(path), w.out); err == nil {
			return nil
		}
		w.hint("ssh-copy-id did not put the key there. The provider's console works without a password:")
	}
	fmt.Fprintf(w.out, "\nIn the provider's console (or any session on %s), as %s, run:\n  %s\n", r.host, r.user, w.st.Cmd(keyLine(firstNonEmpty(a.pubKey, "<your public key>"))))
	return w.p.Wait("Press Enter once it has run.")
}

// haveCopyID reports whether ssh-copy-id is installed; tests replace it.
var haveCopyID = func() bool {
	_, err := exec.LookPath("ssh-copy-id")
	return err == nil
}

// askLANSSH: on a box on the user's own network, SSH from the LAN can stay
// open once SSH closes to the internet (public_ssh: lan), so the desktop
// beside a home server keeps reaching it without Tailscale.
func (w *wizard) askLANSSH(a *answers) error {
	s := &a.starter
	if w.o.publicSSH != "" {
		switch m := config.PublicSSHMode(w.o.publicSSH); m {
		case config.PublicSSHAuto, config.PublicSSHLAN, config.PublicSSHOpen, config.PublicSSHClosed:
			s.PublicSSH = string(m)
		default:
			return fmt.Errorf("--public-ssh %q: use auto, lan, open or closed", w.o.publicSSH)
		}
		w.remember("public-ssh", s.PublicSSH)
		return nil
	}
	if s.NoTailscale || !onLAN(a) {
		return nil
	}
	keep := true
	if w.p.Interactive() {
		w.hint("Once keelbox reaches the box over Tailscale, SSH from anywhere else closes, your LAN included. A yes keeps it open to the LAN (host.firewall.public_ssh: lan in the config).")
		ok, err := w.p.Confirm("Keep SSH open to your LAN as well?", true)
		if err != nil {
			return err
		}
		keep = ok
	}
	s.PublicSSH = map[bool]string{true: string(config.PublicSSHLAN), false: string(config.PublicSSHAuto)}[keep]
	w.remember("public-ssh", s.PublicSSH)
	return nil
}

// onLAN reports whether the box is on the user's own network: this
// machine, or a server keelbox reaches at a private address.
var onLAN = func(a *answers) bool {
	switch a.via {
	case viaLocal:
		return true
	case viaSSH:
		host := a.starter.SSHHost
		addrs := []string{host}
		if _, err := netip.ParseAddr(host); err != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			addrs, _ = net.DefaultResolver.LookupHost(ctx, host)
		}
		for _, h := range addrs {
			if ip, err := netip.ParseAddr(h); err == nil && (ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
				return true
			}
		}
	}
	return false
}

// keepMachineSettings keeps a machine's own clock and language: keelbox up
// on hardware the user has changes neither.
func keepMachineSettings(a *answers) {
	if config.ValidTimezone(a.facts.Timezone) {
		a.starter.Timezone = a.facts.Timezone
	}
	if config.ValidLocale(a.facts.Locale) {
		a.starter.Locale = a.facts.Locale
	}
}

// askLANPorts shows the services the firewall would block from the
// network and asks which stay open to the LAN: one question for each
// known service (all its ports, discovery included), then one for the
// other ports.
func (w *wizard) askLANPorts(a *answers) error {
	cs := lanCandidates(a)
	if len(cs) == 0 {
		if w.o.lanPorts != "" && w.o.lanPorts != "none" && w.o.lanPorts != "all" {
			return fmt.Errorf("--lan-ports %s: nothing listening on the network there would be blocked", w.o.lanPorts)
		}
		return nil
	}
	var picked []lanChoice
	var err error
	if w.o.lanPorts != "" && w.drafted["lan-ports"] && w.p.Interactive() {
		// An answer kept from an earlier run that no longer fits what
		// listens there is asked again.
		if _, err := pickPorts(w.o.lanPorts, cs); err != nil {
			w.o.lanPorts = ""
		}
	}
	switch {
	case w.o.lanPorts != "":
		if picked, err = pickPorts(w.o.lanPorts, cs); err != nil {
			return fmt.Errorf("--lan-ports: %w", err)
		}
	case !w.p.Interactive():
		var keys []string
		for _, c := range cs {
			keys = append(keys, c.Key)
		}
		return w.needFlag(fmt.Sprintf("the firewall would block %s, listening there now", strings.Join(keys, ", ")),
			"--lan-ports with the ones to keep open to the LAN (or none)", "keelbox up "+a.starter.Box+" --lan-ports all")
	default:
		if picked, err = w.askLANChoices(a, cs); err != nil {
			return err
		}
	}
	a.starter.Allow = keepRules(picked)
	w.remember("lan-ports", lanAnswer(picked))
	kept := map[string]bool{}
	for _, c := range picked {
		kept[c.Key] = true
	}
	for _, c := range cs {
		if !kept[c.Key] {
			a.blocked = append(a.blocked, c)
		}
	}
	return nil
}

// lanAnswer is the --lan-ports value for the choices kept.
func lanAnswer(picked []lanChoice) string {
	if len(picked) == 0 {
		return "none"
	}
	var keys []string
	for _, c := range picked {
		keys = append(keys, c.Key)
	}
	return strings.Join(keys, ",")
}

func (w *wizard) askLANChoices(a *answers, cs []lanChoice) ([]lanChoice, error) {
	where := "on this machine"
	if a.via == viaSSH {
		where = "on " + a.starter.SSHHost
	}
	title := fmt.Sprintf("Listening %s now; the firewall will block these from the network:", where)
	var rows []ui.Line
	if w.st.Live {
		title = "Listening " + where + " now"
		rows = append(rows, ui.Text(w.st.Dim("The firewall will block these from the network.")), ui.Blank())
	}
	var ports []lanChoice
	for _, c := range cs {
		if c.Service != "" {
			rows = append(rows, ui.Row(c.Key, c.Label+": "+strings.Join(c.Ports, ", ")))
		} else {
			rows = append(rows, ui.Row(c.Key, c.Label))
			ports = append(ports, c)
		}
	}
	fmt.Fprint(w.out, "\n"+w.st.Card(ui.Card{Title: title, Lines: rows, KeyWidth: 9}))
	if !a.starter.NoTailscale {
		w.hint("They stay reachable over Tailscale. Keep any open to your local network (LAN) as well?")
	}
	var picked []lanChoice
	for _, c := range cs {
		if c.Service == "" {
			continue
		}
		s, _ := services.Lookup(c.Service)
		line := "  {service: " + s.Name + "} opens " + s.Summary()
		if n := s.DiscoveryNote(); n != "" {
			line += "; " + n
		}
		if w.st.Live {
			w.hint(strings.TrimSpace(line) + ".")
		} else {
			fmt.Fprintln(w.out, line+".")
		}
		ok, err := w.p.Confirm("Keep "+s.Title+" reachable on your LAN?", true)
		if err != nil {
			return nil, err
		}
		if ok {
			picked = append(picked, c)
		}
	}
	if len(ports) == 0 {
		return picked, nil
	}
	q := "Ports to keep open on the LAN"
	if len(ports) < len(cs) {
		q = "Other ports to keep open on the LAN"
	}
	w.hint("Answer all, none, or port numbers such as " + fmt.Sprint(ports[0].l.Port) + ".")
	ans, err := w.p.AskValid(q, "all", func(s string) error {
		_, err := pickPorts(s, ports)
		return err
	})
	if err != nil {
		return nil, err
	}
	more, _ := pickPorts(ans, ports)
	return append(picked, more...), nil
}

// askAgentLimits: on a machine that runs other services, agents share its
// CPU and memory with them. up proposes leaving them a quarter
// (users.agent.limits); on a machine of its own, the agent has the default
// memory ceiling and no other limit.
func (w *wizard) askAgentLimits(a *answers) error {
	if v := w.o.agentLimits; v != "" {
		switch v = strings.TrimSuffix(strings.TrimSpace(v), "%"); v {
		case "none", "0", "off":
			a.starter.AgentLimit, a.starter.AgentUnlimited = 0, true
		default:
			n, err := strconv.Atoi(v)
			if err != nil || n < 10 || n > 95 {
				return fmt.Errorf("--limits %q: use a share from 10%% to 95%%, such as 75%%, or none", w.o.agentLimits)
			}
			a.starter.AgentLimit = n
		}
		w.remember("limits", w.o.agentLimits)
		return nil
	}
	if a.via != viaLocal && a.via != viaSSH {
		return nil
	}
	others := otherServices(a)
	if len(others) == 0 {
		return nil
	}
	limit := true
	// --yes takes the proposal, as without a terminal; the summary says so.
	if w.p.Interactive() && !w.o.yes {
		where := "there"
		if a.via == viaLocal {
			where = "here"
		}
		fmt.Fprintln(w.out)
		w.hint(fmt.Sprintf("Other services run %s (%s). Agents can take all the CPU and %s of the memory unless limited further (users.agent.limits in the config).", where, strings.Join(others, ", "), config.DefaultMemoryLimit))
		ok, err := w.p.Confirm(fmt.Sprintf("Limit the agent to %d%% of CPU and memory, so the other services keep the rest?", config.SuggestedLimit), true)
		if err != nil {
			return err
		}
		limit = ok
	}
	if limit {
		a.starter.AgentLimit = config.SuggestedLimit
		w.remember("limits", fmt.Sprintf("%d%%", config.SuggestedLimit))
	} else {
		w.remember("limits", "none")
	}
	return nil
}

// otherServices names what runs on the machine besides SSH: known services
// by name, then a count of the rest (listeners and running containers).
func otherServices(a *answers) []string {
	var ls []listener
	var containers []string
	if a.impact != nil && a.impact.Machine != nil {
		ls = a.impact.Machine.Listeners
		for _, c := range a.impact.Machine.Docker.Containers {
			if c.Running {
				containers = append(containers, c.Name)
			}
		}
	}
	if len(ls) == 0 && a.facts != nil {
		ls = a.facts.Listeners
	}
	// Known services first, so a port of theirs outside the preset (or
	// another listener of the same container) does not count again.
	seen := map[string]bool{}
	var known []string
	for _, l := range ls {
		svc, ok := services.Lookup(l.Service)
		if !ok || l.Service == "" {
			continue
		}
		if !seen["s:"+svc.Name] {
			known = append(known, svc.Title)
		}
		seen["s:"+svc.Name] = true
		if l.Container != "" {
			seen["c:"+l.Container] = true
		}
	}
	sort.Strings(known)
	rest := 0
	for _, l := range ls {
		key := "p:" + firstNonEmpty(l.Process, l.Key())
		if l.Container != "" {
			key = "c:" + l.Container
		}
		if l.Service != "" || seen[key] {
			continue
		}
		seen[key] = true
		rest++
	}
	for _, c := range containers {
		if !seen["c:"+c] {
			seen["c:"+c] = true
			rest++
		}
	}
	if rest > 0 {
		known = append(known, plural(rest, "other service", "other services"))
	}
	return known
}

// machineChanges is the short warning before going ahead on hardware the
// user already has: what keelbox changes there, and what it leaves.
func (w *wizard) machineChanges(a *answers) []string {
	var out []string
	in := "SSH, Tailscale"
	if a.starter.NoTailscale {
		in = "SSH"
	}
	if len(a.starter.Allow) > 0 {
		in += " and, from the LAN, " + config.AllowSummary(a.starter.Allow)
	}
	out = append(out, "SSH takes keys only: password and root logins stop.")
	if a.via == viaLocal {
		out = append(out, "  Keep this terminal open until you have logged in as admin with your key.")
	}
	fw := "The UFW firewall turns on: inbound only " + in + "."
	if a.impact != nil && a.impact.Machine != nil && a.impact.Machine.UFW.Blocks() {
		fw = "The UFW firewall, on already, keeps its rules and adds " + in + "."
	}
	out = append(out, fw)
	if len(a.blocked) > 0 {
		var keys []string
		for _, c := range a.blocked {
			keys = append(keys, c.Key)
		}
		out = append(out, "  Blocked from the network: "+strings.Join(keys, ", ")+".")
	}
	tools := []string{"Docker", "Node"}
	if !a.starter.NoTailscale {
		tools = append(tools, "Tailscale")
	}
	if a.starter.Agents == nil || len(a.starter.Agents) > 0 {
		tools = append(tools, "the agent CLIs")
	}
	last := len(tools) - 1
	out = append(out, "New accounts admin and agent; "+strings.Join(tools[:last], ", ")+" and "+tools[last]+" installed.")
	if f := a.facts; f != nil && f.User != "" && f.User != "root" && f.User != "admin" {
		if a.via == viaSSH && contains(f.CloudUsers, f.User) {
			out = append(out, "The image's account "+f.User+" is locked once admin logs in with your key.")
		} else {
			out = append(out, "Your account "+f.User+" keeps its password and sudo.")
		}
	}
	if f := a.facts; f != nil && f.Hostname != "" && f.Hostname != a.starter.Box {
		if a.rename {
			out = append(out, "Its hostname "+f.Hostname+" becomes "+a.starter.Box+".")
		} else {
			out = append(out, "It keeps its hostname, "+f.Hostname+"; keelbox calls the box "+a.starter.Box+".")
		}
	}
	if f := a.facts; f != nil && f.Desktop != "" {
		out = append(out, "This looks like a desktop ("+f.Desktop+"). keelbox leaves the desktop alone, but the",
			"  firewall blocks what you did not keep open, such as casting or file sharing.")
	}
	out = append(out, "No keelbox command undoes this. The steps by hand: "+docsURL+"/guides/removing-a-box#keep-the-machine-undo-keelbox")
	return out
}

// offerUnfinished: keelbox up with no name, when boxes were left part way,
// asks in one menu whether to continue one or set up another. The only
// unfinished box can also be discarded there, while up has no server for it
// (upState.noServer); otherwise the menu names the commands that remove a
// box. stop is the menu having done all there was to do, with exit code.
func (w *wizard) offerUnfinished() (name string, stop bool, code int, err error) {
	var open []string
	var states []upState
	for _, b := range append(registeredBoxes(), pendingBoxes()...) {
		if st, ok := loadUpState(&config.Config{Box: b}); ok && st.Stage != stageReady {
			open, states = append(open, b), append(states, st)
		}
	}
	if len(open) == 0 || !w.p.Interactive() || w.o.provider != "" {
		return "", false, 0, nil
	}
	var choices []prompt.Choice
	def, cloud := 0, false
	for i, b := range open {
		if states[i].Updated.After(states[def].Updated) {
			def = i
		}
		cloud = cloud || isCloud(states[i].Via)
		choices = append(choices, prompt.Choice{Key: b, Label: "Continue " + b, Detail: states[i].what(b)})
	}
	one := len(open) == 1
	question, stays, box := fmt.Sprintf("%d boxes are not finished. Continue one or set up another?", len(open)), "they stay; remove one: ", "NAME"
	if one {
		question, stays, box = open[0]+" is not finished. Continue it or set up another box?", open[0]+" stays; remove it: ", open[0]
	}
	// forget only stops managing a box: a server keelbox created is
	// deleted by destroy, which nothing here runs.
	remove := "keelbox forget " + box
	if cloud {
		remove += " (first keelbox destroy " + box + ", to delete a server keelbox created)"
	}
	if one && states[0].noServer() {
		choices = append(choices,
			prompt.Choice{Label: "Set up another box", Detail: box + " stays as it is"},
			prompt.Choice{Label: "Discard " + box, Detail: "no server yet; the same as: keelbox forget " + box})
	} else {
		choices = append(choices, prompt.Choice{Label: "Set up another box", Detail: stays + remove})
	}
	w.banner()
	i, err := w.p.Choose(question, choices, def)
	switch {
	case err != nil:
		return "", false, 0, err
	case i < len(open):
		return open[i], false, 0, nil
	case i == len(open):
		return "", false, 0, nil
	}
	// Discard is keelbox forget: it says what it removes and asks first,
	// whatever --yes said about setting a box up.
	o := w.o
	o.yes = false
	return "", true, cmdForget(o, []string{box}, w.p, w.out, w.errOut), nil
}

// lockBox keeps a second keelbox up, or a keelbox create, on the same box
// from running at once; again is the command to run once the other is
// done.
func lockBox(box, again string) (func(), error) {
	if err := os.MkdirAll(boxesDir(), 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(boxesDir(), box+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another keelbox command is working on %s; nothing changed. Wait for it to finish, then: %s", box, again)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// asInvoker makes sudo keelbox up keep its registry where the user's
// keelbox finds it: HOME becomes the home of the user who ran sudo, so the
// box config, its state and the SSH keys read are theirs, and the files
// written are given to them when up returns.
func asInvoker() func() {
	if os.Geteuid() != 0 {
		return func() {}
	}
	uid, gid, ok := sudoIDs()
	if !ok {
		return func() {}
	}
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil || u.HomeDir == "" {
		return func() {}
	}
	old := os.Getenv("HOME")
	_ = os.Setenv("HOME", u.HomeDir)
	return func() {
		// ~/.config may be new as well; nothing outside it is touched, and
		// Lchown never follows a link.
		dirs := []string{configDir(), filepath.Join(configDir(), "boxes")}
		paths := []string{filepath.Dir(configDir())}
		for _, d := range dirs {
			paths = append(paths, d)
			entries, _ := os.ReadDir(d)
			for _, e := range entries {
				paths = append(paths, filepath.Join(d, e.Name()))
			}
		}
		for _, p := range paths {
			if fi, err := os.Lstat(p); err == nil && ownedByRoot(fi) {
				_ = os.Lchown(p, uid, gid)
			}
		}
		_ = os.Setenv("HOME", old)
	}
}

func ownedByRoot(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0
}

func sudoIDs() (int, int, bool) {
	uid, err := strconv.Atoi(os.Getenv("SUDO_UID"))
	if err != nil || uid == 0 {
		return 0, 0, false
	}
	gid, err := strconv.Atoi(os.Getenv("SUDO_GID"))
	if err != nil {
		return 0, 0, false
	}
	return uid, gid, true
}
