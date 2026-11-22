package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/installs"
	"github.com/keelbox/keelbox/internal/ui"
)

// noColour is a terminal without colour: the text as a reader sees it.
var noColour = ui.Style{UTF8: true}

func agentWelcome() welcome {
	return welcome{Box: "devbox-2", User: "agent", Agent: true, OS: "Ubuntu 24.04", CLIs: []string{"claude", "codex"}, Projects: "~/projects"}
}

// row is the welcome's line that starts with label.
func row(text, label string) string {
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "  "+label) {
			return l
		}
	}
	return ""
}

// What the welcome says about packages follows the box's permissions:
// keelbox install where the agent has it, the config where the box has no
// mode, and nothing where the agent has sudo.
func TestWelcomeNamesTheWayToAPackage(t *testing.T) {
	w := agentWelcome()
	w.Install = "auto"
	want := `  devbox-2  ·  you are the agent, an account without sudo  ·  Ubuntu 24.04
  Start an agent   claude    codex
  Your work        ~/projects
  Leave            Ctrl-b then d: everything here keeps running
  Come back        keelbox attach devbox-2   (from your computer)
  Packages         keelbox install NAME: an Ubuntu package, without sudo
  Need root?       from your computer: keelbox ssh devbox-2 --admin
`
	if got := w.render(noColour, 80); got != want {
		t.Errorf("auto at 80 columns:\n%s\nwant:\n%s", got, want)
	}
	if got := w.render(noColour, 40); !strings.Contains(got, "  Packages: keelbox install NAME: an\n    Ubuntu package, without sudo\n") && !strings.Contains(got, "  Packages: keelbox install NAME\n") {
		t.Errorf("auto on a phone splits or drops the command:\n%s", got)
	}
	w.Install = "manual"
	got := w.render(noColour, 80)
	if row(got, "Packages") != "  Packages         keelbox install NAME asks you: keelbox requests devbox-2" || strings.Count(got, "\n") != 7 {
		t.Errorf("manual at 80 columns:\n%s", got)
	}
	if got := w.render(noColour, 120); row(got, "Packages") != "  Packages         keelbox install NAME asks you; answer from your computer: keelbox requests devbox-2" {
		t.Errorf("manual at 120 columns:\n%s", got)
	}
	for _, mode := range []string{"auto", "manual"} {
		w.Install = mode
		if got := w.render(noColour, 80); strings.Contains(got, "host.packages") || row(got, "Need root?") != "  Need root?       from your computer: keelbox ssh devbox-2 --admin" {
			t.Errorf("%s: the config is named for a package, or root's way is gone:\n%s", mode, got)
		}
	}
	// An agent with sudo needs neither row, whatever the mode file says.
	w.Sudo = true
	if got := w.render(noColour, 80); strings.Contains(got, "Packages") || strings.Contains(got, "Need root?") {
		t.Errorf("an agent with sudo is told how to ask for a package:\n%s", got)
	}
}

// The session a box starts at boot is 80 columns wide: the whole welcome
// for the agent with both CLIs, in seven lines.
func TestWelcomeForTheAgentWithBothCLIs(t *testing.T) {
	want := `  devbox-2  ·  you are the agent, an account without sudo  ·  Ubuntu 24.04
  Start an agent   claude    codex
  Your work        ~/projects
  Leave            Ctrl-b then d: everything here keeps running
  Come back        keelbox attach devbox-2   (from your computer)
  Need root?       from your computer: keelbox ssh devbox-2 --admin
                   packages: host.packages, then keelbox apply devbox-2
`
	if got := agentWelcome().render(noColour, 80); got != want {
		t.Errorf("welcome at 80 columns:\n%s\nwant:\n%s", got, want)
	}
	// Unknown width is laid out for 80 columns.
	if got := agentWelcome().render(noColour, 0); got != want {
		t.Errorf("welcome without a width:\n%s", got)
	}
	// Room for the whole sentence about packages.
	wide := agentWelcome().render(noColour, 120)
	if !strings.HasSuffix(wide, "                   packages for good: host.packages in the box's config, then keelbox apply devbox-2\n") || strings.Count(wide, "\n") != 7 {
		t.Errorf("welcome at 120 columns:\n%s", wide)
	}
	mid := agentWelcome().render(noColour, 90)
	if !strings.HasSuffix(mid, "                   packages for good: host.packages, then keelbox apply devbox-2\n") {
		t.Errorf("welcome at 90 columns:\n%s", mid)
	}
}

