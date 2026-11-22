package prompt

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/keelbox/keelbox/internal/ui"
)

// Drawn questions: on a capable terminal each question is drawn, redrawn
// as keys arrive, and folded into one answered line at the end. The
// terminal is in key-at-a-time mode only while a question waits, so Ctrl-C
// still interrupts and a program run afterwards gets the terminal as it was.

// key is one key press: a character, or a named key.
type key struct {
	r    rune
	name string // enter, up, down, home, end, tab, backtab, backspace, kill, word, eof, esc; "" for r
}

func (k key) is(name string) bool { return k.name == name }

// readKey reads one key from the input, folding escape sequences into
// named keys and dropping the ones a question has no use for, such as a
// terminal's late answer to a query.
func (p *Prompter) readKey() (key, error) {
	b, err := p.in.ReadByte()
	if err != nil {
		return key{}, err
	}
	switch b {
	case '\r', '\n':
		return key{name: "enter"}, nil
	case 0x7f, 0x08:
		return key{name: "backspace"}, nil
	case 0x15:
		return key{name: "kill"}, nil
	case 0x17:
		return key{name: "word"}, nil
	case 0x04:
		return key{name: "eof"}, nil
	case 0x09:
		return key{name: "tab"}, nil
	case 0x0e:
		return key{name: "down"}, nil
	case 0x10:
		return key{name: "up"}, nil
	case 0x1b:
		return p.readEscape()
	}
	if b < 0x20 {
		return key{name: "ignore"}, nil
	}
	if err := p.in.UnreadByte(); err != nil {
		return key{}, err
	}
	r, _, err := p.in.ReadRune()
	if err != nil {
		return key{}, err
	}
	return key{r: r}, nil
}

func (p *Prompter) readEscape() (key, error) {
	if p.in.Buffered() == 0 {
		return key{name: "esc"}, nil
	}
	b, err := p.in.ReadByte()
	if err != nil {
		return key{}, err
	}
	switch b {
	case '[', 'O':
		var params []byte
		for {
			c, err := p.in.ReadByte()
			if err != nil {
				return key{}, err
			}
			if c >= 0x40 && c <= 0x7e {
				switch {
				case c == 'A':
					return key{name: "up"}, nil
				case c == 'B':
					return key{name: "down"}, nil
				case c == 'H' || c == '~' && (string(params) == "1" || string(params) == "7"):
					return key{name: "home"}, nil
				case c == 'F' || c == '~' && (string(params) == "4" || string(params) == "8"):
					return key{name: "end"}, nil
				case c == 'Z':
					return key{name: "backtab"}, nil
				}
				return key{name: "ignore"}, nil
			}
			params = append(params, c)
		}
	case ']', 'P', '_':
		// A string sequence: up to BEL or ESC \.
		for {
			c, err := p.in.ReadByte()
			if err != nil {
				return key{}, err
			}
			if c == 0x07 {
				return key{name: "ignore"}, nil
			}
			if c == 0x1b {
				_, _ = p.in.ReadByte()
				return key{name: "ignore"}, nil
			}
		}
	}
	return key{name: "esc"}, nil
}

// nextKey waits for a key, or for Ctrl-C.
func (p *Prompter) nextKey() (key, error) {
	if p.intr == nil {
		return p.readKey()
	}
	type res struct {
		k   key
		err error
	}
	c := make(chan res, 1)
	go func() {
		k, err := p.readKey()
		c <- res{k, err}
	}()
	waiting.Add(1)
	defer waiting.Add(-1)
	select {
	case r := <-c:
		return r.k, r.err
	case <-p.intr:
		// The reader is left waiting; the command is about to end.
		return key{}, p.interrupted()
	}
}

// region is the part of the screen a question has drawn, from the start
// of its first line to the end of its last; draw replaces it.
type region struct {
	p    *Prompter
	rows int
}

func (r *region) draw(lines []string) {
	var b strings.Builder
	if r.rows > 0 {
		b.WriteString("\r")
		if r.rows > 1 {
			fmt.Fprintf(&b, "\x1b[%dA", r.rows-1)
		}
		b.WriteString("\x1b[J")
	}
	b.WriteString(strings.Join(lines, "\n"))
	r.rows = len(lines)
	fmt.Fprint(r.p.out, b.String())
}

