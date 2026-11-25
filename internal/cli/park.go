package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/ui"
)

// keelbox park and keelbox resume (docs/adr/0071-resize-park-resume.md).
// park stops paying for a box's server while the box is not needed: it
// shuts the server down, saves its disk as a snapshot, and deletes the
// server only once the cloud reports the snapshot complete. resume creates
// the server again from the snapshot. The parked state is the snapshot's
// labels in the cloud; this computer keeps a note of it only so that
// keelbox ls and keelbox status --all can say "parked" without asking the
// cloud.
//
// How long to park for is the user's to decide. The card before the
// question gives what that takes: what the server costs and what the
// snapshot will, how long parking and resuming take, and what the box is
// like when it comes back.

// parkBill is what parking a box changes on its cloud's bill, in the
// cloud's own prices where it gives them and in plain words where it does
// not.
type parkBill struct {
	// saves is what stops being billed: "the server's price, EUR 3.79 a
	// month (about EUR 0.14 a day)".
	saves string
	// billed is what stays billed: the snapshot, with its price and the
	// most it can come to where the cloud gives one.
	billed string
	// server is the server's price alone, "EUR 3.79 a month", or "" when
	// the cloud gives none.
	server string
}

// cloudTimes are how long the commands took on a cloud in keelbox's own
// runs on a box with little on its disk (docs/contributing/testing.md),
// each as it reads after "typically": "1 to 2 minutes", "about 2 minutes".
// A resize and a resume include their apply.
type cloudTimes struct {
	resize, park, resume string
}

// serverPrice is what a server costs: a month, and a day where the cloud
// bills by the hour and says what an hour costs (daily 0: a thirtieth of
// the month).
func serverPrice(currency string, monthly, daily float64) (saves, server string) {
	if daily == 0 {
		daily = monthly / 30
	}
	server = fmt.Sprintf("about %s %.2f a month", currency, monthly)
	return fmt.Sprintf("the server's price, %s (%s %.2f a day)", server, currency, daily), server
}

// snapshotAtMost is what a snapshot costs where it is billed for what the
// disk holds: the price per GB and the most that can come to, which is a
// full disk.
func snapshotAtMost(currency, perGB string, price float64, diskGB int) string {
	cost := fmt.Sprintf("the snapshot: %s %s per GB a month for what the disk holds", currency, perGB)
	if diskGB > 0 {
		cost += fmt.Sprintf(", so up to about %s %.2f a month for its %d GB", currency, max(price*float64(diskGB), 0.01), diskGB)
	}
	return cost
}

// parkMark is this computer's note that a box is parked.
type parkMark struct {
	Since time.Time `json:"since"`
	// Address is the public address the box had; its host keys stay pinned
	// under it until resume moves them to the new one.
	Address string `json:"address,omitempty"`
	// Snapshot names the snapshot, as the cloud does.
	Snapshot string `json:"snapshot,omitempty"`
	// Server is what the server cost when it was parked, "EUR 3.79 a
	// month", where the cloud gave a price: resume says it before it
	// creates a server again.
	Server string `json:"server,omitempty"`
}

// billedAgain is what resume says before it creates a server: the server
// is billed from then, and what it cost when this computer parked it into
// the snapshot being resumed. A box whose size was changed since costs
// what its new size does, so the price is given as the one it had.
func billedAgain(mark parkMark, pk *cloudParked) string {
	s := "its server is billed again from the moment it is created"
	if mark.Server != "" && mark.Snapshot == pk.ref {
		s += ": " + mark.Server + " when it was parked"
	}
	return s
}

func parkMarkPath(box string) string { return filepath.Join(boxesDir(), box+".parked.json") }

func loadParkMark(box string) (parkMark, bool) {
	data, err := os.ReadFile(parkMarkPath(box))
	if err != nil {
		return parkMark{}, false
	}
	var m parkMark
	if json.Unmarshal(data, &m) != nil || m.Since.IsZero() {
		return parkMark{}, false
	}
	return m, true
}

