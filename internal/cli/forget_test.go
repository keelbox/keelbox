package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/ui"
)

// forget runs keelbox forget on a terminal where input is typed.
func forget(t *testing.T, st ui.Style, box, input string, o options) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	o.style = st
	code := cmdForget(o, []string{box}, prompt.New(strings.NewReader(input), &out, true).WithStyle(st), &out, &errOut)
	return code, out.String(), errOut.String()
}

// gammaBox registers gamma on Hetzner, as far as keelbox up got with it.
func gammaBox(t *testing.T, st upState) {
	t.Helper()
	isolate(t)
	register(t, config.StarterFor("gamma", "hetzner"))
	if st.Stage != "" {
		if err := saveUpState("gamma", st); err != nil {
			t.Fatal(err)
		}
	}
}

// A cloud box whose server may exist: forget says in a card what it
// removes, that the server stays billed and that keelbox destroy has to
// come first, and asks with a menu whose default keeps the box. What it
// says after forgetting names no command that has stopped working.
func TestForgetCloudBoxKeepsByDefault(t *testing.T) {
	gammaBox(t, upState{Via: viaHetzner, Stage: stageReady, Address: "203.0.113.9"})
	code, out, _ := forget(t, ui.Style{}, "gamma", "\n", options{})
	if code != exitDeclined || !isRegistered("gamma") {
		t.Fatalf("Enter: exit %d, registered %v\n%s", code, isRegistered("gamma"), out)
	}
	wantAll(t, "Enter keeps the box", out,
		"Forget gamma:\n",
		"removed here  ~/.config/keelbox/boxes/gamma.yaml",
		"still billed  The Hetzner server keeps running and is billed until it is deleted",
		"to delete it  keelbox destroy gamma first: forget removes what destroy needs to find it. After forget, only the Hetzner Console deletes it",
		"Forget gamma and leave what is billed in the cloud?\n  1) Keep gamma    nothing changes\n  2) Forget gamma  what is billed stays until you delete it in the Hetzner Console\nChoose 1-2 [1]: ",
		"Nothing changed.\n")

	code, out, _ = forget(t, ui.Style{}, "gamma", "2\n", options{})
	const after = "Forgot gamma. The Hetzner server keeps running and is billed until it is deleted in the Hetzner Console: keelbox no longer manages it.\n"
	if code != 0 || isKnown("gamma") || !strings.HasSuffix(out, after) {
		t.Errorf("forget: exit %d, known %v\n%s", code, isKnown("gamma"), out)
	}
	if strings.Contains(out, "keelbox destroy gamma deletes it") {
		t.Errorf("names a command that no longer finds the box:\n%s", out)
	}
}

// Without a terminal forget still refuses without --yes, and says that the
// server stays billed and which command deletes it first; with --yes it
// forgets and says where the server is deleted now.
func TestForgetCloudBoxWithoutATerminal(t *testing.T) {
	gammaBox(t, upState{Via: viaHetzner, Stage: stageReady, Address: "203.0.113.9"})
	code, _, errs := run("forget", "gamma")
	if code != 2 || !isRegistered("gamma") {
		t.Fatalf("exit %d\n%s", code, errs)
	}
	wantAll(t, "no terminal", errs, "the Hetzner server keeps running and is billed until it is deleted",
		"Delete it first: keelbox destroy gamma;", "without a terminal: keelbox forget gamma --yes\n")
	code, out, errs := run("forget", "gamma", "--yes")
	if code != 0 || isKnown("gamma") || out != "Forgot gamma. The Hetzner server keeps running and is billed until it is deleted in the Hetzner Console: keelbox no longer manages it.\n" {
		t.Errorf("--yes: exit %d\n%s%s", code, out, errs)
	}
}

// Where this computer knows there is no server, forget says so and asks
// its plain question: keelbox up stopped before it created one, or keelbox
// destroy deleted it.
func TestForgetCloudBoxWithoutAServer(t *testing.T) {
	gammaBox(t, upState{Via: viaHetzner, Stage: stageConfigured})
	code, out, _ := forget(t, ui.Style{}, "gamma", "y\n", options{})
	if code != 0 || isKnown("gamma") {
		t.Fatalf("never created: exit %d\n%s", code, out)
	}
	wantAll(t, "never created", out, "the server    none: keelbox up stopped before it created one\n", "Forget gamma? [y/N] ", "Forgot gamma. No server was created for it.\n")
	if strings.Contains(out, "billed") || strings.Contains(out, "Keep gamma") {
		t.Errorf("never created: a billing warning for a server that does not exist:\n%s", out)
	}

	gammaBox(t, upState{})
	if err := saveDestroyedMark("gamma", destroyedMark{At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	code, out, _ = forget(t, ui.Style{}, "gamma", "\n", options{})
	if code != exitDeclined || !isRegistered("gamma") {
		t.Fatalf("destroyed, Enter: exit %d\n%s", code, out)
	}
	wantAll(t, "destroyed", out, "the server    none: keelbox destroy deleted it on "+day(time.Now())+"\n", "Forget gamma? [y/N] ", "Nothing changed.\n")
}

// A box that waits for its server has no config: forget names the files it
// has, the kept answers and the cloud-config.
func TestForgetABoxThatWaitsForItsServer(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	waiting(t)
	code, out, _ := forget(t, ui.Style{}, "web", "y\n", options{})
	if code != 0 || isKnown("web") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	wantAll(t, "waiting", out, "~/.config/keelbox/boxes/web.up.json", "~/.config/keelbox/boxes/web.cloud-config", "keelbox created none",
		"Forgot web. A server you created from its cloud-config is not touched.\n")
	if strings.Contains(out, "web.yaml") {
		t.Errorf("names a config that does not exist:\n%s", out)
	}
}

// The card fits the terminal at every width, with the command to copy
// whole on one line.
func TestForgetCardFitsTheTerminal(t *testing.T) {
	for _, w := range []int{60, 80, 100, 120} {
		gammaBox(t, upState{Via: viaHetzner, Stage: stageReady, Address: "203.0.113.9"})
		_, out, _ := forget(t, ui.Style{Live: true, UTF8: true, Width: w}, "gamma", "\n", options{})
		card, _, _ := strings.Cut(out, "Forget gamma and leave")
		whole := false
		for _, l := range strings.Split(card, "\n") {
			whole = whole || strings.Contains(l, "keelbox destroy gamma")
			if ui.Width(l) >= w {
				t.Errorf("%d: a line is %d columns: %q", w, ui.Width(l), l)
			}
		}
		if !whole || !strings.Contains(card, "╭─ Forget gamma ") {
			t.Errorf("%d: the card, or the command on one line:\n%s", w, card)
		}
	}
}

// forget's help says what forget does not do and what that means for the
// order of the two commands, as a fact.
func TestForgetHelpSaysWhatItLeaves(t *testing.T) {
	c, _ := findCmd("forget")
	help := strings.Join(strings.Fields(commandHelp(c)), " ")
	want := "A cloud server is not deleted, and forget removes what keelbox destroy needs to find it, so destroy comes before forget where the server is to go."
	if !strings.Contains(help, want) {
		t.Errorf("keelbox forget --help lacks %q:\n%s", want, commandHelp(c))
	}
}
