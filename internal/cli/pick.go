package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/ui"
)

// A command that names no box, with several in the registry: on a terminal
// keelbox asks which, with a line on each from what this computer knows
// (no box and no cloud is asked); anywhere else it lists them and says how
// to name one. The box a command last used is offered first, and one whose
// server keelbox destroy deleted never is.

// lastBoxPath holds the name of the box a command last used.
func lastBoxPath() string { return filepath.Join(configDir(), "last-box") }

func lastBox() string {
	data, err := os.ReadFile(lastBoxPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// noteBoxUsed records box, one in the registry, as the box a command last
// used. It is only the default of "Which box?": failing to write it is not
// an error, and a keelbox run with sudo leaves it alone, since the file
// would be root's in the user's directory.
func noteBoxUsed(box string) {
	if _, _, sudo := sudoIDs(); sudo || lastBox() == box || os.MkdirAll(configDir(), 0o700) != nil {
		return
	}
	_ = os.WriteFile(lastBoxPath(), []byte(box+"\n"), 0o600)
}

// boxPrompt is what "Which box?" is asked on; tests replace it. It asks
// only where a person both reads the output and types the answer.
var boxPrompt = func(ctx context.Context, stdout io.Writer, st ui.Style) *prompt.Prompter {
	if !stdoutIsTerminal(stdout) {
		return noPrompt()
	}
	return terminalPrompt(ctx, stdout, st)
}

// pickDetail is a box's line in the question: where it runs, then what
// stands in the way of using it, in a word or two.
func pickDetail(r boxRow) string {
	var parts []string
	switch {
	case r.provider == "?":
	case r.address == "this machine":
		parts = append(parts, r.address)
	case r.address == "-":
		parts = append(parts, r.provider)
	default:
		parts = append(parts, r.provider, r.address)
	}
	if r.state != "" {
		parts = append(parts, r.state)
	}
	return strings.Join(parts, ", ")
}

// likelyBox is the index of the box a command most likely means: the one a
// command last used, else the first that is set up, else the first that
// has a server. It is -1 when every box's server is destroyed.
func likelyBox(rows []boxRow) int {
	last := lastBox()
	for _, want := range []func(boxRow) bool{
		func(r boxRow) bool { return r.name == last },
		func(r boxRow) bool { return r.state == "" },
		func(boxRow) bool { return true },
	} {
		for i, r := range rows {
			if r.state != stateDestroyed && want(r) {
				return i
			}
		}
	}
	return -1
}

// pickBox is the box for cmd out of several: the one picked at p's
// question, or without a terminal an error that lists them.
func pickBox(cmd string, boxes []string, p *prompt.Prompter) (string, error) {
	rows := make([]boxRow, len(boxes))
	names := make([]string, len(boxes))
	choices := make([]prompt.Choice, len(boxes))
	for i, b := range boxes {
		rows[i] = listRow(b)
		names[i] = b
		if rows[i].state == stateDestroyed {
			names[i] += " (" + stateDestroyed + ")"
		}
		choices[i] = prompt.Choice{Key: b, Label: b, Detail: pickDetail(rows[i])}
	}
	def := likelyBox(rows)
	nameOne := fmt.Errorf("%d boxes (%s), and the server of each is destroyed; create one again: keelbox up %s", len(boxes), strings.Join(names, ", "), boxes[0])
	if def >= 0 {
		nameOne = fmt.Errorf("%d boxes (%s); name one: keelbox %s %s", len(boxes), strings.Join(names, ", "), cmd, boxes[def])
	}
	if p == nil || !p.Interactive() {
		return "", nameOne
	}
	setInterruptNote("") // Ctrl-C at the question ends it; nothing has started
	i, err := p.Choose("Which box?", choices, max(def, 0))
	setInterruptNote(stepNote)
	switch {
	case errors.Is(err, prompt.ErrInterrupted):
		return "", err
	case err != nil:
		return "", nameOne
	}
	return boxes[i], nil
}
