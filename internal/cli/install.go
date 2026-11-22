package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/installs"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/modules"
	"github.com/keelbox/keelbox/internal/plain"
	"github.com/keelbox/keelbox/internal/runlock"
	"github.com/keelbox/keelbox/internal/system"
)

// What users.agent.permissions adds to the command line
// (docs/adr/0074-agent-permissions.md): keelbox install, run by the agent
// on its box without sudo; install-run, the root service that takes its
// requests; and keelbox requests, approve and deny, which the owner runs
// from their computer and which run on the box as root.

// The world-readable files keelbox install reads; tests point them
// elsewhere.
var installModePath, installRequestsPath = installs.ModePath, installs.RequestsPath

// How long keelbox install waits; tests shorten them.
var (
	// installAckWait is for root's service to take the request up; while
	// it has not, the request is written again every installNudge, since
	// a write that lands while the service runs may start no run.
	installAckWait = 90 * time.Second
	installNudge   = 5 * time.Second
	// installWait is for an install that has started.
	installWait = 30 * time.Minute
	installPoll = 250 * time.Millisecond
)

// Exit codes of keelbox install besides 0 (installed) and 1 (not
// installed, with the reason).
const exitInstallPending = 3 // recorded; the owner's approval is the next step

// cmdInstall is keelbox install PACKAGE... on the box, as the agent. It
// writes one request to the agent's own file and waits for what root's
// install service makes of it. It needs no rights beyond the agent's and
// decides nothing: every check is the service's.
func cmdInstall(ctx context.Context, o options, pos []string, stdout, stderr io.Writer) int {
	m, err := installs.LoadMode(installModePath)
	if err != nil {
		fmt.Fprintf(stderr, "keelbox: keelbox install runs on a box, as the agent, and this machine has no install service set up (%s: %v). "+
			"On a box, the owner applies it from their computer: keelbox apply BOX. From a computer, open the box first: keelbox ssh BOX, then keelbox install NAME\n", installModePath, err)
		return 1
	}
	if me := currentUser(); me != m.Agent {
		fmt.Fprintf(stderr, "keelbox: keelbox install is for the agent account %s on %s; you are %s. With sudo, install a package yourself: sudo apt-get install NAME. "+
			"What %s asked for: keelbox requests %s, from your computer\n", m.Agent, m.Box, me, m.Agent, m.Box)
		return 2
	}
	if o.status {
		if len(pos) > 0 {
			fmt.Fprintln(stderr, "keelbox: install --status takes no package name: keelbox install --status")
			return 2
		}
		return installStatus(m, stdout, stderr)
	}
	if len(pos) == 0 {
		fmt.Fprintln(stderr, `keelbox: install takes package names: keelbox install libpq-dev --why "psycopg builds against it"; what became of earlier requests: keelbox install --status`)
		return 2
	}
	// Only what would keep the request from reaching root is checked
	// here; root checks, and records, everything else.
	if len(pos) > installs.MaxNames {
		fmt.Fprintf(stderr, "keelbox: not installed: %d package names in one request; the most is %d. Ask for them in smaller groups: keelbox install NAME...\n", len(pos), installs.MaxNames)
		return 1
	}
	size := 0
	for _, p := range pos {
		size += len(p)
	}
	if size > 4096 {
		fmt.Fprintln(stderr, "keelbox: not installed: those are not package names (over 4096 characters). Name packages as apt does: keelbox install libpq-dev")
		return 1
	}
	home := eventHome()
	if home == "" {
		fmt.Fprintln(stderr, "keelbox: install: no home directory for this account")
		return 1
	}
	unlock, err := installs.LockRequests(filepath.Join(home, installs.AgentDir))
	if err != nil {
		fmt.Fprintln(stderr, "keelbox: install:", err)
		return 1
	}
	defer unlock()
	req := installs.Request{ID: installs.NewID(), Time: time.Now().UTC(), Packages: pos, Why: installs.CleanWhy(o.why)}
	file := filepath.Join(home, installs.AgentFile)
	en, err := askRoot(ctx, file, req, m.Box)
	if err != nil {
		fmt.Fprintln(stderr, "keelbox:", err)
		return 1
	}
	if en.State == installs.StateInstalling || en.State == installs.StateApproved {
		fmt.Fprintf(stdout, "Installing %s (request %s)...\n", strings.Join(en.Packages, ", "), en.ID)
		if en, err = installed(ctx, en.ID); err != nil {
			fmt.Fprintln(stderr, "keelbox:", err)
			return 1
		}
	}
	return installOutcome(m, en, stdout, stderr)
}

