package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/ui"
)

// A look during a restart is not a failure; a service that restarts while
// it is watched, or never runs, is.
func TestRCStaysUp(t *testing.T) {
	oldW, oldS, oldP := rcWait, rcSettle, rcPoll
	t.Cleanup(func() { rcWait, rcSettle, rcPoll = oldW, oldS, oldP })
	rcWait, rcSettle, rcPoll = 200*time.Millisecond, 10*time.Millisecond, 5*time.Millisecond
	script := func(states ...[3]string) func() (string, string, string) {
		i := 0
		return func() (string, string, string) {
			s := states[min(i, len(states)-1)]
			i++
			return s[0], s[1], s[2]
		}
	}
	restarting := [3]string{"activating", "auto-restart", "1"}
	up := [3]string{"active", "running", "1"}
	ctx := context.Background()
	if !rcStaysUp(ctx, script(restarting, restarting, up, up)) {
		t.Error("a service that comes up after a restart window read as down")
	}
	if rcStaysUp(ctx, script([3]string{"failed", "failed", "6"})) {
		t.Error("a failed service read as running")
	}
	flapping := 0
	if rcStaysUp(ctx, func() (string, string, string) {
		flapping++
		return "active", "running", string(rune('0' + flapping%10))
	}) {
		t.Error("a service restarting while watched read as running")
	}
}

// The first run on the user's terminal (ADR 0073). The box behind the
// stand-in ssh has a signed-in Claude Code, a Remote Control unit that
// stays up only once Claude Code has both answers, and stand-ins for the
// check of the two answers (python3) and for the interrupt (pkill). Each
// writes what it was asked to box.log.

const (
	rcServer      = "cd ~/projects && claude remote-control --spawn same-dir --remote-control-session-name-prefix lab"
	rcTakesOver   = "A few seconds after both answers, keelbox ends this copy by itself. If it does not, press Ctrl-C when it says Connected."
	rcWaitsForYou = "When it says Connected, press Ctrl-C: keelbox is waiting for that, and nothing more happens until you do."
	rcOnlyTheCopy = `Claude Code's own "Press Ctrl+C to stop" means only the copy on this terminal.`
	rcStarting    = "starting Claude Code Remote Control as a service"
	rcIsRunning   = "Claude Code Remote Control running as a service on lab, without this terminal"

	// In a session script: the user answers y to both questions, which
	// Claude Code records; then a wait, of five seconds at most, for the
	// interrupt, or for a look by keelbox that got the answer named.
	rcUserAnswers = `echo 0 > "$d/check.new"; mv "$d/check.new" "$d/check"; touch "$d/answered"` + "\n"
	rcInterrupted = `n=0; while [ ! -f "$d/interrupted" ] && [ $n -lt 500 ]; do sleep 0.01; n=$((n+1)); done` + "\n"
	rcLookGot     = `n=0; while ! grep -q '^python3 projects %d$' "$d/box.log" && [ $n -lt 500 ]; do sleep 0.01; n=$((n+1)); done` + "\n"
)

