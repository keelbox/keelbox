package ui

import (
	"regexp"
	"strings"
	"unicode"
)

// Severity is how a problem is shown: a failure, a step only the user can
// do, or a stop that changed nothing.
type Severity int

// The severities.
const (
	Failure Severity = iota
	NeedsYou
	Stopped
)

// Trouble is one problem as keelbox shows it: what went wrong, why, and
// the command that continues. Text is an error the way keelbox writes
// them, "what went wrong; why or what to do: keelbox ...", perhaps with
// more lines after the first.
type Trouble struct {
	Severity Severity
	Text     string
	// Note is a sentence shown last, before the command: the state things
	// are left in, such as "Nothing was changed."
	Note string
	// Next is the command that continues, shown when Text ends with none.
	Next string
	// Names are words left as they are written where a sentence starts
	// with one: box names.
	Names []string
	// Prefix starts the first line in plain text: "keelbox: ".
	Prefix string
}

// Problem is the block for text: see Trouble.
func (s Style) Problem(sev Severity, text string) string {
	return s.Trouble(Trouble{Severity: sev, Text: text})
}

// Trouble renders t, ending with a newline. On a capable terminal it is
// one block: a mark and the title, the cause wrapped under it, and one
// arrow line with the command that continues. A line of the text that is
// a list item stays a line. A command the user is meant to copy is never
// wrapped and never has a word capitalised: it sits on a line of its own
// in the accent colour, even when it is longer than the terminal is wide.
// On a terminal that takes plain text only (TERM=dumb, a screen reader)
// the same lines are written without marks; where the width is not known
// (a pipe, a log) the text is one line as it was written.
func (s Style) Trouble(t Trouble) string {
	if !s.Live && s.Width <= 0 {
		return t.Prefix + t.Text + "\n"
	}
	items, next := troubleItems(t.Text, t.Names)
	if next == "" {
		next = t.Next
	}
	head := ""
	if len(items) > 0 && items[0].kind == itemProse {
		head, items = plainInline(items[0].text), items[1:]
	}
	for i := range items {
		if items[i].kind == itemProse {
			items[i].text += sentenceEnd(items[i].text)
		}
	}
	if t.Note != "" {
		items = append(items, troubleItem{kind: itemProse, text: t.Note})
	}
	cols := s.Cols()
	var b strings.Builder
	if !s.Live {
		// The words of the block, without its marks; after the prefix the
		// first word is as the text has it.
		if low := strings.ToLower(head[:min(1, len(head))]); low != "" && strings.HasPrefix(t.Text, low) {
			head = low + head[1:]
		}
		b.WriteString(Hang(t.Prefix+head, cols, 2) + "\n")
		writeItems(&b, s, items, cols, 2)
		if next != "" {
			b.WriteString("  Run: " + next + "\n")
		}
		return b.String()
	}
	var mark string
	switch t.Severity {
	case NeedsYou:
		mark, head = s.Bang(), s.Warn(s.Bold(head))
	case Stopped:
		mark, head = s.Dim("-"), s.Bold(head)
	default:
		mark, head = s.Cross(), s.Bad(s.Bold(head))
	}
	b.WriteString("  " + mark + " " + Hang(head, cols-4, 4) + "\n")
	writeItems(&b, s, items, cols, 4)
	if next != "" {
		b.WriteString("    " + s.Accent(s.glyph("→", "->")) + " " + s.Cmd(next) + "\n")
	}
	return b.String()
}

// writeItems writes a block's lines after its title, indented.
func writeItems(b *strings.Builder, s Style, items []troubleItem, cols, indent int) {
	pad := strings.Repeat(" ", indent)
	for _, it := range items {
		switch it.kind {
		case itemCommand:
			b.WriteString(pad + "  " + s.Cmd(it.text) + "\n")
		case itemList:
			b.WriteString(pad + "- " + Hang(s.inlineCommands(it.text), cols-indent-2, indent+2) + "\n")
		default:
			b.WriteString(pad + Hang(s.inlineCommands(it.text), cols-indent, indent) + "\n")
		}
	}
}

// inlineMark brackets a command that stays inside its sentence, in
// brackets or in a list item: painted, and kept on one line.
const inlineOpen, inlineClose = "\x00<", "\x00>"