// done replaces the question with its answered form and ends the line.
func (r *region) done(lines []string) {
	r.draw(lines)
	fmt.Fprint(r.p.out, "\n")
	r.rows = 0
}

// cols is the width to draw in now (the terminal may have been resized).
func (p *Prompter) cols() int {
	st := p.st
	if p.width != nil {
		if w := p.width(); w > 0 {
			st.Width = w
		}
	}
	return st.Cols()
}

// session puts the terminal in key mode with the cursor hidden; the
// returned function undoes both.
func (p *Prompter) session() (func(), error) {
	restore, err := p.keys()
	if err != nil {
		return nil, fmt.Errorf("read keys from the terminal: %w", err)
	}
	fmt.Fprint(p.out, "\x1b[?25l")
	return func() {
		fmt.Fprint(p.out, "\x1b[?25h")
		restore()
	}, nil
}

// QuestionLines is a question as drawn: a brass ? and the question in
// bold, wrapped with the lines after the first under the question.
func QuestionLines(st ui.Style, mark, question string, cols int) []string {
	lines := ui.Wrap(question, cols-2)
	out := make([]string, len(lines))
	for i, l := range lines {
		lead := "  "
		if i == 0 {
			lead = mark + " "
		}
		out[i] = lead + st.Bold(l)
	}
	return out
}

// label is a question as it stands before its answer once answered: a
// question keeps its question mark, and what asks for a typed value, a
// noun phrase such as "Server address", gets a colon.
func label(question string) string {
	if question == "" {
		return question
	}
	last := []rune(question)[len([]rune(question))-1]
	if unicode.IsLetter(last) || unicode.IsDigit(last) || last == ')' {
		return question + ":"
	}
	return question
}

// answered is a question folded into one line with its answer, which the
// caller has styled.
func answered(st ui.Style, question, answer string, cols int) []string {
	text := question + " " + answer
	lines := ui.Wrap(text, cols-2)
	out := make([]string, len(lines))
	for i, l := range lines {
		lead := "  "
		if i == 0 {
			lead = st.Tick() + " "
		}
		out[i] = lead + l
	}
	return out
}

// stopMark marks a question left without an answer (Ctrl-C, Ctrl-D): the
// dash of a stop that changed nothing, not the mark of a failure.
func stopMark(st ui.Style) string { return st.Dim("-") }

func (p *Prompter) cursor() string {
	if p.st.Colour {
		return "\x1b[7m \x1b[0m"
	}
	return "_"
}

// lineQuestion is a question answered by a typed line.
type lineQuestion struct {
	question, def, hint string
	secret              bool
	empty               string // what the answered question reads after an empty answer: "skipped"
	inline              bool   // the answer goes on the question's last line (yes or no)
	accept              func(string) (string, error)
}

// drawnLine asks a line question: typed on its own line under the
// question, the default shown dimmed until something is typed.
func (p *Prompter) drawnLine(question, def, hint string, secret bool, accept func(string) (string, error)) (string, error) {
	return p.ask(lineQuestion{question: question, def: def, hint: hint, secret: secret, accept: accept})
}

func (p *Prompter) drawnConfirm(question, hint string, accept func(string) (string, error)) (string, error) {
	return p.ask(lineQuestion{question: question, hint: hint, inline: true, accept: accept})
}

func (p *Prompter) drawnWait(message string) error {
	_, err := p.ask(lineQuestion{question: message, hint: "Enter", inline: true, accept: func(string) (string, error) { return "done", nil }})
	return err
}

func (q lineQuestion) shown(buf []rune) string {
	if q.secret {
		n := len(buf)
		if n == 0 {
			return ""
		}
		return strings.Repeat("•", min(n, 24))
	}
	return string(buf)
}

