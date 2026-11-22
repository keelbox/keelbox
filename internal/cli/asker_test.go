package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/ui"
)

// No command builds a Prompter of its own: a question asked through one
// that asker.go did not build would not end at Ctrl-C, and a second Ctrl-C
// would leave the terminal without echo and without a cursor.
func TestQuestionsAreBuiltInOnePlace(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatal(err, files)
	}
	for _, f := range files {
		if f == "asker.go" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, l := range strings.Split(string(data), "\n") {
			if strings.Contains(l, "prompt.Terminal(") || strings.Contains(l, "prompt.New(") {
				t.Errorf("%s:%d builds a Prompter; use terminalPrompt (or noPrompt) from asker.go: %s", f, i+1, strings.TrimSpace(l))
			}
		}
	}
	for _, dir := range []string{"../provider", "../modules", "../engine"} {
		_ = filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
			if err != nil || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			if data, _ := os.ReadFile(p); strings.Contains(string(data), "internal/prompt\"") {
				t.Errorf("%s asks questions; they are asked in internal/cli, through asker.go", p)
			}
			return nil
		})
	}
}

// stopped is a Prompter as terminalPrompt builds it, at which Ctrl-C has
// been pressed: every question ends at once.
func stopped(out *bytes.Buffer) *prompt.Prompter {
	done := make(chan struct{})
	close(done)
	return prompt.New(blocked{}, out, true).OnInterrupt(done).OnStop(func() { stoppedAtQuestion.Store(true) })
}

// blocked is a terminal nobody types at.
type blocked struct{}

func (blocked) Read([]byte) (int, error) { select {} }

// Ctrl-C at a question outside keelbox up ends the command with exit 130
// and one line that nothing changed, on a terminal and off one.
func TestCtrlCAtAQuestionSaysNothingChangedOnce(t *testing.T) {
	isolate(t)
	register(t, config.Starter{Box: "alpha", Provider: config.ProviderSSH, SSHHost: "203.0.113.7", Keys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"}})
	for name, c := range map[string]struct {
		st   ui.Style
		want string
	}{
		"plain":    {ui.Style{}, "keelbox: stopped; nothing changed\n"},
		"terminal": {ui.Style{Live: true, UTF8: true, Width: 80}, "  - Stopped. Nothing changed.\n"},
	} {
		stderrAs(t, c.st)
		stoppedAtQuestion.Store(false)
		stopSaid.Store(false)
		var out, errOut bytes.Buffer
		code := cmdForget(options{}, []string{"alpha"}, stopped(&out), &out, &errOut)
		code = stoppedExit("forget", code, &errOut)
		if code != exitInterrupted || errOut.String() != c.want || strings.Contains(out.String(), "Nothing changed") {
			t.Errorf("%s: exit %d\nstderr %q\nstdout %q", name, code, errOut.String(), out.String())
		}
		if !isRegistered("alpha") {
			t.Errorf("%s: the box was forgotten", name)
		}
	}
	// A command that was not stopped at a question keeps its exit code,
	// and one that says what the stop means is not spoken over.
	stoppedAtQuestion.Store(false)
	var errOut bytes.Buffer
	if code := stoppedExit("forget", 1, &errOut); code != 1 || errOut.Len() != 0 {
		t.Errorf("not stopped: exit %d %q", code, errOut.String())
	}
	stoppedAtQuestion.Store(true)
	stopSaid.Store(true)
	if code := stoppedExit("up", exitNeedsYou, &errOut); code != exitNeedsYou || errOut.Len() != 0 {
		t.Errorf("keelbox up: exit %d %q", code, errOut.String())
	}
	stoppedAtQuestion.Store(false)
	stopSaid.Store(false)
}

// The question at the first connection to a server shows its host keys in
// a card, a key a row, with the command that prints them on the server,
// and asks through the prompt package, No by default.
func TestHostKeyQuestionIsACard(t *testing.T) {
	keys := provider.ParseHostKeys(offeredHostKeys)
	if len(keys) != 3 || !strings.HasPrefix(keys[2].Fingerprint(), "SHA256:") {
		t.Fatalf("test host keys: %v", keys)
	}
	for name, c := range map[string]struct {
		input string
		want  bool
	}{"Enter": {"\n", false}, "no": {"n\n", false}, "yes": {"y\n", true}} {
		var out bytes.Buffer
		p := prompt.New(strings.NewReader(c.input), &out, true)
		if got := confirmHostKeys(p, &out, "203.0.113.7", keys); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
		wantAll(t, name, out.String(), "First connection to 203.0.113.7:\n", "  ssh-rsa              "+keys[0].Fingerprint()+"\n", "  ssh-ed25519          "+keys[2].Fingerprint()+"\n",
			"ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub\n", "Trust and pin these host keys? [y/N] ")
	}
	for _, w := range []int{60, 80, 100, 120} {
		var out bytes.Buffer
		st := ui.Style{Live: true, UTF8: true, Width: w}
		confirmHostKeys(prompt.New(strings.NewReader("\n"), &out, true).WithStyle(st), &out, "203.0.113.7", keys)
		if !strings.Contains(out.String(), "╭─ First connection to 203.0.113.7 ") || !strings.Contains(out.String(), keys[2].Fingerprint()) {
			t.Errorf("%d: no card, or the fingerprint is broken:\n%s", w, out.String())
		}
		for _, l := range strings.Split(out.String(), "\n") {
			if ui.Width(l) >= w {
				t.Errorf("%d: a line is %d columns: %q", w, ui.Width(l), l)
			}
		}
	}
}

// offeredHostKeys are three host keys as ssh-keyscan prints them.
const offeredHostKeys = "203.0.113.10 ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAAgQC7\n" +
	"203.0.113.10 ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBA==\n" +
	"203.0.113.10 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGgB1lM4n8Rr0Uu2XqYt5v3ZcKpJ7dWfHsLaNbEoGiVx\n"

// A no at keelbox up's go-ahead says that nothing changed and that the
// answers are kept, as one block on a terminal with the command that
// continues; off a terminal it is the line it has always been.
func TestUpDeclinedSaysNothingChanged(t *testing.T) {
	var b bytes.Buffer
	(&wizard{st: ui.Style{}, out: &b}).declinedGoAhead("devbox", false, false)
	if got := b.String(); got != "keelbox up: nothing changed. Your answers are kept; continue: keelbox up devbox\n" {
		t.Errorf("plain: %q", got)
	}
	b.Reset()
	(&wizard{st: ui.Style{Live: true, UTF8: true, Width: 100}, out: &b}).declinedGoAhead("devbox", false, true)
	want := "\n  - Nothing changed\n    Your answers are kept.\n    → keelbox up devbox\n  • The Hetzner token stays stored for next time; remove it: keelbox forget-token\n"
	if b.String() != want {
		t.Errorf("on a terminal:\n%q\nwant\n%q", b.String(), want)
	}
}
