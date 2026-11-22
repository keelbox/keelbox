package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// onBoxAs makes this process the agent account on box devbox, with a
// snapshot and a previews status where the agent's status reads them, and
// fails the test if keelbox calls sudo.
func onBoxAs(t *testing.T, me string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("runs as a non-root account")
	}
	oldBox, oldUser, oldSudo := onBox, currentUser, sudoRun
	oldSnap, oldPrev := snapshotPath, previewsStatusPath
	t.Cleanup(func() {
		onBox, currentUser, sudoRun = oldBox, oldUser, oldSudo
		snapshotPath, previewsStatusPath = oldSnap, oldPrev
	})
	onBox = func() (string, string, bool) { return "devbox", "agent", true }
	currentUser = func() string { return me }
	sudoRun = func(args []string, _, _ io.Writer) int {
		t.Errorf("keelbox called sudo %v", args)
		return 1
	}
	dir := t.TempDir()
	snapshotPath, previewsStatusPath = filepath.Join(dir, "status.json"), filepath.Join(dir, "previews.json")
	now := time.Now().UTC().Format(time.RFC3339)
	snap := `{"status":1,"written":"` + now + `","box":"devbox","keelbox":"v0.1.0","release":"2026.11.4","agent":"agent",
"host":{"cpus":1,"memory":976000000,"memory_available":400000000},"disk":[{"mount":"/","percent":41}],
"updates":{"security":3,"reboot_required_since":"` + now + `"},
"doctor":{"time":"` + now + `","ok":40,"warnings":2,"failures":1,"failing":["codex-sandbox"]},
"agents":[{"cli":"codex","active":"0.166.0"}],
"projects":{"dir":"~/projects","total":3,"list":[{"name":"shop","repo":false},{"name":"api\u001b[2J","repo":true,"uncommitted":3,"unpushed":2},{"name":"site","repo":true}]}}`
	prev := `{"agent":"agent","ports":[8401,8499],"tailnet":{"connected":true,"https":true,"name":"devbox.tail1.ts.net","ips":["100.64.0.5"]},
"certificate":true,"previews":[{"name":"demo","port":5173,"https_port":8401,"url":"https://devbox.tail1.ts.net:8401/","listening":true}],"source":"x","updated":"` + now + `"}`
	if err := os.WriteFile(snapshotPath, []byte(snap), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(previewsStatusPath, []byte(prev), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Commands that reach a box from a laptop say the agent is already on it,
// and never ask sudo for a password.
func TestAgentOnBoxLaptopCommands(t *testing.T) {
	onBoxAs(t, "agent")
	for _, cmd := range []string{"ssh", "attach", "login", "up", "sessions", "ls", "notify"} {
		var out, errb bytes.Buffer
		if code := Main([]string{cmd}, &out, &errb); code != 2 {
			t.Errorf("%s: exit %d", cmd, code)
		}
		if !strings.Contains(errb.String(), "you are already on devbox, as agent") || !strings.Contains(errb.String(), "keelbox expose PORT") {
			t.Errorf("%s:\n%s", cmd, errb.String())
		}
	}
}

func TestAgentOnBoxRootCommandsDoNotAskSudo(t *testing.T) {
	onBoxAs(t, "agent")
	for _, cmd := range []string{"doctor", "apply", "plan"} {
		var out, errb bytes.Buffer
		if code := Main([]string{cmd}, &out, &errb); code != 1 {
			t.Errorf("%s: exit %d", cmd, code)
		}
		if !strings.Contains(errb.String(), "agent has no sudo; keelbox will not ask for a password") || !strings.Contains(errb.String(), "keelbox "+cmd+" devbox") {
			t.Errorf("%s:\n%s", cmd, errb.String())
		}
	}
}

// keelbox status for the agent reads what root wrote, without sudo.
func TestAgentStatusFromSnapshot(t *testing.T) {
	onBoxAs(t, "agent")
	was := agentVersions
	agentVersions = func(context.Context) []string { return []string{"codex-cli 0.166.0"} }
	t.Cleanup(func() { agentVersions = was })
	var out, errb bytes.Buffer
	if code := Main([]string{"status"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"devbox", "1 CPU", "/ 41% used", "3 security, reboot", "1 failures, 2 warnings", "codex-sandbox",
		"connected as devbox.tail1.ts.net, certificate in place", "demo https://devbox.tail1.ts.net:8401/ -> 127.0.0.1:5173", "agents     codex-cli 0.166.0\n", "needs root",
		"projects   3 in ~/projects, 2 with work that is only on this box\n",
		"           shop: not a git repository\n",
		`           api\x1b[2J: 3 uncommitted changes, 2 commits not pushed` + "\n",
		"           site: clean and pushed\n"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status lacks %q:\n%s", want, out.String())
		}
	}
	if strings.ContainsRune(out.String(), '\x1b') {
		t.Errorf("an escape sequence from a project's name reached the terminal:\n%q", out.String())
	}
}

// Anyone but the agent is left to the usual path.
func TestNotTheAgentOnBox(t *testing.T) {
	onBoxAs(t, "admin")
	if _, done := asAgentOnBox("ssh", options{}, io.Discard, io.Discard); done {
		t.Error("the admin was handled as the agent")
	}
	onBoxAs(t, "agent")
	if _, done := asAgentOnBox("expose", options{}, io.Discard, io.Discard); done {
		t.Error("expose refused for the agent")
	}
}
