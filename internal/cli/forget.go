package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/ui"
)

// cmdForget is keelbox forget BOX: this machine stops managing the box. It
// removes the box from the registry with what keelbox keeps beside it (up's
// progress, the lock, the tailnet record, a saved cloud-config, the parked
// and destroyed notes) and the box's pinned host keys. The machine itself
// is not touched.
//
// For a cloud box that matters: its server is not deleted, and what forget
// removes is what keelbox destroy needs to find it. So unless this computer
// knows there is no server (forgetPlanFor), forget shows that in a card and
// asks with a menu whose default keeps the box.
func cmdForget(o options, pos []string, p *prompt.Prompter, stdout, stderr io.Writer) int {
	if len(pos) != 1 {
		failf(stderr, "forget takes the box name: keelbox forget acme (list boxes: keelbox ls)")
		return 2
	}
	box := pos[0]
	if !isKnown(box) {
		if near := nearest(box, append(registeredBoxes(), pendingBoxes()...)); near != "" {
			failf(stderr, "no box named %q; did you mean keelbox forget %s?\n", box, near)
		} else {
			failf(stderr, "no box named %q in %s; list boxes: keelbox ls\n", box, tildePath(boxesDir()))
		}
		return 2
	}
	var cfg *config.Config
	if isRegistered(box) {
		c, err := config.Load(boxConfigPath(box))
		if err != nil {
			failf(stderr, "%v\nForget it anyway: rm %s\n", err, boxConfigPath(box))
			return 2
		}
		cfg = c
	}
	plan := forgetPlanFor(box, cfg)
	st := o.style
	if !o.yes {
		if !p.Interactive() {
			if plan.stays != "" {
				failf(stderr, "forget asks first: %s, and forget removes what keelbox destroy needs to find it. Delete it first: keelbox destroy %s; %s",
					lowerFirst(plan.stays), box, orOwner(fmt.Sprintf("or forget %s as it is, without a terminal: keelbox forget %s --yes", box, box), "keelbox forget "+box))
			} else {
				failf(stderr, "forget asks first; %s\n", orOwner("without a terminal: keelbox forget "+box+" --yes", "keelbox forget "+box))
			}
			return 2
		}
		fmt.Fprint(stdout, st.Card(ui.Card{Title: "Forget " + box, Lines: plan.rows(st, box)}))
		ok, err := plan.ask(p, box)
		if errors.Is(err, prompt.ErrInterrupted) {
			return exitInterrupted // Main says that nothing changed
		}
		if err != nil || !ok {
			fmt.Fprintln(stdout, nothingChanged)
			return exitDeclined
		}
	}
	if cfg != nil {
		st, _ := loadUpState(cfg)
		forgetBox(cfg, st.Address)
	}
	for _, f := range []string{boxConfigPath(box), appliedNotePath(box), upStatePath(box), cloudConfigPath(box), parkMarkPath(box), destroyedMarkPath(box), requestsNotePath(box), filepath.Join(boxesDir(), box+".lock")} {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			fail(stderr, err)
			return 1
		}
	}
	if lastBox() == box {
		_ = os.Remove(lastBoxPath()) // a later box of the same name is not the one last used
	}
	after := plan.after
	if o.yes && plan.afterYes != "" {
		after = plan.afterYes
	}
	if st.Live {
		sayAsIs(stdout, st, st.Tick(), "Forgot "+box+". "+after)
	} else {
		fmt.Fprintln(stdout, st.Plain("Forgot "+box+". "+after, 0))
	}
	return 0
}

// nothingChanged is what every command says when the user declines its
// question, and exitDeclined the exit code that goes with it.
const (
	nothingChanged = "Nothing changed."
	exitDeclined   = 1
)

// forgetPlan is what forget says about one box before it asks and after
// it has forgotten it.
type forgetPlan struct {
	// removed is what goes from this computer.
	removed string
	// server is the card's row about the machine or the server, and
	// serverKey its label.
	serverKey, server string
	// stays is set for a cloud box whose server, or parked snapshot, may
	// exist: the sentence that says it stays and is billed. forget then
	// asks with a menu that keeps the box by default.
	stays string
	// console is where the user deletes what stays once keelbox has
	// forgotten the box.
	console string
	// after ends "Forgot BOX."; afterYes replaces it under --yes where a
	// script reads the sentence keelbox has always printed there.
	after, afterYes string
}

// cloudLeft says, per cloud, what forget leaves there and where the user
// deletes it: forget removes the config keelbox destroy would need.
var cloudLeft = map[string][2]string{
	config.ProviderHetzner:      {"The Hetzner server keeps running and is billed until it is deleted", "the Hetzner Console"},
	config.ProviderAWS:          {"The Lightsail instance and its static IP stay and are billed until they are deleted", "the Lightsail console"},
	config.ProviderDigitalOcean: {"The Droplet keeps running and is billed until it is deleted, and its firewall stays", "the DigitalOcean control panel"},
	config.ProviderGCP:          {"The Google Cloud VM, its disk and its reserved address stay and are billed until they are deleted", "the Google Cloud console"},
}