// rcBox sets up that box: the answers as the check reports them (its exit
// code), and what happens on the terminal once the server has it. The
// timings are a few milliseconds, but for connect, the time left to Claude
// Code to connect once both answers are in.
func rcBox(t *testing.T, check int, connect time.Duration, onTerminal string) (*config.Config, *shell, *sshStub) {
	t.Helper()
	cfg := setUpBox(t)
	oldW, oldS, oldP, oldA, oldC := rcWait, rcSettle, rcPoll, rcAsk, rcConnect
	t.Cleanup(func() { rcWait, rcSettle, rcPoll, rcAsk, rcConnect = oldW, oldS, oldP, oldA, oldC })
	rcWait, rcSettle, rcPoll, rcAsk, rcConnect = 80*time.Millisecond, time.Millisecond, 5*time.Millisecond, 2*time.Millisecond, connect
	stub := stubSSH(t)
	stub.vendor("claude", 0)
	stub.write("check", strconv.Itoa(check), 0o644)
	const head = "#!/bin/sh\nd=\"$KB_SSH_STUB\"\n"
	// The unit runs from the first restart on, or from the one up-from
	// names, once Claude Code has both answers.
	stub.write("bin/systemctl", head+`echo "systemctl $*" >> "$d/box.log"
case " $* " in
  *" stop "*) rm -f "$d/unit-up" ;;
  *" restart "*)
    if [ -f "$d/answered" ] && [ "$(grep -c '^systemctl .* restart ' "$d/box.log")" -ge "$(cat "$d/up-from" 2>/dev/null || echo 1)" ]; then
      touch "$d/unit-up"
    else
      rm -f "$d/unit-up"
    fi ;;
  *" is-active "*) [ -f "$d/unit-up" ]; exit $? ;;
  *" show "*)
    if [ -f "$d/unit-up" ]; then
      printf 'ActiveState=active\nSubState=running\nNRestarts=0\n'
    else
      printf 'ActiveState=activating\nSubState=auto-restart\nNRestarts=1\n'
    fi ;;
esac
exit 0
`, 0o755)
	// The check: the directory it was given is its last argument.
	stub.write("bin/python3", head+`for dir in "$@"; do :; done
code=$(cat "$d/check")
echo "python3 $dir $code" >> "$d/box.log"
exit "$code"
`, 0o755)
	// The interrupt reaches the server only while it has the terminal.
	stub.write("bin/pkill", head+`echo "pkill $*" >> "$d/box.log"
[ -f "$d/serving" ] || exit 1
touch "$d/interrupted"
`, 0o755)
	stub.write("session", head+`echo "session $1" >> "$d/box.log"
touch "$d/serving"
`+onTerminal+`rm -f "$d/serving"
exit 130
`, 0o755)
	sh, err := boxShell(context.Background(), cfg, options{}, "agent", io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, sh, stub
}

// asked is what the box was asked to do with tool, in order.
func (s *sshStub) asked(tool string) []string {
	b, _ := os.ReadFile(filepath.Join(s.dir, "box.log"))
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(l, tool+" "); ok {
			out = append(out, rest)
		}
	}
	return out
}

