package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/ui"
)

// threeBoxes is a registry of acme (reached over SSH, set up), lab (on
// Hetzner, keelbox up not finished) and old (on Hetzner, its server
// destroyed), with nothing recorded as last used.
func threeBoxes(t *testing.T) {
	t.Helper()
	isolate(t)
	t.Setenv("SUDO_UID", "")
	register(t, config.StarterFor("acme", "ssh"))
	register(t, config.StarterFor("lab", "hetzner"))
	register(t, config.StarterFor("old", "hetzner"))
	for box, st := range map[string]upState{
		"acme": {Via: viaSSH, Stage: stageReady, Address: "203.0.113.10"},
		"lab":  {Via: viaHetzner, Stage: stageApplied, Address: "192.0.2.7"},
	} {
		if err := saveUpState(box, st); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveDestroyedMark("old", destroyedMark{At: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

// whichBox resolves a command that names no box on a terminal where input
// is typed, and returns the box picked and what the terminal showed.
func whichBox(t *testing.T, input string) (string, string, error) {
	t.Helper()
	var shown bytes.Buffer
	path, err := resolveConfig("attach", nil, prompt.New(strings.NewReader(input), &shown, true))
	for _, b := range registeredBoxes() {
		if path == boxConfigPath(b) {
			return b, shown.String(), err
		}
	}
	return path, shown.String(), err
}

// typing puts a person at the terminal for "Which box?" as Main asks it.
func typing(t *testing.T, input string) *bytes.Buffer {
	t.Helper()
	var shown bytes.Buffer
	old := boxPrompt
	boxPrompt = func(context.Context, io.Writer, ui.Style) *prompt.Prompter {
		return prompt.New(strings.NewReader(input), &shown, true)
	}
	t.Cleanup(func() { boxPrompt = old })
	return &shown
}

// On a terminal a command that names no box shows the boxes, each with
// what this computer knows about it, and takes Enter, a number or a name.
func TestWhichBoxMenu(t *testing.T) {
	threeBoxes(t)
	api, _ := fakeHetzner(t)
	const menu = "Which box?\n" +
		"  1) acme  ssh, 203.0.113.10\n" +
		"  2) lab   hetzner, 192.0.2.7, needs sign-in\n" +
		"  3) old   hetzner, server destroyed\n"
	box, shown, err := whichBox(t, "\n")
	if err != nil || box != "acme" || shown != menu+"Choose 1-3 [1]: " {
		t.Errorf("Enter: %q %v\n%s", box, err, shown)
	}
	if box, _, err := whichBox(t, "2\n"); err != nil || box != "lab" {
		t.Errorf("by number: %q %v", box, err)
	}
	if box, _, err := whichBox(t, "old\n"); err != nil || box != "old" {
		t.Errorf("by name: %q %v", box, err)
	}
	if calls := api.Calls(); len(calls) != 0 {
		t.Errorf("the menu asked the cloud: %v", calls)
	}

	// No answer that names a box: the list, and how to name one.
	const nameOne = "3 boxes (acme, lab, old (server destroyed)); name one: keelbox attach acme"
	for _, input := range []string{"", "9\nnope\n0\n"} {
		if box, _, err := whichBox(t, input); err == nil || err.Error() != nameOne || box != "" {
			t.Errorf("after %q: %q %v", input, box, err)
		}
	}
	// Ctrl-C at the question is not an answer either, and is told apart.
	stopped := make(chan struct{})
	close(stopped)
	waiting, _ := io.Pipe()
	_, err = resolveConfig("attach", nil, prompt.New(waiting, io.Discard, true).OnInterrupt(stopped))
	if !errors.Is(err, prompt.ErrInterrupted) {
		t.Errorf("Ctrl-C: %v", err)
	}
}

// The box offered first is the one a command last used, and never one
// whose server is destroyed.
func TestWhichBoxOffersTheBoxLastUsed(t *testing.T) {
	threeBoxes(t)
	noteBoxUsed("lab")
	if box, shown, err := whichBox(t, "\n"); err != nil || box != "lab" || !strings.HasSuffix(shown, "Choose 1-3 [2]: ") {
		t.Errorf("last used lab: %q %v\n%s", box, err, shown)
	}
	noteBoxUsed("old")
	if box, shown, err := whichBox(t, "\n"); err != nil || box != "acme" || !strings.HasSuffix(shown, "Choose 1-3 [1]: ") {
		t.Errorf("last used old, destroyed: %q %v\n%s", box, err, shown)
	}
	// Nothing set up: the first box that has a server.
	forgetUpState("acme")
	if err := saveDestroyedMark("lab", destroyedMark{At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := saveUpState("acme", upState{Via: viaSSH, Stage: stageApplied}); err != nil {
		t.Fatal(err)
	}
	if box, _, err := whichBox(t, "\n"); err != nil || box != "acme" {
		t.Errorf("one box with a server: %q %v", box, err)
	}
}

// A command records the box it used, keelbox up included, and keelbox
// forget drops the record with the box.
func TestCommandsRecordTheBoxUsed(t *testing.T) {
	isolate(t)
	t.Setenv("SUDO_UID", "")
	register(t, config.StarterFor("home", "local"))
	register(t, config.StarterFor("nas", "local"))
	typing(t, "nas\n")
	// create on a machine you have stops with a line that names the box.
	code, _, errs := run("create")
	if code != 2 || !strings.Contains(errs, "nas is provider local") || lastBox() != "nas" {
		t.Errorf("create, nas picked: exit %d, last used %q\n%s", code, lastBox(), errs)
	}
	if code, _, errs := run("destroy", "home"); code != 2 || !strings.Contains(errs, "home is a machine you have") || lastBox() != "home" {
		t.Errorf("destroy home: exit %d, last used %q\n%s", code, lastBox(), errs)
	}
	shown := typing(t, "\n")
	if code, _, errs := run("resize"); code != 2 || !strings.Contains(errs, "home is a machine you have") || !strings.HasSuffix(shown.String(), "Choose 1-2 [1]: ") {
		t.Errorf("resize, Enter: exit %d\n%s%s", code, shown.String(), errs)
	}
	// A config file of the user's own is not a box in the registry, even
	// under a registered name.
	if err := os.WriteFile("mine.yaml", []byte(config.Example("nas")), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("create", "mine.yaml"); code != 2 || !strings.Contains(errs, "nas is provider local") || lastBox() != "home" {
		t.Errorf("create with a config file: exit %d, last used %q\n%s", code, lastBox(), errs)
	}
	if code, _, _ := run("create", "nas"); code != 2 || lastBox() != "nas" {
		t.Errorf("create nas: exit %d, last used %q", code, lastBox())
	}
	if code, out, errs := run("forget", "nas", "--yes"); code != 0 || lastBox() != "" {
		t.Errorf("forget nas: exit %d, last used %q\n%s%s", code, lastBox(), out, errs)
	}

	// keelbox up on a box that is set up.
	if err := saveUpState("home", upState{Via: viaLocal, Stage: stageReady}); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := run("up", "home"); code != 0 || lastBox() != "home" {
		t.Errorf("up home: exit %d, last used %q\n%s%s", code, lastBox(), out, errs)
	}
}

// Without a terminal nothing is asked: exit 2 and the list, as before, with
// an example that is not a box whose server is destroyed.
func TestSeveralBoxesWithoutATerminal(t *testing.T) {
	isolate(t)
	t.Setenv("SUDO_UID", "")
	onTerminal(t, false)
	register(t, config.StarterFor("devbox", "hetzner"))
	register(t, config.StarterFor("devbox-2", "hetzner"))
	if code, out, errs := run("attach"); code != 2 || out != "" || errs != "keelbox: 2 boxes (devbox, devbox-2); name one: keelbox attach devbox\n" {
		t.Errorf("two boxes: exit %d\n%s%s", code, out, errs)
	}
	// devbox was the box last used, and then its server was destroyed.
	noteBoxUsed("devbox")
	if err := saveDestroyedMark("devbox", destroyedMark{At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{"attach", "status", "apply", "destroy"} {
		if code, out, errs := run(cmd); code != 2 || out != "" || errs != "keelbox: 2 boxes (devbox (server destroyed), devbox-2); name one: keelbox "+cmd+" devbox-2\n" {
			t.Errorf("%s, devbox destroyed: exit %d\n%s%s", cmd, code, out, errs)
		}
	}
	if err := saveDestroyedMark("devbox-2", destroyedMark{At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("attach"); code != 2 || errs != "keelbox: 2 boxes (devbox (server destroyed), devbox-2 (server destroyed)), and the server of each is destroyed; create one again: keelbox up devbox\n" {
		t.Errorf("both destroyed: exit %d\n%s", code, errs)
	}
}
