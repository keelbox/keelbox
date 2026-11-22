package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/prompt"
)

// Sign-in checks run a vendor's status command over ssh and use its exit
// code. These tests are about a check ssh could not run: it is not an
// answer, it is said to be not checked with why, and it stops nothing.

// refusedAs is what keelbox says when the box refuses this computer's key
// for the agent.
const refusedAs = "192.0.2.10 does not accept your SSH key for agent. Check which key logs in: ssh agent@192.0.2.10 true"

// failsAfter is fails from the call after the first n as user.
func (s *sshStub) failsAfter(user string, n, code int, msg string) {
	s.fails(user, code, msg)
	s.write(user+".after", strconv.Itoa(n), 0o644)
}

// works lets user log in again.
func (s *sshStub) works(user string) { _ = os.Remove(filepath.Join(s.dir, user+".code")) }

// forgetCalls starts the record of accounts again.
func (s *sshStub) forgetCalls() { _ = os.Remove(filepath.Join(s.dir, "log")) }

// stopping is a context that already reports Ctrl-C while the commands
// started under it still run to their own end: the interrupt reaches ssh
// too, which exits 255 by itself.
type stopping struct{ context.Context }

func (stopping) Err() error { return context.Canceled }

// doctor's sign-in rows come from exit codes. A check ssh could not run
// gave none: that row says "not checked" with why, and the rows that could
// be checked stay.
func TestDoctorSignInRows(t *testing.T) {
	cfg := setUpBox(t)
	ctx := context.Background()
	stub := stubSSH(t)
	rows := func() string {
		stub.forgetCalls()
		var out bytes.Buffer
		doctorSignIns(ctx, cfg, options{}, false, &out, io.Discard)
		return out.String()
	}
	row := func(level, name, text string) string { return fmt.Sprintf("  %-4s  %-24s %s\n", level, name, text) }

	// The box is off: one row, after one attempt.
	stub.fails("admin", 255, timedOut)
	stub.fails("agent", 255, timedOut)
	if got, want := rows(), "\nsign-in\n"+row("warn", "sign-ins", "not checked: "+offLine); got != want || stub.calls() != "admin" {
		t.Errorf("the box is off: ssh as %q\ngot  %q\nwant %q", stub.calls(), got, want)
	}

	// The agent's key is refused: the admin's row is read, the agent's
	// three are not checked, and none of them reads "not signed in".
	stub.works("admin")
	stub.vendor("tailscale", 0)
	stub.fails("agent", 255, "agent@192.0.2.10: Permission denied (publickey).")
	want := "\nsign-in\n" + row("ok", "tailscale", "signed in") + row("warn", "claude-code", "not checked: "+refusedAs) +
		row("warn", "codex", "not checked: "+refusedAs) + row("warn", "gh", "not checked: "+refusedAs)
	if got := rows(); got != want {
		t.Errorf("the agent's key is refused:\ngot  %q\nwant %q", got, want)
	}

	// The checks ran. A vendor CLI's own exit code, 255 included, is its
	// answer and not ssh's failure.
	stub.works("agent")
	stub.vendor("claude", 0)
	stub.vendor("codex", 255)
	want = "\nsign-in\n" + row("ok", "tailscale", "signed in") + row("ok", "claude-code", "signed in") +
		row("warn", "codex", "not signed in: keelbox login lab codex") + row("warn", "gh", "not signed in: keelbox login lab gh")
	if got := rows(); got != want {
		t.Errorf("the checks ran:\ngot  %q\nwant %q", got, want)
	}

	// Ctrl-C reaches ssh too, which then exits 255 by itself: that is not
	// a check that could not run.
	stub.fails("admin", 255, "Killed by signal 2.")
	sh, err := boxShell(ctx, cfg, options{}, "admin", io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := sh.succeeds(stopping{ctx}, "true"); ok || err != nil {
		t.Errorf("a check stopped by Ctrl-C: %v %v", ok, err)
	}
	if ok, err := sh.succeeds(ctx, "true"); ok || err == nil {
		t.Errorf("ssh's own failure read as an answer: %v %v", ok, err)
	}
}

// keelbox login BOX tailscale checks that sign-in only, as the admin: an
// agent account this computer cannot log in to is not its concern.
func TestLoginChecksOnlyTheSignInAsked(t *testing.T) {
	cfg := setUpBox(t)
	stub := stubSSH(t)
	stub.vendor("tailscale", 0)
	stub.fails("agent", 255, "agent@192.0.2.10: Permission denied (publickey).")
	var buf bytes.Buffer
	res := signInAll(context.Background(), cfg, options{}, "tailscale", prompt.New(strings.NewReader(""), &buf, true), &buf, &buf)
	if res.err != nil || len(res.left()) != 0 || stub.calls() != "admin" || !strings.Contains(buf.String(), "tailscale    signed in\n") {
		t.Errorf("%+v, ssh as %q\n%s", res, stub.calls(), buf.String())
	}
	if res := signInAll(context.Background(), cfg, options{}, "nope", prompt.New(strings.NewReader(""), &buf, true), &buf, &buf); res.err == nil ||
		!strings.Contains(res.err.Error(), "lab has no nope sign-in; it has: tailscale, claude-code, codex, gh") {
		t.Errorf("a sign-in the box does not have: %+v", res)
	}
}

// Sign-ins that could not be checked are said to be so, with why and the
// command that tries again. They stop neither keelbox login nor keelbox
// up, and are left to do like the ones that are missing.
func TestSignInsNotCheckedDoNotStopTheRun(t *testing.T) {
	cfg := setUpBox(t)
	ctx := context.Background()
	stub := stubSSH(t)
	stub.vendor("tailscale", 0)
	stub.fails("agent", 255, "agent@192.0.2.10: Permission denied (publickey).")

	var buf bytes.Buffer
	res := signInAll(ctx, cfg, options{}, "", prompt.New(strings.NewReader(""), &buf, true), &buf, &buf)
	if res.err != nil || res.interrupted != "" || strings.Join(res.unchecked, " ") != "claude-code codex gh" || strings.Join(res.left(), " ") != "claude-code codex gh" {
		t.Fatalf("%+v\n%s", res, buf.String())
	}
	for _, want := range []string{
		"tailscale    signed in\n",
		"claude-code  not checked: " + refusedAs + ". Then: keelbox login lab claude-code\n",
		"codex        not checked: " + refusedAs + ". Then: keelbox login lab codex\n",
		"gh           not checked: " + refusedAs + ". Then: keelbox login lab gh\n",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "not signed in") || strings.Contains(buf.String(), "Next, ") {
		t.Errorf("a sign-in that was not checked is offered, or called missing:\n%s", buf.String())
	}

	// keelbox up asks the same question before its sign-in step.
	left, notChecked := realRunner{}.notSignedIn(ctx, cfg, options{})
	if strings.Join(left, " ") != "claude-code codex gh" || len(notChecked) != 3 ||
		notChecked[1] != "the Codex sign-in was not checked: "+refusedAs+". Then: keelbox login lab codex" {
		t.Errorf("left %v, not checked %q", left, notChecked)
	}
	// With everything signed in, nothing is left.
	stub.works("agent")
	for _, cli := range []string{"claude", "codex", "gh"} {
		stub.vendor(cli, 0)
	}
	if left, notChecked := (realRunner{}).notSignedIn(ctx, cfg, options{}); len(left) != 0 || len(notChecked) != 0 {
		t.Errorf("all signed in: left %v, not checked %q", left, notChecked)
	}
}

// A check that fails after the user says "I have signed in", or after a
// sign-in ran, is not "still not signed in": ssh did not run it.
func TestSignInChecksThatCouldNotRunAfterASignIn(t *testing.T) {
	cfg := setUpBox(t)
	ctx := context.Background()
	const reset = "kex_exchange_identification: Connection reset by peer"
	const why = "ssh admin@192.0.2.10: exit status 255: " + reset + ". Check your key logs in: ssh admin@192.0.2.10 true"
	agentsSignedIn := func() *sshStub {
		stub := stubSSH(t)
		for _, cli := range []string{"claude", "codex", "gh"} {
			stub.vendor(cli, 0)
		}
		return stub
	}

	// "I have signed in": the check fails once, then the user skips.
	stub := agentsSignedIn()
	stub.failsAfter("admin", 1, 255, reset)
	var buf bytes.Buffer
	res := signInAll(ctx, cfg, options{}, "", prompt.New(strings.NewReader("3\n2\n"), &buf, true), &buf, &buf)
	if res.err != nil || strings.Join(res.skipped, " ") != "tailscale" || len(res.unchecked) != 0 {
		t.Fatalf("%+v\n%s", res, buf.String())
	}
	if !strings.Contains(buf.String(), "  Tailscale could not be checked on lab: "+why+"\n") || strings.Contains(buf.String(), "still reports not signed in") {
		t.Errorf("I have signed in, check failed:\n%s", buf.String())
	}

	// The sign-in ran (the stand-in lets it end well), and the check after
	// it could not.
	stub = agentsSignedIn()
	stub.write("sessions-end", "0", 0o644)
	stub.failsAfter("admin", 2, 255, reset)
	buf.Reset()
	res = signInAll(ctx, cfg, options{}, "", prompt.New(strings.NewReader("1\n"), &buf, true), &buf, &buf)
	if res.err != nil || strings.Join(res.unchecked, " ") != "tailscale" || len(res.failed) != 0 || len(res.signed) != 0 {
		t.Fatalf("%+v\n%s", res, buf.String())
	}
	if !strings.Contains(buf.String(), "tailscale    not checked: "+why+". Then: keelbox login lab tailscale\n") || strings.Contains(buf.String(), "still reports not signed in") {
		t.Errorf("after the sign-in, check failed:\n%s", buf.String())
	}

	// Remote Control waits for the Claude Code sign-in; a check that did
	// not run is not "waiting for your sign-in".
	stub = stubSSH(t)
	stub.fails("agent", 255, "agent@192.0.2.10: Permission denied (publickey).")
	sh, err := boxShell(ctx, cfg, options{}, "agent", io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if got := remoteControlLine(ctx, cfg, sh); got != "not checked: "+refusedAs {
		t.Errorf("keelbox sessions: %s", got)
	}
	buf.Reset()
	if remoteControlLogin(ctx, cfg, sh, nil, signInView{}, &buf, &buf) || strings.Contains(buf.String(), "waits for the claude-code sign-in") ||
		!strings.Contains(buf.String(), "Claude Code Remote Control not checked: "+refusedAs+". Then: keelbox login lab claude-code\n") {
		t.Errorf("keelbox login:\n%s", buf.String())
	}
}