// askRoot writes the request and waits until root's record has it. The
// service takes one request at a time: while it installs another, the wait
// goes on, up to installWait.
func askRoot(ctx context.Context, file string, req installs.Request, box string) (installs.Entry, error) {
	start := time.Now()
	deadline := start.Add(installAckWait)
	var written time.Time
	for {
		if time.Since(written) >= installNudge {
			// The time is the write's, so a request that waited its turn
			// is not taken for an old file.
			req.Time = time.Now().UTC()
			if err := installs.WriteRequest(file, req); err != nil {
				return installs.Entry{}, fmt.Errorf("install: %w", err)
			}
			written = time.Now()
		}
		if st, err := installs.LoadState(installRequestsPath); err == nil {
			if en := st.FindRef(req.ID); en != nil {
				return *en, nil
			}
			for _, other := range st.Requests {
				if limit := start.Add(installWait); other.State == installs.StateInstalling && time.Now().Before(limit) {
					deadline = time.Now().Add(installAckWait)
					if deadline.After(limit) {
						deadline = limit
					}
				}
			}
		}
		if !time.Now().Before(deadline) {
			return installs.Entry{}, fmt.Errorf("not installed: request %s is saved in %s, but root's install service did not take it up within %s. "+
				"It waits while keelbox apply runs on this box: run the same command again in a few minutes. If it still does not, tell the user to check the box: keelbox doctor %s",
				req.ID, file, time.Since(start).Round(time.Millisecond), box)
		}
		select {
		case <-ctx.Done():
			return installs.Entry{}, ctx.Err()
		case <-time.After(installPoll):
		}
	}
}

// installed waits for a request that root is installing.
func installed(ctx context.Context, id string) (installs.Entry, error) {
	deadline := time.Now().Add(installWait)
	for {
		if st, err := installs.LoadState(installRequestsPath); err == nil {
			if en := st.Find(id); en != nil && en.Done() {
				return *en, nil
			}
		}
		if !time.Now().Before(deadline) {
			return installs.Entry{}, fmt.Errorf("request %s is still installing after %s; it carries on without this command. See what became of it: keelbox install --status", id, installWait)
		}
		select {
		case <-ctx.Done():
			return installs.Entry{}, fmt.Errorf("stopped waiting; request %s carries on without this command. See what became of it: keelbox install --status", id)
		case <-time.After(installPoll):
		}
	}
}

// installOutcome prints what root made of a request and returns keelbox
// install's exit code. Everything in en is root's, or the agent's own.
func installOutcome(m installs.Mode, en installs.Entry, stdout, stderr io.Writer) int {
	what := strings.Join(en.Packages, ", ")
	switch en.State {
	case installs.StateInstalled:
		var already []string
		for _, p := range en.Packages {
			if !contains(en.Installed, p) {
				already = append(already, p)
			}
		}
		switch {
		case len(en.Installed) == 0:
			fmt.Fprintf(stdout, "Already installed: %s. Nothing to do.\n", what)
		case len(already) > 0:
			fmt.Fprintf(stdout, "Installed %s. Already installed: %s.\n", strings.Join(en.Installed, ", "), strings.Join(already, ", "))
		default:
			fmt.Fprintf(stdout, "Installed %s.\n", strings.Join(en.Installed, ", "))
		}
		return 0
	case installs.StatePending:
		fmt.Fprintf(stdout, "Request %s is recorded: %s. Nothing is installed yet.\n", en.ID, what)
		fmt.Fprintf(stdout, "On %s the owner approves each request (users.agent.permissions: manual). Tell the user to run, from their computer:\n", m.Box)
		fmt.Fprintf(stdout, "  keelbox approve %s %s        (keelbox requests %s lists what waits)\n", m.Box, en.ID, m.Box)
		fmt.Fprintln(stdout, "Carry on with other work meanwhile. keelbox install --status shows what they decided.")
		return exitInstallPending
	case installs.StateDenied:
		fmt.Fprintf(stderr, "keelbox: not installed: the owner denied request %s (%s). Do not ask again or work around it: tell the user what the work needs it for\n", en.ID, what)
		return 1
	case installs.StateFailed:
		fmt.Fprintf(stderr, "keelbox: not installed: %s\n", plain.Line(en.Detail))
		fmt.Fprintf(stderr, "Tell the user. They can read apt's output on the box: keelbox ssh %s --admin, then sudo journalctl -u %s\n", m.Box, installs.Service)
		return 1
	}
	fmt.Fprintf(stderr, "keelbox: not installed: %s\n", plain.Line(en.Detail))
	admin := "keelbox ssh " + m.Box + " --admin"
	if len(en.Packages) > 0 {
		admin += ", then sudo apt-get install " + strings.Join(en.Packages, " ")
	}
	fmt.Fprintf(stderr, "Do not work around a refusal. If the work needs this, tell the user what and why; only they can install it, as the admin: %s\n", admin)
	return 1
}