func saveParkMark(box string, m parkMark) error {
	if err := os.MkdirAll(boxesDir(), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(parkMarkPath(box), append(data, '\n'), 0o600)
}

func forgetParkMark(box string) { _ = os.Remove(parkMarkPath(box)) }

// day is a date as the messages about parked boxes show it.
func day(t time.Time) string { return t.Local().Format("2006-01-02") }

// parkedError is what a command that needs the box's server says when the
// box is parked.
type parkedError struct {
	box, ref string
	since    time.Time
}

func (e *parkedError) Error() string {
	return fmt.Sprintf("%s has been parked since %s: its server is deleted and its disk is kept as a snapshot. Bring it back: keelbox resume %s", e.box, day(e.since), e.ref)
}

// rememberParked notes on this computer that the cloud holds the box
// parked, keeping the address noted when it was parked here, and returns
// the error that says so.
func rememberParked(cfg *config.Config, ref string, p *cloudParked) error {
	m, _ := loadParkMark(cfg.Box)
	m.Since, m.Snapshot = p.since, p.ref
	_ = saveParkMark(cfg.Box, m)
	return &parkedError{box: cfg.Box, ref: ref, since: p.since}
}

// parkedLocally is the error for a box this computer has noted as parked,
// or nil, for the commands that do not ask the cloud.
func parkedLocally(cfg *config.Config, ref string) *parkedError {
	m, ok := loadParkMark(cfg.Box)
	if !ok {
		return nil
	}
	return &parkedError{box: cfg.Box, ref: ref, since: m.Since}
}

// needsCloudBox is what resize, park and resume say about a box that is
// not a server keelbox created on a cloud.
func needsCloudBox(cmd string, cfg *config.Config, stderr io.Writer) int {
	what := map[string]string{
		"resize": "changes the size of",
		"park":   "deletes and keeps the disk of",
		"resume": "brings back",
	}[cmd]
	fmt.Fprintf(stderr, "keelbox: %s %s a server keelbox created on a cloud (provider hetzner, aws, digitalocean or gcp); %s is a machine you have (provider %s), so there is nothing for it to do.\n",
		cmd, what, cfg.Box, cfg.Target.Provider)
	return 2
}

// boxComesBack waits until the box's sshd takes a command as the admin
// after its server started, over the tailnet when this computer reaches
// it there and otherwise at its public address; tests replace it.
var boxComesBack = waitForBox

func waitForBox(ctx context.Context, cfg *config.Config, public addr, stdout, stderr io.Writer) error {
	const timeout = 6 * time.Minute
	deadline := time.Now().Add(timeout)
	a := public
	for {
		// reach prefers the tailnet address once it answers; a box that
		// had closed public SSH answers only there.
		a = reach(ctx, cfg, public, io.Discard)
		if dialable(ctx, a, 5*time.Second) == nil {
			break
		}
		if time.Now().After(deadline) {
			where := public.String()
			if t, ok := tailnetAddr(cfg); ok && cfg.Tailscale() && t != public {
				where += " or over the tailnet at " + t.String()
			}
			return fmt.Errorf("the server runs, but nothing answers SSH at %s after %s. A box that had closed public SSH answers only over the tailnet: this computer must be on it, and the box rejoins only if its Tailscale key did not expire meanwhile (%s/guides/public-ssh)", where, timeout, docsURL)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	s := sshTarget(cfg, a, options{}, stdout, stderr)
	if err := s.TrustHost(ctx); err != nil {
		return err
	}
	if err := s.WaitReachable(ctx, 3*time.Minute); err != nil {
		return err
	}
	// cloud-init runs again on a new server; the apply comes after it.
	_, _ = s.Output(ctx, "cloud-init status --wait >/dev/null 2>&1 || true")
	return nil
}

// movePins pins the box's host keys under its new public address. They are
// the keys this computer already trusts for the box, found under the
// address it had or its other names: the disk, and so the keys, are the
// same. With none found, whatever was pinned for the new address belonged
// to another server and is dropped, and the first connection asks as for
// any new host.
func movePins(cfg *config.Config, to addr, old ...string) {
	s := sshTarget(cfg, to, options{}, io.Discard, io.Discard)
	names := append([]string{}, old...)
	names = append(names, cfg.Target.SSH.Host)
	if t, ok := tailnetAddr(cfg); ok {
		names = append(names, t.host)
	}
	for _, n := range names {
		if n == "" {
			continue
		}
		port := cfg.Target.SSH.Port
		if t, ok := tailnetAddr(cfg); ok && t.host == n {
			port = t.port
		}
		from := sshTarget(cfg, addr{n, port}, options{}, io.Discard, io.Discard)
		if from.IsKnown() {
			_ = s.CopyPins(n, port)
			return
		}
	}
	provider.ForgetHost(knownHosts(cfg), knownHostsName(to))
}

// publicSSHClosed reports whether the box has closed public SSH, read from
// the box as it runs now. A box that does not answer in time counts as
// open: this only decides whether park adds a warning. Tests replace it.
var publicSSHClosed = func(ctx context.Context, cfg *config.Config, srv *cloudServer) bool {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	public := addr{srv.ip, cfg.Target.SSH.Port}
	if name := cfg.Target.SSH.Host; name != "" {
		public = addr{name, cfg.Target.SSH.Port}
	}
	st, err := boxPublicSSH(ctx, cfg, reach(ctx, cfg, public, io.Discard))
	return err == nil && st.State == "closed"
}

// hostKeyKeeper is a cloud that cannot tell a new server, in its user
// data, to keep the SSH host keys on the disk: a file on the box has to
// say so before the disk boots on another server. resize and park check
// for it before anything changes.
type hostKeyKeeper interface {
	// hostKeysFile is that file, and why says what happens without it.
	hostKeysFile() (path, why string)
}

// boxHasFile reports whether the file is on the box, by the exit code of
// test over SSH. Tests replace it.
var boxHasFile = func(ctx context.Context, cfg *config.Config, a addr, path string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := sshTarget(cfg, a, options{}, io.Discard, io.Discard).Output(ctx, "test -f "+path)
	var exit *exec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	}
	return false, err
}

// keepsHostKeys reports whether the box is ready for its disk to boot on
// another server, where the cloud needs a file on it for that
// (hostKeyKeeper). When the file is missing or the box cannot be asked, it
// says why and how to put it there, and cmd stops before any change.
func keepsHostKeys(ctx context.Context, cl cloud, cfg *config.Config, public addr, cmd, ref string, stderr io.Writer) bool {
	k, ok := cl.(hostKeyKeeper)
	if !ok {
		return true
	}
	path, why := k.hostKeysFile()
	has, err := boxHasFile(ctx, cfg, reach(ctx, cfg, public, io.Discard), path)
	if has {
		return true
	}
	what := fmt.Sprintf("%s has no %s", cfg.Box, path)
	if err != nil {
		what = fmt.Sprintf("%s could not be asked whether it has %s (%v)", cfg.Box, path, err)
	}
	fmt.Fprintf(stderr, "keelbox: nothing changed: %s. %s. Put it there: keelbox apply %s; then: keelbox %s %s\n", what, why, ref, cmd, ref)
	return false
}

// cloudPark is keelbox park.
func cloudPark(ctx context.Context, cl cloud, cfg *config.Config, o options, p *prompt.Prompter, logf func(string, ...any), stdout, stderr io.Writer) int {
	box, ref := cfg.Box, boxRef(cfg, o.configFile)
	fail := func(err error) int {
		fmt.Fprintln(stderr, "keelbox:", err)
		return 1
	}
	srv, err := cl.find(ctx)
	if err != nil {
		pk, perr := cl.parked(ctx)
		switch {
		case perr != nil:
			return fail(perr)
		case pk != nil:
			_ = rememberParked(cfg, ref, pk)
			sayInfo(stdout, o.style, fmt.Sprintf("%s is already parked, since %s: %s holds its disk (%s) and is what is billed. Bring it back: keelbox resume %s", box, day(pk.since), pk.ref, pk.detail, ref))
			return 0
		}
		return fail(err)
	}
	// Asked first: what a cloud refuses to park (a server with a second
	// disk, say) stops here, before the question.
	bill, err := cl.parkCost(ctx, srv)
	if err != nil {
		return fail(err)
	}
	// What an earlier park or resume that did not finish left.
	old, err := cl.parked(ctx)
	if err != nil {
		return fail(err)
	}
	if !o.yes && !p.Interactive() {
		fmt.Fprintf(stderr, "keelbox: park asks first; %s\n", orOwner("without a terminal: keelbox park "+ref+" --yes", "keelbox park "+ref))
		return 2
	}
	public := addr{srv.ip, cfg.Target.SSH.Port}
	if name := cfg.Target.SSH.Host; name != "" {
		public = addr{name, cfg.Target.SSH.Port}
	}
	// A server that is off beside a complete snapshot is a park whose delete
	// failed: the file was checked before that park shut the server down,
	// and a server that is off cannot be asked.
	if half := srv.stopped && old != nil; !half && !keepsHostKeys(ctx, cl, cfg, public, "park", ref, stderr) {
		return 1
	}
	// A box that has closed public SSH comes back reachable over the
	// tailnet only, and only while its Tailscale key is good.
	tailnetOnly := ""
	if cfg.Tailscale() && publicSSHClosed(ctx, cfg, srv) {
		tailnetOnly = fmt.Sprintf("public SSH is closed on %s, so after a resume it answers over the tailnet only. If its Tailscale key expires while it is parked it cannot rejoin by itself, "+
			"and the way in is then the cloud's rescue console (%s/guides/public-ssh). Check the key's expiry in the Tailscale admin console first", box, docsURL)
	}
	// What the snapshot is, said before anything changes (the security
	// model's "A parked box's snapshot").
	const holds = "holds everything on the disk, credentials and the agent's sign-ins included: whoever can read snapshots in this cloud account can read them, " +
		"and parking revokes nothing"
	if !o.yes {
		st := o.style
		// The address has a row of its own.
		server := strings.Replace(srv.about, ", "+srv.ip+")", ")", 1)
		times := cl.typical()
		rows := []ui.Line{
			ui.Row("what happens", fmt.Sprintf("%s shuts down: running sessions, agents, dev servers and containers stop. Its disk is saved as a snapshot and, once the cloud reports the snapshot complete, %s is deleted", box, server)),
			ui.Row("saves", linked(st, billed(st, bill.saves))),
			ui.Row("still billed", linked(st, billed(st, bill.billed))),
			ui.Row("takes", fmt.Sprintf("typically %s to park and %s to resume, for a box with little on its disk; a fuller disk takes longer", times.park, times.resume)),
			ui.Row("comes back", fmt.Sprintf("at a new address (%s is given up), with its files and sign-ins as they were. Sessions and dev servers have to be started again, as after a reboot", srv.ip)),
			ui.Row("the snapshot", holds),
		}
		if tailnetOnly != "" {
			rows = append(rows, ui.Row(st.Warn("check first"), linked(st, tailnetOnly)))
		}
		rows = append(rows, ui.Row("bring it back", command(st, "keelbox resume "+ref)))
		showGoAhead(stdout, st, "Park "+box, rows)
		ok, err := p.Confirm("Park "+box+"?", false)
		if err != nil || !ok {
			fmt.Fprintln(stdout, "Nothing changed.")
			return 1
		}
	} else {
		sayNote(logf, o.style, fmt.Sprintf("the snapshot %s (%s/guides/resize-park-resume)", holds, docsURL))
		if tailnetOnly != "" {
			sayNote(logf, o.style, tailnetOnly)
		}
	}
	start := time.Now()
	// Not stopped half-way by Ctrl-C between the steps that follow a
	// confirmed snapshot; before that, Ctrl-C ends the wait and the server
	// is started again.
	held := context.WithoutCancel(ctx)
	// What happens next is a list of steps on a terminal and the log's
	// lines elsewhere; whatever else is printed meanwhile goes through the
	// view, so that it lands under the steps.
	v := newStepView(stdout, o, logf)
	defer v.close()
	stdout, stderr = v.writer(stdout), v.writer(stderr)
	back := func(why string, err error) int {
		v.failed()
		fmt.Fprintf(stderr, "keelbox: %s: %v\n", why, err)
		v.begin("Starting "+srv.ref+" again", "")
		if s, serr := cl.start(held, srv); serr != nil {
			v.failed()
			fmt.Fprintf(stderr, "keelbox: %s is not parked and not deleted, but starting it again failed: %v. Start it: keelbox resume %s\n", box, serr, ref)
		} else {
			v.finished(fmt.Sprintf("%s runs again at %s", s.ref, s.ip), "")
			fmt.Fprintf(stderr, "keelbox: %s is not parked: %s was not deleted and runs again at %s. Nothing is lost; try again: keelbox park %s\n", box, s.ref, s.ip, ref)
		}
		return 1
	}
	v.begin("Shutting "+box+" down", "")
	v.say("shutting %s down", box)
	if err := cl.stop(ctx, srv, v.note); err != nil {
		return back("the server did not shut down", err)
	}
	v.finished("Shut "+box+" down", "")
	v.begin("Saving its disk as a snapshot", "nothing is deleted before the snapshot is complete")
	v.say("%s is off; saving its disk as a snapshot (minutes for a full disk)", srv.ref)
	pk, err := cl.snapshot(ctx, srv, v.note)
	if err != nil {
		return back("the snapshot was not completed", err)
	}
	if pk == nil || pk.since.IsZero() {
		return back("the snapshot was not confirmed", fmt.Errorf("%s reported none", cl.title()))
	}
	v.finished(fmt.Sprintf("Saved its disk as %s (%s)", pk.ref, pk.detail), "")
	v.say("%s is complete (%s)", pk.ref, pk.detail)
	// Only now, with the snapshot confirmed by the cloud, is the server
	// deleted.
	v.begin("Deleting "+srv.ref, "")
	if err := cl.release(held, srv, v.note); err != nil {
		v.failed()
		fmt.Fprintf(stderr, "keelbox: %s is complete, but deleting the server failed: %v\n", pk.ref, err)
		fmt.Fprintf(stderr, "keelbox: %s is off, not deleted, and still billed. Delete it and finish: keelbox park %s; or start it again: keelbox resume %s\n", srv.ref, ref, ref)
		return 1
	}
	v.finished("", "Deleted "+srv.ref)
	if old != nil {
		// The box as it was before an earlier park or resume that did not
		// finish: the new snapshot replaces it.
		v.begin("Deleting the older "+old.ref, "")
		if err := cl.discard(held, old, v.note); err != nil {
			v.failed()
			fmt.Fprintf(stderr, "keelbox: warning: the older %s could not be deleted (%v); it is billed until it is: keelbox destroy %s removes every snapshot of the box\n", old.ref, err, ref)
		} else {
			v.finished("", "Deleted the older "+old.ref)
		}
	}
	if err := saveParkMark(box, parkMark{Since: pk.since, Address: srv.ip, Snapshot: pk.ref, Server: bill.server}); err != nil {
		fmt.Fprintln(stderr, "keelbox: warning: could not note here that the box is parked:", err)
	}
	showOutcome(stdout, o.style, parkedOutcome(o.style, box, ref, time.Since(start), srv, pk, bill, cl.typical()))
	return 0
}

// cloudResume is keelbox resume.
func cloudResume(ctx context.Context, cl cloud, cfg *config.Config, o options, logf func(string, ...any), stdout, stderr io.Writer) int {
	box, ref := cfg.Box, boxRef(cfg, o.configFile)
	fail := func(err error) int {
		fmt.Fprintln(stderr, "keelbox:", err)
		return 1
	}
	pk, err := cl.parked(ctx)
	if err != nil {
		return fail(err)
	}
	mark, _ := loadParkMark(box)
	st, hasState := loadUpState(cfg)
	start := time.Now()
	srv, ferr := cl.find(ctx)
	// As in cloudPark: steps on a terminal, the log's lines elsewhere.
	v := newStepView(stdout, o, logf)
	defer v.close()
	stdout, stderr = v.writer(stdout), v.writer(stderr)
	switch {
	case ferr != nil && pk == nil:
		return fail(ferr)
	case ferr == nil && pk == nil:
		forgetParkMark(box)
		if srv.stopped {
			// What a park or a resize that could not start the server
			// again left: the box as it was, switched off.
			v.begin("Starting "+srv.ref, box+" is not parked, but its server is off")
			v.say("%s is not parked, but %s is off; starting it", box, srv.ref)
			if srv, err = cl.start(ctx, srv); err != nil {
				v.failed()
				return fail(err)
			}
			v.finished("Started "+srv.ref, "")
			sayInfo(stdout, o.style, fmt.Sprintf("%s runs again at %s. Check it: keelbox doctor %s", srv.ref, srv.ip, ref))
			return 0
		}
		sayInfo(stdout, o.style, fmt.Sprintf("%s is not parked: %s runs at %s. Nothing to resume.", box, srv.ref, srv.ip))
		return 0
	case ferr != nil:
		v.heading("Resuming "+box, fmt.Sprintf("from %s, parked since %s", pk.ref, day(pk.since)))
		v.note("%s", billedAgain(mark, pk))
		v.begin("Creating its server from "+pk.ref, "")
		srv, err = cl.restore(ctx, pk, v.note)
		if err != nil {
			v.failed()
			fmt.Fprintln(stderr, "keelbox:", err)
			var refused *cloudRefusal
			switch {
			case errors.As(err, &refused):
				path := o.configFile
				if path == "" {
					path = boxConfigPath(box)
				}
				fmt.Fprintf(stderr, "keelbox: %s is still parked: %s is kept, and nothing else changed. %s\n", box, pk.ref, refused.configFix(path, "keelbox resume "+ref, ""))
			case srv != nil:
				fmt.Fprintf(stderr, "keelbox: %s may exist; %s is kept. Continue: keelbox resume %s\n", srv.ref, pk.ref, ref)
			default:
				fmt.Fprintf(stderr, "keelbox: %s is still parked: %s is kept. Try again: keelbox resume %s\n", box, pk.ref, ref)
			}
			return 1
		}
		v.finished(fmt.Sprintf("Created %s at %s", srv.ref, srv.ip), "")
		// A new server at a new address, with the disk's own host keys.
		movePins(cfg, addr{srv.ip, cfg.Target.SSH.Port}, mark.Address, st.Address)
	default:
		// A server and a parked snapshot: a park that stopped before the
		// server was deleted, or a resume that stopped before its apply
		// was done. Either way the server is the box; start it and finish.
		v.heading("Resuming "+box, "finishing what an earlier park or resume left")
		v.begin("Starting "+srv.ref, pk.ref+" is still kept")
		v.say("%s exists and %s is still kept: finishing what an earlier park or resume left", srv.ref, pk.ref)
		srv, err = cl.start(ctx, srv)
		if err != nil {
			v.failed()
			return fail(err)
		}
		v.finished(fmt.Sprintf("%s runs at %s", srv.ref, srv.ip), "")
	}
	ip := addr{srv.ip, cfg.Target.SSH.Port}
	o.publicIP = srv.ip
	if hasState && st.Address != srv.ip {
		st.Address = srv.ip
		_ = saveUpState(box, st)
	}
	// As for every command on a cloud box: target.ssh.host, when set, is
	// the name the box is reached by, with the keys pinned for its address.
	public := ip
	if name := cfg.Target.SSH.Host; name != "" {
		public = addr{name, cfg.Target.SSH.Port}
		_ = sshTarget(cfg, public, options{}, io.Discard, io.Discard).CopyPins(ip.host, ip.port)
	}
	v.begin("Waiting for SSH at "+ip.host, "the server boots, then its first-boot setup runs")
	v.say("%s running at %s; waiting for SSH", srv.ref, ip.host)
	kept := fmt.Sprintf("%s is kept until an apply has passed. Fix the cause above, then: keelbox resume %s", pk.ref, ref)
	if err := boxComesBack(ctx, cfg, public, stdout, stderr); err != nil {
		v.failed()
		fmt.Fprintln(stderr, "keelbox:", err)
		fmt.Fprintf(stderr, "keelbox: %s\n", kept)
		return 1
	}
	v.finished("SSH answers at "+ip.host, "")
	// The normal apply, as the admin: a new server runs cloud-init again,
	// and every step is checked against what that may have changed. It has
	// its own view, under the steps so far.
	o.sshUser, o.askSudo = "", false
	if code := applyExisting(ctx, cl, cfg, srv, public, o, v.note, stdout, stderr); code != 0 {
		if code != exitDetached {
			fmt.Fprintf(stderr, "keelbox: the apply did not pass, so %s\n", kept)
		} else {
			fmt.Fprintf(stderr, "keelbox: %s is kept until an apply has passed; once this one is done: keelbox resume %s\n", pk.ref, ref)
		}
		return code
	}
	held := context.WithoutCancel(ctx)
	v.begin("Deleting "+pk.ref, "the apply passed, so the server holds the disk")
	if err := cl.discard(held, pk, v.note); err != nil {
		v.failed()
		fmt.Fprintf(stderr, "keelbox: %s is back, but %s could not be deleted: %v. It is billed until it is: keelbox resume %s\n", box, pk.ref, err, ref)
		return 1
	}
	v.finished("", "Deleted "+pk.ref)
	if mark.Address != "" && mark.Address != srv.ip {
		// The address the box had may be another server's by now.
		provider.ForgetHost(knownHosts(cfg), knownHostsName(addr{mark.Address, cfg.Target.SSH.Port}))
	}
	forgetParkMark(box)
	showOutcome(stdout, o.style, resumedOutcome(o.style, cfg, ref, time.Since(start), srv, pk, mark.Address))
	return 0
}

// destroyParked is keelbox destroy on a parked box: there is no server to
// read the agent's projects from, so it says what the snapshot holds and
// asks as it does for a server that does not answer, then for the box name.
func destroyParked(ctx context.Context, cl cloud, cfg *config.Config, o options, p *cloudParked, in *bufio.Reader, logf func(string, ...any), stdout, stderr io.Writer) int {
	box, ref := cfg.Box, boxRef(cfg, o.configFile)
	why := fmt.Sprintf("%s has been parked since %s, so keelbox cannot check it for work that exists only there. %s (%s) holds everything that was on the box, the agent's projects and sign-ins included",
		box, day(p.since), p.ref, p.detail)
	if !o.discardWork {
		// As for a server that does not answer (ADR 0064): --yes does not
		// answer for work nobody could look at.
		if !stdinIsTerminal() {
			fmt.Fprintf(stderr, "keelbox: not deleted: %s. Bring it back and look: keelbox resume %s; %s\n", why, ref,
				orOwner("or delete it without the check: keelbox destroy "+ref+" --yes --discard-work", "keelbox destroy "+ref))
			return 1
		}
		sayAsIs(stdout, o.style, o.style.Bang(), why+". Deleting the snapshot deletes all of it.")
		if !confirm(in, stdout, "Delete it without knowing?") {
			fmt.Fprintln(stderr, "keelbox: not confirmed; nothing deleted")
			return 1
		}
	}
	if !o.yes {
		fmt.Fprintf(stdout, "Delete %s and everything in it? Type the box name to confirm: ", p.ref)
		line, _ := in.ReadString('\n')
		if strings.TrimSpace(line) != box {
			fmt.Fprintln(stderr, "keelbox: not confirmed; nothing deleted")
			return 1
		}
	}
	if err := cl.discard(ctx, p, logf); err != nil {
		fmt.Fprintln(stderr, "keelbox:", err)
		return 1
	}
	// What create made besides the server, which park kept for resume.
	cl.sweep(ctx, logf)
	mark, _ := loadParkMark(box)
	st, _ := loadUpState(cfg)
	forgetBox(cfg, mark.Address)
	if st.Address != "" && st.Address != mark.Address {
		provider.ForgetHost(knownHosts(cfg), st.Address)
	}
	forgetUpState(box)
	forgetParkMark(box)
	stillValid(cfg, false, stdout)
	noteDestroyed(cfg, o, stdout, stderr)
	return 0
}
