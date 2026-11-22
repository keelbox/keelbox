package modules

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The welcome's shell lines, run by bash itself with a stand-in tmux and a
// stand-in keelbox: which marks make the shell print, and what it leaves in
// the mark afterwards. The terminal test is taken out, as a test has none;
// the last case puts it back.
func TestSessionWelcomeShellActsOnTheMark(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o700); err != nil { //nolint:gosec // G306: an executable
			t.Fatal(err)
		}
		return p
	}
	// tmux answers display-message with $KB_REPLY and records the rest.
	write("tmux", "#!/bin/sh\nif [ \"$1\" = display-message ]; then [ -n \"$KB_REPLY\" ] || exit 1; echo \"$KB_REPLY\"; exit 0; fi\necho \"tmux $*\" >> "+calls+"\n")
	keelbox := write("keelbox", "#!/bin/sh\nprintf 'keelbox' >> "+calls+"\nfor a in \"$@\"; do printf ' [%s]' \"$a\" >> "+calls+"; done\necho >> "+calls+"\necho WELCOME\nexit ${KB_EXIT:-0}\n")
	snippet := SessionWelcome(WelcomeOptions{Box: "devbox-2", Agent: "agent", Projects: "projects"})
	script := write("welcome.sh", strings.Replace(strings.Replace(snippet, BinaryPath, keelbox, 1), "[ -t 1 ]", "true", 1)+"\n")
	if strings.Contains(snippet, keelbox) || !strings.Contains(snippet, "[ -t 1 ]") {
		t.Fatalf("the snippet is not what this test rewrites:\n%s", snippet)
	}
	if out, err := exec.Command(bash, "-n", write("block.sh", SessionHint+"\n\n"+snippet+"\n")).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
	run := func(file, reply string, exit int, env ...string) (stdout, did string) {
		t.Helper()
		_ = os.Remove(calls)
		cmd := exec.Command(bash, file)
		cmd.Env = append([]string{"PATH=" + dir + ":/usr/bin:/bin", "TMUX=/tmp/tmux-1/default,1,0", "TMUX_PANE=%7", "KB_REPLY=" + reply, fmt.Sprintf("KB_EXIT=%d", exit)}, env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", reply, err, out)
		}
		data, _ := os.ReadFile(calls)
		return string(out), string(data)
	}
	now := time.Now().Unix()
	fresh, old := fmt.Sprint(now-1), fmt.Sprint(now-welcomeMarkAge-5)
	welcomed := "keelbox [welcome] [--box] [devbox-2] [--agent] [agent] [--projects] [projects] [--session] [main]\n"
	cleared, retry := "tmux set-option -qu @keelbox-welcome\n", "tmux set-option -q @keelbox-welcome retry%7\n"

	for _, c := range []struct {
		name, reply   string
		exit          int
		stdout, calls string
	}{
		{"the marked pane of a new session", "%7 " + fresh + " main", 0, "WELCOME\n", welcomed + cleared},
		{"a session name with a space is one argument", "%7 " + fresh + " my work", 0, "WELCOME\n", strings.Replace(welcomed, "[main]", "[my work]", 1) + cleared},
		{"keelbox cannot print it: retry at the first attach", "%7 " + fresh + " main", 127, "WELCOME\n", welcomed + retry},
		{"tmux started this shell again for it, long after the session began", "again%7 " + old + " main", 0, "WELCOME\n", welcomed + cleared},
		{"again, and it still cannot", "again%7 " + old + " main", 2, "WELCOME\n", welcomed + retry},
		{"no mark", "- " + fresh + " main", 0, "", ""},
		{"another pane's mark", "%8 " + fresh + " main", 0, "", ""},
		{"a mark left at retry is tmux's to act on", "retry%7 " + fresh + " main", 0, "", ""},
		{"a session named like a pane, without a mark", "- " + fresh + " %7", 0, "", ""},
		{"tmux does not answer", "", 0, "", ""},
	} {
		if out, did := run(script, c.reply, c.exit); out != c.stdout || did != c.calls {
			t.Errorf("%s: printed %q and ran:\n%swant %q and:\n%s", c.name, out, did, c.stdout, c.calls)
		}
	}
	// A mark that arrives long after the session began (a restore at boot)
	// is dropped. bash before 5.0 has no clock of its own and acts on it.
	if has, _ := exec.Command(bash, "-c", "echo ${EPOCHSECONDS:-}").Output(); strings.TrimSpace(string(has)) != "" {
		if out, did := run(script, "%7 "+old+" main", 0); out != "" || did != cleared {
			t.Errorf("a late mark: printed %q and ran:\n%s", out, did)
		}
	}
	// Outside tmux, and with no terminal (the snippet as it is written),
	// the shell does not even ask tmux.
	if out, did := run(script, "%7 "+fresh+" main", 0, "TMUX="); out != "" || did != "" {
		t.Errorf("outside tmux: printed %q and ran:\n%s", out, did)
	}
	if out, did := run(write("asis.sh", strings.Replace(snippet, BinaryPath, keelbox, 1)+"\n"), "%7 "+fresh+" main", 0); out != "" || did != "" {
		t.Errorf("without a terminal: printed %q and ran:\n%s", out, did)
	}
}
