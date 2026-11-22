package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/ui"
)

// Every question keelbox puts to a person is asked through a Prompter
// built here, so that Ctrl-C at any of them does the same: the question
// ends, the terminal is as it was before it (echo and line editing on, the
// cursor shown), and the command exits 130 after one line that says
// nothing changed. TestQuestionsAreBuiltInOnePlace fails for a question
// built anywhere else.

// stoppedAtQuestion is set when Ctrl-C ended a question; stopSaid when the
// command says what that means itself (keelbox up, keelbox login).
var stoppedAtQuestion, stopSaid atomic.Bool

// terminalPrompt asks on the terminal, in st, the questions of a command
// that runs under ctx: the first Ctrl-C cancels ctx, which ends a question
// that is waiting.
func terminalPrompt(ctx context.Context, out io.Writer, st ui.Style) *prompt.Prompter {
	return prompt.Terminal(os.Stdin, out).WithStyle(st).OnInterrupt(ctx.Done()).OnStop(func() { stoppedAtQuestion.Store(true) })
}

// followPrompt asks what to do with a run that carries on on the box. Its
// question is asked at a Ctrl-C, and the follower ends it at the next one:
// stopping there changes what happens to the run, so it is not a stop
// that changed nothing.
func followPrompt(out io.Writer) *prompt.Prompter { return prompt.Terminal(os.Stdin, out) }

// commandCtx is the context of the command that runs: the one its first
// Ctrl-C cancels. A question asked deep inside a command, such as the one
// about a server's host keys at the first connection, ends with it.
var commandCtx atomic.Pointer[context.Context]

// questionCtx is the running command's context.
func questionCtx() context.Context {
	if c := commandCtx.Load(); c != nil {
		return *c
	}
	return context.Background()
}

// readerPrompt asks on in, a reader that more than one question reads
// from, so that none buffers another's answer away. When in reads the
// terminal the questions are asked there, as terminalPrompt asks them.
func readerPrompt(ctx context.Context, in *bufio.Reader, src io.Reader, out io.Writer, st ui.Style) *prompt.Prompter {
	if f, ok := src.(*os.File); ok && f == os.Stdin && prompt.IsTerminal(f) {
		return terminalPrompt(ctx, out, st)
	}
	return prompt.New(in, out, true).WithStyle(st).OnInterrupt(ctx.Done()).OnStop(func() { stoppedAtQuestion.Store(true) })
}

// confirm asks a yes or no question on in, No by default, for code that
// reads its answers from a reader it shares; anything but a yes is no.
func confirm(in io.Reader, out io.Writer, question string) bool {
	ok, _ := prompt.New(in, out, true).Confirm(question, false)
	return ok
}

// confirmHostKeys is the question at the first connection to a server
// whose host keys nobody gave keelbox: the keys in a card, where to
// compare them, and a yes or no, No by default.
func confirmHostKeys(p *prompt.Prompter, out io.Writer, host string, offered []provider.HostKey) bool {
	st := p.Style()
	var rows []ui.Line
	for _, k := range offered {
		rows = append(rows, ui.Row(k.Type, k.Fingerprint()))
	}
	rows = append(rows, ui.Blank(), ui.Text(st.Dim("Compare with the provider's console, or with what this prints on the server:")),
		ui.Text(st.Cmd(ui.Unbroken("ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub"))))
	fmt.Fprint(out, st.Card(ui.Card{Title: "First connection to " + host, Lines: rows}))
	ok, _ := p.Confirm("Trust and pin these host keys?", false)
	return ok
}

// noPrompt asks nothing: every question fails for want of a terminal. For
// code that must not stop to ask, such as a read of every box at once.
func noPrompt() *prompt.Prompter { return prompt.New(strings.NewReader(""), io.Discard, false) }

// stoppedExit is the end of a command a Ctrl-C stopped at a question: one
// line that nothing changed, unless the command said so, and exit 130.
// Any other run keeps its exit code.
func stoppedExit(cmd string, code int, stderr io.Writer) int {
	if !stoppedAtQuestion.Load() || stopSaid.Load() {
		return code
	}
	// park and resize say "Nothing changed." themselves.
	if cmd != "park" && cmd != "resize" {
		if st := errStyle(stderr); st.Live {
			sayAsIs(stderr, st, st.Dim("-"), "Stopped. Nothing changed.")
		} else {
			sayAsIs(stderr, st, "", "stopped; nothing changed")
		}
	}
	return exitInterrupted
}
