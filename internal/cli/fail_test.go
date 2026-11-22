package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/ui"
)

// stderrAs makes errors written in this test be drawn as on a terminal
// cols wide.
func stderrAs(t *testing.T, st ui.Style) {
	t.Helper()
	old := errStyle
	errStyle = func(io.Writer) ui.Style { return st }
	t.Cleanup(func() { errStyle = old })
}

// A command's error on a terminal is one block, wrapped to the width, with
// the command to run whole on the arrow line; off a terminal it is the
// line "keelbox: text" a script reads.
func TestErrorsAreOneBlockOnATerminal(t *testing.T) {
	isolate(t)
	err := &noAnswer{"203.0.113.7 does not answer on port 22; check it is on and reachable from here: ssh agent@203.0.113.7"}
	var plain bytes.Buffer
	fail(&plain, err)
	if got, want := plain.String(), "keelbox: "+err.msg+"\n"; got != want {
		t.Errorf("off a terminal:\ngot  %q\nwant %q", got, want)
	}
	for _, w := range []int{60, 80, 100, 120} {
		stderrAs(t, ui.Style{Live: true, UTF8: true, Width: w})
		var b bytes.Buffer
		fail(&b, err)
		want := "\n  ✗ 203.0.113.7 does not answer on port 22\n    Check it is on and reachable from here.\n    → ssh agent@203.0.113.7\n"
		if b.String() != want {
			t.Errorf("%d columns:\ngot  %q\nwant %q", w, b.String(), want)
		}
	}
}

// Every command's errors go through that block: a usage error from Main,
// a box that is not there, and prose longer than the terminal wrapped.
func TestCommandErrorsWrapToTheTerminal(t *testing.T) {
	isolate(t)
	for _, w := range []int{60, 80, 100, 120} {
		stderrAs(t, ui.Style{Live: true, UTF8: true, Width: w})
		for _, args := range [][]string{
			{"forget"},
			{"forget", "gamma"},
			{"attach", "gamma"},
			{"frobnicate"},
			{"init", "a", "b"},
			{"completion"},
		} {
			var out, errOut bytes.Buffer
			if code := Main(args, &out, &errOut); code != 2 {
				t.Errorf("%d %v: exit %d", w, args, code)
			}
			got := errOut.String()
			if !strings.HasPrefix(got, "\n  ✗ ") || strings.Contains(got, "keelbox: ") {
				t.Errorf("%d %v: not a block:\n%s", w, args, got)
			}
			for _, l := range strings.Split(got, "\n") {
				if ui.Width(l) >= w {
					t.Errorf("%d %v: a line is %d columns: %q", w, args, ui.Width(l), l)
				}
			}
		}
	}
	stderrAs(t, ui.Style{Live: true, UTF8: true, Width: 80})
	var out, errOut bytes.Buffer
	Main([]string{"forget"}, &out, &errOut)
	if want := "\n  ✗ forget takes the box name\n    (list boxes: keelbox ls)\n    → keelbox forget acme\n"; errOut.String() != want {
		t.Errorf("got  %q\nwant %q", errOut.String(), want)
	}
}

// keelbox up's error is one block: the answers that are kept are its last
// line, and one arrow line names the command that continues. No second,
// unstyled line follows it.
func TestUpErrorIsOneBlockWithOneArrow(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	fakeAWS(t)
	defer func(f func() bool) { awsInstalled = f }(awsInstalled)
	awsInstalled = func() bool { return false }
	stderrAs(t, ui.Style{Live: true, UTF8: true, Width: 100})
	code, out := upWith(t, &fakeRunner{}, script(), "up", "acme", "--provider", "aws", "--ssh-key", key, "--yes")
	if code != exitNeedsYou {
		t.Fatalf("exit %d\n%s", code, out)
	}
	_, block, _ := strings.Cut(out, "\n  ! ")
	want := "The AWS CLI (the aws command) is not installed or not on PATH\n" +
		"    keelbox creates AWS servers through it. Install it:\n" +
		"    https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html, then sign in:\n" +
		"      aws login\n" +
		"    Nothing was changed. Your answers so far are kept.\n" +
		"    → keelbox up acme\n"
	if block != want {
		t.Errorf("got:\n%s\nwant:\n%s", block, want)
	}
}

// A question ended by Ctrl-C is not an error to print: Main says once
// that nothing changed.
func TestFailSaysNothingForAnInterruptedQuestion(t *testing.T) {
	var b bytes.Buffer
	fail(&b, prompt.ErrInterrupted)
	if b.Len() != 0 {
		t.Errorf("got %q", b.String())
	}
}

// A box that does not answer is said once: keelbox login ends with that
// one error, keelbox up leaves the sign-ins for later in one line, and
// keelbox doctor adds no sign-in rows to the error its run printed.
func TestABoxThatDoesNotAnswerIsSaidOnce(t *testing.T) {
	cfg := setUpBox(t)
	ctx := context.Background()
	stub := stubSSH(t)
	stub.fails("admin", 255, timedOut)
	stub.fails("agent", 255, timedOut)

	var out bytes.Buffer
	p := prompt.New(strings.NewReader(""), &out, true)
	res := signInAll(ctx, cfg, options{}, "", p, &out, io.Discard)
	var gone *noAnswer
	if !errors.As(res.err, &gone) || res.err.Error() != offLine || out.Len() != 0 || len(res.unchecked) != 4 {
		t.Errorf("keelbox login: err %v, unchecked %v, printed %q", res.err, res.unchecked, out.String())
	}

	out.Reset()
	res = signInAll(ctx, cfg, options{inUp: true}, "", p, &out, io.Discard)
	if res.err != nil || strings.Count(out.String(), "does not answer") != 1 || !strings.Contains(out.String(), "Then: keelbox login lab\n") {
		t.Errorf("keelbox up: err %v, printed %q", res.err, out.String())
	}

	out.Reset()
	doctorSignIns(ctx, cfg, options{}, true, &out, io.Discard)
	if out.Len() != 0 {
		t.Errorf("keelbox doctor after a run that failed: %q", out.String())
	}
}