// installStatus is keelbox install --status: the agent's requests as root
// recorded them.
func installStatus(m installs.Mode, stdout, stderr io.Writer) int {
	st, err := installs.LoadState(installRequestsPath)
	if err != nil {
		fmt.Fprintf(stderr, "keelbox: %s: %v\n", installRequestsPath, err)
		return 1
	}
	if len(st.Requests) == 0 {
		fmt.Fprintf(stdout, "No package requests on %s yet (users.agent.permissions: %s). Ask for one: keelbox install NAME --why \"what needs it\"\n", m.Box, m.Mode)
		return 0
	}
	fmt.Fprintf(stdout, "Package requests on %s (users.agent.permissions: %s), oldest first:\n", m.Box, m.Mode)
	requestTable(stdout, st.Requests, false)
	if n := len(st.Pending()); n > 0 {
		fmt.Fprintf(stdout, "\n%d waiting for approval: the user runs keelbox approve %s from their computer.\n", n, m.Box)
	}
	return 0
}

// requestTable prints requests, one a line, with the reason a request was
// refused or failed under it and, with why, the agent's stated reason.
// The names passed root's check; the reason is the agent's text.
func requestTable(out io.Writer, list []installs.Entry, why bool) {
	fmt.Fprintf(out, "%-8s  %-16s  %-8s  %-10s  %s\n", "ID", "ASKED", "BY", "STATE", "PACKAGES")
	for _, en := range list {
		pkgs := strings.Join(en.Packages, ", ")
		if pkgs == "" {
			pkgs = "-"
		}
		fmt.Fprintf(out, "%-8s  %-16s  %-8s  %-10s  %s\n", en.ID, en.Asked.Local().Format("2006-01-02 15:04"), plain.Line(en.By), en.State, plain.Line(pkgs))
		if why && en.Why != "" {
			fmt.Fprintf(out, "          the agent's reason: %s\n", plain.Line(installs.CleanWhy(en.Why)))
		}
		if en.Detail != "" {
			fmt.Fprintf(out, "          %s\n", plain.Line(en.Detail))
		}
	}
}

// installsLock is the lock every change to root's record of the requests
// is made under.
func installsLock() (func(), error) {
	l, err := runlock.Own(runlock.InstallsPath, 2*time.Minute)
	if err != nil {
		return nil, err
	}
	return l.Unlock, nil
}