func (s Style) inlineCommands(text string) string {
	if !strings.Contains(text, inlineOpen) {
		return text
	}
	var b strings.Builder
	for {
		i := strings.Index(text, inlineOpen)
		if i < 0 {
			break
		}
		j := strings.Index(text[i:], inlineClose)
		if j < 0 {
			break
		}
		b.WriteString(text[:i])
		b.WriteString(s.Cmd(Unbroken(text[i+len(inlineOpen) : i+j])))
		text = text[i+j+len(inlineClose):]
	}
	b.WriteString(text)
	return b.String()
}

// plainInline is text with its inline commands as plain words.
func plainInline(text string) string {
	return strings.NewReplacer(inlineOpen, "", inlineClose, "").Replace(text)
}

type itemKind int

const (
	itemProse itemKind = iota
	itemCommand
	itemList
)

type troubleItem struct {
	kind itemKind
	text string
}

// troubleItems takes an error's text apart: its sentences, the list lines
// among them, and the commands it tells the user to run. next is the
// command the text ends with, without the words that led to it.
func troubleItems(text string, names []string) (items []troubleItem, next string) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	var paras []string
	if first := lines[0]; first != "" {
		// The title is what went wrong: up to the "; " keelbox writes
		// before the cause, or the first sentence where there is none.
		if i := strings.Index(first, "; "); i >= 0 && commandBefore(first, i) < 0 {
			paras = append(paras, first[:i], first[i+2:])
		} else if i := sentenceBreak(first); i > 0 {
			paras = append(paras, first[:i], first[i+2:])
		} else {
			paras = append(paras, first)
		}
	}
	paras = append(paras, lines[1:]...)
	for n, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(p, "- "); ok {
			items = append(items, troubleItem{itemList, markInline(rest)})
			continue
		}
		items = append(items, proseItems(p, names, n == 0)...)
	}
	// The command the text ends with is the arrow line, and the words that
	// led to it ("then", "Continue") are what the arrow says.
	// An aside in brackets after it ("(list boxes: keelbox ls)") stays above
	// the arrow.
	k := len(items) - 1
	for k > 0 && items[k].kind == itemProse && strings.HasPrefix(items[k].text, "(") {
		k--
	}
	if k < 0 || items[k].kind != itemCommand {
		return items, ""
	}
	// An alternative ("or remove the box: keelbox forget acme") is not the
	// one command that continues: both stay with the words that lead to
	// them.
	if k > 0 && items[k-1].kind == itemProse && alternative(items[k-1].text) {
		return items, ""
	}
	next = items[k].text
	items = append(items[:k], items[k+1:]...)
	if k > 0 && items[k-1].kind == itemProse {
		if lead := leadIn(items[k-1].text); lead == "" {
			items = append(items[:k-1], items[k:]...)
		} else {
			items[k-1].text = lead
		}
	}
	return items, next
}

// sentenceBreak is where the first sentence of s ends, at its full stop,
// when another follows it: outside brackets and quotes, and not inside a
// command. It is 0 for one sentence.
func sentenceBreak(s string) int {
	depth, quoted := 0, false
	for i := 0; i+2 < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth = max(0, depth-1)
		case '"':
			quoted = !quoted
		case '.':
			if depth == 0 && !quoted && s[i+1] == ' ' && unicode.IsUpper(rune(s[i+2])) && commandBefore(s, i) < 0 {
				return i
			}
		}
	}
	return 0
}

// alternative reports whether the words that lead to a command offer it
// as one of two ways on: their last clause starts with "or".
func alternative(lead string) bool {
	lead = strings.ToLower(lead)
	if i := strings.LastIndex(lead, ", "); i >= 0 {
		lead = lead[i+2:]
	}
	return strings.HasPrefix(lead, "or ") || strings.HasPrefix(lead, "or,")
}

// commandBefore is where a command that runs past position i of s starts,
// or -1: a "; " inside a command is not the end of the title.
func commandBefore(s string, i int) int {
	for j := 0; j < i; j++ {
		if strings.HasPrefix(s[j:], ": ") {
			if n := commandLen(s[j+2:]); n > 0 && j+2+n > i {
				return j + 2
			}
		}
	}
	return -1
}

