package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/plain"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/ui"
)

// keelbox destroy's check for work that exists only on the box
// (docs/adr/0064-work-on-the-box.md): before a server is deleted, keelbox
// reads the agent's projects there and stops when one is not a git
// repository, has uncommitted changes, commits not pushed or no remote, or
// when it could not look.

// workCheckTimeout bounds the look at the box: a server that is off must
// not hold the question up for long.
const workCheckTimeout = 2 * time.Minute

// boxProjects reads the agent's projects on the box now: keelbox status
// --json over SSH, as doctor and status run. Tests replace it.
var boxProjects = func(ctx context.Context, cfg *config.Config, ip string, o options) (*snapshot.Projects, error) {
	ctx, cancel := context.WithTimeout(ctx, workCheckTimeout)
	defer cancel()
	public := addr{ip, cfg.Target.SSH.Port}
	if name := cfg.Target.SSH.Host; name != "" {
		public = addr{name, cfg.Target.SSH.Port}
	}
	var out, errOut bytes.Buffer
	q := options{json: true, sshUser: o.sshUser, binary: o.binary, configFile: o.configFile}
	if code := runSSH(ctx, "status", cfg, reach(ctx, cfg, public, io.Discard), q, &out, &errOut); code != 0 {
		msg := lastLine(errOut.String())
		if msg == "" {
			msg = fmt.Sprintf("keelbox status exited %d", code)
		}
		return nil, errors.New(strings.TrimPrefix(plain.Line(msg), "keelbox: "))
	}
	// The snapshot is the last line that is a JSON object; ssh may print
	// before it.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if !strings.HasPrefix(lines[i], "{") {
			continue
		}
		s, err := snapshot.Parse([]byte(lines[i]))
		if err != nil {
			return nil, err
		}
		if s.Projects == nil {
			return nil, errors.New("the box could not read the agent's projects")
		}
		return s.Projects, nil
	}
	return nil, errors.New("the box gave no status")
}

// stdinIsTerminal reports whether a person can answer; tests replace it.
var stdinIsTerminal = func() bool { return prompt.IsTerminal(os.Stdin) }

// destroyWorkCheck returns 0 when the server may be deleted: no project
// holds work that is only there, or the owner said yes to losing it. --yes
// does not answer this question; --discard-work skips the check.
func destroyWorkCheck(ctx context.Context, cfg *config.Config, ip string, o options, ask *prompt.Prompter, stdout, stderr io.Writer) int {
	st, box := o.style, cfg.Box
	say(stdout, st, st.Faint(st.Bullet()), fmt.Sprintf("checking %s for work that exists only there", box))
	p, err := boxProjects(ctx, cfg, ip, o)
	override := fmt.Sprintf("keelbox destroy %s --yes --discard-work", box)
	owner := "keelbox destroy " + box
	if err != nil {
		why := fmt.Sprintf("could not check %s for work that exists only there (%v)", box, err)
		if !stdinIsTerminal() {
			failf(stderr, "not deleted: %s. Start the server and run it again, %s\n", why, orOwner("or delete it without the check: "+override, owner))
			return 1
		}
		sayAsIs(stdout, st, st.Bang(), why+". Deleting the server deletes whatever is on it.")
		return confirmed(ask, "Delete it without knowing?", stdout)
	}
	here := p.OnlyHere()
	if len(here) == 0 && p.Unlisted() == 0 {
		say(stdout, st, st.Tick(), fmt.Sprintf("no work that is only on %s: %s", box, p.Summary()))
		return 0
	}
	fmt.Fprint(stdout, workList(st, box, p))
	if !stdinIsTerminal() {
		failf(stderr, "not deleted: %s holds work that exists nowhere else. Push it or copy it off first, %s\n", box, orOwner("or delete it with the server: "+override, owner))
		return 1
	}
	return confirmed(ask, "Delete this work with the server?", stdout)
}

// confirmed asks a question whose yes deletes something, No by default,
// and returns 0 for a yes: a no says that nothing changed.
func confirmed(ask *prompt.Prompter, question string, stdout io.Writer) int {
	ok, err := ask.Confirm(question, false)
	switch {
	case errors.Is(err, prompt.ErrInterrupted):
		return exitInterrupted // Main says that nothing changed
	case err != nil || !ok:
		return declined(stdout, "", "")
	}
	return 0
}

// workList is what destroy shows before it asks: each project with work
// that is only on the box, at most snapshot.Shown, and how to keep it.
// Names are the agent's: plain text, one line each.
func workList(st ui.Style, box string, p *snapshot.Projects) string {
	here := p.OnlyHere()
	var b strings.Builder
	fmt.Fprintf(&b, "%s on %s holds work that exists nowhere else:\n", p.Dir, box)
	w := 0
	for i, pr := range here {
		if i < snapshot.Shown {
			w = max(w, ui.Width(plain.Line(pr.Name)))
		}
	}
	for i, pr := range here {
		if i == snapshot.Shown {
			break
		}
		state := []string{pr.State()}
		if st.Live {
			state = ui.Wrap(pr.State(), max(st.Cols()-w-4, 20))
		}
		for j, l := range state {
			name := ""
			if j == 0 {
				name = plain.Line(pr.Name)
			}
			fmt.Fprintf(&b, "  %s  %s\n", ui.Pad(name, w), st.Warn(l))
		}
	}
	more := len(here) - min(len(here), snapshot.Shown)
	switch {
	case more > 0 && p.Unlisted() > 0:
		fmt.Fprintf(&b, "  and %d more, and %d that were not read\n", more, p.Unlisted())
	case more > 0:
		fmt.Fprintf(&b, "  and %d more\n", more)
	case p.Unlisted() > 0:
		fmt.Fprintf(&b, "  and %d more that were not read\n", p.Unlisted())
	}
	keep := fmt.Sprintf("Deleting the server deletes it. To keep it, commit and push as the agent (keelbox ssh %s), or copy it off, before you delete.", box)
	if st.Live {
		keep = ui.Hang(keep, st.Cols(), 0)
	}
	b.WriteString(keep + "\n")
	return b.String()
}