// installRun is keelbox-install.service. It waits out the pace, returns at
// once when there is nothing to act on, and leaves a request where it is
// while an apply runs: the agent's keelbox install writes it again.
func installRun(ctx context.Context, e *engine.Env, stdout, stderr io.Writer) int {
	const cmd = installs.Command
	time.Sleep(installs.Pace(e.X, time.Now().UTC()))
	if waiting, problem := installs.Waiting(e, time.Now().UTC()); !waiting {
		if problem != "" {
			// A link, a FIFO or an oversized file in place of the request.
			fmt.Fprintf(stderr, "keelbox %s: %s\n", cmd, plain.Line(problem))
		}
		fmt.Fprintf(stdout, "keelbox %s: no new package request\n", cmd)
		return 0
	}
	shared, err := runlock.Shared(runlock.Path)
	if errors.Is(err, runlock.ErrBusy) {
		fmt.Fprintf(stdout, "keelbox %s: skipped, an apply holds the lock; the request waits\n", cmd)
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "keelbox %s: %v\n", cmd, err)
		return 1
	}
	// Not held while apt-get runs: an apply that starts meanwhile waits for
	// dpkg's lock as it does for any other apt, and is not turned away.
	shared.Unlock()
	e.Log = stderr // a logbook record that cannot be written is said in the journal
	out, err := installs.Run(ctx, e, installs.Options{Lock: installsLock, Notify: func(en installs.Entry) { notifyRequest(ctx, e, en, stderr) }})
	for _, p := range out.Problems {
		fmt.Fprintf(stderr, "keelbox %s: %s\n", cmd, plain.Line(p))
	}
	for _, en := range out.Entries {
		fmt.Fprintf(stdout, "keelbox %s: request %s by %s: %s %s\n", cmd, en.ID, plain.Line(en.By), en.State, plain.Line(strings.Join(en.Packages, " ")))
		if en.Detail != "" {
			fmt.Fprintf(stderr, "keelbox %s: request %s: %s\n", cmd, en.ID, plain.Line(en.Detail))
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "keelbox %s: %v\n", cmd, err)
		return 1
	}
	return 0
}

// notifyRequest queues the event for a request that waits for approval and
// sends what is due, under the event queue's lock.
func notifyRequest(ctx context.Context, e *engine.Env, en installs.Entry, stderr io.Writer) {
	own, err := runlock.Own(runlock.DayTwoPath, 30*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "keelbox %s: the request waits, but its notification was not queued: %v\n", installs.Command, err)
		return
	}
	defer own.Unlock()
	now := time.Now().UTC()
	if _, err := events.Emit(e.X, installs.PendingEvent(e.Cfg.Box, en, now)); err != nil {
		fmt.Fprintf(stderr, "keelbox %s: %v\n", installs.Command, err)
		return
	}
	if _, err := events.Dispatch(ctx, e.X, senders(e), now); err != nil {
		fmt.Fprintf(stderr, "keelbox %s: %v\n", installs.Command, err)
	}
}

// waitingLine is the count every command that touches requests ends with,
// and the laptop reads back for keelbox ls (pendingRe).
func waitingLine(n int, box string) string {
	return fmt.Sprintf("%d waiting for approval on %s.", n, box)
}

// Requests is keelbox requests on the box, as root: the requests that wait
// for the owner, the approved ones the service has not finished, and with
// --all the ones already decided.
func Requests(e *engine.Env, o options, stdout, stderr io.Writer) int {
	st, err := installs.ReadState(e.X)
	if err != nil {
		fmt.Fprintln(stderr, "keelbox:", err)
		return 1
	}
	box := e.Cfg.Box
	pending := st.Pending()
	fmt.Fprintln(stdout, waitingLine(len(pending), box))
	if len(pending) > 0 {
		fmt.Fprintln(stdout)
		requestTable(stdout, pending, true)
		fmt.Fprintf(stdout, "\nThe package names passed keelbox's checks; a reason is the agent's own text. Approve: keelbox approve %s ID (or --all). Deny: keelbox deny %s ID.\n", box, box)
	}
	var decided, under []installs.Entry
	for _, en := range st.Requests {
		switch {
		case en.State == installs.StatePending:
		case en.Done():
			decided = append(decided, en)
		default:
			under = append(under, en)
		}
	}
	if len(under) > 0 {
		// Approved, and the service has not finished them: said without
		// --all, so one that stays here is seen.
		fmt.Fprintf(stdout, "\nApproved and not installed yet; the install service takes one at a time. To start it again: keelbox approve %s ID. To stop one it has not begun: keelbox deny %s ID.\n", box, box)
		requestTable(stdout, under, true)
	}
	switch {
	case o.all && len(decided) > 0:
		fmt.Fprintf(stdout, "\nDecided, oldest first:\n")
		requestTable(stdout, decided, true)
	case o.all:
		fmt.Fprintln(stdout, "No request has been decided yet.")
	case len(pending) == 0:
		fmt.Fprintf(stdout, "%s (users.agent.permissions: %s) Requests already decided: keelbox requests %s --all\n", modeSentence(e.Cfg), e.Cfg.AgentPermissions(), box)
	}
	return 0
}

