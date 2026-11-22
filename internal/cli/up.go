package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/provider/aws"
	"github.com/keelbox/keelbox/internal/provider/digitalocean"
	"github.com/keelbox/keelbox/internal/provider/gcp"
	"github.com/keelbox/keelbox/internal/provider/hetzner"
	"github.com/keelbox/keelbox/internal/ui"
	"github.com/keelbox/keelbox/internal/version"
)

// keelbox up: from nothing to a signed-in box in one command
// (docs/adr/0025-keelbox-up.md). It asks where the box runs and a few
// choices, writes the config to the registry, then runs the same steps a
// user would: create or apply, login, apply again to close public SSH,
// and prints how to reach the box. Progress is kept in boxes/<box>.up.json,
// so running it again continues where it stopped.

// exitNeedsYou is up's exit code when it stopped at a step only the user
// can do (create the server from a cloud-config, sign in) and printed the
// command that continues.
const exitNeedsYou = 3

// upHooks report progress from inside create to up's state.
type upHooks struct {
	onServer func(ip string)
}

func (h *upHooks) server(ip string) {
	if h != nil && h.onServer != nil {
		h.onServer(ip)
	}
}

// upRunner does up's side effects on the box; tests replace it.
type upRunner interface {
	// create makes a cloud server and applies it (keelbox create).
	create(ctx context.Context, cfg *config.Config, o options, stdout, stderr io.Writer) int
	// apply converges the box as keelbox apply does, as o.sshUser when set.
	apply(ctx context.Context, cfg *config.Config, o options, stdout, stderr io.Writer) int
	// reachable reports whether user can log in to the box at host now.
	reachable(ctx context.Context, cfg *config.Config, host, user string) bool
	// firstContact pins a cloud-config host key (when given), waits for the
	// server to answer SSH as the admin and for cloud-init, then replaces
	// the host key with keys made on the server.
	firstContact(ctx context.Context, cfg *config.Config, hostKey string, stdout io.Writer) error
	// notSignedIn lists the sign-ins keelbox login walks through that are
	// not done, from exit codes only.
	notSignedIn(ctx context.Context, cfg *config.Config, o options) (left, notChecked []string)
	// login runs keelbox login's sign-in step on the user's terminal.
	login(ctx context.Context, cfg *config.Config, o options, p *prompt.Prompter, stdout, stderr io.Writer) loginResult
	// remoteControl starts Claude Code Remote Control on the box once it is
	// installed, with its first run on the user's terminal; false leaves it
	// for keelbox login.
	remoteControl(ctx context.Context, cfg *config.Config, o options, p *prompt.Prompter, stdout, stderr io.Writer) bool
	// skills offers, on a terminal, to copy skills from this computer to
	// the agent on the box, once, as the box becomes ready.
	skills(ctx context.Context, cfg *config.Config, o options, p *prompt.Prompter, stdout, stderr io.Writer)
	// copyID puts the public key at pubPath on the server for user with
	// ssh-copy-id, on the user's terminal.
	copyID(ctx context.Context, cfg *config.Config, user, pubPath string, out io.Writer) error
	// sudo reports whether sudo works here without a password, first
	// asking for it on the terminal when ask is set.
	sudo(ctx context.Context, ask bool, out io.Writer) bool
	// inspect runs the read-only probe on this machine (cfg nil) or over
	// SSH as user, confirming an unknown host key on the terminal.
	inspect(ctx context.Context, cfg *config.Config, user string, out io.Writer) (machineFacts, error)
	// impact runs keelbox plan --impact for cfg on the machine, as user
	// over SSH: what the first apply would change there. Changes nothing.
	impact(ctx context.Context, cfg *config.Config, o options) (impactReport, error)
}

// wizard is one run of keelbox up.
type wizard struct {
	p           *prompt.Prompter
	o           options
	run         upRunner
	out, errOut io.Writer
	start       time.Time
	bannered    bool // the banner is on the screen
	fresh       bool // configured in this run
	progressed  bool // a stage was completed in this run
	changing    bool // past the questions: a stage that changes things has started
	box         string
	st          ui.Style
	draft       *upDraft        // the answers so far, on a terminal (updraft.go)
	drafted     map[string]bool // flags set from the draft, not the command line
	via         string          // where the box runs, once known: for the step indicator
	stepAt      string          // the step shown last
}

// banner opens keelbox up on a terminal, once, when it has its first
// question to ask or its first step to run: not above a one-line error
// about how it was called.
func (w *wizard) banner() {
	if w.bannered || !w.p.Interactive() {
		return
	}
	w.bannered = true
	if b := w.st.Banner(version.String(), ui.Tagline); b != "" {
		fmt.Fprintln(w.out, b)
	}
}

// logf says what up is doing: "keelbox up: ..." in plain text, a brass
// marker on a styled terminal.
func (w *wizard) logf(format string, a ...any) {
	if w.st.Live {
		say(w.out, w.st, w.st.Pointer(), fmt.Sprintf(format, a...))
		return
	}
	fmt.Fprintln(w.out, w.st.Plain("keelbox up: "+fmt.Sprintf(format, a...), 2))
}

// hint prints text that explains the next question: plain, or dimmed and
// wrapped on a styled terminal.
func (w *wizard) hint(text string) {
	if !w.st.Live {
		fmt.Fprintln(w.out, w.st.Plain(text, 0))
		return
	}
	fmt.Fprintln(w.out, "  "+w.st.Dim(ui.Hang(text, w.st.Cols()-2, 2)))
}

// ok says a check passed: "  ok: text", or a tick on a styled terminal.
func (w *wizard) ok(text string) {
	if !w.st.Live {
		fmt.Fprintln(w.out, w.st.Plain("  ok: "+text, 6))
		return
	}
	sayAsIs(w.out, w.st, w.st.Tick(), text) // it starts with the host's name
}

// lognl is logf after a blank line in plain text; on a styled terminal the
// step indicator before it already sets it apart.
func (w *wizard) lognl(format string, a ...any) {
	if !w.st.Live {
		fmt.Fprintln(w.out)
	}
	w.logf(format, a...)
}

// applying says an apply starts. On a styled terminal the step heading
// and the progress view's own heading say it.
func (w *wizard) applying(format string, a ...any) {
	if !w.st.Live {
		w.lognl(format, a...)
	}
}

// again says why an apply runs a second time; on a styled terminal as a
// hint under which the progress view names the box.
func (w *wizard) again(format string, a ...any) {
	if !w.st.Live {
		w.logf(format, a...)
		return
	}
	w.hint(capitalFirst(fmt.Sprintf(format, a...)) + ".")
}

// routeSteps are the steps keelbox up shows for each place a box runs.
var routeSteps = map[string][]string{
	viaHetzner:   {"Where the box runs", "Hetzner token", "Server", "Agents and access", "Review", "Set up", "Sign in"},
	viaAWS:       {"Where the box runs", "AWS sign-in", "Server", "Agents and access", "Review", "Set up", "Sign in"},
	viaDO:        {"Where the box runs", "DigitalOcean sign-in", "Server", "Agents and access", "Review", "Set up", "Sign in"},
	viaGCP:       {"Where the box runs", "Google Cloud sign-in", "Server", "Agents and access", "Review", "Set up", "Sign in"},
	viaSSH:       {"Where the box runs", "Server", "Agents and access", "What runs there", "Review", "Set up", "Sign in"},
	viaLocal:     {"Where the box runs", "This machine", "Agents and access", "What runs here", "Review", "Set up", "Sign in"},
	viaCloudInit: {"Where the box runs", "Cloud-config", "Agents and access", "Review", "Create the server", "Set up", "Sign in"},
}

// step shows the step indicator, "Step 2 of 7 · Hetzner token", when up
// asks on a terminal and the step is one of the route's.
func (w *wizard) step(name string) {
	if !w.p.Interactive() || name == w.stepAt {
		return
	}
	steps := routeSteps[w.via]
	if steps == nil {
		steps = routeSteps[viaSSH] // every route starts with the same step and has as many
	}
	for i, n := range steps {
		if n == name {
			w.stepAt = name
			fmt.Fprint(w.out, w.st.Step(i+1, len(steps), name))
			return
		}
	}
}

// needFlag explains an answer up could not ask for.
func (w *wizard) needFlag(what, flag, example string) error {
	return fmt.Errorf("%s: no terminal to ask on; pass %s, for example: %s", what, flag, example)
}

// hostKeyPlaceholder stands for a fingerprint in an example command: the
// user reads the real one off the server or the provider's console.
const hostKeyPlaceholder = "SHA256:..."

// upFlagOrder puts the flags that say where the box is first in a command
// upCommand prints; the rest follow by name, and --yes comes last.
var upFlagOrder = []string{"provider", "host", "ssh-user", "host-key"}

var plainArg = regexp.MustCompile(`^[A-Za-z0-9_./=:@+~,%-]+$`)

// upCommand is the keelbox up command that repeats the flags this run was
// given, with change replacing or adding some, so that a fix an error
// prints works when it is run as printed. Answers taken from a kept draft
// are left out: the next run finds them there again.
func (w *wizard) upCommand(change map[string]string) string {
	o := w.o
	fs := flagSet(&o, io.Discard) // defining the flags resets their fields
	o = w.o
	words := map[string]string{}
	fs.VisitAll(func(f *flag.Flag) {
		v, changed := change[f.Name]
		switch {
		case changed:
		case !w.o.set[f.Name] || w.drafted[f.Name] || f.Name == "name":
			return
		default:
			v = f.Value.String()
		}
		if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
			words[f.Name] = "--" + f.Name
			if v == "false" {
				words[f.Name] += "=false"
			}
			return
		}
		if v == "" {
			return // an answer skipped, not one to repeat
		}
		if !plainArg.MatchString(v) {
			v = quoteSh(v)
		}
		words[f.Name] = "--" + f.Name + " " + v
	})
	out := []string{"keelbox up"}
	if w.box != "" {
		out = append(out, w.box)
	}
	for _, n := range upFlagOrder {
		if words[n] != "" {
			out = append(out, words[n])
			delete(words, n)
		}
	}
	yes := words["yes"]
	delete(words, "yes")
	rest := make([]string, 0, len(words))
	for n := range words {
		rest = append(rest, n)
	}
	sort.Strings(rest)
	for _, n := range rest {
		out = append(out, words[n])
	}
	if yes != "" {
		out = append(out, yes)
	}
	return strings.Join(out, " ")
}

