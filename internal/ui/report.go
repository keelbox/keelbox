package ui

import (
	"bytes"
	"io"
	"regexp"
	"strings"
	"sync"
)

// Report styles the text keelbox doctor and keelbox status print, on the
// box or here, as it passes to a terminal: marks for ok, warn and FAIL,
// module names as headings, keys dimmed. It adds style around the lines it
// recognises and passes every other line unchanged, so a box on another
// keelbox version still reads; the text itself is the box's, already made
// plain (internal/plain) where an account on the box could have written it.
type Report struct {
	mu      sync.Mutex
	out     io.Writer
	st      Style
	status  bool
	partial []byte
}

// NewReport styles doctor output (status false) or status output to out.
func NewReport(out io.Writer, st Style, status bool) *Report {
	return &Report{out: out, st: st, status: status}
}

func (r *Report) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partial = append(r.partial, b...)
	for {
		i := bytes.IndexByte(r.partial, '\n')
		if i < 0 {
			break
		}
		line := string(r.partial[:i])
		r.partial = r.partial[i+1:]
		if _, err := io.WriteString(r.out, r.line(line)+"\n"); err != nil {
			return len(b), err
		}
	}
	return len(b), nil
}

// Close writes what is left of an unfinished line.
func (r *Report) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.partial) > 0 {
		_, _ = io.WriteString(r.out, r.line(string(r.partial)))
		r.partial = nil
	}
}

var (
	doctorHeadRe  = regexp.MustCompile(`^keelbox (doctor|status): (\S+) \((.*)\)$`)
	findingRe     = regexp.MustCompile(`^  (ok  |warn|FAIL)  (.{24}) ?(.*)$`)
	moduleRe      = regexp.MustCompile(`^[a-z][a-z-]*$`)
	doctorCountRe = regexp.MustCompile(`^(\d+) ok, (\d+) warnings?, (\d+) failures?\.$`)
	statusRowRe   = regexp.MustCompile(`^([a-z][a-z -]{0,9}?)( +)(\S.*)$`)
)

// statusValueCol is the column a status row's value starts at.
const statusValueCol = 11

// findingDetailCol is the column a finding's detail starts at: the mark,
// the 24-column check name and the spaces around them.
const findingDetailCol = 30

func (r *Report) line(l string) string {
	st := r.st
	if m := doctorHeadRe.FindStringSubmatch(l); m != nil {
		return st.Heading(capital(m[1])) + st.Sep() + st.Bold(m[2]) + " " + st.Dim(m[3])
	}
	if r.status {
		// A row's value wraps under itself, and so does a line that
		// carries on under a value.
		if m := statusRowRe.FindStringSubmatch(l); m != nil && len(m[1])+len(m[2]) == statusValueCol {
			return st.Dim(m[1]) + m[2] + Hang(m[3], st.Cols()-statusValueCol, statusValueCol)
		}
		if rest, ok := strings.CutPrefix(l, strings.Repeat(" ", statusValueCol)); ok && rest != "" && rest[0] != ' ' {
			return strings.Repeat(" ", statusValueCol) + Hang(rest, st.Cols()-statusValueCol-2, statusValueCol+2)
		}
		return l
	}
	if m := findingRe.FindStringSubmatch(l); m != nil {
		check, detail := strings.TrimRight(m[2], " "), m[3]
		pad := strings.Repeat(" ", len(m[2])-len(check))
		level := strings.TrimSpace(m[1])
		// A long detail wraps under its own column, or under the check's
		// name where the terminal leaves that column too narrow to read.
		lines, indent := []string{detail}, findingDetailCol
		if w := st.Cols() - findingDetailCol; w >= 30 {
			lines = Wrap(detail, w)
		} else if Width(detail) > w {
			lines, indent, pad = append([]string{""}, Wrap(detail, st.Cols()-6)...), 6, ""
		}
		for i, d := range lines {
			if level == "ok" {
				lines[i] = st.Dim(d)
			}
		}
		detail = " " + strings.Join(lines, "\n"+strings.Repeat(" ", indent))
		if lines[0] == "" {
			detail = detail[1:]
		}
		switch level {
		case "ok":
			return "  " + Pad(st.Tick(), 2) + " " + check + pad + detail
		case "warn":
			return "  " + Pad(st.Bang(), 2) + " " + st.Warn(check) + pad + detail
		}
		return "  " + Pad(st.Cross(), 2) + " " + st.Bad(st.Bold(check)) + pad + detail
	}
	if moduleRe.MatchString(l) {
		return st.Bold(l)
	}
	if m := doctorCountRe.FindStringSubmatch(l); m != nil {
		// A count of none is dimmed and unmarked: a red cross beside
		// "0 failures" reads as a failure.
		count := func(mark string, n int, one, many string) string {
			if n == 0 {
				return st.Dim(plural(n, one, many))
			}
			return mark + " " + plural(n, one, many)
		}
		return st.Tick() + " " + m[1] + " ok" + st.Sep() + count(st.Bang(), atoi(m[2]), "warning", "warnings") + st.Sep() + count(st.Cross(), atoi(m[3]), "failure", "failures")
	}
	return l
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}
