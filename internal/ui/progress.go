package ui

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Progress turns an apply into a compact view on a terminal: one line per
// phase (hardening the server, developer tools, agents, ...), the running
// phase with a spinner, its live elapsed time and, under it, the step it is
// on. Settled phases say how many changes they made; a failed step is
// listed under its phase. It renders Events, which come from apply's text
// log (Write: the same text apply writes everywhere else, wherever it runs)
// or from any other source that can say what an apply is doing. Use it
// only when the style is Live; otherwise write the log straight through.
// --verbose shows the log itself, module by module and step by step.
type Progress struct {
	mu      sync.Mutex
	out     io.Writer
	st      Style
	phases  []string       // the phases of the enabled modules, in order
	phaseOf map[string]int // module -> index in phases
	labelW  int
	now     func() time.Time

	partial []byte
	header  string // the apply's heading, shown before its first result
	box     string
	failed  int // steps failed in the whole apply
	cur     *phase
	rows    int  // rows the live lines take on screen
	paused  bool // the apply said it stopped at Ctrl-C
	frame   int
	stop    chan struct{}
	done    chan struct{}
	closed  bool
}

type phase struct {
	label                    string
	index                    int
	steps, changed, skipped  int
	failed                   []string
	module, step, stepDetail string
	start                    time.Time
}

// EventKind is what an Event says.
type EventKind int

// The events of an apply.
const (
	EventBegin  EventKind = iota // the apply started: Box, Detail (release, platform)
	EventModule                  // a module started: Module
	EventStep                    // a step was checked or applied: Word, Step, Detail
	EventDone                    // the apply's summary: Text
	EventNote                    // anything else to show: Text
)

// Event is one thing an apply reports.
type Event struct {
	Kind   EventKind
	Box    string
	Module string
	// Word is the step's outcome as apply logs it: ok, change, skip,
	// defer or FAILED.
	Word   string
	Step   string
	Detail string
	Text   string
}

// Phases group modules into what a reader recognises, in apply order. A
// module not listed is its own phase.
var Phases = []struct {
	Label   string
	Modules []string
}{
	{"Hardening the server", []string{"host", "access"}},
	{"Developer tools", []string{"runtime"}},
	{"Sessions and previews", []string{"sessions", "primer", "logbook", "previews"}},
	{"Backups, health and alerts", []string{"backup", "notify", "doctor", "watch"}},
	{"Agents", []string{"permissions", "agent-policy", "agents"}},
}

// NewProgress renders an apply of the named modules (in apply order) to
// out, a terminal. Close stops the spinner and settles the last line.
func NewProgress(out io.Writer, st Style, modules []string) *Progress {
	p := &Progress{out: out, st: st, phaseOf: map[string]int{}, now: time.Now}
	label := map[string]string{}
	for _, ph := range Phases {
		for _, m := range ph.Modules {
			label[m] = ph.Label
		}
	}
	for _, m := range modules {
		l := label[m]
		if l == "" {
			l = m
		}
		if len(p.phases) == 0 || p.phases[len(p.phases)-1] != l {
			p.phases = append(p.phases, l)
		}
		p.phaseOf[m] = len(p.phases) - 1
		p.labelW = max(p.labelW, Width(l))
	}
	return p
}

// Start runs the spinner until Close.
func (p *Progress) Start() *Progress {
	p.stop, p.done = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(p.done)
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-t.C:
				p.mu.Lock()
				if p.cur != nil {
					p.frame++
					p.draw()
				}
				p.mu.Unlock()
			}
		}
	}()
	return p
}

// Write takes apply's text log, line by line.
func (p *Progress) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.partial = append(p.partial, b...)
	for {
		i := bytes.IndexByte(p.partial, '\n')
		if i < 0 {
			break
		}
		line := string(p.partial[:i])
		p.partial = p.partial[i+1:]
		p.event(ParseLine(strings.TrimRight(line, "\r"), p.phaseOf))
	}
	return len(b), nil
}

// Event renders one event.
func (p *Progress) Event(e Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.event(e)
}

var stepWords = map[string]bool{"ok": true, "change": true, "defer": true, "FAILED": true, "skip": true}

var beginRe = regexp.MustCompile(`^keelbox apply: (\S+) \((.*)\)$`)

// ParseLine reads one line of apply's text log as an event. modules are
// the module names the apply runs; a step line belongs to the last one.
func ParseLine(l string, modules map[string]int) Event {
	if m := beginRe.FindStringSubmatch(l); m != nil {
		return Event{Kind: EventBegin, Box: m[1], Detail: m[2]}
	}
	if _, ok := modules[l]; ok {
		return Event{Kind: EventModule, Module: l}
	}
	if strings.HasPrefix(l, "  ") {
		f := strings.Fields(l)
		if len(f) >= 2 && stepWords[f[0]] {
			rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), f[0]))
			name, detail, _ := strings.Cut(rest, ": ")
			return Event{Kind: EventStep, Word: f[0], Step: name, Detail: detail}
		}
	}
	if strings.HasPrefix(l, "Applied ") || strings.HasPrefix(l, "No changes") {
		return Event{Kind: EventDone, Text: l}
	}
	return Event{Kind: EventNote, Text: l}
}