func (p *Prompter) ask(q lineQuestion) (string, error) {
	end, err := p.session()
	if err != nil {
		return "", err
	}
	defer end()
	r := &region{p: p}
	st := p.st
	var buf []rune
	errMsg := ""
	render := func() []string {
		cols := p.cols()
		lines := QuestionLines(st, st.Ask(), q.question, cols)
		in := q.shown(buf)
		// The dots of a secret give way to the count and the hint beside
		// them on a narrow terminal.
		if room := cols - 38; q.secret && ui.Width(in) > room {
			in = strings.Repeat("•", max(room, 4))
		}
		if !q.secret && ui.Width(in) > cols-6 {
			rs := []rune(in)
			in = "…" + string(rs[len(rs)-(cols-7):])
		}
		field := in + p.cursor()
		if in == "" && q.def != "" {
			field = st.Dim(q.def) + p.cursor()
		}
		if q.secret && len(buf) > 0 {
			field += st.Dim(fmt.Sprintf("  %d characters", len(buf)))
		}
		if q.inline {
			last := lines[len(lines)-1] + " " + st.Dim(q.hint) + " " + field
			if ui.Width(last) <= cols {
				lines[len(lines)-1] = last
			} else {
				lines = append(lines, "  "+st.Dim(q.hint)+" "+field)
			}
		} else {
			hint := ""
			if q.hint != "" {
				hint = "  " + st.Dim("("+q.hint+")")
			}
			lines = append(lines, "  "+st.Pointer()+" "+field+hint)
		}
		if errMsg != "" {
			for i, l := range ui.Wrap(errMsg, cols-6) {
				lead := "    "
				if i == 0 {
					lead = "  " + st.Cross() + " "
				}
				lines = append(lines, lead+st.Bad(l))
			}
		}
		return lines
	}
	r.draw(render())
	for {
		k, err := p.nextKey()
		for err == nil {
			switch {
			case k.is("enter"):
				a, aerr := q.accept(strings.TrimSpace(string(buf)))
				if aerr == nil {
					shown := st.Accent(a)
					switch {
					case q.secret:
						shown = st.Accent(strings.Repeat("•", min(len(buf), 8))) + st.Dim(fmt.Sprintf(" received, %d characters", len(buf)))
					case a == "" && q.empty != "":
						shown = st.Dim(q.empty)
					}
					r.done(answered(st, label(q.question), shown, p.cols()))
					return a, nil
				}
				// Asked again, with the reason under the field until a key
				// is typed. The refused text goes: a corrected answer typed
				// after it would otherwise be added to it.
				errMsg, buf = aerr.Error(), nil
			case k.is("backspace"):
				if len(buf) > 0 {
					buf = buf[:len(buf)-1]
				}
			case k.is("kill"):
				buf = nil
			case k.is("word"):
				s := strings.TrimRight(string(buf), " ")
				if i := strings.LastIndexByte(s, ' '); i >= 0 {
					buf = []rune(s[:i+1])
				} else {
					buf = nil
				}
			case k.is("eof"):
				if len(buf) == 0 {
					r.done(append(render()[:0:0], QuestionLines(st, stopMark(st), q.question, p.cols())...))
					return "", ErrNoAnswer
				}
			case k.name == "" && unicode.IsPrint(k.r):
				buf = append(buf, k.r)
				errMsg = ""
			}
			if p.in.Buffered() == 0 {
				break
			}
			k, err = p.readKey() // a paste: take what is there before drawing
		}
		if err != nil {
			if errors.Is(err, ErrInterrupted) {
				r.done(QuestionLines(st, stopMark(st), q.question, p.cols()))
				return "", err
			}
			r.done(render())
			return "", ErrNoAnswer
		}
		r.draw(render())
	}
}

