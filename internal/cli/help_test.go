package cli

import (
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/ui"
)

// typed are the lines of help that are a command to type: they are never
// wrapped, so they may be longer than the width.
func typed(c cmdDoc) map[string]bool {
	out := map[string]bool{}
	for _, e := range c.examples {
		out[e.cmd] = true
	}
	return out
}

// keelbox help and every --help fit the terminal from 60 to 120 columns:
// no line of prose passes the width, the lines after the first hang under
// it, no word is broken, and an example command stays whole on its line.
func TestHelpWrapsToTheTerminal(t *testing.T) {
	type page struct {
		name  string
		text  func(ui.Style) string
		typed map[string]bool
	}
	pages := []page{{"help", overviewIn, nil}}
	for _, c := range commands {
		pages = append(pages, page{c.name + " --help", func(st ui.Style) string { return commandHelpIn(st, c) }, typed(c)})
	}
	for _, p := range pages {
		words := strings.Fields(p.text(ui.Style{}))
		for _, w := range []int{60, 80, 100, 120} {
			got := p.text(ui.Style{Live: true, UTF8: true, Width: w})
			for _, l := range strings.Split(got, "\n") {
				if p.typed[strings.TrimSpace(l)] {
					continue // a command to type, whole
				}
				if ui.Width(l) >= w {
					t.Errorf("%s at %d columns: a line is %d wide: %q", p.name, w, ui.Width(l), l)
				}
			}
			for cmd := range p.typed {
				if !strings.Contains(got, "  "+cmd) {
					t.Errorf("%s at %d columns: the example %q is not whole on a line:\n%s", p.name, w, cmd, got)
				}
			}
			if strings.Join(strings.Fields(got), " ") != strings.Join(words, " ") {
				t.Errorf("%s at %d columns: the words differ from the documented form:\n%s", p.name, w, got)
			}
		}
	}
}

// What a command or a flag does hangs under itself beside its name, and
// goes under the name where the terminal leaves too little room beside it.
func TestHelpRowsHang(t *testing.T) {
	up, _ := findCmd("up")
	at80 := commandHelpIn(ui.Style{Width: 81}, up)
	want := "  --host-key FPR            ssh: the server's SHA256 host key fingerprint,\n" +
		"                            pinned at first connection\n"
	if !strings.Contains(at80, want) {
		t.Errorf("a flag's text does not hang under itself at 80 columns:\n%s", at80)
	}
	// Off a terminal help is laid out for 100 columns: the documented form.
	if doc := commandHelp(up); doc != commandHelpIn(ui.Style{Width: helpDocWidth + 1}, up) {
		t.Errorf("off a terminal help is not the %d column form:\n%s", helpDocWidth, doc)
	}
	over := overviewIn(ui.Style{Width: 61})
	want = "  destroy [box]             delete the Hetzner, AWS,\n" +
		"                            DigitalOcean or Google Cloud\n" +
		"                            server keelbox created\n"
	if !strings.Contains(over, want) {
		t.Errorf("a summary does not hang under itself at 60 columns:\n%s", over)
	}
	// The long example is never wrapped, and its note goes under it.
	long := "keelbox up nas --provider ssh --host 192.168.1.20 --ssh-user ubuntu --host-key " + hostKeyPlaceholder + " --yes"
	if !strings.Contains(commandHelpIn(ui.Style{Width: 61}, up), "  "+long+"\n") {
		t.Error("the long example was wrapped at 60 columns")
	}
}

// The block that shows an error tells a command to run from a sentence by
// the command's name, so it has to know every one.
func TestErrorBlockKnowsEveryCommand(t *testing.T) {
	known := map[string]bool{}
	for _, n := range ui.Commands {
		known[n] = true
	}
	for _, c := range commands {
		if !known[c.name] {
			t.Errorf("ui.Commands lacks %q: an error that names keelbox %s would not show it as a command", c.name, c.name)
		}
	}
	for _, n := range []string{"help", "boxes"} {
		if !known[n] {
			t.Errorf("ui.Commands lacks %q", n)
		}
	}
}