func TestWelcomeSaysOnlyWhatIsTrueOfTheBox(t *testing.T) {
	t.Run("one agent CLI", func(t *testing.T) {
		w := agentWelcome()
		w.CLIs = []string{"codex"}
		got := w.render(noColour, 80)
		if row(got, "Start an agent") != "  Start an agent   codex" || strings.Contains(got, "claude") {
			t.Errorf("only codex is installed:\n%s", got)
		}
	})
	t.Run("no agent CLI", func(t *testing.T) {
		w := agentWelcome()
		w.CLIs = nil
		got := w.render(noColour, 80)
		if strings.Contains(got, "Start an agent") || strings.Contains(got, "claude") || strings.Contains(got, "codex") {
			t.Errorf("no agent CLI is installed:\n%s", got)
		}
		if strings.Count(got, "\n") != 6 || row(got, "Leave") == "" || row(got, "Come back") == "" {
			t.Errorf("the other lines stay:\n%s", got)
		}
	})
	t.Run("the agent has sudo", func(t *testing.T) {
		w := agentWelcome()
		w.Sudo = true
		got := w.render(noColour, 80)
		if !strings.HasPrefix(got, "  devbox-2  ·  you are the agent, an account with sudo  ·  Ubuntu 24.04\n") || strings.Contains(got, "without sudo") {
			t.Errorf("an agent with sudo is told it has none:\n%s", got)
		}
		// Root is one sudo away: no way round to explain.
		if strings.Contains(got, "Need root?") || strings.Contains(got, "--admin") || strings.Contains(got, "host.packages") || strings.Count(got, "\n") != 5 {
			t.Errorf("an agent with sudo is sent to the admin for root:\n%s", got)
		}
	})
	t.Run("the agent has no sudo", func(t *testing.T) {
		got := agentWelcome().render(noColour, 80)
		if row(got, "Need root?") != "  Need root?       from your computer: keelbox ssh devbox-2 --admin" {
			t.Errorf("no way to root is given:\n%s", got)
		}
		if !strings.Contains(got, "host.packages, then keelbox apply devbox-2\n") {
			t.Errorf("no way to install packages for good:\n%s", got)
		}
	})
	t.Run("another projects directory", func(t *testing.T) {
		w := agentWelcome()
		w.Projects = "~/src/work"
		if got := w.render(noColour, 80); row(got, "Your work") != "  Your work        ~/src/work" || strings.Contains(got, "~/projects") {
			t.Errorf("projects directory:\n%s", got)
		}
	})
	t.Run("a session other than main", func(t *testing.T) {
		w := agentWelcome()
		w.Session = "refactor"
		// The command without the note beside it: both do not fit.
		if got := w.render(noColour, 80); row(got, "Come back") != "  Come back        keelbox attach devbox-2 --session refactor" || strings.Count(got, "\n") != 7 {
			t.Errorf("coming back to session refactor:\n%s", got)
		}
		if got := w.render(noColour, 120); row(got, "Come back") != "  Come back        keelbox attach devbox-2 --session refactor   (from your computer)" {
			t.Errorf("coming back to session refactor, wide:\n%s", got)
		}
		w.Session = "main"
		if got := w.render(noColour, 80); strings.Contains(got, "--session") {
			t.Errorf("main needs no --session:\n%s", got)
		}
		w.Session = "odd; rm -rf ~"
		if got := w.render(noColour, 80); strings.Contains(got, "--session") || strings.Contains(got, "rm -rf") {
			t.Errorf("a session name keelbox attach would refuse is printed as a command:\n%s", got)
		}
	})
	t.Run("an account that is not the agent", func(t *testing.T) {
		w := agentWelcome()
		w.User, w.Agent, w.Sudo = "sam", false, true
		if got := w.render(noColour, 80); !strings.HasPrefix(got, "  devbox-2  ·  you are sam  ·  Ubuntu 24.04\n") || strings.Contains(got, "the agent") {
			t.Errorf("another account is called the agent:\n%s", got)
		}
	})
	t.Run("a system that does not say what it is", func(t *testing.T) {
		w := agentWelcome()
		w.OS = ""
		if got := w.render(noColour, 80); !strings.HasPrefix(got, "  devbox-2  ·  you are the agent, an account without sudo\n") {
			t.Errorf("no system name:\n%s", got)
		}
	})
}

