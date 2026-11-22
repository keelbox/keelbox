package prompt

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/ui"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var where = []Choice{
	{Key: "ssh", Label: "An existing server (SSH)", Detail: "a home server, mini PC, VM or VPS you reach over SSH"},
	{Key: "local", Label: "This machine", Detail: "set up the Ubuntu machine you are on, with sudo"},
	{Key: "hetzner", Label: "A new Hetzner server", Detail: "keelbox creates it through the Hetzner API (needs an API token)"},
	{Key: "cloud-init", Label: "A server I create myself (cloud-config)", Detail: "any provider: paste a snippet keelbox prints, no token"},
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/prompt -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden file:\n%s\nwant:\n%s", name, got, want)
	}
}

// The menu at the widths people use, with and without colour, and the
// plain list a screen reader or TERM=dumb gets.
func TestGoldenMenus(t *testing.T) {
	for _, w := range []int{60, 80, 110, 200} {
		for name, st := range map[string]ui.Style{
			"nocolour": {Live: true, UTF8: true, Width: w},
			"colour":   {Live: true, Colour: true, UTF8: true, Depth: ui.Depth256, Width: w},
		} {
			lines := QuestionLines(st, st.Ask(), "Where should the box run?", st.Cols())
			lines = append(lines, MenuLines(st, where, 2, st.Cols())...)
			lines = append(lines, menuHint(st, len(where)))
			golden(t, fmt.Sprintf("menu-%d-%s.txt", w, name), strings.Join(lines, "\n")+"\n")
		}
		golden(t, fmt.Sprintf("menu-%d-plain.txt", w), strings.Join(listLines(where, w), "\n")+"\n")
	}
}

// The wrap bug: at no width from 60 to 200 columns does a line of a menu
// pass the width, or a detail break mid-word.
func TestMenusFitEveryWidth(t *testing.T) {
	long := append([]Choice{{Key: "gh", Label: "Sign in now", Detail: "a GitHub account for the agent, for git push and pull requests (one of its own, or a fine-grained token, keeps your other repositories out of reach)"}}, where...)
	for w := 60; w <= 200; w++ {
		st := ui.Style{Live: true, Colour: true, UTF8: true, Width: w}
		drawn := append(QuestionLines(st, st.Ask(), "GitHub: a GitHub account for the agent, for git push and pull requests (one of its own, or a fine-grained token, keeps your other repositories out of reach)", st.Cols()),
			MenuLines(st, long, 0, st.Cols())...)
		for _, l := range append(drawn, listLines(long, w)...) {
			if ui.Width(l) >= w {
				t.Fatalf("%d columns: a line is %d wide: %q", w, ui.Width(l), ui.Strip(l))
			}
			if s := strings.TrimSpace(ui.Strip(l)); strings.HasPrefix(s, "ken") || strings.HasPrefix(s, ")") {
				t.Fatalf("%d columns: broken mid-word: %q", w, s)
			}
		}
	}
}