// proseItems splits a paragraph at the commands it names after a colon:
// each goes on a line of its own, except inside brackets, where it stays
// in its sentence.
func proseItems(p string, names []string, title bool) []troubleItem {
	var items []troubleItem
	prose := ""
	depth := 0
	flush := func() {
		t := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(prose), ",;."))
		prose = ""
		if t == "" || t == ")" {
			return
		}
		// Only what the text opens with can be about a command by its
		// name ("forget takes the box name"); later, "create it" is a verb.
		items = append(items, troubleItem{itemProse, sentence(t, names, title && len(items) == 0)})
	}
	for i := 0; i < len(p); {
		switch p[i] {
		case '(':
			depth++
		case ')':
			depth = max(0, depth-1)
		}
		if strings.HasPrefix(p[i:], ": ") {
			if n := commandLen(p[i+2:]); n > 0 {
				cmd := p[i+2 : i+2+n]
				if depth > 0 {
					prose += ": " + inlineOpen + cmd + inlineClose
				} else {
					prose += ":"
					flush()
					items = append(items, troubleItem{itemCommand, cmd})
				}
				i += 2 + n
				continue
			}
		}
		prose += p[i : i+1]
		i++
	}
	flush()
	return items
}

// markInline brackets the commands of a list item, which stay in it.
func markInline(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); {
		if strings.HasPrefix(p[i:], ": ") {
			if n := commandLen(p[i+2:]); n > 0 {
				b.WriteString(": " + inlineOpen + p[i+2:i+2+n] + inlineClose)
				i += 2 + n
				continue
			}
		}
		b.WriteByte(p[i])
		i++
	}
	return b.String()
}

// Commands are keelbox's own commands, as a message names them after
// "keelbox": the package that defines them keeps this list in step.
var Commands = []string{
	"up", "ls", "boxes", "init", "create", "resize", "park", "resume", "destroy", "forget", "attach", "ssh", "ssh-config", "agent-skill", "sessions", "skills",
	"login", "expose", "report", "plan", "apply", "doctor", "status", "reports", "notify", "agents", "rollback", "reboot", "backup",
	"restore", "install", "add", "update", "requests", "approve", "deny", "forget-token", "completion", "self-update", "version", "help",
}

// commandArg says, for the programs keelbox's messages tell the user to
// run, what the word after the program's name looks like when the two are
// a command and not the start of a sentence about the program ("ssh
// admin@host", not "ssh timed out").
var commandArg = map[string]*regexp.Regexp{
	"ssh":         regexp.MustCompile(`^(-|[^ ]+@)`),
	"ssh-keygen":  regexp.MustCompile(`^-`),
	"ssh-copy-id": regexp.MustCompile(`^-`),
	"ssh-add":     regexp.MustCompile(`^[-~/]`),
	"journalctl":  regexp.MustCompile(`^-`),
	"curl":        regexp.MustCompile(`^-`),
	"sudo":        regexp.MustCompile(`^(keelbox|tailscale|systemctl|usermod|reboot|du|apt|apt-get|ufw)$`),
	"doctl":       regexp.MustCompile(`^(auth|compute)$`),
	"aws":         regexp.MustCompile(`^(login|lightsail|configure|sso)$`),
	"gcloud":      regexp.MustCompile(`^(auth|config|compute|services)$`),
	"hcloud":      regexp.MustCompile(`^(context|server)$`),
	"tailscale":   regexp.MustCompile(`^(up|login|status|set)$`),
	"tmux":        regexp.MustCompile(`^(new|attach|ls)$`),
	"git":         regexp.MustCompile(`^config$`),
	"gh":          regexp.MustCompile(`^auth$`),
	"rm":          regexp.MustCompile(`^[-~/.]`),
	"cat":         regexp.MustCompile(`^[~/.]`),
	"chmod":       regexp.MustCompile(`^[0-7]{3,4}$`),
}

// commandLen is the length of the command s starts with, or 0: a program
// keelbox's messages name, its arguments, and no further than the end of
// the sentence it stands in.
func commandLen(s string) int {
	prog, rest, _ := strings.Cut(s, " ")
	arg, _, _ := strings.Cut(rest, " ")
	arg = strings.TrimRight(arg, ".,;:)?")
	switch re, known := commandArg[prog]; {
	case prog == "keelbox":
		if !contains(Commands, arg) {
			return 0
		}
	case !known || !re.MatchString(arg):
		return 0
	}
	n := commandEnd(s)
	if prog == "keelbox" && !keelboxArgs(strings.Fields(s[:n])) {
		return 0 // a sentence about a command, not one to run: "keelbox destroy deleted it on ..."
	}
	return n
}