// At 80 columns and wider the welcome is seven lines at most, none wider
// than the terminal, for box names up to 12 characters.
func TestWelcomeKeepsToSevenLines(t *testing.T) {
	for _, box := range []string{"a1", "acme", "devbox-2", "build-box-12"} {
		for width := 80; width <= 200; width++ {
			w := agentWelcome()
			w.Box = box
			got := w.render(noColour, width)
			lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
			if len(lines) > 7 {
				t.Fatalf("%s at %d columns: %d lines:\n%s", box, width, len(lines), got)
			}
			for _, l := range lines {
				if ui.Width(l) >= width {
					t.Fatalf("%s at %d columns: line of %d:\n%s", box, width, ui.Width(l), got)
				}
			}
		}
	}
}

// A phone: the labels move onto the lines, text wraps under them, and a
// command to type is never split over two lines.
func TestWelcomeOnANarrowTerminal(t *testing.T) {
	want := `  devbox-2 · you are the agent, an
  account without sudo · Ubuntu 24.04
  Start an agent: claude    codex
  Your work: ~/projects
  Leave: Ctrl-b then d: everything
    here keeps running
  Come back: keelbox attach devbox-2
  Need root? from your computer:
    keelbox ssh devbox-2 --admin
    packages: host.packages, then
    keelbox apply devbox-2
`
	if got := agentWelcome().render(noColour, 40); got != want {
		t.Errorf("welcome at 40 columns:\n%s\nwant:\n%s", got, want)
	}
	for width := welcomeMinCols; width < 80; width++ {
		got := agentWelcome().render(noColour, width)
		for _, l := range strings.Split(got, "\n") {
			if ui.Width(l) >= width {
				t.Errorf("at %d columns: line of %d: %q", width, ui.Width(l), l)
			}
		}
		for _, cmd := range []string{"keelbox attach devbox-2", "keelbox ssh devbox-2 --admin", "keelbox apply devbox-2", "~/projects", "Ctrl-b then d"} {
			if !strings.Contains(got, cmd) {
				t.Errorf("at %d columns: %q is missing or split:\n%s", width, cmd, got)
			}
		}
		if strings.Contains(got, welcomeTie) {
			t.Errorf("at %d columns: a tie was left in the text: %q", width, got)
		}
	}
	// A box name longer than the line wraps like any other text.
	w := agentWelcome()
	w.Box = "a-very-long-box-name-for-a-build-machine"
	if got := w.render(noColour, 36); strings.Contains(got, welcomeTie) || !strings.Contains(got, "--admin") {
		t.Errorf("a long box name on a narrow terminal:\n%q", got)
	}
}