// modeSentence says what the box does with a package request.
func modeSentence(c *config.Config) string {
	switch c.AgentPermissions() {
	case config.PermissionsManual:
		return "The agent's package requests wait here for you."
	case config.PermissionsFull:
		return "The agent has sudo; packages it asks for with keelbox install are installed without asking."
	}
	return "Packages the agent asks for with keelbox install are checked and installed without asking."
}

// startInstalls starts the install service and waits for the run to end;
// the service, not the caller's session, runs apt-get. Tests replace it.
var startInstalls = func(ctx context.Context, e *engine.Env) error {
	_, err := e.X.Run(ctx, system.C("systemctl", "start", installs.Service))
	return err
}

// approveSettle is how long approve waits before it starts the service
// again for a request a run left untouched (an apply held the lock).
var approveSettle, approveTries = 5 * time.Second, 3

// approveRequests is keelbox approve on the box, as root: it marks the
// request, or with --all every request that waits, as approved, starts the
// install service and says what the service made of each.
func approveRequests(ctx context.Context, e *engine.Env, o options, opts installs.Options, stdout, stderr io.Writer) int {
	box := e.Cfg.Box
	id := o.request
	if id == "" && !o.all {
		st, err := installs.ReadState(e.X)
		if err != nil {
			fmt.Fprintln(stderr, "keelbox:", err)
			return 1
		}
		switch waiting := st.Pending(); len(waiting) {
		case 0:
			fmt.Fprintln(stdout, waitingLine(0, box))
			return 0
		case 1:
			id = waiting[0].ID
		default:
			fmt.Fprintln(stdout, waitingLine(len(waiting), box))
			fmt.Fprintln(stdout)
			requestTable(stdout, waiting, true)
			fmt.Fprintf(stderr, "keelbox: approve needs to know which: keelbox approve %s ID, or all of them: keelbox approve %s --all\n", box, box)
			return 2
		}
	}
	marked, err := installs.Approve(e, id, o.all, opts)
	switch {
	case errors.Is(err, installs.ErrNoRequest):
		fmt.Fprintf(stderr, "keelbox: no request %s waits on %s. List the ones that do: keelbox requests %s\n", id, box, box)
		return 1
	case err != nil:
		fmt.Fprintln(stderr, "keelbox:", err)
		return 1
	}
	if len(marked) == 0 {
		fmt.Fprintln(stdout, waitingLine(0, box))
		return 0
	}
	for _, en := range marked {
		fmt.Fprintf(stdout, "Approved request %s: installing %s for %s...\n", en.ID, strings.Join(en.Packages, ", "), plain.Line(en.By))
	}
	code := 0
	var st installs.State
	for try := 0; ; try++ {
		if err := startInstalls(ctx, e); err != nil {
			fmt.Fprintf(stderr, "keelbox: the requests are approved, but %s did not run: %s. Once keelbox apply %s has set the service up, start it again: keelbox approve %s%s\n",
				installs.Service, plain.Line(err.Error()), box, box, approveArg(o, id))
			return 1
		}
		if st, err = installs.ReadState(e.X); err != nil {
			fmt.Fprintln(stderr, "keelbox:", err)
			return 1
		}
		left := 0
		for _, m := range marked {
			if en := st.Find(m.ID); en != nil && !en.Done() {
				left++
			}
		}
		if left == 0 || try+1 >= approveTries {
			break
		}
		// The run left them: an apply held the lock. It is asked again.
		select {
		case <-ctx.Done():
			return exitInterrupted
		case <-time.After(approveSettle):
		}
	}
	for _, m := range marked {
		en := st.Find(m.ID)
		switch {
		case en == nil:
			code = 1
			fmt.Fprintf(stderr, "keelbox: request %s is no longer in root's record of requests, so what became of it is not known here. The logbook has it: keelbox ssh %s --admin, then sudo grep request-%s %s\n",
				m.ID, box, m.ID, logbook.Path)
		case en.State == installs.StateInstalled && len(en.Installed) == 0:
			fmt.Fprintf(stdout, "Request %s: already installed, nothing to do.\n", en.ID)
		case en.State == installs.StateInstalled:
			fmt.Fprintf(stdout, "Request %s: installed %s.\n", en.ID, strings.Join(en.Installed, ", "))
		case !en.Done():
			code = 1
			fmt.Fprintf(stderr, "keelbox: request %s is approved, and %s has not installed it yet; it does while no keelbox apply runs. Start it again: keelbox approve %s %s\n",
				en.ID, installs.Service, box, en.ID)
		default:
			code = 1
			fmt.Fprintf(stderr, "keelbox: request %s was not installed: %s. To install it yourself: keelbox ssh %s --admin, then sudo apt-get install %s\n",
				en.ID, strings.TrimSuffix(plain.Line(en.Detail), "."), box, strings.Join(en.Packages, " "))
		}
	}
	fmt.Fprintln(stdout, waitingLine(len(st.Pending()), box))
	return code
}