// forgetPlanFor reads what this computer knows about box: cfg is nil for a
// box that waits for its server (keelbox up printed a cloud-config and has
// no address yet). No box and no cloud is asked.
//
// A cloud box's server is taken to be gone only where this computer has
// that on record: keelbox destroy noted that it deleted it, or keelbox up
// stopped before it started creating one. Anything else, a server may
// exist, and forget says so.
func forgetPlanFor(box string, cfg *config.Config) forgetPlan {
	if cfg == nil {
		return forgetPlan{
			removed:   "the answers keelbox up kept (" + tildePath(upStatePath(box)) + ") and the cloud-config (" + tildePath(cloudConfigPath(box)) + ")",
			serverKey: "the server",
			server:    "keelbox created none. A server you created from the cloud-config is not touched",
			after:     "A server you created from its cloud-config is not touched.",
		}
	}
	plan := forgetPlan{
		removed:   tildePath(boxConfigPath(box)) + ", what keelbox noted about " + box + " beside it, and the host keys pinned for it",
		serverKey: "the machine",
		server:    "not touched: it stays set up as it is",
		after:     "The machine is not touched.",
	}
	left, cloud := cloudLeft[cfg.Target.Provider]
	if !cloud {
		return plan
	}
	plan.serverKey = "the server"
	if m, ok := loadDestroyedMark(box); ok {
		plan.server = "none: keelbox destroy deleted it on " + day(m.At)
		plan.after = "keelbox destroy deleted its server on " + day(m.At) + "."
		return plan
	}
	if m, ok := loadParkMark(box); ok {
		plan.stays = "The snapshot of its disk (" + box + " has been parked since " + day(m.Since) + ") stays in the cloud and is billed until it is deleted"
		plan.console = left[1]
		plan.after = "The snapshot of its disk stays in the cloud and is billed until it is deleted in " + left[1] + ": keelbox no longer manages it."
		plan.afterYes = "It has been parked since " + day(m.Since) + ": the snapshot of its disk stays in the cloud and is billed until it is deleted in the cloud's console: keelbox no longer manages it."
		return plan
	}
	if st, ok := loadUpState(cfg); ok && isCloud(st.Via) && st.Stage == stageConfigured && !st.Applying && st.Address == "" {
		plan.server = "none: keelbox up stopped before it created one"
		plan.after = "No server was created for it."
		return plan
	}
	plan.stays, plan.console = left[0], left[1]
	plan.after = left[0] + " in " + left[1] + ": keelbox no longer manages it."
	return plan
}

// rows are the card forget shows before it asks.
func (f forgetPlan) rows(st ui.Style, box string) []ui.Line {
	rows := []ui.Line{ui.Row("removed here", f.removed)}
	if f.stays == "" {
		return append(rows, ui.Row(f.serverKey, f.server))
	}
	return append(rows,
		ui.Row(st.Warn("still billed"), f.stays),
		ui.Row("to delete it", st.Cmd(ui.Unbroken("keelbox destroy "+box))+" first: forget removes what destroy needs to find it. After forget, only "+f.console+" deletes it"))
}

// ask is forget's question: yes or no, No by default, or for a cloud box
// whose server may exist a menu that keeps the box by default.
func (f forgetPlan) ask(p *prompt.Prompter, box string) (bool, error) {
	if f.stays == "" {
		return p.Confirm("Forget "+box+"?", false)
	}
	i, err := p.Choose("Forget "+box+" and leave what is billed in the cloud?", []prompt.Choice{
		{Key: "keep", Label: "Keep " + box, Detail: "nothing changes"},
		{Key: "forget", Label: "Forget " + box, Detail: "what is billed stays until you delete it in " + f.console},
	}, 0)
	return i == 1, err
}

// lowerFirst is s with its first letter in lower case, for a sentence put
// after other words.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// doctorSignIns adds a row per sign-in to keelbox doctor: an agent CLI can
// be installed and still signed out.
func doctorSignIns(ctx context.Context, cfg *config.Config, o options, failed bool, stdout, stderr io.Writer) {
	if len(signIns(cfg)) == 0 {
		return
	}
	states := signInStates(ctx, cfg, o, "", io.Discard)
	if failed && allDown(states) != nil {
		return // the doctor run itself said the box does not answer
	}
	fmt.Fprintln(stdout, "\nsign-in")
	// None could be checked, for one reason (the box is off): one row.
	same := len(states) > 0
	for _, s := range states {
		same = same && s.unchecked != nil && s.unchecked.Error() == states[0].unchecked.Error()
	}
	if same {
		fmt.Fprintf(stdout, "  %-4s  %-24s not checked: %v\n", "warn", "sign-ins", states[0].unchecked)
		return
	}
	ref := boxRef(cfg, o.configFile)
	for _, s := range states {
		switch {
		case s.unchecked != nil:
			// Not "not signed in": nothing was read.
			fmt.Fprintf(stdout, "  %-4s  %-24s not checked: %v\n", "warn", s.name, s.unchecked)
		case s.done:
			fmt.Fprintf(stdout, "  %-4s  %-24s signed in\n", "ok", s.name)
		default:
			fmt.Fprintf(stdout, "  %-4s  %-24s not signed in: keelbox login %s %s\n", "warn", s.name, ref, s.name)
		}
	}
}

// upNotFinished says, after status and doctor, that keelbox up has not
// finished the box: it does every step left, the ones the hints above
// name included, so one command is the next step.
func upNotFinished(cfg *config.Config, style ui.Style, stdout io.Writer) {
	st, ok := loadUpState(cfg)
	if !ok || st.Stage == stageReady {
		return
	}
	text := fmt.Sprintf("keelbox up has not finished %s (%s). It does what is left, the steps above included: keelbox up %s", cfg.Box, st.what(cfg.Box), cfg.Box)
	fmt.Fprintln(stdout)
	if style.Live {
		sayAsIs(stdout, style, style.Bang(), paintCommands(style, text))
		return
	}
	fmt.Fprintln(stdout, style.Plain(text, 0))
}
