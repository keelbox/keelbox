// Package prompt asks the user questions on a terminal: a line with a
// default, yes or no, a choice from a menu, or a secret typed without
// echo. It is deliberately small and stdlib-only. On a capable terminal
// (ui.Style.Live) a menu takes the arrow keys and numbers, and each answered
// question folds into one line; anywhere else (TERM=dumb, ACCESSIBLE, a
// test's scripted input) it asks one plain line at a time. Without a
// terminal every question fails with ErrNoTerminal, so a command run by a
// script or an agent stops and says which flag to pass instead of hanging.
package prompt

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/keelbox/keelbox/internal/term"
	"github.com/keelbox/keelbox/internal/ui"
)

// ErrNoTerminal means a question was asked without a terminal to answer it.
var ErrNoTerminal = errors.New("no terminal to ask on")

// ErrNoAnswer means the input ended before an answer (Ctrl-D).
var ErrNoAnswer = errors.New("no answer")

// ErrInterrupted means the user pressed Ctrl-C at a question.
var ErrInterrupted = errors.New("interrupted")

// attempts is how often an answer is asked for where no person types it:
// scripted input, which would otherwise be read to its end. On a terminal
// a question is asked until it is answered, and Ctrl-C is the way out.
const attempts = 3

// OnTerminal reports whether a person at a real terminal answers: a
// question then has no limit of tries.
func (p *Prompter) OnTerminal() bool { return p.keys != nil }

// again reports whether a question that has been asked i times is asked
// once more.
func (p *Prompter) again(i int) bool { return p.OnTerminal() || i < attempts }

// Prompter asks questions on one input and output.
type Prompter struct {
	in  *bufio.Reader
	out io.Writer
	tty bool
	st  ui.Style
	// hide turns echo off for Secret and returns the restore function;
	// nil when the input is not a real terminal (tests, scripted input).
	hide func() (func(), error)
	// keys puts the terminal in key-at-a-time mode for the drawn
	// questions; nil when the input is not a real terminal.
	keys func() (func(), error)
	// width is the output terminal's width now; nil uses the style's.
	width func() int
	// intr, when set, ends a question waiting for an answer: Ctrl-C.
	intr <-chan struct{}
	// stopped, when set, is told that a question ended that way.
	stopped func()
}

// waiting counts the questions that wait for an answer now.
var waiting atomic.Int32

// Waiting reports whether a question waits for an answer now: a Ctrl-C
// then ends the question, and is not about a step that is running.
func Waiting() bool { return waiting.Load() > 0 }

// OnStop has f called when a question ends with ErrInterrupted, so that
// the command can say once that it stopped.
func (p *Prompter) OnStop(f func()) *Prompter {
	p.stopped = f
	return p
}

// interrupted is a question ended by Ctrl-C.
func (p *Prompter) interrupted() error {
	if p.stopped != nil {
		p.stopped()
	}
	return ErrInterrupted
}

// OnInterrupt makes a question waiting for an answer return ErrInterrupted
// when ch closes (the command's context on Ctrl-C), instead of waiting for
// Enter. Echo turned off for a secret is restored first.
func (p *Prompter) OnInterrupt(ch <-chan struct{}) *Prompter {
	p.intr = ch
	return p
}

// WithStyle draws the questions in st, the output's style. On a real
// terminal with a live style, menus take the arrow keys.
func (p *Prompter) WithStyle(st ui.Style) *Prompter {
	p.st = st
	return p
}

// Style is the style the questions are drawn in.
func (p *Prompter) Style() ui.Style { return p.st }

// New asks on in and out. interactive says whether a person answers; when
// false every question returns ErrNoTerminal. Echo is never turned off:
// use Terminal for a real terminal.
func New(in io.Reader, out io.Writer, interactive bool) *Prompter {
	return &Prompter{in: bufio.NewReader(in), out: out, tty: interactive}
}

// Terminal asks on f when it is a terminal, with echo off for secrets.
func Terminal(f *os.File, out io.Writer) *Prompter {
	p := New(f, out, IsTerminal(f))
	if p.tty {
		p.hide = func() (func(), error) { return term.EchoOff(f) }
		p.keys = func() (func(), error) { return term.Keys(f) }
		if o, ok := out.(*os.File); ok {
			p.width = func() int { return term.Width(o) }
		}
	}
	return p
}

// IsTerminal reports whether f is a terminal.
func IsTerminal(f *os.File) bool { return term.IsTerminal(f) }

// Interactive reports whether questions can be asked.
func (p *Prompter) Interactive() bool { return p.tty }

// Out is where questions are written.
func (p *Prompter) Out() io.Writer { return p.out }

// drawn reports whether questions are drawn and answered key by key.
func (p *Prompter) drawn() bool { return p.keys != nil && p.st.Live && !p.st.Accessible }

// plain is a question as one plain line, wrapped to the terminal where
// its width is known (TERM=dumb, a screen reader).
func (p *Prompter) plain(question string) string { return p.st.Plain(question, 0) }

