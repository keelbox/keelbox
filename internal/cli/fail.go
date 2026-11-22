package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/term"
	"github.com/keelbox/keelbox/internal/ui"
)

// Every command's error is shown the same way (ui.Trouble): on a terminal
// one block with what went wrong, why, and the command that continues on a
// line of its own; anywhere else the line "keelbox: text", as a script or
// a log reads it.

// mainStyle is the style Main found for stdout: the background it asked
// the terminal for serves stderr too.
var mainStyle atomic.Pointer[ui.Style]

// errStyle is the style errors written to stderr are drawn in; tests
// replace it. Plain unless stderr is a terminal.
var errStyle = func(stderr io.Writer) ui.Style {
	f, ok := stderr.(*os.File)
	if !ok || !term.IsTerminal(f) {
		return ui.Style{}
	}
	st := ui.For(f)
	if m := mainStyle.Load(); m != nil && m.Colour {
		st.Light = m.Light
	}
	return st
}

// fail prints the error a command ends with. An interrupted question
// prints nothing here: Main says that once (stoppedAtQuestion).
func fail(stderr io.Writer, err error) {
	if err == nil || errors.Is(err, prompt.ErrInterrupted) {
		return
	}
	if stoppedAtQuestion.Load() && errors.Is(err, provider.ErrHostKeyUnconfirmed) {
		return // Ctrl-C at the question about the host keys
	}
	trouble(stderr, ui.Trouble{Text: err.Error(), Prefix: "keelbox: "})
}

// failf is fail for a message written in place.
func failf(stderr io.Writer, format string, a ...any) {
	trouble(stderr, ui.Trouble{Text: fmt.Sprintf(format, a...), Prefix: "keelbox: "})
}

// notef says something a command goes on after: a warning, or what it
// tries next. "keelbox: text" in plain text; marked and wrapped on a
// terminal.
func notef(stderr io.Writer, format string, a ...any) {
	st := errStyle(stderr)
	sayAsIs(stderr, st, st.Bang(), fmt.Sprintf(format, a...))
}

// trouble writes t to stderr in stderr's style, after a blank line where
// it is a block. It fills in the box names, which a block leaves as they
// are written.
func trouble(stderr io.Writer, t ui.Trouble) {
	st := errStyle(stderr)
	t.Text = strings.TrimRight(t.Text, "\n")
	t.Names = append(registeredBoxes(), pendingBoxes()...)
	if st.Live {
		fmt.Fprintln(stderr)
	}
	fmt.Fprint(stderr, st.Trouble(t))
}
