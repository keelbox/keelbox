package cli

import (
	"flag"
	"io"
	"os"
	"strings"
	"testing"
)

var updateDocs = flag.Bool("update", false, "rewrite the help blocks in docs/reference/cli.md and the golden files")

const cliDoc = "../../docs/reference/cli.md"

// helpBlocks are the generated blocks docs/reference/cli.md carries, by
// marker name: the overview, and each command's --help.
func helpBlocks() map[string]string {
	blocks := map[string]string{"keelbox help": overview()}
	for _, c := range commands {
		blocks["keelbox "+c.name+" --help"] = commandHelp(c)
	}
	return blocks
}

func markers(name string) (string, string) {
	return "<!-- " + name + ": begin -->\n```text\n", "```\n<!-- " + name + ": end -->"
}

// TestCLIReferenceMatchesHelp keeps docs/reference/cli.md in step with
// keelbox help and every command's --help. Run with -update to rewrite the
// blocks.
func TestCLIReferenceMatchesHelp(t *testing.T) {
	data, err := os.ReadFile(cliDoc)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	for name, want := range helpBlocks() {
		begin, end := markers(name)
		i, j := strings.Index(doc, begin), strings.Index(doc, end)
		if i < 0 || j < i {
			t.Errorf("docs/reference/cli.md: no block for `%s` (markers %q)", name, strings.TrimSpace(begin))
			continue
		}
		if got := doc[i+len(begin) : j]; got != want {
			if *updateDocs {
				doc = doc[:i+len(begin)] + want + doc[j:]
				continue
			}
			t.Errorf("docs/reference/cli.md is out of date for `%s`; run go test ./internal/cli -run TestCLIReferenceMatchesHelp -update\n--- doc\n%s\n--- help\n%s", name, got, want)
		}
	}
	if *updateDocs {
		if err := os.WriteFile(cliDoc, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Every flag keelbox parses is documented under some command, and every
// documented flag exists.
func TestHelpListsEveryFlag(t *testing.T) {
	registered := map[string]bool{}
	flagSet(&options{}, io.Discard).VisitAll(func(f *flag.Flag) { registered["--"+f.Name] = true })
	documented := map[string]bool{}
	for _, c := range commands {
		for _, f := range flagNames(c) {
			documented[f] = true
			if !registered[f] {
				t.Errorf("keelbox %s --help documents %s, which keelbox does not parse", c.name, f)
			}
		}
	}
	for f := range registered {
		if !documented[f] {
			t.Errorf("flag %s is in no command's --help", f)
		}
	}
}

// The Flags table in docs/reference/cli.md names, for each flag, the
// commands whose --help lists it. A flag without a row is one only up
// takes; a Commands cell that names no command in backticks is prose
// ("remote commands") and is not checked.
func TestFlagsTableMatchesHelp(t *testing.T) {
	data, err := os.ReadFile(cliDoc)
	if err != nil {
		t.Fatal(err)
	}
	_, table, ok := strings.Cut(string(data), "\n## Flags\n")
	if !ok {
		t.Fatal("docs/reference/cli.md has no Flags section")
	}
	table, _, _ = strings.Cut(table, "\n## ")
	rows := map[string][]string{}
	for _, l := range strings.Split(table, "\n") {
		cells := strings.Split(l, "|")
		if len(cells) < 4 || !strings.HasPrefix(strings.TrimSpace(cells[1]), "`--") {
			continue
		}
		flag := strings.Fields(strings.Trim(strings.TrimSpace(cells[1]), "`"))[0]
		rows[flag] = []string{}
		for i, part := range strings.Split(cells[2], "`") {
			if i%2 == 1 { // inside backticks: a command, perhaps with a flag
				rows[flag] = append(rows[flag], strings.Fields(part)[0])
			}
		}
	}
	inHelp := map[string][]string{}
	for _, c := range commands {
		for _, f := range flagNames(c) {
			inHelp[f] = append(inHelp[f], c.name)
		}
	}
	for f, cmds := range inHelp {
		row, ok := rows[f]
		switch {
		case !ok && (len(cmds) != 1 || cmds[0] != "up"):
			t.Errorf("the Flags table has no row for %s, which %v take", f, cmds)
		case ok && len(row) > 0 && !sameSet(row, cmds):
			t.Errorf("the Flags table lists %s under %v; --help lists it for %v", f, row, cmds)
		}
	}
	for f := range rows {
		if _, ok := inHelp[f]; !ok {
			t.Errorf("the Flags table has a row for %s, which is in no command's --help", f)
		}
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]bool{}
	for _, x := range a {
		seen[x] = true
	}
	for _, x := range b {
		if !seen[x] {
			return false
		}
	}
	return true
}

func TestEveryCommandHasHelp(t *testing.T) {
	for _, c := range commands {
		h := commandHelp(c)
		if !strings.HasPrefix(h, "keelbox "+c.name) || !strings.Contains(h, "Examples:\n  keelbox") && !strings.Contains(h, "Examples:\n  sudo keelbox") {
			t.Errorf("%s: help needs a synopsis and an example:\n%s", c.name, h)
		}
		for _, l := range strings.Split(h, "\n") {
			if len([]rune(l)) > helpDocWidth && !typed(c)[strings.TrimSpace(l)] {
				t.Errorf("%s: help line over %d columns: %q", c.name, helpDocWidth, l)
			}
		}
	}
}