// cmdUp is keelbox up [box].
func cmdUp(ctx context.Context, o options, pos []string, p *prompt.Prompter, run upRunner, stdout, stderr io.Writer) int {
	w := &wizard{p: p.OnInterrupt(ctx.Done()), o: o, run: run, out: stdout, errOut: stderr, start: time.Now(), st: o.style}
	defer asInvoker()()
	stopSaid.Store(true) // up says what a Ctrl-C at one of its questions means
	setInterruptNote("") // while asking, Ctrl-C ends the question; nothing has changed
	code, err := w.up(ctx, pos)
	if isRegistered(w.box) {
		noteBoxUsed(w.box) // offered first the next time a command names no box
	}
	again := "keelbox up"
	switch {
	case w.box != "" && isKnown(w.box):
		again += " " + w.box
	case w.draft != nil && w.draft.Box != "":
		again += " " + w.draft.Box
	}
	kept := ""
	if w.draft != nil && !w.changing {
		if _, ok := loadDraft(); ok {
			kept = " Your answers so far are kept."
		}
	}
	var stopped *signInStopped
	var needs *needsYouError
	var waits *waitingStopped
	// On a terminal each outcome is one block: what happened, why, and the
	// one command that continues.
	est := errStyle(stderr)
	say := func(sev ui.Severity, text string) {
		trouble(stderr, ui.Trouble{Severity: sev, Text: text, Prefix: "keelbox up: "})
	}
	// failed is the block for an error. Where the answers are kept, that
	// is its last line, and the command that continues is its arrow unless
	// the error names the one that fixes it.
	failed := func(sev ui.Severity, err error, lead string) {
		t := ui.Trouble{Severity: sev, Text: err.Error(), Prefix: "keelbox up: "}
		if kept != "" {
			t.Note, t.Next = "Nothing was changed."+kept, again
		}
		if est.Live || est.Width > 0 {
			trouble(stderr, t)
			return
		}
		fmt.Fprintln(stderr, "keelbox up:", err)
		if kept != "" {
			fmt.Fprintf(stderr, "Nothing was changed.%s %s: %s\n", kept, lead, again)
		}
	}
	switch {
	case code == exitDetached && err == nil:
		return code
	case errors.As(err, &stopped):
		say(ui.Stopped, fmt.Sprintf("stopped at the %s sign-in; nothing is half-done. Continue: %s", stopped.name, again))
		return exitInterrupted
	case errors.As(err, &waits):
		// The natural moment to go and create the server: nothing is lost.
		say(ui.Stopped, fmt.Sprintf("stopped; %s is saved and waits for its server. Its cloud-config is in %s. Continue: %s",
			waits.box, tildePath(cloudConfigPath(waits.box)), again))
		return code
	case errors.Is(err, errStopHere):
		say(ui.Stopped, fmt.Sprintf("stopped; nothing was changed.%s Continue: %s", kept, again))
		return exitNeedsYou
	case errors.Is(err, prompt.ErrInterrupted) || (err != nil || code != 0) && ctx.Err() != nil && !w.changing:
		if kept != "" {
			say(ui.Stopped, fmt.Sprintf("stopped; nothing was changed.%s Continue: %s", kept, again))
		} else {
			say(ui.Stopped, fmt.Sprintf("stopped; nothing was changed. Start again: %s", again))
		}
		return exitInterrupted
	case (err != nil || code != 0) && ctx.Err() != nil:
		say(ui.Stopped, fmt.Sprintf("stopped between two steps, so nothing is half-done. Continue: %s", again))
		return exitInterrupted
	case errors.Is(err, prompt.ErrNoAnswer) && !w.changing && kept != "":
		// Ctrl-D at a question before anything changed: as Ctrl-C there.
		say(ui.Stopped, fmt.Sprintf("stopped; nothing was changed.%s Continue: %s", kept, again))
		return 1
	case errors.Is(err, prompt.ErrNoAnswer) && !w.changing:
		say(ui.Stopped, fmt.Sprintf("stopped; nothing was changed. Start again: %s", again))
		return 1
	case errors.Is(err, prompt.ErrNoAnswer):
		say(ui.Stopped, fmt.Sprintf("stopped; nothing more was changed.%s Continue later: %s", kept, again))
		return 1
	case errors.As(err, &needs):
		failed(ui.NeedsYou, err, "Then continue")
		return exitNeedsYou
	case err != nil:
		failed(ui.Failure, err, "Once that is fixed, continue")
		if code == 0 {
			code = 1
		}
	}
	return code
}

// errStopHere is the user choosing to stop and come back later.
var errStopHere = errors.New("stopped by the user")

// needsYouError is a step only the user can do before up can go on, such
// as putting the SSH key on the server; up exits 3.
type needsYouError struct{ msg string }

func (e *needsYouError) Error() string { return e.msg }

// waitingStopped is keelbox up left at the question for the address of a
// server the user has yet to create: the box is saved and waits for it.
type waitingStopped struct{ box string }

func (e *waitingStopped) Error() string { return e.box + " waits for its server" }

// signInStopped is Ctrl-C during the sign-in step.
type signInStopped struct{ name string }

func (e *signInStopped) Error() string { return "stopped at the " + e.name + " sign-in" }

func (w *wizard) up(ctx context.Context, pos []string) (int, error) {
	if len(pos) > 1 {
		return 2, fmt.Errorf("up takes one box name, got %d: keelbox up %s", len(pos), pos[0])
	}
	name := w.o.name
	if len(pos) == 1 {
		name = pos[0]
	}
	if name != "" {
		if err := validBoxName(name); err != nil {
			return 2, fmt.Errorf("box name %q: %v, for example: keelbox up %s", name, err, suggestName(name))
		}
	}
	if name == "" {
		if ok, err := w.offerDraft(); err != nil {
			return 1, err
		} else if ok {
			name = w.o.name
		} else if n, stop, code, err := w.offerUnfinished(); err != nil {
			return 1, err
		} else if stop {
			return code, nil
		} else {
			name = n
		}
	} else if d, ok := loadDraft(); ok && d.Box == name && !isKnown(name) && w.p.Interactive() {
		w.useDraft(d)
		w.logf("using the answers you gave for %s before", name)
	}
	w.box = name
	if name != "" {
		if st, cfg, ok, err := w.existing(name); err != nil {
			return 2, err
		} else if ok {
			if cfg != nil && isCloud(cfg.Target.Provider) {
				// A parked box has no server, and up would create one
				// beside the snapshot that holds the box.
				if perr := parkedLocally(cfg, name); perr != nil {
					return 1, perr
				}
			}
			return w.resume(ctx, cfg, st)
		}
	}
	cfg, st, proceed, err := w.configure(ctx, name)
	var needs *needsYouError
	switch {
	case errors.As(err, &needs):
		return exitNeedsYou, err
	case err != nil:
		return 2, err
	case !proceed:
		return 0, nil // the user said no
	}
	w.fresh = true
	return w.resume(ctx, cfg, st)
}

// offerDraft: keelbox up with no name, when answers from a run that did not
// get as far as writing a config are kept, offers to carry on with them.
func (w *wizard) offerDraft() (bool, error) {
	d, ok := loadDraft()
	if !ok || !w.p.Interactive() || (d.Box != "" && isKnown(d.Box)) {
		return false, nil
	}
	if p := w.o.provider; p != "" && p != d.Answers["provider"] {
		return false, nil
	}
	w.banner()
	yes, err := w.p.Confirm(fmt.Sprintf("You started setting up %s. Carry on with those answers?", d.describe()), true)
	if err != nil {
		return false, err
	}
	if !yes {
		forgetDraft()
		return false, nil
	}
	w.useDraft(d)
	return true, nil
}

var digitsOnly = regexp.MustCompile(`^[0-9]+$`)

// validBoxName accepts a hostname label that is not only digits (a number
// typed at the name question is more likely a menu answer).
func validBoxName(s string) error {
	if !boxNameRe.MatchString(s) {
		return errors.New("use lowercase letters, digits and hyphens")
	}
	if digitsOnly.MatchString(s) {
		return errors.New("use a name with a letter in it, such as devbox")
	}
	return nil
}

var notName = regexp.MustCompile(`[^a-z0-9-]+`)

func suggestName(s string) string {
	s = strings.Trim(notName.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" || validBoxName(s) != nil {
		return "devbox"
	}
	return s
}

// existing loads a box up already knows: its config in the registry, or
// the answers of a cloud-config box whose server does not exist yet.
func (w *wizard) existing(name string) (upState, *config.Config, bool, error) {
	path := boxConfigPath(name)
	if _, err := os.Stat(path); err == nil {
		cfg, err := config.Load(path)
		if err != nil {
			return upState{}, nil, false, fmt.Errorf("%w\nFix %s, then run keelbox up %s again", err, path, name)
		}
		st, ok := loadUpState(cfg)
		if !ok {
			// A config written by hand or by init: start at the beginning.
			st = upState{Via: cfg.Target.Provider, Stage: stageConfigured}
		}
		return st, cfg, true, nil
	}
	st, ok := loadUpState(&config.Config{Box: name})
	if ok && st.Pending != nil {
		return st, nil, true, nil
	}
	return upState{}, nil, false, nil
}

// --- questions ----------------------------------------------------------

// whereCloud is the "where" answer that leads to the "Which cloud?"
// question; the cloud picked there is the route (viaHetzner and the rest).
const whereCloud = "cloud"

