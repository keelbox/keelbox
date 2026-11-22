package cli

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/keelbox/keelbox/internal/ui"
)

// stepView shows what keelbox park, resume and resize are doing while they
// run. On a terminal it is a short list of steps: the one that runs has a
// spinner and its elapsed time, a finished one is a line with a tick and
// the time it took, and what the cloud's side said during a step stays
// under it. Everywhere else (a log, a pipe, --verbose, a screen reader) it
// is the lines these commands have always printed, one "keelbox: ..." line
// an event, which scripts and the end-to-end test read.
//
// A command calls begin and done (or failed) around each step, says for
// the plain log, and hands note to the cloud's calls as their logf. Each of
// them prints in one of the two forms and is silent in the other.
type stepView struct {
	out  io.Writer
	st   ui.Style
	live bool
	// logf is the command's own: a "keelbox: ..." line.
	logf func(string, ...any)
	now  func() time.Time

	mu    sync.Mutex
	cur   *runningStep
	rows  int // rows the live lines take on screen
	frame int
	stop  chan struct{}
	done  chan struct{}
}

type runningStep struct {
	label string
	hint  string // shown under the step until the cloud's side says something
	notes []string
	start time.Time
	// quiet is set once something else wrote to the terminal while the
	// step ran (a question, an error): its line is no longer redrawn, and
	// the step is settled under what was written.
	quiet bool
	// open is set while what was written last did not end its line, as a
	// question does not: the step's line then starts on the next one.
	open bool
}

// stepWidth is the widest the list gets: a card's width, so that the
// times end where the card after them does.
const stepWidth = 88

func newStepView(out io.Writer, o options, logf func(string, ...any)) *stepView {
	return &stepView{out: out, st: o.style, logf: logf, now: time.Now,
		live: o.style.Live && !o.style.Accessible && !o.verbose}
}

// cols is where a line of the list ends.
func (v *stepView) cols() int { return min(v.st.Cols(), stepWidth) }

// heading names what follows, as an apply's view does: "Resuming acme"
// and, dimmed, what from.
func (v *stepView) heading(title, detail string) {
	if !v.live {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	line := v.st.Bold(ui.Unbroken(title))
	if detail != "" {
		line += " " + v.st.Dim(detail)
	}
	fmt.Fprintln(v.out, "  "+ui.Hang(line, v.cols()-4, 4))
}

// begin starts a step: label says what is being done, hint what to know
// while it runs.
func (v *stepView) begin(label, hint string) {
	if !v.live {
		return
	}
	v.settle(true, "")
	v.mu.Lock()
	v.cur = &runningStep{label: label, hint: hint, start: v.now()}
	v.draw()
	v.stop, v.done = make(chan struct{}), make(chan struct{})
	stop, done := v.stop, v.done
	v.mu.Unlock()
	go func() {
		defer close(done)
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				v.mu.Lock()
				v.frame++
				v.draw()
				v.mu.Unlock()
			}
		}
	}()
}

// say is a line of the plain log, which the list replaces on a terminal.
func (v *stepView) say(format string, a ...any) {
	if !v.live {
		v.logf(format, a...)
	}
}

// note is what the cloud's side says: a line of the plain log, and on a
// terminal a line under the step that runs. Between two steps it is a line
// of its own.
func (v *stepView) note(format string, a ...any) {
	if !v.live {
		v.logf(format, a...)
		return
	}
	text := fmt.Sprintf(format, a...)
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cur == nil {
		fmt.Fprintln(v.out, "  "+v.st.Faint(v.st.Bullet())+" "+ui.Hang(amounts(text), v.cols()-4, 4))
		return
	}
	v.cur.notes = append(v.cur.notes, text)
	v.draw()
}

// finished settles the step that runs as done: label says what was done.
// With an empty label it is the first thing the cloud's side said, which
// for a deletion is the sentence itself ("deleted acme (id 42)"), or
// fallback when it said nothing.
func (v *stepView) finished(label, fallback string) {
	if !v.live {
		return
	}
	if label == "" {
		v.mu.Lock()
		if v.cur != nil && len(v.cur.notes) > 0 && !strings.HasPrefix(v.cur.notes[0], "warning") {
			label, v.cur.notes = v.cur.notes[0], v.cur.notes[1:]
		} else {
			label = fallback
		}
		v.mu.Unlock()
	}
	v.settle(true, label)
}

// failed settles the step that runs as not done. The command's own message
// follows it.
func (v *stepView) failed() {
	if v.live {
		v.settle(false, "")
	}
}

