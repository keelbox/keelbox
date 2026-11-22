package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/ui"
)

// A box whose cloud-config keelbox up printed, and whose server is yet to
// be created, is known to every command: each answers that it is not set
// up yet and names keelbox up, named or as the only box there is.
func TestEveryCommandKnowsABoxThatWaitsForItsServer(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	waiting(t)
	const want = "keelbox: web is not set up yet (waiting for the server): keelbox up web\n"
	for _, cmd := range []string{"status", "attach", "ssh", "sessions", "doctor", "plan", "apply", "login", "backup", "destroy", "rollback", "reports"} {
		for _, args := range [][]string{{cmd, "web"}, {cmd}} {
			if code, out, errs := run(args...); code != 2 || errs != want {
				t.Errorf("keelbox %s: exit %d\n%s%s", strings.Join(args, " "), code, out, errs)
			}
		}
	}
	if _, out, _ := run("ls"); !strings.Contains(out, "waiting for the server: keelbox up web\n") {
		t.Errorf("keelbox ls:\n%s", out)
	}
}

// Stopping at the question for the server's address, the moment to go and
// create the server, says that the box is saved and waits, where its
// cloud-config is, and the command that asks again.
func TestUpStoppedWhileWaitingSaysTheBoxIsSaved(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	waiting(t)
	const saved = "stopped; web is saved and waits for its server. Its cloud-config is in ~/.config/keelbox/boxes/web.cloud-config. Continue: keelbox up web\n"

	// Ctrl-D: the input ends at the question.
	code, out := upWith(t, &fakeRunner{}, script(), "up", "web")
	if code != 1 || !strings.HasSuffix(out, "keelbox up: "+saved) || strings.Contains(out, "Start again") {
		t.Errorf("Ctrl-D: exit %d\n%s", code, out)
	}

	// Ctrl-C: the command's context is cancelled while the question waits.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o, pos, err := parse("up", []string{"web"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	code = cmdUp(ctx, o, pos, prompt.New(blocked{}, &b, true), &fakeRunner{}, &b, &b)
	if code != exitInterrupted || !strings.HasSuffix(b.String(), "keelbox up: "+saved) || strings.Contains(b.String(), "Start again") {
		t.Errorf("Ctrl-C: exit %d\n%s", code, b.String())
	}
	if !isKnown("web") {
		t.Error("the box is gone")
	}

	// On a terminal it is the Stopped block, with the box's name as it is.
	stderrAs(t, ui.Style{Live: true, UTF8: true, Width: 100})
	b.Reset()
	cmdUp(ctx, o, pos, prompt.New(blocked{}, &b, true), &fakeRunner{}, &b, &b)
	want := "\n  - Stopped\n    web is saved and waits for its server. Its cloud-config is in\n    ~/.config/keelbox/boxes/web.cloud-config.\n    → keelbox up web\n"
	if !strings.HasSuffix(b.String(), want) {
		t.Errorf("on a terminal:\n%s\nwant it to end with:\n%s", b.String(), want)
	}
}

// The instructions for creating the server are a card that wraps to the
// terminal, at every width and in plain text, with the command that prints
// the cloud-config again whole on one line.
func TestCloudConfigInstructionsWrapToTheTerminal(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	waiting(t)
	for _, w := range []int{60, 80, 100, 120} {
		for name, st := range map[string]ui.Style{
			"drawn": {Live: true, UTF8: true, Width: w},
			"plain": {Width: w},
		} {
			var b bytes.Buffer
			if err := (&wizard{st: st, out: &b}).showCloudConfig("web"); err != nil {
				t.Fatal(err)
			}
			card, snippet, ok := strings.Cut(b.String(), "#cloud-config\n")
			if !ok || !strings.Contains(snippet, "ssh_authorized_keys:") {
				t.Fatalf("%d %s: no cloud-config after the card:\n%s", w, name, b.String())
			}
			copyLine := false
			for _, l := range strings.Split(card, "\n") {
				copyLine = copyLine || strings.Contains(l, "cat ~/.config/keelbox/boxes/web.cloud-config")
				if ui.Width(l) >= w {
					t.Errorf("%d %s: a line is %d columns: %q", w, name, ui.Width(l), l)
				}
			}
			if !copyLine {
				t.Errorf("%d %s: the command that prints it again is not whole on one line:\n%s", w, name, card)
			}
			words := strings.Join(strings.Fields(strings.NewReplacer("│", " ").Replace(card)), " ")
			for _, sentence := range []string{
				"Create an Ubuntu 24.04 (or newer) server with your provider.",
				`Paste the cloud-config below into its "Cloud config" or "User data" field (Hetzner: Cloud config; DigitalOcean, Vultr, AWS: User data).`,
				"Add no SSH key there: the cloud-config installs yours.",
			} {
				if !strings.Contains(words, sentence) {
					t.Errorf("%d %s: a word is broken in %q:\n%s", w, name, sentence, card)
				}
			}
		}
	}
}