// Colour is the terminal theme's own 16 (the pane may be light or dark)
// and never the only carrier: the text is the same without it.
func TestWelcomeInColourReadsTheSame(t *testing.T) {
	colour := ui.Style{UTF8: true, Colour: true, Depth: ui.Depth16}
	for _, width := range []int{40, 80, 120} {
		got := agentWelcome().render(colour, width)
		if ui.Strip(got) != agentWelcome().render(noColour, width) {
			t.Errorf("at %d columns the coloured text differs:\n%q", width, got)
		}
		if strings.Contains(got, "38;2;") || strings.Contains(got, "38;5;") {
			t.Errorf("at %d columns: colours beyond the theme's 16: %q", width, got)
		}
	}
	if got := agentWelcome().render(colour, 80); !strings.Contains(got, "\x1b[33mkeelbox attach devbox-2\x1b[0m") || !strings.Contains(got, "\x1b[1mdevbox-2\x1b[0m") {
		t.Errorf("commands in the accent, the box in bold: %q", got)
	}
	ascii := agentWelcome().render(ui.Style{}, 80)
	if !strings.HasPrefix(ascii, "  devbox-2  -  you are the agent, an account without sudo  -  Ubuntu 24.04\n") {
		t.Errorf("without UTF-8:\n%s", ascii)
	}
}

// stubWelcome puts the command on a terminal of the given width, as user
// on a box with the given facts.
func stubWelcome(t *testing.T, width int, terminal bool, user string, clis []string, sudoGroup bool) {
	t.Helper()
	oldTerm, oldFacts, oldUser, oldInstall := welcomeTerminal, welcomeFacts, currentUser, welcomeInstall
	t.Cleanup(func() {
		welcomeTerminal, welcomeFacts, currentUser, welcomeInstall = oldTerm, oldFacts, oldUser, oldInstall
	})
	welcomeInstall = func() string { return "" }
	welcomeTerminal = func(io.Writer) (ui.Style, int, bool) { return noColour, width, terminal }
	welcomeFacts = func() (string, []string, bool) { return "Ubuntu 24.04", clis, sudoGroup }
	currentUser = func() string { return user }
}