// whereChoices are the places a box can run, most used first: hardware
// people already have (a server they reach over SSH, the machine they are
// on), then a rented server keelbox creates, then one they create. The
// clouds share one entry, and askCloud asks which.
func whereChoices() []prompt.Choice {
	var names []string
	for _, e := range cloudEntries() {
		names = append(names, e.name)
	}
	return []prompt.Choice{
		{Key: viaSSH, Label: "An existing server (SSH)", Detail: "a home server, mini PC, VM or VPS you reach over SSH"},
		{Key: viaLocal, Label: "This machine", Detail: "set up the Ubuntu machine you are on, with sudo"},
		{Key: whereCloud, Label: "A new cloud server", Detail: "keelbox creates it at " + orList(names)},
		{Key: viaCloudInit, Label: "A server I create myself (cloud-config)", Detail: "any provider: paste a snippet keelbox prints, no token"},
	}
}

// providerNames are the values --provider takes, as a sentence lists them.
func providerNames() string {
	names := []string{viaSSH, viaLocal}
	for _, e := range cloudEntries() {
		names = append(names, e.via)
	}
	return orList(append(names, viaCloudInit))
}

// orList joins names as "a, b or c".
func orList(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	last := len(names) - 1
	return strings.Join(names[:last], ", ") + " or " + names[last]
}

// answers are what configure gathers before anything is written.
type answers struct {
	via         string
	starter     config.Starter
	pubKey      string // the public key line both accounts get
	offer       *hetzner.Offer
	awsOffer    *aws.Offer
	doOffer     *digitalocean.Offer
	client      *hetzner.Client
	hostKey     bool // cloud-init: put a single-use host key in the snippet
	notes       []string
	facts       *machineFacts // what keelbox found on this machine or the server
	blocked     []lanChoice   // services and ports the firewall will block from the LAN
	impact      *impactReport // keelbox plan --impact on the machine, before the go-ahead
	adopt       bool          // the owner accepted what the first apply cuts off or replaces
	tokenStored bool          // the Hetzner token is in the credential store
	rename      bool          // the machine's hostname becomes the box name
	offerRC     bool          // ask about Remote Control after the Claude Code sign-in
}

func (w *wizard) configure(ctx context.Context, name string) (*config.Config, upState, bool, error) {
	var a answers
	w.banner()
	if w.p.Interactive() {
		if w.st.Live {
			w.hint("Set up a box for AI coding agents. Ctrl-C stops at any question; keelbox up continues later.")
		} else {
			fmt.Fprintln(w.out, w.st.Plain("keelbox up: set up a box for AI coding agents. Ctrl-C stops; keelbox up continues later.", 2))
		}
		if w.draft == nil {
			w.draft = &upDraft{Answers: map[string]string{}}
		}
	}
	w.step("Where the box runs")
	if err := w.askWhere(ctx, &a); err != nil {
		return nil, upState{}, false, err
	}
	w.via = a.via
	if a.via == viaLocal {
		w.step("This machine")
		if err := w.prepareLocal(ctx, &a); err != nil {
			return nil, upState{}, false, err
		}
	}
	if name == "" && !w.p.Interactive() {
		return nil, upState{}, false, w.needFlag("box name", "a name", "keelbox up acme --provider "+a.via+" --yes")
	}
	// An existing server is looked at before it is named: the name
	// defaults to its hostname.
	if name == "" && a.via != viaSSH {
		n, err := w.askName(&a)
		if err != nil {
			return nil, upState{}, false, err
		}
		name = n
	}
	w.box = name
	a.starter.Box = name
	if name != "" {
		w.rememberBox(name) // keelbox up NAME carries on with these answers
	}
	if a.facts != nil {
		keepMachineSettings(&a)
		// The account keelbox up runs from here is the user's own, even when
		// cloud-init made it: it is not locked.
		a.starter.KeepCloudUser = contains(a.facts.CloudUsers, a.facts.User)
	}
	var err error
	switch a.via {
	case viaSSH:
		w.step("Server")
		err = w.askSSH(&a)
	case viaCloudInit:
		w.step("Cloud-config")
		err = w.askCloudInit(&a)
	case viaLocal:
		a.starter.Provider = config.ProviderLocal
	default:
		if e, ok := cloudNamed(a.via); ok {
			err = e.ask(ctx, w, &a)
		}
	}
	if err != nil {
		return nil, upState{}, false, err
	}
	if err := w.askKey(ctx, &a); err != nil {
		return nil, upState{}, false, err
	}
	if a.via == viaSSH {
		if err := w.inspectServer(ctx, &a); err != nil {
			return nil, upState{}, false, err
		}
		if name == "" {
			n, err := w.askName(&a)
			if err != nil {
				return nil, upState{}, false, err
			}
			name = n
		}
		w.box, a.starter.Box = name, name
	}
	if err := w.askRename(&a); err != nil {
		return nil, upState{}, false, err
	}
	w.step("Agents and access")
	if err := w.askAgents(&a); err != nil {
		return nil, upState{}, false, err
	}
	if a.via == viaSSH || a.via == viaLocal {
		w.step(map[bool]string{true: "What runs here", false: "What runs there"}[a.via == viaLocal])
	}
	if err := w.inspectImpact(ctx, &a); err != nil {
		return nil, upState{}, false, err
	}
	if err := w.askLANPorts(&a); err != nil {
		return nil, upState{}, false, err
	}
	if err := w.askLANSSH(&a); err != nil {
		return nil, upState{}, false, err
	}
	if err := w.askAgentLimits(&a); err != nil {
		return nil, upState{}, false, err
	}
	// Render once now so a bad combination fails before anything changes.
	check := a.starter
	if check.Provider == config.ProviderSSH && check.SSHHost == "" {
		check.SSHHost = "203.0.113.10" // a cloud-config box gets its address later
	}
	if _, err := check.Render(); err != nil {
		return nil, upState{}, false, err
	}
	w.step("Review")
	if ok, err := w.confirmPlan(&a); err != nil || !ok {
		if err == nil {
			w.declinedGoAhead(name, len(openGated(&a)) > 0, a.tokenStored)
		}
		return nil, upState{}, false, err
	}
	if a.via == viaHetzner {
		if err := w.uploadKey(ctx, &a); err != nil {
			return nil, upState{}, false, err
		}
	}
	st := upState{Via: a.via, Stage: stageConfigured, Adopt: a.adopt || w.o.adopt, OfferRemoteControl: a.offerRC}
	if a.via == viaSSH {
		st.Bootstrap, st.Address = w.o.sshUser, a.starter.SSHHost
		st.SudoPassword = a.facts != nil && !a.facts.Sudo
	}
	if a.via == viaCloudInit {
		if err := w.printCloudConfig(ctx, &a, &st); err != nil {
			return nil, upState{}, false, err
		}
		forgetDraft()
		return nil, st, true, nil
	}
	cfg, err := writeBoxConfig(a.starter)
	if err != nil {
		return nil, upState{}, false, err
	}
	if err := saveUpState(name, st); err != nil {
		return nil, upState{}, false, err
	}
	forgetDraft()
	w.logf("wrote %s", tildePath(boxConfigPath(name)))
	return cfg, st, true, nil
}

// declinedGoAhead is what up says after a no at its go-ahead: the one
// sentence every declined question gets, that the answers are kept (the
// draft stays, so the next keelbox up asks only this again), and the
// command that continues. On a terminal it is the block of a stop.
func (w *wizard) declinedGoAhead(name string, gated, tokenStored bool) {
	const stays = "The Hetzner token stays stored for next time; remove it: keelbox forget-token"
	if !w.st.Live {
		if gated {
			fmt.Fprintln(w.out, w.st.Plain("keelbox up: nothing changed. Your answers are kept: keep what the items marked * would stop first (the hints say how), then continue: keelbox up "+name, 2))
		} else {
			fmt.Fprintln(w.out, w.st.Plain("keelbox up: nothing changed. Your answers are kept; continue: keelbox up "+name, 2))
		}
		if tokenStored {
			fmt.Fprintln(w.out, w.st.Plain(stays, 0))
		}
		return
	}
	text := "nothing changed; your answers are kept. Continue: keelbox up " + name
	if gated {
		text = "nothing changed; your answers are kept. To keep what the items marked * would stop, the hints say how. Continue: keelbox up " + name
	}
	fmt.Fprint(w.out, "\n"+w.st.Trouble(ui.Trouble{Severity: ui.Stopped, Text: text, Names: []string{name}}))
	if tokenStored {
		sayAsIs(w.out, w.st, w.st.Faint(w.st.Bullet()), paintCommands(w.st, stays))
	}
}