func approveArg(o options, id string) string {
	if o.all {
		return " --all"
	}
	return " " + id
}

// denyRequest is keelbox deny on the box, as root.
func denyRequest(e *engine.Env, o options, opts installs.Options, stdout, stderr io.Writer) int {
	box := e.Cfg.Box
	if e.Log == nil {
		e.Log = stderr // a logbook record that cannot be written is said
	}
	en, err := installs.Deny(e, o.request, opts)
	switch {
	case errors.Is(err, installs.ErrNoRequest):
		fmt.Fprintf(stderr, "keelbox: no request %s waits on %s. List the ones that do: keelbox requests %s\n", o.request, box, box)
		return 1
	case err != nil:
		fmt.Fprintln(stderr, "keelbox:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Denied request %s (%s); nothing was installed.\n", en.ID, strings.Join(en.Packages, ", "))
	st, _ := installs.ReadState(e.X)
	fmt.Fprintln(stdout, waitingLine(len(st.Pending()), box))
	return 0
}

// requestArg takes approve's and deny's request ID out of the positional
// arguments: 8 hex digits that name neither a file nor a box.
func requestArg(pos []string) ([]string, string) {
	for i := len(pos) - 1; i >= 0; i-- {
		p := pos[i]
		if !installs.ValidID(p) || isRegistered(p) {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			continue
		}
		return append(append([]string(nil), pos[:i]...), pos[i+1:]...), p
	}
	return pos, ""
}

// unlistedPackages are the packages installed for the agent that a box
// rebuilt from its config would not have.
func unlistedPackages(ctx context.Context, e *engine.Env) (still, unlisted []string) {
	still = installs.StillInstalled(ctx, e)
	return still, installs.Unlisted(still, modules.BasePackages, e.Cfg)
}

// packagesHint is the YAML that keeps them, as host.packages should read.
func packagesHint(c *config.Config, unlisted []string, indent string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimSuffix(installs.PackagesYAML(c, unlisted), "\n"), "\n") {
		b.WriteString(indent + l + "\n")
	}
	return b.String()
}

// installRows are keelbox status's rows for the agent's packages and its
// requests: what was installed for it, the YAML to add when the config
// does not name it, and how many requests wait.
func installRows(ctx context.Context, e *engine.Env, out io.Writer) {
	box := e.Cfg.Box
	if still, unlisted := unlistedPackages(ctx, e); len(still) > 0 {
		fmt.Fprintf(out, "%-10s agent-installed: %s (%d)\n", "packages", strings.Join(still, ", "), len(still))
		if len(unlisted) > 0 {
			fmt.Fprintf(out, "%-10s not in host.packages: %s. A box rebuilt from the config would not have them; to keep them, %s's config should read:\n", "", strings.Join(unlisted, ", "), box)
			fmt.Fprint(out, packagesHint(e.Cfg, unlisted, strings.Repeat(" ", 11)))
		}
	}
	st, err := installs.ReadState(e.X)
	if err != nil {
		return
	}
	if n := len(st.Pending()); n > 0 || e.Cfg.AgentPermissions() == config.PermissionsManual {
		v := fmt.Sprintf("%d waiting for approval", n)
		if n > 0 {
			v += ": keelbox requests " + box
		}
		fmt.Fprintf(out, "%-10s %s\n", "requests", v)
	}
}

