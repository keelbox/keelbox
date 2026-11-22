package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/prompt"
)

// laptopSkills makes a home with skills: alpha for Claude Code, with a
// script and files that may hold secrets; gamma for Codex, linked into
// Claude Code's folder; a .system folder that is Codex's own.
func laptopSkills(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	write := func(rel, body string, mode os.FileMode) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(".claude/skills/alpha/SKILL.md", "---\nname: alpha\ndescription: >\n  Reviews a change\n  before it ships.\n---\nBody.\n", 0o644)
	write(".claude/skills/alpha/scripts/run.sh", "#!/bin/sh\necho hi\n", 0o755)
	write(".claude/skills/alpha/.env", "KEY=1\n", 0o600)
	write(".claude/skills/alpha/ref/notes.md", "notes\n", 0o644)
	write(".agents/skills/gamma/SKILL.md", "---\ndescription: Deploys.\n---\n", 0o644)
	write(".codex/skills/.system/thing/SKILL.md", "---\ndescription: x\n---\n", 0o644)
	if err := os.Symlink(filepath.Join(home, ".agents/skills/gamma"), filepath.Join(home, ".claude/skills/gamma")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(home, ".claude/skills/alpha/ref/passwd")); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestLocalSkills(t *testing.T) {
	got := localSkills(laptopSkills(t))
	if len(got) != 2 || got[0].name != "alpha" || got[1].name != "gamma" {
		t.Fatalf("%+v", got)
	}
	a, g := got[0], got[1]
	if a.desc != "Reviews a change before it ships." || !a.scripts || a.tools() != "Claude Code" {
		t.Errorf("alpha: %+v", a)
	}
	if strings.Join(a.left, "; ") != ".env (may hold a secret); ref/passwd (a link or special file)" {
		t.Errorf("alpha left out: %q", a.left)
	}
	if g.tools() != "Claude Code and Codex" || g.scripts || len(g.roots) != 2 {
		t.Errorf("gamma: %+v", g)
	}
}

// keelbox skills copies the skills named to the agent's home, in the same
// folders, then sees them there; a different copy on the box is kept
// unless --overwrite says to replace it.
func TestSkillsCopiesToTheAgent(t *testing.T) {
	cfg, _ := fakeVendors(t)
	box := os.Getenv("HOME") // the agent's home: this machine is the box
	laptop := laptopSkills(t)
	skillHome = func() (string, error) { return laptop, nil }
	defer func(old func() (string, error)) { skillHome = old }(skillHome)
	noTerm := prompt.New(strings.NewReader(""), nil, false)
	run := func(o options) (int, string) {
		var out bytes.Buffer
		code := cmdSkills(context.Background(), cfg, o, noTerm, &out, &out)
		return code, out.String()
	}

	if code, out := run(options{}); code != 2 || !strings.Contains(out, "keelbox skills lab --skill alpha") {
		t.Errorf("no terminal, nothing named: %d %s", code, out)
	}
	if code, out := run(options{skill: "nope"}); code != 2 || !strings.Contains(out, "keelbox skills lab --list") {
		t.Errorf("unknown skill: %d %s", code, out)
	}
	if code, out := run(options{list: true}); code != 0 || !strings.Contains(out, "alpha") || !strings.Contains(out, "Claude Code and Codex") {
		t.Errorf("list: %d %s", code, out)
	}
	code, out := run(options{skill: "alpha,gamma"})
	if code != 0 {
		t.Fatalf("%d %s", code, out)
	}
	for _, p := range []string{".claude/skills/alpha/SKILL.md", ".claude/skills/alpha/scripts/run.sh", ".claude/skills/alpha/ref/notes.md", ".claude/skills/gamma/SKILL.md", ".agents/skills/gamma/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(box, p)); err != nil {
			t.Errorf("not copied: %s", p)
		}
	}
	for _, p := range []string{".claude/skills/alpha/.env", ".claude/skills/alpha/ref/passwd"} {
		if _, err := os.Lstat(filepath.Join(box, p)); err == nil {
			t.Errorf("copied %s", p)
		}
	}
	if fi, err := os.Stat(filepath.Join(box, ".claude/skills/alpha/scripts/run.sh")); err != nil || fi.Mode()&0o100 == 0 {
		t.Errorf("the script lost its mode: %v", fi)
	}
	if !strings.Contains(out, "alpha: left out .env (may hold a secret)") || strings.Count(out, "copied alpha")+strings.Count(out, "copied gamma") != 3 {
		t.Errorf("output:\n%s", out)
	}
	if _, out := run(options{skill: "alpha"}); !strings.Contains(out, "alpha is already on lab") {
		t.Errorf("second run:\n%s", out)
	}
	if err := os.WriteFile(filepath.Join(box, ".claude/skills/alpha/SKILL.md"), []byte("changed on the box"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, out := run(options{skill: "alpha"}); !strings.Contains(out, "kept lab's own alpha in ~/.claude/skills/alpha; replace it, without a terminal: keelbox skills lab --skill alpha --overwrite") {
		t.Errorf("different copy:\n%s", out)
	}
	if _, out := run(options{skill: "alpha", overwrite: true}); !strings.Contains(out, "copied alpha to lab") {
		t.Errorf("overwrite:\n%s", out)
	}
	if b, _ := os.ReadFile(filepath.Join(box, ".claude/skills/alpha/SKILL.md")); !strings.Contains(string(b), "Reviews a change") {
		t.Errorf("not replaced: %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(box, ".claude/skills/.keelbox-*")); len(left) != 0 {
		t.Errorf("temporary folders left: %v", left)
	}

	// On a terminal: the warning, then a list with nothing ticked.
	var term bytes.Buffer
	p := prompt.New(strings.NewReader("2\n"), &term, true)
	if code := cmdSkills(context.Background(), cfg, options{}, p, &term, &term); code != 0 {
		t.Fatalf("%d %s", code, term.String())
	}
	flat := strings.Join(strings.Fields(term.String()), " ")
	for _, want := range []string{"can read every file copied", "1) alpha Claude Code; has scripts. Reviews a change before it ships.", "gamma is already on lab"} {
		if !strings.Contains(flat, want) {
			t.Errorf("terminal lacks %q:\n%s", want, term.String())
		}
	}
}

func TestShorten(t *testing.T) {
	for in, want := range map[string]string{
		"short":                        "short",
		"a description that runs long": "a description…",
	} {
		if got := shorten(in, 16); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