// askName asks for the box name. On a machine keelbox has looked at, the
// default is its own hostname, so nothing is renamed by accepting it.
func (w *wizard) askName(a *answers) (string, error) {
	if !w.p.Interactive() {
		return "", w.needFlag("box name", "a name", "keelbox up acme --provider "+a.via+" --yes")
	}
	def, hint := "devbox", "The box's name is also the server's name and its hostname."
	if a.facts != nil {
		hint = ""
		if h := a.facts.Hostname; validBoxName(h) == nil && !isKnown(h) {
			def, hint = h, "Enter keeps the machine's hostname as the box's name."
		}
	}
	for i := 2; isKnown(def); i++ {
		def = fmt.Sprintf("devbox-%d", i)
	}
	if hint != "" {
		w.hint(hint)
	}
	n, err := w.p.AskValid("Box name", def, func(s string) error {
		if err := validBoxName(s); err != nil {
			return err
		}
		if isKnown(s) {
			return fmt.Errorf("%s exists; continue it with keelbox up %s, or pick another name", s, s)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	w.rememberBox(n)
	return n, nil
}

// askRename: a machine the user already has keeps its hostname unless they
// say otherwise. keelbox needs a DNS-style hostname, so one that is not
// becomes the box name, and the summary says so.
func (w *wizard) askRename(a *answers) error {
	f := a.facts
	if f == nil || f.Hostname == "" || f.Hostname == a.starter.Box {
		return nil
	}
	if !config.ValidHostname(f.Hostname) {
		a.rename = true
		return nil
	}
	if v, ok := w.draftAnswer("rename"); ok {
		a.rename = v == "true"
	} else if w.p.Interactive() {
		ok, err := w.p.Confirm(fmt.Sprintf("Rename the machine from %s to %s as well (its hostname)?", f.Hostname, a.starter.Box), false)
		if err != nil {
			return err
		}
		a.rename = ok
		w.remember("rename", fmt.Sprint(ok))
	}
	if !a.rename {
		a.starter.Hostname = f.Hostname
	}
	return nil
}

// askWhere asks where the box runs, and for a new cloud server, which
// cloud creates it. --provider answers both.
func (w *wizard) askWhere(ctx context.Context, a *answers) error {
	if p := w.o.provider; p != "" {
		if _, cloud := cloudNamed(p); !cloud && p != viaSSH && p != viaLocal && p != viaCloudInit {
			return fmt.Errorf("--provider %q: use %s", p, providerNames())
		}
		a.via = p
		w.remember("provider", p)
		return nil
	}
	if !w.p.Interactive() {
		return w.needFlag("where the box runs", "--provider "+providerNames(), "keelbox up acme --provider ssh --host 203.0.113.10 --host-key "+hostKeyPlaceholder+" --yes")
	}
	choices := whereChoices()
	// Run as root, or on a server with no desktop, this machine is the
	// likely answer: keelbox up was started on the box itself. The row
	// says why it is the one Enter takes.
	def := 0
	if localLikely() {
		def = 1
		choices[def].Detail += "; preselected because " + localWhy()
	}
	i, err := w.p.Choose("Where should the box run?", choices, def)
	if err != nil {
		return err
	}
	a.via = choices[i].Key
	if a.via == whereCloud {
		if a.via, err = w.askCloud(ctx); err != nil {
			return err
		}
	}
	w.remember("provider", a.via)
	return nil
}

// askCloud asks which cloud creates the server. Each row says what the
// cloud needs on this computer and whether it is there. A cloud whose CLI
// is missing can still be picked: its own step says how to install it.
// Hetzner is the default, unless keelbox has no Hetzner token to use and
// exactly one of the other clouds is ready.
func (w *wizard) askCloud(ctx context.Context) (string, error) {
	entries := cloudEntries()
	var choices []prompt.Choice
	var ready []int
	for i, e := range entries {
		detail, ok := e.needs(ctx, w.o)
		choices = append(choices, prompt.Choice{Key: e.via, Label: e.name, Detail: detail})
		if ok {
			ready = append(ready, i)
		}
	}
	def := 0
	if len(ready) == 1 {
		def = ready[0]
	}
	i, err := w.p.Choose("Which cloud?", choices, def)
	if err != nil {
		return "", err
	}
	return entries[i].via, nil
}

// hetznerNeeds is the Hetzner line of the "Which cloud?" question: the
// token askHetzner would use, looked for where it looks, and not read
// from the hcloud CLI.
func hetznerNeeds(ctx context.Context, o options) (string, bool) {
	switch {
	case o.tokenFile != "":
		return "uses your token file", true
	case os.Getenv("HCLOUD_TOKEN") != "":
		return "uses HCLOUD_TOKEN", true
	case o.hcloudContext != "":
		return "hcloud CLI signed in", true
	}
	if _, err := openStore(nil).Get(ctx, "hetzner"); err == nil {
		return "token stored", true
	}
	if name := hcloudActiveContext(ctx); name != "" && config.ValidHcloudContext(name) == nil {
		return "hcloud CLI signed in", true
	}
	return "needs an API token", false
}

// askHetzner gets a working token, then the location and server type.
func (w *wizard) askHetzner(ctx context.Context, a *answers) error {
	s := &a.starter
	s.Provider, s.Image = config.ProviderHetzner, "ubuntu-24.04"
	w.step("Hetzner token")
	var token string
	switch {
	case w.o.tokenFile != "":
		t, err := hetzner.TokenFromFile(config.ExpandHome(w.o.tokenFile))
		if err != nil {
			return err
		}
		token, s.TokenFile = t, w.o.tokenFile
		w.remember("token-file", w.o.tokenFile)
	case os.Getenv("HCLOUD_TOKEN") != "":
		token = os.Getenv("HCLOUD_TOKEN")
	case w.o.hcloudContext != "":
		if err := config.ValidHcloudContext(w.o.hcloudContext); err != nil {
			return fmt.Errorf("--hcloud-context %q: %w", w.o.hcloudContext, err)
		}
		t, err := hcloudToken(ctx, w.o.hcloudContext, s.Box)
		if err != nil {
			return err
		}
		token, s.HcloudContext = t, w.o.hcloudContext
		w.remember("hcloud-context", w.o.hcloudContext)
	default:
		store := openStore(warnTo(w.out))
		t, err := store.Get(ctx, "hetzner")
		var fromHcloud bool
		if err != nil && w.p.Interactive() {
			// Signed in to Hetzner's own CLI already: offer its token
			// instead of a new one to paste.
			ht, ok, herr := w.offerHcloud(ctx, s)
			if herr != nil {
				return herr
			}
			if ok {
				t, fromHcloud = ht, true
			}
		}
		switch {
		case fromHcloud:
			token = t
		case err == nil:
			token, a.tokenStored = t, true
		case !w.p.Interactive():
			return w.needFlag("no Hetzner API token is stored", "--token-file with a mode 600 file holding one", "keelbox up "+s.Box+" --provider hetzner --token-file ~/hetzner.token --yes")
		default:
			t, _, err := askHetznerToken(ctx, w.p, w.out, store, "hetzner")
			if err != nil {
				return err
			}
			token, a.tokenStored = t, true
		}
	}
	a.client = newHetznerClient(token)
	tc, err := a.client.CheckToken(ctx)
	if err != nil {
		if errors.Is(err, hetzner.ErrTokenRefused) && s.HcloudContext != "" {
			return fmt.Errorf("%w; it is the token of the hcloud CLI's context %q. Check it there (hcloud server list), or run keelbox up %s again and answer no to store another token with keelbox", err, s.HcloudContext, s.Box)
		}
		if errors.Is(err, hetzner.ErrTokenRefused) && s.TokenFile == "" {
			return fmt.Errorf("%w; store a new one: keelbox forget-token, then keelbox up %s", err, s.Box)
		}
		return err
	}
	if tc.Unmanaged > 0 {
		a.notes = append(a.notes, fmt.Sprintf("the token's project has %s keelbox did not create; keelbox leaves them alone, but a project of its own keeps the token away from them", plural(tc.Unmanaged, "server", "servers")))
	}
	if s2, err := a.client.ServerByName(ctx, s.Box); err != nil {
		return err
	} else if s2 != nil {
		return fmt.Errorf("the Hetzner project already has a server named %s (id %d); pick another name: keelbox up %s-2", s.Box, s2.ID, s.Box)
	}
	locs, err := a.client.Locations(ctx)
	if err != nil {
		return err
	}
	types, err := a.client.ServerTypes(ctx)
	if err != nil {
		return err
	}
	w.step("Server")
	if err := w.askLocation(a, locs, types); err != nil {
		return err
	}
	w.remember("location", s.Location)
	if err := w.askSize(a, types); err != nil {
		return err
	}
	w.remember("size", s.ServerType)
	return nil
}

// offerHcloud asks whether to use the token of the hcloud CLI's active
// context, when there is one keelbox can name in a config. A yes records
// the context in the config; the token itself is read from hcloud now and
// again at each later use, and stored nowhere by keelbox.
func (w *wizard) offerHcloud(ctx context.Context, s *config.Starter) (string, bool, error) {
	name := hcloudActiveContext(ctx)
	if name == "" || config.ValidHcloudContext(name) != nil {
		return "", false, nil
	}
	w.hint(fmt.Sprintf("The hcloud CLI on this computer is signed in to a Hetzner project (context %q). keelbox can use that token: "+
		"it asks hcloud for it each time and keeps no copy. It reaches every server in that project.", name))
	ok, err := w.p.Confirm(fmt.Sprintf("Use the token of the hcloud context %q?", name), true)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	t, err := hcloudToken(ctx, name, s.Box)
	if err != nil {
		// Carry on with a token of keelbox's own.
		fmt.Fprintf(w.out, "  %s %v\n", w.st.Bang(), err)
		return "", false, nil
	}
	s.HcloudContext = name
	w.remember("hcloud-context", name)
	return t, true, nil
}

func (w *wizard) askLocation(a *answers, locs []hetzner.Location, types []hetzner.ServerType) error {
	s := &a.starter
	var names []string
	for _, l := range locs {
		names = append(names, l.Name)
	}
	if w.o.location != "" {
		for _, l := range locs {
			if l.Name == w.o.location {
				s.Location = l.Name
				return nil
			}
		}
		return fmt.Errorf("--location %q is not a Hetzner location; use one of: %s", w.o.location, strings.Join(names, ", "))
	}
	def := 0
	var choices []prompt.Choice
	for i, l := range locs {
		if l.Name == "fsn1" {
			def = i
		}
		n := len(hetzner.Offers(types, l.Name))
		choices = append(choices, prompt.Choice{Key: l.Name, Label: l.Name, Detail: fmt.Sprintf("%s, %s (%d sizes)", l.City, l.Country, n)})
	}
	if len(locs) == 0 {
		return errors.New("the Hetzner API lists no locations")
	}
	if !w.p.Interactive() {
		s.Location = locs[def].Name
		return nil
	}
	i, err := w.p.Choose("Which location?", choices, def)
	if err != nil {
		return err
	}
	s.Location = locs[i].Name
	return nil
}

func (w *wizard) askSize(a *answers, types []hetzner.ServerType) error {
	s := &a.starter
	offers := hetzner.Offers(types, s.Location)
	if len(offers) == 0 {
		return fmt.Errorf("no server type can be ordered in %s now; pick another location: keelbox up %s --location nbg1", s.Location, s.Box)
	}
	pick := func(i int) {
		o := offers[i]
		a.offer, s.ServerType = &o, o.Type.Name
	}
	if w.o.size != "" {
		for i, o := range offers {
			if o.Type.Name == w.o.size {
				pick(i)
				return nil
			}
		}
		var names []string
		for _, o := range offers {
			names = append(names, o.Type.Name)
		}
		return fmt.Errorf("--size %q cannot be ordered in %s now; it can: %s", w.o.size, s.Location, strings.Join(names, ", "))
	}
	def := hetzner.DefaultOffer(offers)
	if !w.p.Interactive() {
		pick(def)
		return nil
	}
	var choices []prompt.Choice
	for _, o := range offers {
		choices = append(choices, prompt.Choice{Key: o.Type.Name, Label: o.Type.Name, Detail: o.Describe()})
	}
	recommend(choices, def)
	w.hint(fmt.Sprintf("Estimated prices from the Hetzner API for %s, with VAT. Hetzner bills by the hour until the server is deleted. %s", s.Location, sizeHint))
	i, err := w.p.Choose("Which size?", choices, def)
	if err != nil {
		return err
	}
	pick(i)
	return nil
}

var hostRe = regexp.MustCompile(`^[A-Za-z0-9.:-]+$`)

func validHost(h string) error {
	if !hostRe.MatchString(h) {
		return errors.New("an IP address or DNS name, such as 203.0.113.10")
	}
	return nil
}

func validHostKey(k string) error {
	if k == "" {
		return nil
	}
	if !regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`).MatchString(k) {
		return errors.New("a SHA256 fingerprint as ssh-keygen -l prints it: SHA256: and 43 characters")
	}
	return nil
}

func (w *wizard) askSSH(a *answers) error {
	s := &a.starter
	s.Provider = config.ProviderSSH
	example := firstNonEmpty(s.Box, "acme")
	switch {
	case w.o.host != "":
		if err := validHost(w.o.host); err != nil {
			return fmt.Errorf("--host %q: %v", w.o.host, err)
		}
		s.SSHHost = w.o.host
	case !w.p.Interactive():
		return w.needFlag("the server's address", "--host", "keelbox up "+example+" --provider ssh --host 203.0.113.10 --ssh-user root --host-key "+hostKeyPlaceholder+" --yes")
	default:
		h, err := w.p.AskValid("Server's IP address or DNS name", "", validHost)
		if err != nil {
			return err
		}
		s.SSHHost = h
	}
	w.remember("host", s.SSHHost)
	switch {
	case w.o.sshUser != "":
	case !w.p.Interactive():
		w.o.sshUser = "root"
	default:
		users := []prompt.Choice{
			{Key: "root", Label: "root", Detail: "most VPS providers"},
			{Key: "ubuntu", Label: "ubuntu", Detail: "cloud images such as AWS, Oracle, LXD"},
			{Key: "other", Label: "another account", Detail: "yours on a home server, with sudo"},
		}
		i, err := w.p.Choose("Which account do you log in to the server as now?", users, 0)
		if err != nil {
			return err
		}
		u := users[i].Key
		if u == "other" {
			if u, err = w.p.AskValid("Account name", "", func(x string) error {
				if !regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,30}$`).MatchString(x) {
					return errors.New("a Linux user name")
				}
				return nil
			}); err != nil {
				return err
			}
		}
		w.o.sshUser = u
	}
	w.remember("ssh-user", w.o.sshUser)
	pinned := (&provider.SSH{Host: s.SSHHost, Port: 22}).IsKnown()
	switch {
	case w.o.hostKey != "":
		if err := validHostKey(w.o.hostKey); err != nil {
			return fmt.Errorf("--host-key: %v", err)
		}
		s.HostKey = w.o.hostKey
	case w.o.set["host-key"] || pinned || !w.p.Interactive():
		// Skipped before, or pinned at an earlier connection.
	default:
		w.hint("The server's SSH host key fingerprint lets keelbox check it is talking to your server. " +
			"Find it in the provider's console, or run ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub there. " +
			"Skipped, keelbox shows the fingerprints at the first connection for you to compare.")
		k, err := w.p.AskOptional("Host key fingerprint", validHostKey)
		if err != nil {
			return err
		}
		s.HostKey = k
	}
	w.remember("host-key", s.HostKey)
	return nil
}

