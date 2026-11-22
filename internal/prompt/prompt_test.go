package prompt

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/ui"
)

func scripted(input string) (*Prompter, *bytes.Buffer) {
	var out bytes.Buffer
	return New(strings.NewReader(input), &out, true), &out
}

func TestAskDefaultAndValidation(t *testing.T) {
	p, out := scripted("\nBad Name\nacme\n")
	got, err := p.Ask("Box name", "my-box")
	if err != nil || got != "my-box" {
		t.Fatalf("default: %q %v", got, err)
	}
	got, err = p.AskValid("Box name", "", func(s string) error {
		if strings.ContainsAny(s, " ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			return errors.New("lowercase letters, digits and hyphens")
		}
		return nil
	})
	if err != nil || got != "acme" {
		t.Fatalf("re-ask: %q %v", got, err)
	}
	if !strings.Contains(out.String(), "Box name [my-box]: ") || !strings.Contains(out.String(), "  lowercase letters") {
		t.Errorf("output:\n%s", out)
	}
}

func TestAskGivesUpAfterThreeBadAnswers(t *testing.T) {
	p, _ := scripted("x\nx\nx\nfine\n")
	if _, err := p.AskValid("Q", "", func(string) error { return errors.New("no") }); err == nil {
		t.Fatal("accepted after three refusals")
	}
}

func TestConfirm(t *testing.T) {
	p, out := scripted("\nmaybe\nn\nYES\n")
	for i, want := range []bool{true, false, true} {
		got, err := p.Confirm("Go on?", true)
		if err != nil || got != want {
			t.Fatalf("answer %d: %v %v", i, got, err)
		}
	}
	if !strings.Contains(out.String(), "[Y/n]") || !strings.Contains(out.String(), "answer y or n") {
		t.Errorf("output:\n%s", out)
	}
}

func TestChooseByNumberKeyOrDefault(t *testing.T) {
	cs := []Choice{{Key: "hetzner", Label: "Hetzner", Detail: "new server"}, {Key: "ssh", Label: "SSH"}, {Label: "Here"}}
	p, out := scripted("\n3\nSSH\n7\n2\n")
	for i, want := range []int{1, 2, 1, 1} {
		got, err := p.Choose("Where?", cs, 1)
		if err != nil || got != want {
			t.Fatalf("answer %d: %d %v", i, got, err)
		}
	}
	o := out.String()
	for _, w := range []string{"  1) Hetzner  new server\n", "  2) SSH\n", "Choose 1-3 [2]: ", "type a number from 1 to 3"} {
		if !strings.Contains(o, w) {
			t.Errorf("output lacks %q:\n%s", w, o)
		}
	}
}

func TestSecretIsNotEchoedByThePrompter(t *testing.T) {
	p, out := scripted("\nhunter2-token\n")
	got, err := p.Secret("Hetzner API token")
	if err != nil || got != "hunter2-token" {
		t.Fatalf("%q %v", got, err)
	}
	if strings.Contains(out.String(), "hunter2") {
		t.Errorf("secret written to output:\n%s", out)
	}
	if !strings.Contains(out.String(), "(input hidden)") || !strings.Contains(out.String(), "nothing was entered") {
		t.Errorf("output:\n%s", out)
	}
}

func TestNoTerminalNeverReads(t *testing.T) {
	var out bytes.Buffer
	p := New(strings.NewReader("yes\n"), &out, false)
	if _, err := p.Ask("Q", "d"); !errors.Is(err, ErrNoTerminal) {
		t.Errorf("ask: %v", err)
	}
	if _, err := p.Confirm("Q", true); !errors.Is(err, ErrNoTerminal) {
		t.Errorf("confirm: %v", err)
	}
	if _, err := p.Choose("Q", []Choice{{Label: "a"}}, 0); !errors.Is(err, ErrNoTerminal) {
		t.Errorf("choose: %v", err)
	}
	if _, err := p.Secret("Q"); !errors.Is(err, ErrNoTerminal) {
		t.Errorf("secret: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("wrote %q", out.String())
	}
}

func TestEndOfInputIsNoAnswer(t *testing.T) {
	p, _ := scripted("")
	if _, err := p.Ask("Q", ""); !errors.Is(err, ErrNoAnswer) {
		t.Errorf("%v", err)
	}
	p, _ = scripted("last line without newline")
	if got, err := p.Ask("Q", ""); err != nil || got != "last line without newline" {
		t.Errorf("%q %v", got, err)
	}
}

func TestInterruptEndsAQuestion(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	intr := make(chan struct{})
	var out bytes.Buffer
	p := New(r, &out, true).OnInterrupt(intr)
	go func() { time.Sleep(50 * time.Millisecond); close(intr) }()
	if _, err := p.Ask("Box name", "devbox"); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("got %v, want ErrInterrupted", err)
	}
}