func (p *Progress) event(e Event) {
	switch e.Kind {
	case EventBegin:
		p.finish(false)
		// Shown with the first phase that has something to say: an apply
		// that changes nothing is one line.
		p.box, p.header = e.Box, fmt.Sprintf("  %s %s\n", p.st.Bold("Applying "+e.Box), p.st.Dim(e.Detail))
	case EventModule:
		i, ok := p.phaseOf[e.Module]
		if !ok {
			return
		}
		if p.cur == nil || p.cur.index != i {
			p.finish(false)
			p.cur = &phase{label: p.phases[i], index: i, start: p.now()}
		}
		p.cur.module, p.cur.step, p.cur.stepDetail = e.Module, "", ""
		p.draw()
	case EventStep:
		if p.cur == nil {
			return
		}
		ph := p.cur
		ph.steps++
		// The engine's error names the step again; once is enough.
		d := strings.TrimPrefix(e.Detail, ph.module+"/"+e.Step+": ")
		ph.step, ph.stepDetail = e.Step, d
		switch e.Word {
		case "change":
			ph.changed++
		case "skip":
			ph.skipped++
		case "FAILED":
			p.failed++
			ph.failed = append(ph.failed, e.Step+": "+d)
		}
		p.draw()
	case EventDone:
		p.finish(false)
		p.clear()
		fmt.Fprintln(p.out, p.summary(e.Text))
		p.header = ""
		p.draw()
	case EventNote:
		if e.Text == "" && p.cur != nil {
			return // the gap before apply's summary
		}
		if strings.HasPrefix(e.Text, "Deferred ") {
			// After the last module: settle it rather than print inside it.
			p.finish(false)
			p.clear()
			p.emit(fmt.Sprintf("  %s %s", p.st.Bang(), Hang(strings.TrimSuffix(e.Text, "."), p.st.Cols()-4, 4)))
			return
		}
		p.clear()
		p.emit(e.Text)
		p.draw()
	}
}

var failedStepRe = regexp.MustCompile(`^keelbox: \S+/\S+ failed: `)
var failedCountRe = regexp.MustCompile(`^keelbox: \d+ steps? failed; the rest of the box is applied`)

var appliedRe = regexp.MustCompile(`^Applied (\d+) change(?:s|\(s\))? in (\S+?)\.`)
var noChangesRe = regexp.MustCompile(`^No changes(?: applied)?\.? ?(?:The box matches the config )?\((\d+) checks, (\S+?)\)\.`)

// emit prints a line of the apply's result, after its heading.
func (p *Progress) emit(line string) {
	if p.header != "" {
		fmt.Fprint(p.out, p.header)
		p.header = ""
	}
	fmt.Fprintln(p.out, line)
}

// summary is apply's last line as a result: "✓ Set up in 1m38s · 106
// changes". The logbook's path stays in the plain log and --verbose.
func (p *Progress) summary(text string) string {
	if p.failed > 0 {
		return fmt.Sprintf("  %s %s%s%s", p.st.Cross(), p.st.Bold(plural(p.failed, "step", "steps")+" failed"), p.st.Sep(), "the rest of the box is applied")
	}
	if m := noChangesRe.FindStringSubmatch(text); m != nil && p.header != "" {
		return fmt.Sprintf("  %s %s%s%s", p.st.Tick(), p.st.Bold(p.box+" is up to date"), p.st.Sep(), p.st.Dim(m[1]+" checks in "+m[2]))
	}
	if m := appliedRe.FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[1])
		return fmt.Sprintf("  %s %s%s%s", p.st.Tick(), p.st.Bold("Applied in "+m[2]), p.st.Sep(), plural(n, "change", "changes"))
	}
	if m := noChangesRe.FindStringSubmatch(text); m != nil {
		return fmt.Sprintf("  %s %s%s%s", p.st.Tick(), p.st.Bold("No changes"), p.st.Sep(), p.st.Dim("the box matches the config, "+m[1]+" checks in "+m[2]))
	}
	return "  " + p.st.Bold(text)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Err is a writer for the same terminal's error stream: what it writes
// appears above the live lines, which are redrawn after it.
func (p *Progress) Err(w io.Writer) io.Writer { return errWriter{p, w} }

type errWriter struct {
	p *Progress
	w io.Writer
}

func (e errWriter) Write(b []byte) (int, error) {
	p := e.p
	p.mu.Lock()
	defer p.mu.Unlock()
	s := string(b)
	// The apply's own last word: settle the phase it stopped in first.
	if strings.HasPrefix(s, "keelbox: apply failed") || strings.HasPrefix(s, "keelbox: apply stopped") {
		p.paused = strings.HasPrefix(s, "keelbox: apply stopped")
		p.finish(true)
	}
	// A failed step is listed under its phase and in the summary already.
	if p.failed > 0 && (failedStepRe.MatchString(s) || failedCountRe.MatchString(s)) {
		return len(b), nil
	}
	p.clear()
	n, err := e.w.Write(b)
	p.draw()
	return n, err
}