func (w *wizard) askCloudInit(a *answers) error {
	s := &a.starter
	s.Provider, s.SSHHost = config.ProviderSSH, w.o.host
	if w.o.host != "" {
		if err := validHost(w.o.host); err != nil {
			return fmt.Errorf("--host %q: %v", w.o.host, err)
		}
	}
	a.hostKey = !w.o.noHostKey
	if w.p.Interactive() && !w.o.set["no-host-key"] {
		w.hint("keelbox can put a single-use SSH host key in the snippet, so it recognises the server at first " +
			"connection without you comparing fingerprints. It replaces the key with new ones made on the server then.")
		ok, err := w.p.Confirm("Include the host key?", true)
		if err != nil {
			return err
		}
		a.hostKey = ok
	}
	w.remember("no-host-key", fmt.Sprint(!a.hostKey))
	return nil
}

// askKey finds the user's SSH key for the box, or offers to make one.
func (w *wizard) askKey(ctx context.Context, a *answers) error {
	s := &a.starter
	if a.via == viaLocal && w.o.sshKey == "" {
		keys := authorizedKeys()
		if len(keys) > 0 {
			s.Keys = keys
			a.notes = append(a.notes, fmt.Sprintf("both accounts get the %s in ~/.ssh/authorized_keys, the ones that log in here now", plural(len(keys), "key", "keys")))
			return nil
		}
		if !w.p.Interactive() {
			return w.needFlag("~/.ssh/authorized_keys has no keys for the box's accounts", "--ssh-key with the laptop's public key", "keelbox up "+s.Box+" --provider local --ssh-key ~/laptop.pub --yes")
		}
		f, err := w.p.AskValid("Public key file of the laptop you will connect from", "", func(p string) error {
			_, err := readPublicKey(config.ExpandHome(p))
			return err
		})
		if err != nil {
			return err
		}
		s.KeyFiles = []string{f}
		w.remember("ssh-key", f)
		return nil
	}
	k, err := findKey(w.o.sshKey)
	if err != nil {
		return err
	}
	if k == nil {
		if !w.p.Interactive() {
			return w.needFlag("no SSH key in ~/.ssh", "--ssh-key, or create one first: ssh-keygen -t ed25519", w.upCommand(map[string]string{"ssh-key": "~/.ssh/id_ed25519"}))
		}
		if k, err = w.makeKey(ctx); err != nil {
			return err
		}
	}
	a.pubKey = k.public
	s.KeyFiles = []string{tildePath(k.pubPath)}
	w.remember("ssh-key", tildePath(k.privPath))
	if !k.isDefault {
		s.IdentityFile = tildePath(k.privPath)
	}
	if note := keyAgentNote(ctx, k); note != "" {
		a.notes = append(a.notes, note)
	}
	return nil
}

func (w *wizard) askAgents(a *answers) error {
	s := &a.starter
	choices := []prompt.Choice{
		{Key: "both", Label: "Claude Code and Codex"},
		{Key: config.AgentClaudeCode, Label: "Claude Code"},
		{Key: config.AgentCodex, Label: "Codex"},
		{Key: "none", Label: "Neither", Detail: "only the toolchain; install agents yourself"},
	}
	lists := [][]string{config.KnownAgents, {config.AgentClaudeCode}, {config.AgentCodex}, {}}
	switch {
	case w.o.agents != "":
		agents, err := parseAgents(w.o.agents)
		if err != nil {
			return err
		}
		s.Agents = agents
	case w.p.Interactive():
		w.hint("You sign in to each agent yourself; keelbox never sees the credentials.")
		i, err := w.p.Choose("Which agents?", choices, 0)
		if err != nil {
			return err
		}
		s.Agents = lists[i]
	default:
		s.Agents = config.KnownAgents
	}
	w.remember("agents", agentsFlag(s.Agents))
	hasClaude := false
	for _, x := range s.Agents {
		hasClaude = hasClaude || x == config.AgentClaudeCode
	}
	switch {
	case w.o.set["remote-control"]:
		if w.o.remoteControl && !hasClaude {
			return errors.New("--remote-control needs Claude Code: --agents claude-code,codex --remote-control")
		}
		s.RemoteControl = w.o.remoteControl
	case hasClaude && w.p.Interactive():
		// Asked after the Claude Code sign-in, where it makes sense and
		// can start at once (ADR 0061).
		a.offerRC = true
	}
	if !a.offerRC {
		w.remember("remote-control", fmt.Sprint(s.RemoteControl))
	}
	switch {
	case w.o.set["gh"]:
		s.NoGH = !w.o.gh
	case w.p.Interactive():
		ok, err := w.p.Confirm("Install the GitHub CLI (gh), so the agent can sign in to GitHub for git push and pull requests?", true)
		if err != nil {
			return err
		}
		s.NoGH = !ok
	}
	w.remember("gh", fmt.Sprint(!s.NoGH))
	if err := w.askGit(a); err != nil {
		return err
	}
	if err := w.askPermissions(a); err != nil {
		return err
	}
	switch {
	case w.o.set["tailscale"]:
		s.NoTailscale = !w.o.tailscale
	case w.p.Interactive():
		w.hint(tailscaleHint)
		ok, err := w.p.Confirm("Use Tailscale to reach the box?", true)
		if err != nil {
			return err
		}
		s.NoTailscale = !ok
	}
	w.remember("tailscale", fmt.Sprint(!s.NoTailscale))
	return w.askReboots(a)
}

// rebootModes are the answers to askReboots, in the order asked.
var rebootModes = []string{config.AutoRebootNightly, config.AutoRebootWeekly, config.AutoRebootNever}