func (p *Prompter) line() (string, error) {
	if p.intr == nil {
		return p.readLine()
	}
	type answer struct {
		s   string
		err error
	}
	c := make(chan answer, 1)
	go func() {
		s, err := p.readLine()
		c <- answer{s, err}
	}()
	waiting.Add(1)
	defer waiting.Add(-1)
	select {
	case a := <-c:
		return a.s, a.err
	case <-p.intr:
		// The reader is left waiting; the command is about to end.
		fmt.Fprintln(p.out)
		return "", p.interrupted()
	}
}

func (p *Prompter) readLine() (string, error) {
	s, err := p.in.ReadString('\n')
	if err != nil && (s == "" || !errors.Is(err, io.EOF)) {
		if errors.Is(err, io.EOF) {
			return "", ErrNoAnswer
		}
		return "", err
	}
	// A terminal's late answer to a query is not part of what was typed.
	return strings.TrimSpace(ui.Strip(s)), nil
}

// Ask asks for a line. An empty answer is def.
func (p *Prompter) Ask(question, def string) (string, error) {
	return p.AskValid(question, def, nil)
}

// AskValid asks for a line until valid accepts it (nil accepts anything
// but an empty answer without a default). An answer it refuses is asked
// for again with the reason, and the refused text is not kept.
func (p *Prompter) AskValid(question, def string, valid func(string) error) (string, error) {
	if !p.tty {
		return "", ErrNoTerminal
	}
	check := func(a string) error {
		switch {
		case a == "":
			return errors.New("an answer is needed")
		case valid != nil:
			return valid(a)
		}
		return nil
	}
	if p.drawn() {
		return p.drawnLine(question, def, "", false, func(a string) (string, error) {
			if a == "" {
				a = def
			}
			return a, check(a)
		})
	}
	var last error
	for i := 0; p.again(i); i++ {
		if def != "" {
			fmt.Fprint(p.out, p.plain(fmt.Sprintf("%s [%s]: ", question, def))+" ")
		} else {
			fmt.Fprint(p.out, p.plain(question+":")+" ")
		}
		a, err := p.line()
		if err != nil {
			return "", err
		}
		if a == "" {
			a = def
		}
		if last = check(a); last == nil {
			return a, nil
		}
		fmt.Fprintf(p.out, "  %v\n", last)
	}
	return "", last
}

// AskOptional asks for a line that may be left empty: Enter skips it and
// returns "". Anything typed must pass valid. The answered question then
// reads "skipped".
func (p *Prompter) AskOptional(question string, valid func(string) error) (string, error) {
	return p.AskOr(question, "skip", "skipped", valid)
}

// AskOr asks for a line that may be left empty, where Enter stands for
// something the question does not ask twice: enter says what in a word or
// two ("skip", "your default") and left what the answered question then
// reads ("skipped", "your default"). An empty answer is returned as "".
func (p *Prompter) AskOr(question, enter, left string, valid func(string) error) (string, error) {
	if !p.tty {
		return "", ErrNoTerminal
	}
	if p.drawn() {
		return p.ask(lineQuestion{question: question, hint: "Enter: " + enter, empty: left, accept: func(a string) (string, error) {
			if a == "" {
				return "", nil
			}
			return a, valid(a)
		}})
	}
	var last error
	for i := 0; p.again(i); i++ {
		fmt.Fprint(p.out, p.plain(question+" [Enter: "+enter+"]:")+" ")
		a, err := p.line()
		if err != nil {
			return "", err
		}
		if a == "" {
			return "", nil
		}
		if last = valid(a); last == nil {
			return a, nil
		}
		fmt.Fprintf(p.out, "  %v\n", last)
	}
	return "", last
}

// AskTyped asks for a line to be typed exactly, such as a box's name to
// confirm deleting it: nothing is offered, nothing is checked, and an
// empty answer is returned as it is. The caller compares.
func (p *Prompter) AskTyped(question string) (string, error) {
	if !p.tty {
		return "", ErrNoTerminal
	}
	if p.drawn() {
		return p.drawnLine(question, "", "", false, func(a string) (string, error) { return a, nil })
	}
	fmt.Fprint(p.out, p.plain(question+":")+" ")
	return p.line()
}

// Wait prints a line and waits for Enter.
func (p *Prompter) Wait(message string) error {
	if !p.tty {
		return ErrNoTerminal
	}
	if p.drawn() {
		return p.drawnWait(message)
	}
	fmt.Fprint(p.out, p.plain(message)+" ")
	_, err := p.line()
	return err
}