// Close settles the view: a phase still running is shown as stopped.
func (p *Progress) Close() {
	if p.stop != nil {
		close(p.stop)
		<-p.done
		p.stop = nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	if len(p.partial) > 0 {
		p.event(ParseLine(string(p.partial), p.phaseOf))
		p.partial = nil
	}
	p.finish(true)
}

// connector leads the line under a phase: the step, or a failure.
func (p *Progress) connector() string { return "    " + p.st.Faint(p.st.glyph("⎿", "-")) + " " }

// row is a phase's line: mark, label, what it did, and the time, right
// aligned, fitted to the width.
func (p *Progress) row(mark, label, what, took string) string {
	whatW := max(0, min(18, p.st.Cols()-p.labelW-18))
	return "  " + mark + " " + Pad(label, p.labelW) + "  " + Pad(clip(what, whatW), whatW) + " " + padLeft(took, 6)
}

func padLeft(s string, n int) string {
	if w := Width(s); w < n {
		return strings.Repeat(" ", n-w) + s
	}
	return s
}

// finish replaces the running phase's live lines with its result. A phase
// that changed nothing is not listed: a reader looks for what happened.
func (p *Progress) finish(stopped bool) {
	ph := p.cur
	if ph == nil {
		return
	}
	p.cur = nil
	p.clear()
	took := Duration(p.now().Sub(ph.start))
	dim := p.st.Dim
	switch {
	case stopped && p.paused:
		// Stopped as asked, between two steps: nothing failed.
		what := "stopped before its first step"
		if ph.step != "" {
			what = fmt.Sprintf("stopped after %s (step %d)", ph.step, ph.steps)
		}
		p.emit(p.row(dim("-"), ph.label, "", dim(took)))
		p.emit(p.connector() + dim(what))
	case stopped:
		p.emit(p.row(p.st.Cross(), ph.label, p.st.Bad("stopped"), dim(took)))
		p.emit(p.connector() + p.st.Bad(fmt.Sprintf("at %s (step %d)", ph.step, ph.steps)))
	case len(ph.failed) > 0:
		p.emit(p.row(p.st.Cross(), ph.label, p.st.Bad(fmt.Sprintf("%d failed", len(ph.failed))), dim(took)))
	case ph.changed > 0:
		p.emit(p.row(p.st.Tick(), ph.label, plural(ph.changed, "change", "changes"), dim(took)))
	}
	// Each failure once, under its phase: the step, then the cause.
	for _, f := range ph.failed {
		step, cause, _ := strings.Cut(f, ": ")
		p.emit(p.connector() + p.st.Bad(step))
		for _, l := range Wrap(cause, p.st.Cols()-6) {
			p.emit("      " + l)
		}
	}
}

// draw writes the live lines of the running phase: a spinner, the phase,
// its elapsed time and the count of phases; under it, the step it is on.
func (p *Progress) draw() {
	ph := p.cur
	if ph == nil || p.closed {
		return
	}
	p.clear()
	frames := p.st.spinner()
	glyph := p.st.Accent(frames[p.frame%len(frames)])
	count := p.st.Dim(fmt.Sprintf("%d/%d", ph.index+1, len(p.phases)))
	what := count
	if ph.changed > 0 {
		what = plural(ph.changed, "change", "changes") + p.st.Sep() + count
	}
	line := p.row(glyph, p.st.Bold(ph.label), what, p.st.Dim(Duration(p.now().Sub(ph.start))))
	step := "checking " + ph.module
	if ph.step != "" {
		step = ph.module + "/" + ph.step
		if ph.stepDetail != "" {
			step += ": " + ph.stepDetail
		}
	}
	under := p.connector() + p.st.Dim(clip(step, p.st.Cols()-6))
	fmt.Fprint(p.out, line+"\n"+under)
	p.rows = 2
}

// clear erases the live lines.
func (p *Progress) clear() {
	if p.rows == 0 {
		return
	}
	fmt.Fprint(p.out, "\r\x1b[K")
	for i := 1; i < p.rows; i++ {
		fmt.Fprint(p.out, "\x1b[1A\r\x1b[K")
	}
	p.rows = 0
}

// clip cuts s (which may be styled) to n columns with an ellipsis.
func clip(s string, n int) string {
	if Width(s) <= n {
		return s
	}
	r := []rune(Strip(s))
	if n <= 1 {
		return string(r[:max(n, 0)])
	}
	return string(r[:n-1]) + "…"
}

// Duration is a short elapsed time: 41s, 3m07s.
func Duration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

func (s Style) spinner() []string {
	if s.UTF8 {
		return []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	}
	return []string{"|", "/", "-", `\`}
}