// askReboots asks when the box restarts for an update that needs it
// (host.auto_reboot, ADR 0090). Without a terminal the answer is
// --auto-reboot, or nightly.
func (w *wizard) askReboots(a *answers) error {
	box := a.starter.Box
	if box == "" {
		box = "BOX"
	}
	mode := config.AutoRebootNightly
	switch {
	case w.o.set["auto-reboot"] && w.o.autoReboot == "":
		// Given empty: not asked, the default.
	case w.o.set["auto-reboot"]:
		if mode = w.o.autoReboot; !config.ValidAutoReboot(mode) {
			return fmt.Errorf("--auto-reboot %q: use nightly, weekly or never, for example: --auto-reboot nightly", mode)
		}
	case w.p.Interactive():
		w.hint("Some security updates take effect only after a restart. Claude Code and Codex conversations in tmux open again after it; other programs in tmux stop.")
		i, err := w.p.Choose("When should the box restart for an update that needs it?", []prompt.Choice{
			{Key: config.AutoRebootNightly, Label: "Nightly (recommended)", Detail: "between 01:00 and 06:00 " + boxZone(a.starter.Timezone) + ", the box's timezone, once nothing has been in use for 30 minutes"},
			{Key: config.AutoRebootWeekly, Label: "Weekly", Detail: "the same, on Sunday night"},
			{Key: config.AutoRebootNever, Label: "Manually", Detail: "you run keelbox reboot " + box + " when keelbox status says one is needed"},
		}, 0)
		if err != nil {
			return err
		}
		mode = rebootModes[i]
	}
	a.starter.AutoReboot = mode
	w.remember("auto-reboot", mode)
	return nil
}

// boxZone is the box's timezone: host.timezone, UTC when unset.
func boxZone(tz string) string {
	if tz == "" {
		return "UTC"
	}
	return tz
}

// rebootsRow is the review card's line for host.auto_reboot.
func rebootsRow(mode, box, tz string) string {
	switch mode {
	case config.AutoRebootWeekly:
		return "weekly: on Sunday night between 01:00 and 06:00 " + boxZone(tz) + " once idle, when an update needs it"
	case config.AutoRebootNever:
		return "manually: you run keelbox reboot " + box + " when an update needs it"
	case "", config.AutoRebootNightly:
		return "nightly: between 01:00 and 06:00 " + boxZone(tz) + " once idle, when an update needs it"
	}
	return "at " + mode + " " + boxZone(tz) + ", idle or not, when an update needs it"
}

// recommend marks the size keelbox preselects, the cheapest that is enough
// for agents, as the recommended one. A list with no such size has none.
func recommend(choices []prompt.Choice, def int) {
	if def >= 0 && def < len(choices) && !strings.Contains(choices[def].Detail, "small for agents") {
		choices[def].Detail += "; recommended"
	}
}

// sizeHint goes with the question for a server's size: what the size
// keelbox preselects is enough for. No line breaks inside an amount.
var sizeHint = ui.Unbroken("2 vCPU") + " and " + ui.Unbroken("4 GB") + " are enough for one or two agents. On an Arm64 size, your own container images and binaries need an arm64 build."

// tailscaleHint says what Tailscale is and what a yes changes, before the
// question that offers it.
const tailscaleHint = "Tailscale is a private network between your devices. With it, SSH from outside that network closes once keelbox reaches the box over it; " +
	"without it, the box is reached over public SSH only. Yes is the recommended answer."

// permissionModes are the answers to askPermissions, in the order asked:
// the default first.
var permissionModes = []string{config.PermissionsAuto, config.PermissionsManual, config.PermissionsFull}

// askPermissions asks how much the agent may change the box
// (users.agent.permissions, ADR 0074). Without a terminal the answer is
// --agent-permissions, or auto.
func (w *wizard) askPermissions(a *answers) error {
	box := a.starter.Box
	if box == "" {
		box = "BOX"
	}
	mode := config.PermissionsAuto
	switch {
	case w.o.set["agent-permissions"] && w.o.agentPermissions == "":
		// Given empty: not asked, the default.
	case w.o.set["agent-permissions"]:
		if mode = w.o.agentPermissions; !config.ValidPermissions(mode) {
			return fmt.Errorf("--agent-permissions %q: use auto, manual or full, for example: --agent-permissions auto", mode)
		}
	case w.p.Interactive():
		i, err := w.p.Choose("How much may the agent change this box?", []prompt.Choice{
			{Key: config.PermissionsAuto, Label: "Auto (recommended)", Detail: "installs Ubuntu packages itself with keelbox install; no root shell"},
			{Key: config.PermissionsManual, Label: "Manual", Detail: "asks first; you approve with keelbox approve " + box},
			{Key: config.PermissionsFull, Label: "Full", Detail: "sudo: it can change anything, and none of keelbox's limits on the agent then hold"},
		}, 0)
		if err != nil {
			return err
		}
		mode = permissionModes[i]
	}
	a.starter.Permissions = mode
	w.remember("agent-permissions", mode)
	return nil
}

// permissionsRow is the review card's line for users.agent.permissions.
func permissionsRow(mode, box string) string {
	switch mode {
	case config.PermissionsManual:
		return "manual: the agent asks for Ubuntu packages; you approve each with keelbox approve " + box
	case config.PermissionsFull:
		return "full: the agent has sudo, and none of keelbox's limits on it then hold"
	}
	return "auto: the agent installs Ubuntu packages itself with keelbox install; it has no sudo"
}