func TestAskOptionalWaitAndWideChoices(t *testing.T) {
	var out strings.Builder
	p := New(strings.NewReader("\nbad\nok\n\n"), &out, true)
	if a, err := p.AskOptional("Fingerprint", func(s string) error { return nil }); a != "" || err != nil {
		t.Errorf("Enter: %q %v", a, err)
	}
	valid := func(s string) error {
		if s != "ok" {
			return errors.New("not ok")
		}
		return nil
	}
	if a, err := p.AskOptional("Fingerprint", valid); a != "ok" || err != nil || !strings.Contains(out.String(), "Fingerprint [Enter: skip]: ") || !strings.Contains(out.String(), "not ok") {
		t.Errorf("%q %v\n%s", a, err, out.String())
	}
	if err := p.Wait("Press Enter."); err != nil {
		t.Error(err)
	}
	var choices []Choice
	for i := 0; i < 10; i++ {
		choices = append(choices, Choice{Label: fmt.Sprintf("c%d", i), Detail: "d"})
	}
	out.Reset()
	_, _ = New(strings.NewReader("\n"), &out, true).Choose("Size:", choices, 0)
	if !strings.Contains(out.String(), "   1) c0  d\n") || !strings.Contains(out.String(), "  10) c9  d\n") {
		t.Errorf("numbers not aligned:\n%s", out.String())
	}
}

// A question ended by Ctrl-C tells the command so (OnStop), and while a
// question waits Waiting reports it: the Ctrl-C is then about the
// question, not about a step that runs.
func TestInterruptedQuestionTellsTheCommand(t *testing.T) {
	r, w := io.Pipe()
	defer w.Close()
	intr := make(chan struct{})
	told := 0
	var out bytes.Buffer
	p := New(r, &out, true).OnInterrupt(intr).OnStop(func() { told++ })
	go func() {
		for !Waiting() {
			time.Sleep(time.Millisecond)
		}
		close(intr)
	}()
	if _, err := p.Confirm("Forget alpha?", false); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("got %v", err)
	}
	if told != 1 {
		t.Errorf("OnStop called %d times", told)
	}
	if Waiting() {
		t.Error("still waiting after the question ended")
	}
}

// On a terminal that takes plain lines (TERM=dumb, a screen reader) a
// question and its choices wrap to the width; off one they are one line.
func TestPlainQuestionsWrapToTheTerminal(t *testing.T) {
	const q = "Install the GitHub CLI (gh), so the agent can sign in to GitHub for git push and pull requests?"
	for _, w := range []int{60, 80, 100, 120} {
		var out bytes.Buffer
		p := New(strings.NewReader("y\n2\n"), &out, true).WithStyle(ui.Style{Width: w})
		if ok, err := p.Confirm(q, true); err != nil || !ok {
			t.Fatalf("%d: %v %v", w, ok, err)
		}
		out.WriteString("\n") // where a terminal echoes the typed answer and Enter
		if _, err := p.Choose("Where should the box run?", where, 0); err != nil {
			t.Fatal(err)
		}
		for _, l := range strings.Split(out.String(), "\n") {
			if len([]rune(l)) > w {
				t.Errorf("%d: a line is %d columns: %q", w, len([]rune(l)), l)
			}
		}
		if !strings.Contains(strings.Join(strings.Fields(out.String()), " "), q+" [Y/n]") {
			t.Errorf("%d: the question changed:\n%s", w, out.String())
		}
	}
	var out bytes.Buffer
	p := New(strings.NewReader("y\n"), &out, true)
	_, _ = p.Confirm(q, true)
	if out.String() != q+" [Y/n] " {
		t.Errorf("off a terminal: %q", out.String())
	}
}

// AskTyped returns what was typed as it is, an empty line included: the
// caller compares it with the name it asked for.
func TestAskTyped(t *testing.T) {
	p, out := scripted("acme\n\n")
	for _, want := range []string{"acme", ""} {
		got, err := p.AskTyped("Delete server 42 and everything on it? Type the box name to confirm")
		if err != nil || got != want {
			t.Errorf("got %q %v, want %q", got, err, want)
		}
	}
	if !strings.HasPrefix(out.String(), "Delete server 42 and everything on it? Type the box name to confirm: ") {
		t.Errorf("output: %q", out.String())
	}
	if _, err := New(strings.NewReader("acme\n"), io.Discard, false).AskTyped("Q"); !errors.Is(err, ErrNoTerminal) {
		t.Errorf("without a terminal: %v", err)
	}
}