// applyPackagesNote says once, after an apply, which packages installed
// for the agent the config does not name, with the YAML that keeps them.
func applyPackagesNote(ctx context.Context, e *engine.Env, out io.Writer) {
	_, unlisted := unlistedPackages(ctx, e)
	if len(unlisted) == 0 || installs.Told(e.X, unlisted) {
		return
	}
	fmt.Fprintf(out, "Installed for the agent and not in host.packages: %s. A box rebuilt from the config would not have them; to keep them, %s's config should read:\n",
		strings.Join(unlisted, ", "), e.Cfg.Box)
	fmt.Fprint(out, packagesHint(e.Cfg, unlisted, "    "))
}

// requestsNote is what this computer last heard from a box about the
// agent's package requests, kept beside the box's config for keelbox ls,
// which reaches no box. It holds a count and a time.
type requestsNote struct {
	Waiting int       `json:"waiting"`
	Seen    time.Time `json:"seen"`
}

func requestsNotePath(box string) string { return filepath.Join(boxesDir(), box+".requests.json") }

func loadRequestsNote(box string) (requestsNote, bool) {
	data, err := os.ReadFile(requestsNotePath(box))
	if err != nil {
		return requestsNote{}, false
	}
	var n requestsNote
	if json.Unmarshal(data, &n) != nil || n.Waiting < 0 {
		return requestsNote{}, false
	}
	return n, true
}

// saveRequestsNote keeps the count; none waiting needs no note.
func saveRequestsNote(box string, waiting int, now time.Time) {
	if waiting <= 0 {
		_ = os.Remove(requestsNotePath(box))
		return
	}
	data, err := json.Marshal(requestsNote{Waiting: waiting, Seen: now.UTC()})
	if err != nil {
		return
	}
	_ = os.WriteFile(requestsNotePath(box), append(data, '\n'), 0o600)
}

// notesRequests reports whether this run is one whose output says how many
// requests wait on a box of this computer's registry: status, requests,
// approve or deny, run from here and not the box's own half of it.
func notesRequests(cmd string, cfg *config.Config, o options, path, provider string) bool {
	switch cmd {
	case "status", "requests", "approve", "deny":
	default:
		return false
	}
	if o.json || provider == config.ProviderLocal && os.Geteuid() == 0 {
		return false
	}
	return boxRef(cfg, path) == cfg.Box && isRegistered(cfg.Box)
}

// waitingRe finds the count in what a box printed: status's requests row,
// or the line requests, approve and deny print (waitingLine).
var waitingRe = regexp.MustCompile(`(?m)^(?:requests +)?(\d+) waiting for approval\b`)

// noteRequests keeps the count a command's output gave. A status that
// succeeded without the row has none waiting.
func noteRequests(box, cmd string, code int, out string) {
	m := waitingRe.FindAllStringSubmatch(out, -1)
	switch {
	case len(m) > 0:
		n, _ := strconv.Atoi(m[len(m)-1][1])
		saveRequestsNote(box, n, time.Now())
	case cmd == "status" && code == 0:
		saveRequestsNote(box, 0, time.Now())
	}
}

// requestsCell is what keelbox ls adds to a box's status while requests
// wait there, as far as this computer last heard.
func requestsCell(box string) string {
	n, ok := loadRequestsNote(box)
	if !ok || n.Waiting == 0 {
		return ""
	}
	return fmt.Sprintf("%s waiting as of %s (keelbox requests %s)", plural(n.Waiting, "package request", "package requests"), n.Seen.Local().Format("Jan 2 15:04"), box)
}