// laptopGit reads one key of this computer's own git config: the identity
// keelbox up offers for the agent's commits. Tests replace it.
var laptopGit = func(key string) string {
	out, err := exec.Command("git", "config", "--get", key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// askGit asks for the name and email the agent's commits carry
// (users.agent.git, ADR 0064), offering this computer's. It can be
// skipped; without a terminal only --git-name and --git-email set it.
func (w *wizard) askGit(a *answers) error {
	name, email := w.o.gitName, w.o.gitEmail
	switch {
	case w.o.set["git-name"] || w.o.set["git-email"]:
		if name == "" && email == "" {
			break // skipped, by flag or in the answers kept from last time
		}
		if name == "" || email == "" {
			return errors.New("--git-name and --git-email go together: --git-name \"Your Name\" --git-email you@example.com")
		}
		if err := config.ValidGitName(name); err != nil {
			return fmt.Errorf("--git-name %q: %w", name, err)
		}
		if err := config.ValidGitEmail(email); err != nil {
			return fmt.Errorf("--git-email %q: %w", email, err)
		}
	case w.p.Interactive():
		w.hint("The agent's git commits carry a name and an email. Without them git on the box refuses to commit.")
		name, email = laptopGit("user.name"), laptopGit("user.email")
		if config.ValidGitName(name) == nil && config.ValidGitEmail(email) == nil {
			ok, err := w.p.Confirm(fmt.Sprintf("Commit as %s <%s>, as git does on this computer?", name, email), true)
			if err != nil {
				return err
			}
			if ok {
				break
			}
		}
		var err error
		if name, err = w.p.AskOptional("Name on the agent's commits", config.ValidGitName); err != nil {
			return err
		}
		email = ""
		if name != "" {
			if email, err = w.p.AskValid("Email on the agent's commits", "", config.ValidGitEmail); err != nil {
				return err
			}
		}
	}
	a.starter.GitName, a.starter.GitEmail = name, email
	w.remember("git-name", name)
	w.remember("git-email", email)
	return nil
}

func agentsFlag(agents []string) string {
	if len(agents) == 0 {
		return "none"
	}
	return strings.Join(agents, ",")
}

func parseAgents(v string) ([]string, error) {
	if v == "none" {
		return []string{}, nil
	}
	var out []string
	for _, x := range strings.Split(v, ",") {
		x = strings.TrimSpace(x)
		switch x {
		case "claude", config.AgentClaudeCode:
			out = append(out, config.AgentClaudeCode)
		case config.AgentCodex:
			out = append(out, config.AgentCodex)
		default:
			return nil, fmt.Errorf("--agents %q: use claude-code, codex, both separated by a comma, or none", v)
		}
	}
	return out, nil
}

// confirmPlan shows what up is about to do and asks; without a terminal,
// --yes is the answer.
func (w *wizard) confirmPlan(a *answers) (bool, error) {
	s := a.starter
	var rows []ui.Line
	row := func(k, v string) { rows = append(rows, ui.Row(k, v)) }
	st := w.st
	question := "Go ahead?"
	switch a.via {
	case viaHetzner:
		row("where", fmt.Sprintf("a new Hetzner server, %s in %s, %s", s.ServerType, s.Location, s.Image))
		if a.offer != nil {
			row("cost", st.Bold(fmt.Sprintf("about EUR %.2f a month", a.offer.Monthly))+fmt.Sprintf(" or EUR %.4f an hour with VAT, an estimate from Hetzner's prices, billed by Hetzner until %s", a.offer.Hourly, st.Cmd("keelbox destroy "+s.Box)))
			question = fmt.Sprintf("Create the server (about EUR %.2f a month until you destroy it)?", a.offer.Monthly)
		}
	case viaAWS:
		row("where", fmt.Sprintf("a new AWS Lightsail instance, %s in %s, %s", s.Bundle, s.Region, s.Blueprint))
		if a.awsOffer != nil {
			row("cost", st.Bold(fmt.Sprintf("about USD %.2f a month", a.awsOffer.Bundle.Price))+" before tax, an estimate from AWS's prices, billed by AWS by the hour until "+st.Cmd("keelbox destroy "+s.Box))
			question = fmt.Sprintf("Create the server (about USD %.2f a month until you destroy it)?", a.awsOffer.Bundle.Price)
		}
	case viaDO:
		row("where", fmt.Sprintf("a new DigitalOcean Droplet, %s in %s, %s", s.Size, s.Region, s.Image))
		if a.doOffer != nil {
			row("cost", st.Bold(fmt.Sprintf("about USD %.2f a month", a.doOffer.Size.PriceMonthly))+" before tax, an estimate from DigitalOcean's prices, billed by DigitalOcean until "+st.Cmd("keelbox destroy "+s.Box)+", also while powered off")
			question = fmt.Sprintf("Create the server (about USD %.2f a month until you destroy it)?", a.doOffer.Size.PriceMonthly)
		}
	case viaGCP:
		row("where", fmt.Sprintf("a new Google Cloud VM, %s in %s, project %s, Ubuntu 24.04", s.MachineType, s.Zone, s.Project))
		// gcloud prints no prices, and keelbox does not make one up.
		row("cost", "not shown: gcloud has no price list. Google's prices: "+gcp.PricingURL+". Billed by Google until "+st.Cmd("keelbox destroy "+s.Box)+
			"; a stopped VM still pays for its disk and its address")
		question = "Create the server (billed by Google until you destroy it; price not shown)?"
	case viaSSH:
		user := w.o.sshUser
		if user == "" {
			user = "admin"
		}
		row("where", fmt.Sprintf("%s over SSH, first as %s", s.SSHHost, user))
	case viaCloudInit:
		row("where", "a server you create with the cloud-config keelbox prints next")
	case viaLocal:
		where := "this machine, with sudo"
		if f := a.facts; f != nil {
			where = fmt.Sprintf("this machine (%s, Ubuntu %s), with sudo", f.Hostname, f.OSVersion)
		}
		row("where", where)
	}
	if len(s.Allow) > 0 {
		row("lan", "open to the LAN: "+config.AllowSummary(s.Allow))
		if !s.NoTailscale {
			c := &config.Config{}
			c.Host.Firewall.Allow, c.Previews.Ports = s.Allow, config.DefaultPreviewPorts
			if note := c.PreviewSkipNote(); note != "" {
				row("previews", "on the tailnet; "+strings.TrimPrefix(note, "previews "))
			}
		}
	}
	switch {
	case s.AgentLimit > 0:
		row("limits", fmt.Sprintf("the agent gets at most %d%% of CPU and memory; logins yield to system services when both are busy", s.AgentLimit))
	case s.AgentUnlimited:
		row("limits", "none: the agent can use all the CPU and memory")
	}
	agents := strings.Join(s.Agents, ", ")
	if agents == "" {
		agents = "none"
	}
	row("agents", agents)
	row("permission", permissionsRow(s.Permissions, s.Box))
	row("reboots", rebootsRow(s.AutoReboot, s.Box, s.Timezone))
	switch {
	case s.RemoteControl:
		row("claude app", "Claude Code Remote Control, after you sign in")
	case a.offerRC:
		row("claude app", "after the Claude Code sign-in, keelbox asks whether to reach the box from the Claude app too")
	}
	if !s.NoGH {
		row("github", "the GitHub CLI, for the agent's own GitHub sign-in")
	}
	if s.GitName != "" {
		row("git", fmt.Sprintf("the agent commits as %s <%s>", s.GitName, s.GitEmail))
	} else {
		row("git", `none: without a name and email git on the box refuses to commit. Set them with --git-name "Your Name" --git-email you@example.com, or later under users.agent.git in the config`)
	}
	switch {
	case s.NoTailscale:
		row("access", "SSH only (no Tailscale), open to the LAN and the internet")
	case s.PublicSSH == string(config.PublicSSHLAN):
		row("access", "Tailscale; SSH stays open to your LAN and closes to the internet once keelbox reaches the box over Tailscale")
	case s.PublicSSH == string(config.PublicSSHOpen):
		row("access", "Tailscale; SSH stays open to the LAN and the internet")
	default:
		row("access", "Tailscale; SSH from outside the tailnet, your LAN included, closes once keelbox reaches the box over it")
	}
	if len(s.KeyFiles) > 0 {
		row("ssh key", strings.Join(s.KeyFiles, ", "))
	}
	if a.via == viaCloudInit {
		row("config", tildePath(boxConfigPath(s.Box))+", once the server's address is known")
	} else {
		row("config", tildePath(boxConfigPath(s.Box)))
	}
	for _, n := range a.notes {
		row("note", n)
	}
	if a.via != viaLocal && a.via != viaSSH {
		rows = append(rows, ui.Blank(), ui.Text(st.Dim("keelbox hardens the server: no SSH passwords or root login, a firewall, an admin and an agent account, the image's default account locked.")))
	}
	fmt.Fprint(w.out, "\n"+st.Card(ui.Card{Title: "About to set up " + s.Box, Lines: rows, KeyWidth: 10}))
	if a.via == viaLocal || a.via == viaSSH {
		// Hardware the user already has: say what changes on it.
		var lines []ui.Line
		for _, l := range w.machineChanges(a) {
			if strings.HasPrefix(l, "  ") {
				lines = append(lines, ui.Line{Mark: " ", Text: st.Dim(strings.TrimSpace(l))})
				continue
			}
			lines = append(lines, ui.Line{Mark: st.Faint(st.Bullet()), Text: l})
		}
		fmt.Fprint(w.out, "\n"+st.Card(ui.Card{Title: "On " + map[bool]string{true: "this machine", false: s.SSHHost}[a.via == viaLocal] + ", keelbox changes", Lines: lines}))
		w.showImpact(a)
	}
	// One question: on a machine in use, the go-ahead is also the yes to
	// what the first apply would stop there (ADR 0038), and says so.
	open := openGated(a)
	switch {
	case len(open) > 0 && !w.o.adopt:
		if !w.p.Interactive() {
			return false, w.needFlag("what the first apply would cut off or replace there ("+strings.Join(open, ", ")+")",
				"--adopt to accept it", "keelbox up "+s.Box+" --provider "+a.via+" --adopt --yes")
		}
		fmt.Fprintln(w.out)
		ok, err := w.p.Confirm(fmt.Sprintf("Go ahead, accepting the %s marked *?", plural(len(open), "item", "items")), false)
		if ok {
			a.adopt = true
		}
		return ok, err
	case a.impact != nil && (len(a.impact.gated()) > 0 || w.o.adopt):
		// Every item that gates is a port up asked about, or --adopt was given.
		a.adopt = true
	}
	if w.o.yes {
		return true, nil
	}
	if !w.p.Interactive() {
		return false, w.needFlag("confirmation", "--yes", "keelbox up "+s.Box+" --provider "+a.via+" --yes")
	}
	return w.p.Confirm(question, true)
}

// uploadKey puts the SSH key in the Hetzner project, reusing a copy.
func (w *wizard) uploadKey(ctx context.Context, a *answers) error {
	name := "keelbox-" + suggestName(laptopName())
	k, created, err := hetzner.EnsureSSHKey(ctx, a.client, name, a.pubKey)
	if err != nil {
		return fmt.Errorf("upload the SSH key to Hetzner: %w", err)
	}
	if created {
		w.logf("uploaded your SSH key to the Hetzner project as %s", k.Name)
	} else {
		w.logf("your SSH key is already in the Hetzner project as %s", k.Name)
	}
	a.starter.HetznerKeys = []string{k.Name}
	return nil
}

func laptopName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "laptop"
	}
	h, _, _ = strings.Cut(h, ".")
	return h
}

// writeBoxConfig writes the box's commented config to the registry.
func writeBoxConfig(s config.Starter) (*config.Config, error) {
	out, err := s.Render()
	if err != nil {
		return nil, err
	}
	path := boxConfigPath(s.Box)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return nil, err
	}
	return config.Load(path)
}

// tildePath shows a path under the home directory with ~.
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" && strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// tildeText is text with the home directory in it written as ~, for a
// sentence that names a path under it.
func tildeText(text string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		return text
	}
	return strings.ReplaceAll(text, home+string(os.PathSeparator), "~"+string(os.PathSeparator))
}

// --- the cloud-config path ----------------------------------------------

func cloudConfigPath(box string) string { return filepath.Join(boxesDir(), box+".cloud-config") }