// Confirm asks a yes or no question. An empty answer is def.
func (p *Prompter) Confirm(question string, def bool) (bool, error) {
	if !p.tty {
		return false, ErrNoTerminal
	}
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	parse := func(a string) (bool, bool) {
		switch strings.ToLower(a) {
		case "":
			return def, true
		case "y", "yes":
			return true, true
		case "n", "no":
			return false, true
		}
		return false, false
	}
	if p.drawn() {
		a, err := p.drawnConfirm(question, hint, func(a string) (string, error) {
			v, ok := parse(a)
			if !ok {
				return "", errors.New("answer y or n")
			}
			return map[bool]string{true: "Yes", false: "No"}[v], nil
		})
		return a == "Yes", err
	}
	for i := 0; p.again(i); i++ {
		fmt.Fprint(p.out, p.plain(question+" "+hint)+" ")
		a, err := p.line()
		if err != nil {
			return false, err
		}
		if v, ok := parse(a); ok {
			return v, nil
		}
		fmt.Fprintln(p.out, "  answer y or n")
	}
	return false, errors.New("no yes or no answer")
}

// Choice is one option of Choose.
type Choice struct {
	Key    string // accepted as an answer besides the number, such as "hetzner"
	Label  string
	Detail string // shown beside the label, or under it when the width is short
}

// Choose shows the choices and returns the index picked. An empty answer
// is def; the number or the choice's key are accepted, and on a drawn menu
// the arrow keys move the selection.
func (p *Prompter) Choose(question string, choices []Choice, def int) (int, error) {
	if !p.tty {
		return 0, ErrNoTerminal
	}
	if len(choices) == 0 {
		return 0, errors.New("nothing to choose from")
	}
	if def < 0 || def >= len(choices) {
		def = 0
	}
	if p.drawn() {
		return p.drawnMenu(question, choices, def)
	}
	fmt.Fprintln(p.out, p.plain(question))
	for _, l := range listLines(choices, p.st.Width) {
		fmt.Fprintln(p.out, l)
	}
	for i := 0; p.again(i); i++ {
		fmt.Fprintf(p.out, "Choose 1-%d [%d]: ", len(choices), def+1)
		a, err := p.line()
		if err != nil {
			return 0, err
		}
		if a == "" {
			return def, nil
		}
		if j, ok := pick(a, choices); ok {
			return j, nil
		}
		fmt.Fprintf(p.out, "  type a number from 1 to %d\n", len(choices))
	}
	return 0, errors.New("no valid choice")
}

// pick reads a typed answer: a choice's number or its key.
func pick(a string, choices []Choice) (int, bool) {
	if n, err := strconv.Atoi(a); err == nil && n >= 1 && n <= len(choices) {
		return n - 1, true
	}
	for j, c := range choices {
		if c.Key != "" && strings.EqualFold(a, c.Key) {
			return j, true
		}
	}
	return 0, false
}

// listLines is the plain numbered list: the details beside the labels when
// each fits there on one line (width 0, unknown: always), else under each.
func listLines(choices []Choice, width int) []string {
	label := 0
	for _, c := range choices {
		label = max(label, len([]rune(c.Label)))
	}
	digits := len(strconv.Itoa(len(choices)))
	col := 2 + digits + 2 + label + 2 // where a detail beside the label starts
	cols := width - 1
	beside := true
	for _, c := range choices {
		beside = beside && (width <= 0 || len([]rune(c.Detail)) <= cols-col)
	}
	var out []string
	for i, c := range choices {
		num := fmt.Sprintf("  %*d) ", digits, i+1)
		switch {
		case c.Detail == "":
			out = append(out, num+c.Label)
		case width <= 0:
			out = append(out, fmt.Sprintf("%s%-*s  %s", num, label, c.Label, c.Detail))
		case beside:
			out = append(out, fmt.Sprintf("%s%-*s  %s", num, label, c.Label, c.Detail))
		default:
			ind := len(num) + 2
			out = append(out, num+c.Label)
			for _, l := range ui.Wrap(c.Detail, cols-ind) {
				out = append(out, strings.Repeat(" ", ind)+l)
			}
		}
	}
	return out
}

// Secret asks for a value without echoing it. Nothing typed is written
// anywhere; an empty answer is asked again. On a drawn terminal a dot
// shows for each character typed, so a paste is seen to have arrived.
func (p *Prompter) Secret(question string) (string, error) {
	if !p.tty {
		return "", ErrNoTerminal
	}
	if p.drawn() {
		return p.drawnLine(question, "", "input hidden", true, func(a string) (string, error) {
			if a == "" {
				return "", errors.New("nothing was entered")
			}
			return a, nil
		})
	}
	for i := 0; p.again(i); i++ {
		fmt.Fprint(p.out, p.plain(question+" (input hidden):")+" ")
		restore := func() {}
		if p.hide != nil {
			r, err := p.hide()
			if err != nil {
				return "", fmt.Errorf("turn off terminal echo: %w", err)
			}
			restore = r
		}
		a, err := p.line()
		restore()
		if p.hide == nil {
			fmt.Fprintln(p.out)
		}
		if err != nil {
			return "", err
		}
		if a != "" {
			return a, nil
		}
		fmt.Fprintln(p.out, "  nothing was entered")
	}
	return "", ErrNoAnswer
}