func TestWelcomeCommand(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("runs as a non-root account")
	}
	args := []string{"welcome", "--box", "devbox-2", "--agent", "agent", "--projects", "projects", "--session", "main"}
	run := func(args ...string) (int, string, string) {
		var out, errs bytes.Buffer
		code := Main(args, &out, &errs)
		return code, out.String(), errs.String()
	}
	t.Run("as the shell runs it", func(t *testing.T) {
		stubWelcome(t, 80, true, "agent", []string{"claude"}, false)
		code, out, errs := run(args...)
		if code != 0 || errs != "" || out != strings.Replace(agentWelcome().render(noColour, 80), "claude    codex", "claude", 1) {
			t.Errorf("exit %d, stderr %q:\n%s", code, errs, out)
		}
	})
	t.Run("the box's permissions, for the agent alone", func(t *testing.T) {
		stubWelcome(t, 80, true, "agent", nil, false)
		welcomeInstall = func() string { return "auto" }
		if _, out, _ := run(args...); !strings.Contains(out, "  Packages         keelbox install NAME: an Ubuntu package, without sudo\n") {
			t.Errorf("auto, as the agent:\n%s", out)
		}
		if _, out, _ := run(append(args, "--sudo")...); strings.Contains(out, "Packages") {
			t.Errorf("auto, with sudo:\n%s", out)
		}
		stubWelcome(t, 80, true, "sam", nil, false)
		welcomeInstall = func() string { return "auto" }
		if _, out, _ := run(args...); strings.Contains(out, "keelbox install") || !strings.Contains(out, "host.packages") {
			t.Errorf("another account is given the agent's command:\n%s", out)
		}
	})
	t.Run("the mode file as apply writes it", func(t *testing.T) {
		old := installModePath
		t.Cleanup(func() { installModePath = old })
		installModePath = filepath.Join(t.TempDir(), "mode.json")
		if got := welcomeInstallFromFile(); got != "" {
			t.Errorf("no mode file reads as %q", got)
		}
		cfg := &config.Config{Box: "devbox-2"}
		cfg.Users.Agent.Name = "agent"
		if err := os.WriteFile(installModePath, installs.ModeFile(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := welcomeInstallFromFile(); got != "auto" {
			t.Errorf("a default config's mode file reads as %q", got)
		}
	})
	t.Run("sudo from the config", func(t *testing.T) {
		stubWelcome(t, 80, true, "agent", nil, false)
		_, out, _ := run(append(args, "--sudo")...)
		if !strings.Contains(out, "an account with sudo") || strings.Contains(out, "Need root?") {
			t.Errorf("--sudo:\n%s", out)
		}
	})
	t.Run("sudo from the group, whatever the config said", func(t *testing.T) {
		stubWelcome(t, 80, true, "agent", nil, true)
		_, out, _ := run(args...)
		if !strings.Contains(out, "an account with sudo") || strings.Contains(out, "without sudo") {
			t.Errorf("an account in the sudo group is told it has no sudo:\n%s", out)
		}
	})
	t.Run("another account", func(t *testing.T) {
		stubWelcome(t, 80, true, "sam", nil, true)
		_, out, _ := run(args...)
		if !strings.Contains(out, "you are sam") || strings.Contains(out, "the agent") {
			t.Errorf("as sam:\n%s", out)
		}
	})
	t.Run("another projects directory", func(t *testing.T) {
		stubWelcome(t, 80, true, "agent", nil, false)
		_, out, _ := run("welcome", "--box", "devbox-2", "--agent", "agent", "--projects", "src/work/")
		if !strings.Contains(out, "  Your work        ~/src/work\n") {
			t.Errorf("--projects src/work/:\n%s", out)
		}
	})
	t.Run("nothing without a terminal", func(t *testing.T) {
		stubWelcome(t, 0, false, "agent", []string{"claude"}, false)
		if code, out, errs := run(args...); code != 0 || out != "" || errs != "" {
			t.Errorf("exit %d, stdout %q, stderr %q", code, out, errs)
		}
		// The real check, on a buffer: not a terminal.
		welcomeTerminal = func(w io.Writer) (ui.Style, int, bool) { return ui.Style{}, 0, false }
		if code, out, _ := run(args...); code != 0 || out != "" {
			t.Errorf("exit %d, stdout %q", code, out)
		}
	})
	t.Run("nothing on a terminal too narrow to lay it out", func(t *testing.T) {
		stubWelcome(t, welcomeMinCols-1, true, "agent", []string{"claude"}, false)
		if code, out, errs := run(args...); code != 0 || out != "" || errs != "" {
			t.Errorf("exit %d, stdout %q, stderr %q", code, out, errs)
		}
	})
	t.Run("without the box's name, or with a flag it does not know", func(t *testing.T) {
		stubWelcome(t, 80, true, "agent", nil, false)
		for _, bad := range [][]string{{"welcome"}, {"welcome", "--box", "devbox-2", "--colour"}, {"welcome", "--box", "devbox-2", "extra"}} {
			code, out, errs := run(bad...)
			if code != 2 || out != "" || !strings.HasSuffix(errs, "keelbox attach BOX\n") {
				t.Errorf("%v: exit %d, stdout %q, stderr %q", bad, code, out, errs)
			}
		}
	})
}

// The real terminal check: a buffer, a file and a pipe are not terminals.
func TestWelcomeTerminalIsARealOne(t *testing.T) {
	if _, _, ok := welcomeTerminal(&bytes.Buffer{}); ok {
		t.Error("a buffer is taken for a terminal")
	}
	f, err := os.Create(t.TempDir() + "/out")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, _, ok := welcomeTerminal(f); ok {
		t.Error("a file is taken for a terminal")
	}
	if code := cmdWelcome([]string{"--box", "devbox-2"}, f, io.Discard); code != 0 {
		t.Errorf("exit %d without a terminal", code)
	}
	if st, _ := f.Stat(); st.Size() != 0 {
		t.Error("something was written without a terminal")
	}
}

// Nobody types it: it is not in help, in the command list or in what the
// shell completes.
func TestWelcomeIsNotInHelp(t *testing.T) {
	if _, ok := findCmd("welcome"); ok {
		t.Error("welcome is a documented command")
	}
	if strings.Contains(overview(), "welcome") {
		t.Errorf("keelbox help lists welcome:\n%s", overview())
	}
	for _, n := range commandNames() {
		if n == "welcome" {
			t.Error("welcome is among the command names")
		}
	}
}