// printCloudConfig writes the snippet the user pastes when creating the
// server, keeps it (mode 600) until keelbox has replaced its host key, and
// stops unless the server's address is already known.
func (w *wizard) printCloudConfig(ctx context.Context, a *answers, st *upState) error {
	var hk *provider.HostKeyPair
	if a.hostKey {
		priv, pub, err := provider.GenerateHostKey(ctx, a.starter.Box)
		if err != nil {
			return err
		}
		hk = &provider.HostKeyPair{Private: priv, Public: pub}
		st.HostKey = strings.TrimSpace(pub)
	}
	snippet, err := provider.CloudConfig(a.starter.Box, "admin", []string{a.pubKey}, hk)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(boxesDir(), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(cloudConfigPath(a.starter.Box), []byte(snippet), 0o600); err != nil {
		return err
	}
	pending := a.starter
	st.Pending, st.Stage, st.Bootstrap = &pending, stageWaiting, "admin"
	return saveUpState(a.starter.Box, *st)
}

// showCloudConfig prints the saved snippet and how to use it.
func (w *wizard) showCloudConfig(box string) error {
	snippet, err := os.ReadFile(cloudConfigPath(box))
	if err != nil {
		return fmt.Errorf("the cloud-config for %s is gone (%v); start again: rm %s, then keelbox up %s", box, err, upStatePath(box), box)
	}
	st, path := w.st, tildePath(cloudConfigPath(box))
	num := func(n string) string { return st.BoldAccent(n) }
	fmt.Fprint(w.out, "\n"+st.Card(ui.Card{
		Title: "Create the server for " + box,
		Lines: []ui.Line{
			ui.Row(num("1."), "Create an Ubuntu 24.04 (or newer) server with your provider."),
			ui.Row(num("2."), `Paste the cloud-config below into its "Cloud config" or "User data" field (Hetzner: Cloud config; DigitalOcean, Vultr, AWS: User data).`),
			ui.Row(num("3."), "Add no SSH key there: the cloud-config installs yours."),
			ui.Blank(),
			ui.Row("saved in", path+", until keelbox has replaced its host key"),
			ui.Row("copy it", st.Cmd(ui.Unbroken("cat "+path))),
		},
	})+"\n")
	fmt.Fprint(w.out, string(snippet))
	fmt.Fprintln(w.out)
	return nil
}

// --- running the stages -------------------------------------------------

// resume runs the stages a box has not completed.
func (w *wizard) resume(ctx context.Context, cfg *config.Config, st upState) (int, error) {
	box := st.boxName(cfg)
	unlock, err := lockBox(box)
	if err != nil {
		return 1, err
	}
	defer unlock()
	w.banner()
	// From here on things change: Ctrl-C lets the current step finish.
	w.changing = true
	w.via = st.Via
	setInterruptNote(stepNote)
	if !w.fresh {
		w.logf("continuing %s (%s)", box, st.what(box))
		w.ignoredFlags(box)
	}
	save := func(stage string) error {
		st.Stage, w.progressed = stage, true
		return saveUpState(box, st)
	}
	if st.Stage == stageReady {
		w.ready(cfg, st)
		return 0, nil
	}

	// The cloud-config path: wait for the user's server.
	if st.Stage == stageWaiting {
		w.step("Create the server")
		host := w.o.host
		if host == "" && w.p.Interactive() {
			if err := w.showCloudConfig(box); err != nil {
				return 1, err
			}
			h, err := w.p.AskValid("The server's IP address, once it is running", "", validHost)
			switch {
			case errors.Is(err, prompt.ErrInterrupted):
				return exitInterrupted, &waitingStopped{box}
			case errors.Is(err, prompt.ErrNoAnswer):
				return 1, &waitingStopped{box}
			case err != nil:
				return 1, err
			}
			host = h
		}
		if host == "" {
			if err := w.showCloudConfig(box); err != nil {
				return 1, err
			}
			fmt.Fprintf(w.out, "Then continue: keelbox up %s --host <the server's IP address>\n", box)
			return exitNeedsYou, nil
		}
		if err := validHost(host); err != nil {
			return 2, fmt.Errorf("--host %q: %v", host, err)
		}
		s := *st.Pending
		s.SSHHost = host
		c, err := writeBoxConfig(s)
		if err != nil {
			return 1, err
		}
		cfg = c
		st.Pending, st.Address = nil, host
		w.logf("wrote %s", tildePath(boxConfigPath(box)))
		if err := w.run.firstContact(ctx, cfg, st.HostKey, w.out); err != nil {
			return 1, fmt.Errorf("%w\nCheck the server is running with the cloud-config above, then: keelbox up %s", err, box)
		}
		_ = os.Remove(cloudConfigPath(box))
		if err := save(stageServer); err != nil {
			return 1, err
		}
	}

	if !st.done(stageApplied) {
		w.step("Set up")
		if !st.Applying {
			st.Applying = true
			if err := saveUpState(box, st); err != nil {
				return 1, err
			}
		}
		if code := w.firstApply(ctx, cfg, &st); code != 0 {
			if code == exitDetached {
				return code, nil // left running on the box; the follower said how to pick it up
			}
			if isCloud(st.Via) && parkedLocally(cfg, box) != nil {
				return code, nil // the cloud holds the box parked; create said so, with the fix
			}
			return code, fmt.Errorf("the first apply did not finish; fix the cause above, then: keelbox up %s", box)
		}
		if err := save(stageApplied); err != nil {
			return 1, err
		}
	}

	if !st.done(stageSignedIn) {
		// A sign-in that could not be checked is left for later like one
		// that is missing; it never stops the set-up.
		left, notChecked := w.run.notSignedIn(ctx, cfg, w.o)
		if w.o.skipSignIn || !w.p.Interactive() {
			for _, n := range notChecked { // on a terminal the sign-in step says so itself
				fmt.Fprintf(w.out, "\nkeelbox up: %s\n", n)
			}
		}
		switch {
		case len(left) == 0:
		case w.o.skipSignIn:
			st.Left = left
			fmt.Fprintf(w.out, "\nkeelbox up: leaving the sign-ins for later (--skip-sign-in): %s. Sign in with: keelbox login %s\n", strings.Join(left, ", "), box)
		case !w.p.Interactive():
			fmt.Fprintf(w.out, "\nkeelbox up: %s is set up; signing in is yours to do. In a terminal, run:\n  keelbox up %s\n"+
				"It runs each vendor's own sign-in (%s); keelbox never sees the credentials. To finish without them: keelbox up %s --skip-sign-in\n", box, box, signInNames(cfg), box)
			return exitNeedsYou, nil
		default:
			w.step("Sign in")
			// Ctrl-C during a sign-in ends the whole step (signInAll), not
			// the next step of an apply.
			setInterruptNote("")
			o := w.o
			o.inUp = true
			res := w.run.login(ctx, cfg, o, w.p, w.out, w.errOut)
			setInterruptNote(stepNote)
			switch {
			case res.interrupted != "":
				return exitInterrupted, &signInStopped{res.interrupted}
			case res.err != nil:
				return 1, res.err
			}
			st.Left = res.left()
		}
		if st.OfferRemoteControl {
			if w.p.Interactive() && !contains(st.Left, config.AgentClaudeCode) {
				c, err := w.askRemoteControl(cfg, &st)
				if err != nil {
					return 1, err
				}
				cfg = c
			}
			st.OfferRemoteControl = false
		}
		if err := save(stageSignedIn); err != nil {
			return 1, err
		}
	}

	if !st.done(stageReady) {
		if cfg.Target.Provider != config.ProviderLocal || st.RemoteControlNext {
			fmt.Fprintln(w.out)
			switch {
			case cfg.Target.Provider == config.ProviderLocal:
				w.again("applying %s again to set up Remote Control", box)
			case cfg.Tailscale() && contains(st.Left, "tailscale"):
				w.again("applying %s again; public SSH stays open until it is on the tailnet", box)
			case cfg.Tailscale():
				w.again("applying %s again, over the tailnet once it answers there, to close public SSH", box)
			default:
				w.again("applying %s again after the sign-ins", box)
			}
			o := w.o
			o.sshUser, o.inUp, o.upLast, o.configFile = "", true, true, boxConfigPath(box)
			if code := w.run.apply(ctx, cfg, o, w.out, w.errOut); code != 0 {
				if code == exitDetached {
					return code, nil
				}
				return code, fmt.Errorf("the last apply did not finish; fix the cause above, then: keelbox up %s", box)
			}
		}
		if st.RemoteControlNext {
			setInterruptNote("")
			o := w.o
			o.inUp = true
			if !w.run.remoteControl(ctx, cfg, o, w.p, w.out, w.errOut) && !contains(st.Left, "remote-control") {
				st.Left = append(st.Left, "remote-control")
			}
			setInterruptNote(stepNote)
			st.RemoteControlNext = false
		}
		w.run.skills(ctx, cfg, w.o, w.p, w.out, w.errOut)
		if err := save(stageReady); err != nil {
			return 1, err
		}
	}
	w.ready(cfg, st)
	return 0, nil
}

// isKnown reports whether up already knows a box of this name.
func isKnown(box string) bool {
	if isRegistered(box) {
		return true
	}
	_, ok := loadUpState(&config.Config{Box: box})
	return ok
}

func (st upState) boxName(cfg *config.Config) string {
	if cfg != nil {
		return cfg.Box
	}
	return st.Pending.Box
}

// ignoredFlags warns that choices given for a box already configured do
// not change it.
func (w *wizard) ignoredFlags(box string) {
	var ignored []string
	for _, f := range []string{"provider", "size", "location", "profile", "doctl-context", "gcp-project", "hcloud-context", "agents", "remote-control", "tailscale", "ssh-key", "token-file", "host-key"} {
		if w.o.set[f] {
			ignored = append(ignored, "--"+f)
		}
	}
	if len(ignored) > 0 {
		fmt.Fprintf(w.out, "keelbox up: %s is already configured, so %s change nothing; edit %s and run keelbox apply %s\n",
			box, strings.Join(ignored, ", "), tildePath(boxConfigPath(box)), box)
	}
}

// firstApply brings a configured box to applied: creates the cloud
// server, or applies over SSH as the bootstrap account, or applies here.
func (w *wizard) firstApply(ctx context.Context, cfg *config.Config, st *upState) int {
	box := cfg.Box
	o := w.o
	o.inUp, o.configFile = true, boxConfigPath(box)
	o.adopt = o.adopt || st.Adopt
	o.hooks = &upHooks{
		onServer: func(ip string) {
			st.Address, st.Bootstrap, st.Stage = ip, bootstrapUser(st.Via), stageServer
			_ = saveUpState(box, *st)
		},
	}
	if isCloud(st.Via) {
		if st.Stage == stageConfigured {
			w.lognl("creating %s", box)
			return w.run.create(ctx, cfg, o, w.out, w.errOut)
		}
		// The server exists from an earlier run: apply as the image's
		// account (root on Hetzner, ubuntu on AWS) while it can still log
		// in (the first apply turns that off), else as admin. DigitalOcean
		// and Google Cloud have no such account.
		o.sshUser = ""
		if b := bootstrapUser(st.Via); b != "" && st.Address != "" && w.run.reachable(ctx, cfg, st.Address, b) {
			o.sshUser = b
		}
		w.applying("applying %s", box)
		return w.run.apply(ctx, cfg, o, w.out, w.errOut)
	}
	switch st.Via {
	case viaLocal:
		w.applying("applying %s to this machine with sudo", box)
		return w.run.apply(ctx, cfg, o, w.out, w.errOut)
	}
	// ssh and cloud-init: as the bootstrap account first while it works.
	o.sshUser = ""
	if b := st.Bootstrap; b != "" && b != cfg.Target.SSH.User {
		if st.Stage == stageConfigured || w.run.reachable(ctx, cfg, cfg.Target.SSH.Host, b) {
			o.sshUser = b
		}
	}
	if o.sshUser != "" && st.SudoPassword {
		if !w.p.Interactive() {
			fmt.Fprintf(w.errOut, "keelbox up: %s's sudo on %s asks for a password, which sudo reads on a terminal. Run keelbox up %s on one.\n", o.sshUser, cfg.Target.SSH.Host, box)
			return exitNeedsYou
		}
		o.askSudo = true
	}
	w.applying("applying %s over SSH", box)
	return w.run.apply(ctx, cfg, o, w.out, w.errOut)
}

func signInNames(cfg *config.Config) string {
	var n []string
	for _, s := range signIns(cfg) {
		n = append(n, s.name)
	}
	if len(n) == 0 {
		return "nothing to sign in to"
	}
	return strings.Join(n, ", ")
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}