// The check is real Python, run here against a home of the test's own: it
// answers for the two keys and the directory asked about, by exit code, and
// prints nothing.
func TestRCAnsweredReadsTwoFlags(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	const both = `{"remoteDialogSeen": true, "oauthAccount": {"emailAddress": "you@example.com"}, "projects": {"%s/projects": {"hasTrustDialogAccepted": true}}}`
	for name, c := range map[string]struct {
		file string // "" is no file
		dir  string
		want int
	}{
		"both yes":                         {both, "projects", 0},
		"no file yet":                      {"", "projects", 1},
		"neither":                          {`{"projects": {}}`, "projects", 1},
		"trusted, Remote Control not yet":  {`{"projects": {"%s/projects": {"hasTrustDialogAccepted": true}}}`, "projects", 1},
		"Remote Control yes, no trust yet": {`{"remoteDialogSeen": true, "projects": {"%s/projects": {"hasTrustDialogAccepted": false}}}`, "projects", 1},
		"another directory is trusted":     {both, "work/api", 1},
		"a yes that is not true":           {`{"remoteDialogSeen": "true", "projects": {"%s/projects": {"hasTrustDialogAccepted": 1}}}`, "projects", 1},
		"not JSON":                         {`{"remoteDialogSeen": tr`, "projects", 2},
		"not an object":                    {`[true]`, "projects", 2},
		"projects is not an object":        {`{"remoteDialogSeen": true, "projects": []}`, "projects", 2},
	} {
		home := t.TempDir()
		if c.file != "" {
			if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(strings.ReplaceAll(c.file, "%s", home)), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("sh", "-c", rcAnswered(c.dir))
		cmd.Env = append(os.Environ(), "HOME="+home)
		out, err := cmd.CombinedOutput()
		code := 0
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != c.want || len(out) != 0 {
			t.Errorf("%s: exit %d, want %d; printed %q", name, code, c.want, out)
		}
	}
	// The directory is the one the box resolves: a home reached through a
	// link is the same directory.
	home := t.TempDir()
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(home, link); err != nil {
		t.Skip("no symlinks:", err)
	}
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(strings.ReplaceAll(both, "%s", resolved)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", rcAnswered("projects"))
	cmd.Env = append(os.Environ(), "HOME="+link)
	if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
		t.Errorf("a home behind a link: %v %q", err, out)
	}
	// One line, so that sudo -i on a box that is this machine keeps it whole.
	if strings.Contains(rcAnswered("projects"), "\n") || strings.Contains(rcAnsweredPy, "'") {
		t.Errorf("the check does not survive its way to the box: %q", rcAnswered("projects"))
	}
}

// Both answers already recorded: there is nothing to ask, so the server
// never gets the terminal and keelbox goes on to the service.
func TestRemoteControlSkipsTheTerminalWhenBothAnswersAreIn(t *testing.T) {
	cfg, sh, stub := rcBox(t, 0, 0, rcUserAnswers+rcInterrupted)
	stub.write("answered", "", 0o644)
	stub.write("up-from", "2", 0o644) // the start before the look at the answers did not stay up
	var buf bytes.Buffer
	if !remoteControlLogin(context.Background(), cfg, sh, nil, signInView{}, &buf, &buf) {
		t.Fatalf("not running:\n%s", buf.String())
	}
	if ran := stub.asked("session"); len(ran) != 0 {
		t.Errorf("the server ran on the terminal: %q", ran)
	}
	if sent := stub.asked("pkill"); len(sent) != 0 {
		t.Errorf("an interrupt was sent: %q", sent)
	}
	if got, want := buf.String(), "remote       both answers are in: "+rcStarting+"\nremote       "+rcIsRunning; !strings.HasPrefix(got, want) || strings.Contains(got, "Answer y to both") {
		t.Errorf("output:\n%s", got)
	}
	if dirs := stub.asked("python3"); len(dirs) != 1 || dirs[0] != "projects 0" {
		t.Errorf("the check was asked about %q", dirs)
	}

	// With both answers in and a service that still does not run, the
	// answers are not what is missing, and no terminal run would add them.
	cfg, sh, stub = rcBox(t, 0, 0, "")
	buf.Reset()
	if remoteControlLogin(context.Background(), cfg, sh, nil, signInView{}, &buf, &buf) {
		t.Fatalf("running:\n%s", buf.String())
	}
	if want := "keelbox: Claude Code Remote Control is not running, though both first-run answers are recorded. Its errors: keelbox ssh lab, then journalctl --user -u keelbox-claude-remote-control.service. Later: keelbox login lab claude-code\n"; !strings.HasSuffix(buf.String(), want) ||
		strings.Contains(buf.String(), "still needs both answers") || len(stub.asked("session")) != 0 {
		t.Errorf("output:\n%s\nsessions %q", buf.String(), stub.asked("session"))
	}
}

// The user answers y to both while the server has the terminal: keelbox
// sees the box record them, interrupts the server once, as the agent, and
// starts the service. It says beforehand that it will.
func TestRemoteControlEndsTheTerminalRunOnceBothAnswersAreIn(t *testing.T) {
	cfg, sh, stub := rcBox(t, 1, 0, rcUserAnswers+rcInterrupted)
	st := ui.Style{Colour: true, Live: true, UTF8: true, Width: 200}
	var buf bytes.Buffer
	if !remoteControlLogin(context.Background(), cfg, sh, nil, signInView{st: st}, &buf, &buf) {
		t.Fatalf("not running:\n%s", buf.String())
	}
	if ran := stub.asked("session"); len(ran) != 1 || ran[0] != rcServer {
		t.Errorf("on the terminal: %q", ran)
	}
	if _, err := os.Stat(filepath.Join(stub.dir, "interrupted")); err != nil {
		t.Error("the server on the terminal was never interrupted")
	}
	want := fmt.Sprintf("-INT -u %d -f ^([^ ]*/)?claude remote-control ", os.Getuid())
	if sent := stub.asked("pkill"); len(sent) != 1 || sent[0] != want {
		t.Errorf("interrupts: %q, want one %q", sent, want)
	}
	// The interrupt comes before the service is started again.
	b, _ := os.ReadFile(filepath.Join(stub.dir, "box.log"))
	log := string(b)
	if i, j := strings.Index(log, "\npkill "), strings.LastIndex(log, "\nsystemctl --user restart "); i < 0 || j < i {
		t.Errorf("the box was asked:\n%s", log)
	}
	// With the answers known to be missing, the service is not started
	// before the questions only to be watched failing: one start, after.
	if n := strings.Count(log, "\nsystemctl --user restart "); n != 1 {
		t.Errorf("the service was started %d times, want once, after the answers:\n%s", n, log)
	}
	out := buf.String()
	for _, want := range []string{
		"  " + st.BoldAccent(ui.Hang(rcTakesOver, 70, 2)) + "\n", // in the accent colour, under the heading
		rcOnlyTheCopy,
		"Both answers are in: " + rcStarting + "\n",
		rcIsRunning,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "keelbox is waiting for that") {
		t.Errorf("the user is told keelbox waits for Ctrl-C:\n%s", out)
	}
	// Without a terminal's styling: the same words, on one line.
	cfg, sh, _ = rcBox(t, 1, 0, rcUserAnswers+rcInterrupted)
	buf.Reset()
	if !remoteControlLogin(context.Background(), cfg, sh, nil, signInView{}, &buf, &buf) ||
		!strings.Contains(buf.String(), `- "Enable Remote Control?" `+rcTakesOver+" keelbox then runs Remote Control as a service, so it keeps working after you close this terminal. "+rcOnlyTheCopy+"\n") ||
		!strings.Contains(buf.String(), "remote       both answers are in: "+rcStarting+"\n") {
		t.Errorf("plain output:\n%s", buf.String())
	}
}

// The interrupt waits for Claude Code to connect. A run that ends in that
// time, by the user's own Ctrl-C, gets none, and nothing is left waiting.
func TestRemoteControlSendsNoInterruptBeforeTheWait(t *testing.T) {
	cfg, sh, stub := rcBox(t, 1, 30*time.Second, rcUserAnswers+fmt.Sprintf(rcLookGot, 0))
	var buf bytes.Buffer
	if !remoteControlLogin(context.Background(), cfg, sh, nil, signInView{}, &buf, &buf) {
		t.Fatalf("not running:\n%s", buf.String())
	}
	if sent := stub.asked("pkill"); len(sent) != 0 {
		t.Errorf("an interrupt was sent: %q", sent)
	}
	if !strings.Contains(buf.String(), "remote       "+rcStarting+"\n") {
		t.Errorf("keelbox says it ended a run the user ended:\n%s", buf.String())
	}
}

// A check that cannot tell promises nothing: the user is told to press
// Ctrl-C, as before, and no interrupt is ever sent, also when the check
// stops answering part way and answers again later.
func TestRemoteControlLeavesCtrlCWhenTheCheckCannotTell(t *testing.T) {
	// From the start (no python3, a file it cannot parse): no watch at all.
	cfg, sh, stub := rcBox(t, 2, 0, `touch "$d/answered"; sleep 0.05`+"\n")
	var buf bytes.Buffer
	if !remoteControlLogin(context.Background(), cfg, sh, nil, signInView{}, &buf, &buf) {
		t.Fatalf("not running:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), rcWaitsForYou) || !strings.Contains(buf.String(), rcOnlyTheCopy) || strings.Contains(buf.String(), "by itself") ||
		!strings.Contains(buf.String(), "remote       "+rcStarting+"\n") {
		t.Errorf("output:\n%s", buf.String())
	}
	if sent, checks, ran := stub.asked("pkill"), stub.asked("python3"), stub.asked("session"); len(sent) != 0 || len(checks) != 1 || len(ran) != 1 {
		t.Errorf("interrupts %q, checks %q, on the terminal %q", sent, checks, ran)
	}

	// Part way: the watch stops there, and a later yes is not acted on.
	cannotTell := `echo 2 > "$d/check.new"; mv "$d/check.new" "$d/check"` + "\n"
	cfg, sh, stub = rcBox(t, 1, 0, cannotTell+fmt.Sprintf(rcLookGot, 2)+rcUserAnswers+"sleep 0.2\n")
	buf.Reset()
	if !remoteControlLogin(context.Background(), cfg, sh, nil, signInView{}, &buf, &buf) {
		t.Fatalf("not running:\n%s", buf.String())
	}
	if sent := stub.asked("pkill"); len(sent) != 0 {
		t.Errorf("an interrupt was sent: %q", sent)
	}
	// The words before the run always say what to do if keelbox does not
	// end it.
	if !strings.Contains(buf.String(), "If it does not, press Ctrl-C when it says Connected.") || !strings.Contains(buf.String(), "remote       "+rcStarting+"\n") {
		t.Errorf("output:\n%s", buf.String())
	}
}

// The user presses Ctrl-C, or answers no and Claude Code exits, before both
// answers are in: no interrupt, the watch ends with the run, and the rest is
// as it was.
func TestRemoteControlRunTheUserEnds(t *testing.T) {
	// Two looks that got a no: the one before the run, and one during it.
	twoLooks := `n=0; while [ "$(grep -c '^python3 projects 1$' "$d/box.log")" -lt 2 ] && [ $n -lt 500 ]; do sleep 0.01; n=$((n+1)); done` + "\n"
	cfg, sh, stub := rcBox(t, 1, 0, twoLooks)
	var buf bytes.Buffer
	if remoteControlLogin(context.Background(), cfg, sh, nil, signInView{}, &buf, &buf) {
		t.Fatalf("running:\n%s", buf.String())
	}
	if sent, ran := stub.asked("pkill"), stub.asked("session"); len(sent) != 0 || len(ran) != 1 {
		t.Errorf("interrupts %q, on the terminal %q", sent, ran)
	}
	if !strings.Contains(buf.String(), "remote       "+rcStarting+"\n") ||
		!strings.HasSuffix(buf.String(), "keelbox: Claude Code Remote Control is not running: Claude Code still needs both answers (enable Remote Control: y; trust ~/projects: y). Later: keelbox login lab claude-code\n") {
		t.Errorf("output:\n%s", buf.String())
	}
	// Nothing asks the box anything once the command has returned.
	checks := len(stub.asked("python3"))
	if checks < 3 { // before the run, at least once during it, and for the message
		t.Errorf("the answers were looked at %d times", checks)
	}
	time.Sleep(40 * time.Millisecond)
	if later := len(stub.asked("python3")); later != checks {
		t.Errorf("the watch went on after the run: %d looks, then %d", checks, later)
	}
}

// Turning Remote Control on after the sign-in changes only its switch: the
// file reads as if up had been told yes at the start.
func TestEnableRemoteControl(t *testing.T) {
	render := func(rc bool) string {
		out, err := config.Starter{Box: "lab", Provider: config.ProviderLocal, Agents: []string{config.AgentClaudeCode}, RemoteControl: rc,
			Keys: []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"}}.Render()
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	path := filepath.Join(t.TempDir(), "lab.yaml")
	if err := os.WriteFile(path, []byte(render(false)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := enableRemoteControl(path)
	if err != nil || !cfg.RemoteControl() {
		t.Fatalf("%v %v", cfg, err)
	}
	if b, _ := os.ReadFile(path); string(b) != render(true) {
		t.Errorf("got:\n%s\nwant:\n%s", b, render(true))
	}
	// Already on, or edited beyond recognition: no change, an error.
	if _, err := enableRemoteControl(path); err == nil {
		t.Error("changed a config with no remote_control switch off")
	}
}