// keelboxArgs reports whether the words after "keelbox COMMAND" read as
// its arguments and not as the rest of a sentence: flags and their values,
// anything that is no plain word (a path, a placeholder, a quoted value),
// and at most two plain words, a box and one more ("login lab codex").
func keelboxArgs(words []string) bool {
	plain, value, span := 0, false, ""
	for _, w := range words[min(2, len(words)):] {
		switch {
		case span != "":
			if strings.HasSuffix(w, span) {
				span = ""
			}
		case strings.HasPrefix(w, "-"):
			value = !strings.Contains(w, "=") // the word after a flag may be its value
		case value:
			value = false
		case strings.HasPrefix(w, "<") && !strings.HasSuffix(w, ">"):
			span = ">"
		case strings.HasPrefix(w, `"`) && (len(w) == 1 || !strings.HasSuffix(w, `"`)):
			span = `"`
		case plainWord(w):
			if plain++; plain > 2 {
				return false
			}
		}
	}
	return true
}

// plainWord reports whether w is lower-case letters and hyphens only, as a
// word of a sentence is, and a box name can be.
func plainWord(w string) bool {
	for _, r := range w {
		if (r < 'a' || r > 'z') && r != '-' {
			return false
		}
	}
	return w != ""
}

// commandEnd is where the command s starts with ends: at the end of the
// sentence or the clause it stands in.
func commandEnd(s string) int {
	quote := byte(0)
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		after := s[i+1:]
		switch {
		case c == '<' && (i == 0 || s[i-1] == ' ') && strings.Contains(after, ">"):
			quote = '>'
		case c == '"' || c == '\'' && (i == 0 || s[i-1] == ' '):
			quote = c
		case c == '\n':
			return i
		case c == '(':
			if i > 0 && s[i-1] == ' ' {
				return i - 1
			}
			depth++
		case c == ')':
			if depth == 0 {
				return i
			}
			depth--
		case c == ',' && (after == "" || after[0] == ' '), c == ';' && (after == "" || after[0] == ' '), c == '?':
			return i
		case c == '.' && (after == "" || after[0] == ' ' || after[0] == '\n') && (i == 0 || s[i-1] != '.'):
			return i
		case c == ' ' && strings.HasPrefix(after, "or "):
			return i
		}
	}
	return len(s)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// sentence is s starting with a capital, unless it starts with a word
// that is written as it is: one of names, a program, a word that is no
// ordinary word (a path, an address, a config key, a flag) or, where the
// text opens with it (title), one of keelbox's commands.
func sentence(s string, names []string, title bool) string {
	if s == "" {
		return s
	}
	word := s
	if i := strings.IndexAny(s, " ("); i >= 0 {
		word = s[:i]
	}
	word = strings.TrimRight(word, ".,;:")
	_, program := commandArg[word]
	switch {
	case !unicode.IsLower(rune(s[0])),
		word == "keelbox", program, title && contains(Commands, word), contains(names, word),
		strings.ContainsAny(word, "/.~_@=:0123456789"):
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// capital is s with its first letter in upper case.
func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func sentenceEnd(s string) string {
	if s == "" || strings.ContainsAny(s[len(s)-1:], ".):?!") || strings.HasSuffix(s, inlineClose) {
		return ""
	}
	return "."
}

// leadIn is s without the words that lead to a command at its end.
func leadIn(s string) string {
	s = strings.TrimSuffix(strings.TrimSpace(s), ":")
	for again := true; again; {
		again = false
		for _, t := range []string{", then", " then", ", or", " Then", " Continue", " continue", ", for example", " for example"} {
			if strings.HasSuffix(s, t) {
				s, again = strings.TrimSuffix(s, t), true
			}
		}
	}
	switch strings.ToLower(s) {
	case "then", "continue", "or":
		return ""
	}
	return strings.TrimSuffix(strings.TrimSuffix(s, ","), ".")
}