// MenuLines draws a menu's choices for cols columns: a pointer at the
// selected one, its label in brass; the details dimmed beside the labels
// when each fits there on one line, else under each label.
func MenuLines(st ui.Style, choices []Choice, sel, cols int) []string {
	label := 0
	for _, c := range choices {
		label = max(label, ui.Width(c.Label))
	}
	digits := len(strconv.Itoa(len(choices)))
	lead := 4 + digits + 2 // "  › " + number + 2
	col := lead + label + 3
	// Beside the labels only when every detail fits on its line there: a
	// detail wrapped in a narrow column reads worse than one under its label.
	beside := true
	for _, c := range choices {
		beside = beside && ui.Width(c.Detail) <= cols-col
	}
	var out []string
	for i, c := range choices {
		mark, name := "    ", c.Label
		if i == sel {
			mark, name = "  "+st.Pointer()+" ", st.BoldAccent(c.Label)
		}
		num := st.Dim(fmt.Sprintf("%*d", digits, i+1)) + "  "
		switch {
		case c.Detail == "":
			out = append(out, mark+num+name)
		case beside:
			lines := ui.Wrap(c.Detail, cols-col)
			out = append(out, mark+num+ui.Pad(name, label)+"   "+st.Dim(lines[0]))
			for _, l := range lines[1:] {
				out = append(out, strings.Repeat(" ", col)+st.Dim(l))
			}
		default:
			out = append(out, mark+num+name)
			for _, l := range ui.Wrap(c.Detail, cols-lead) {
				out = append(out, strings.Repeat(" ", lead)+st.Dim(l))
			}
		}
	}
	return out
}

// menuHint is the line under a menu.
func menuHint(st ui.Style, n int) string {
	keys := []string{st.Bold("↑↓") + st.Dim(" move"), st.Bold(fmt.Sprintf("1-%d", n)) + st.Dim(" pick"), st.Bold("Enter") + st.Dim(" choose")}
	if !st.UTF8 {
		keys[0] = st.Bold("up/down") + st.Dim(" move")
	}
	return "  " + strings.Join(keys, st.Sep())
}

func (p *Prompter) drawnMenu(question string, choices []Choice, def int) (int, error) {
	end, err := p.session()
	if err != nil {
		return 0, err
	}
	defer end()
	r := &region{p: p}
	st := p.st
	sel, typed, errMsg := def, "", ""
	n := len(choices)
	render := func() []string {
		cols := p.cols()
		lines := QuestionLines(st, st.Ask(), question, cols)
		lines = append(lines, MenuLines(st, choices, sel, cols)...)
		hint := menuHint(st, n)
		if typed != "" {
			hint = "  " + st.Pointer() + " " + typed + p.cursor()
		}
		lines = append(lines, hint)
		if errMsg != "" {
			lines = append(lines, "  "+st.Cross()+" "+st.Bad(errMsg))
		}
		return lines
	}
	r.draw(render())
	for {
		k, err := p.nextKey()
		if err != nil {
			if errors.Is(err, ErrInterrupted) {
				r.done(QuestionLines(st, stopMark(st), question, p.cols()))
				return 0, err
			}
			r.done(render())
			return 0, ErrNoAnswer
		}
		switch {
		case k.is("enter"):
			if typed == "" {
				r.done(answered(st, label(question), st.Accent(choices[sel].Label), p.cols()))
				return sel, nil
			}
			if j, ok := pick(typed, choices); ok {
				r.done(answered(st, label(question), st.Accent(choices[j].Label), p.cols()))
				return j, nil
			}
			typed = ""
			errMsg = fmt.Sprintf("type a number from 1 to %d, or move with the arrow keys", n)
		case k.is("up") || k.is("backtab"):
			sel, typed = (sel+n-1)%n, ""
		case k.is("down") || k.is("tab"):
			sel, typed = (sel+1)%n, ""
		case k.is("home"):
			sel, typed = 0, ""
		case k.is("end"):
			sel, typed = n-1, ""
		case k.is("esc") || k.is("kill"):
			typed = ""
		case k.is("backspace"):
			if typed != "" {
				typed = string([]rune(typed)[:len([]rune(typed))-1])
			}
		case k.is("eof"):
			r.done(QuestionLines(st, stopMark(st), question, p.cols()))
			return 0, ErrNoAnswer
		case k.name == "" && k.r >= '0' && k.r <= '9' && n < 10:
			typed = string(k.r)
			errMsg = ""
		case k.name == "" && unicode.IsPrint(k.r) && k.r != ' ':
			typed += string(k.r)
			errMsg = ""
		}
		if typed != "" {
			if j, ok := pick(typed, choices); ok {
				sel = j
			} else {
				for j, c := range choices {
					if c.Key != "" && strings.HasPrefix(strings.ToLower(c.Key), strings.ToLower(typed)) {
						sel = j
						break
					}
				}
			}
		}
		r.draw(render())
	}
}
