package prompt

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/keelbox/keelbox/internal/ui"
)

// MultiChoose shows choices to tick and returns the indexes ticked, in
// list order. Nothing is ticked at first, so Enter alone picks none. On a
// drawn terminal the arrow keys move, Space ticks and a ticks or clears
// them all; elsewhere the numbers are typed, such as "1 3 5" or "2-4", or
// "a" for all.
func (p *Prompter) MultiChoose(question string, choices []Choice) ([]int, error) {
	if !p.tty {
		return nil, ErrNoTerminal
	}
	if len(choices) == 0 {
		return nil, errors.New("nothing to choose from")
	}
	if p.drawn() {
		return p.drawnMulti(question, choices)
	}
	fmt.Fprintln(p.out, p.plain(question))
	for _, l := range listLines(choices, p.st.Width) {
		fmt.Fprintln(p.out, l)
	}
	for i := 0; p.again(i); i++ {
		fmt.Fprintf(p.out, "Numbers, such as 1 3 or 2-4, a for all, Enter for none: ")
		a, err := p.line()
		if err != nil {
			return nil, err
		}
		if picked, ok := pickMany(a, len(choices)); ok {
			return picked, nil
		}
		fmt.Fprintf(p.out, "  type numbers from 1 to %d, a for all, or nothing\n", len(choices))
	}
	return nil, errors.New("no valid choice")
}

// pickMany reads typed numbers: "1 3", "1,3", "2-4", "a" for all, "" for
// none. The result is in list order, each index once.
func pickMany(a string, n int) ([]int, bool) {
	a = strings.TrimSpace(a)
	switch strings.ToLower(a) {
	case "":
		return nil, true
	case "a", "all":
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out, true
	}
	ticked := make([]bool, n)
	for _, f := range strings.FieldsFunc(a, func(r rune) bool { return r == ' ' || r == ',' }) {
		lo, hi, isRange := strings.Cut(f, "-")
		from, err := strconv.Atoi(lo)
		if err != nil {
			return nil, false
		}
		to := from
		if isRange {
			if to, err = strconv.Atoi(hi); err != nil {
				return nil, false
			}
		}
		if from < 1 || to > n || from > to {
			return nil, false
		}
		for j := from; j <= to; j++ {
			ticked[j-1] = true
		}
	}
	var out []int
	for i, t := range ticked {
		if t {
			out = append(out, i)
		}
	}
	return out, true
}

// MultiLines draws the choices with a box each, ticked or not, and the
// pointer at sel.
func MultiLines(st ui.Style, choices []Choice, ticked []bool, sel, cols int) []string {
	box := func(on bool) string {
		if on {
			return st.Accent("[x]")
		}
		return st.Dim("[ ]")
	}
	label := 0
	for _, c := range choices {
		label = max(label, ui.Width(c.Label))
	}
	lead := 4 + 3 + 1 // "  › " + "[x]" + space
	col := lead + label + 3
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
		head := mark + box(ticked[i]) + " "
		switch {
		case c.Detail == "":
			out = append(out, head+name)
		case beside:
			lines := ui.Wrap(c.Detail, cols-col)
			out = append(out, head+ui.Pad(name, label)+"   "+st.Dim(lines[0]))
			for _, l := range lines[1:] {
				out = append(out, strings.Repeat(" ", col)+st.Dim(l))
			}
		default:
			out = append(out, head+name)
			for _, l := range ui.Wrap(c.Detail, cols-lead) {
				out = append(out, strings.Repeat(" ", lead)+st.Dim(l))
			}
		}
	}
	return out
}

// multiHint is the line under a list to tick.
func multiHint(st ui.Style) string {
	move := st.Bold("↑↓") + st.Dim(" move")
	if !st.UTF8 {
		move = st.Bold("up/down") + st.Dim(" move")
	}
	keys := []string{move, st.Bold("Space") + st.Dim(" tick"), st.Bold("a") + st.Dim(" all or none"), st.Bold("Enter") + st.Dim(" done")}
	return "  " + strings.Join(keys, st.Sep())
}

func (p *Prompter) drawnMulti(question string, choices []Choice) ([]int, error) {
	end, err := p.session()
	if err != nil {
		return nil, err
	}
	defer end()
	r := &region{p: p}
	st := p.st
	n := len(choices)
	ticked := make([]bool, n)
	sel := 0
	render := func() []string {
		cols := p.cols()
		lines := QuestionLines(st, st.Ask(), question, cols)
		lines = append(lines, MultiLines(st, choices, ticked, sel, cols)...)
		return append(lines, multiHint(st))
	}
	r.draw(render())
	for {
		k, err := p.nextKey()
		if err != nil {
			if errors.Is(err, ErrInterrupted) {
				r.done(QuestionLines(st, stopMark(st), question, p.cols()))
				return nil, err
			}
			r.done(render())
			return nil, ErrNoAnswer
		}
		switch {
		case k.is("enter"):
			var out []int
			var names []string
			for i, t := range ticked {
				if t {
					out = append(out, i)
					names = append(names, choices[i].Label)
				}
			}
			answer := "none"
			if len(names) > 0 {
				answer = strings.Join(names, ", ")
			}
			r.done(answered(st, label(question), st.Accent(answer), p.cols()))
			return out, nil
		case k.is("up") || k.is("backtab"):
			sel = (sel + n - 1) % n
		case k.is("down") || k.is("tab"):
			sel = (sel + 1) % n
		case k.is("home"):
			sel = 0
		case k.is("end"):
			sel = n - 1
		case k.name == "" && k.r == ' ':
			ticked[sel] = !ticked[sel]
		case k.name == "" && (k.r == 'a' || k.r == 'A'):
			all := true
			for _, t := range ticked {
				all = all && t
			}
			for i := range ticked {
				ticked[i] = !all
			}
		case k.is("eof"):
			r.done(QuestionLines(st, stopMark(st), question, p.cols()))
			return nil, ErrNoAnswer
		}
		r.draw(render())
	}
}