// did is something done between two steps: a line of the plain log, and
// on a terminal a line with a tick.
func (v *stepView) did(format string, a ...any) {
	if !v.live {
		v.logf(format, a...)
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	fmt.Fprintln(v.out, "  "+v.st.Tick()+" "+ui.Hang(capitalFirst(fmt.Sprintf(format, a...)), v.cols()-4, 4))
}

// close settles a step a command returned in the middle of.
func (v *stepView) close() { v.failed() }

// writer is w for everything else that writes to the terminal while the
// view is on it: the command's errors, a question about a host key, an
// apply. What it writes goes under the steps settled so far. A step that
// runs loses its live line first and is settled when it ends.
func (v *stepView) writer(w io.Writer) io.Writer {
	if !v.live {
		return w
	}
	return stepWriter{v, w}
}

type stepWriter struct {
	v *stepView
	w io.Writer
}

func (s stepWriter) Write(b []byte) (int, error) {
	v := s.v
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cur != nil {
		if !v.cur.quiet {
			v.clear()
			v.cur.quiet = true
		}
		if len(b) > 0 {
			v.cur.open = b[len(b)-1] != '\n'
		}
	}
	return s.w.Write(b)
}

// settle ends the step that runs, if one does, and prints its line: a
// tick, what was done and how long it took, then what was noted under it.
func (v *stepView) settle(ok bool, label string) {
	v.mu.Lock()
	stop, done := v.stop, v.done
	v.stop, v.done = nil, nil
	v.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	s := v.cur
	if s == nil {
		return
	}
	v.cur = nil
	v.clear()
	if s.open {
		fmt.Fprintln(v.out)
	}
	mark := v.st.Tick()
	if label == "" {
		label = s.label
	}
	if !ok {
		mark, label = v.st.Cross(), v.st.Bad(s.label)
	}
	fmt.Fprintln(v.out, v.row(mark, capitalFirst(label), v.st.Dim(ui.Duration(v.now().Sub(s.start))), false))
	for _, n := range s.notes {
		text := v.st.Dim(n)
		if strings.HasPrefix(n, "warning") {
			text = n
		}
		fmt.Fprintln(v.out, v.connector()+ui.Hang(amounts(text), v.cols()-6, 6))
	}
}

// row is a step's line: the mark, what it does and, at the right edge, the
// time. A label too long for the line goes on under itself, with the time
// where it ends and no amount broken in two, or is cut short while the
// step runs (one), where the line is redrawn in place.
func (v *stepView) row(mark, label, took string, one bool) string {
	room := v.cols() - 4 - ui.Width(took) - 2
	lines := ui.Wrap(amounts(label), room)
	if one && len(lines) > 1 {
		lines = []string{clipTo(ui.Strip(label), room)}
	}
	last := len(lines) - 1
	lines[last] = ui.Pad(lines[last], room) + "  " + took
	return "  " + mark + " " + strings.Join(lines, "\n    ")
}

// connector leads a line under a step.
func (v *stepView) connector() string {
	glyph := "-"
	if v.st.UTF8 {
		glyph = "⎿"
	}
	return "    " + v.st.Faint(glyph) + " "
}

// draw writes the live lines of the step that runs: a spinner, the step
// and its elapsed time; under it, the last thing noted, or the hint.
func (v *stepView) draw() {
	s := v.cur
	if s == nil || s.quiet {
		return
	}
	v.clear()
	frames := []string{"|", "/", "-", `\`}
	if v.st.UTF8 {
		frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	}
	line := v.row(v.st.Accent(frames[v.frame%len(frames)]), v.st.Bold(s.label), v.st.Dim(ui.Duration(v.now().Sub(s.start))), true)
	under := s.hint
	if n := len(s.notes); n > 0 {
		under = s.notes[n-1]
	}
	v.rows = 1
	if under != "" {
		line += "\n" + v.connector() + v.st.Dim(clipTo(under, v.cols()-6))
		v.rows = 2
	}
	fmt.Fprint(v.out, line)
}

// clear takes the live lines off the screen.
func (v *stepView) clear() {
	if v.rows == 0 {
		return
	}
	fmt.Fprint(v.out, "\r\x1b[K")
	for i := 1; i < v.rows; i++ {
		fmt.Fprint(v.out, "\x1b[1A\r\x1b[K")
	}
	v.rows = 0
}

// clipTo cuts plain text to n columns, with an ellipsis.
func clipTo(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:max(n, 0)])
	}
	return string(r[:n-1]) + "…"
}
