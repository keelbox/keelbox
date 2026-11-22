package modules

import (
	"context"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

func TestTmuxUnitChangeNeverRestartsTheServer(t *testing.T) {
	s := newSim(t)
	ctx := context.Background()
	e := simEnv(t, s, testConfig(t, ""))
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	path := "/home/agent/.config/systemd/user/" + tmuxService
	unit := s.content(path)
	if !strings.Contains(unit, "ExecStop=-/bin/sh -c 'tmux has-session 2>/dev/null || exit 0; exec /home/agent/.tmux/plugins/tmux-resurrect/scripts/save.sh'") {
		t.Errorf("tmux unit saves without a server:\n%s", unit)
	}
	// An older keelbox's unit text: the upgrade apply rewrites it and
	// reloads, and never restarts the server the agent's sessions run in.
	s.Files[path].Data = []byte(strings.Replace(unit, "has-session 2>/dev/null || exit 0; ", "", 1))
	s.Calls = nil
	res, err := engine.Apply(ctx, e, All())
	if err != nil || len(res.Changes) != 1 || res.Changes[0].Step != "tmux-boot-unit" {
		t.Fatalf("upgrade apply: %v %v", res.Changes, err)
	}
	if !s.Ran("systemctl --user -M agent@ daemon-reload") {
		t.Error("unit files not reloaded")
	}
	for _, c := range s.Commands() {
		if strings.Contains(c, "restart "+tmuxService) || strings.Contains(c, "stop "+tmuxService) || strings.Contains(c, "kill-server") {
			t.Errorf("the tmux server was touched: %s", c)
		}
	}
}

// The agent's user manager started (lingering) before apply wrote
// environment.d, and a tmux server runs with the old PATH: apply reloads
// the manager and gives the running server keelbox's PATH without ending
// it, so a session started with a command finds the agent CLIs before any
// reboot. The file from earlier releases goes.
func TestAgentEnvironmentReachesARunningManagerAndTmux(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	s.userEnv = "PATH=/usr/local/bin:/usr/bin:/bin\n"
	s.tmuxEnv = map[string]string{"PATH": "/usr/local/bin:/usr/bin:/bin"}
	s.Files["/home/agent/.config/environment.d/10-keelbox.conf"] = &system.FakeFile{Data: []byte("PATH=/old\n"), Mode: 0o644, Owner: "agent", Group: "agent"}
	s.Calls = nil
	res, err := engine.Apply(ctx, e, All())
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, c := range res.Changes {
		steps = append(steps, c.Step)
	}
	if got := strings.Join(steps, " "); !strings.Contains(got, "agent-environment-d-old") || !strings.Contains(got, "agent-environment-live") {
		t.Errorf("changes: %s", got)
	}
	if !strings.HasPrefix(envValue(s.userEnv, "PATH"), "/home/agent/.local/share/mise/shims:") {
		t.Errorf("user manager PATH: %q", s.userEnv)
	}
	if p := s.tmuxEnv["PATH"]; !strings.HasPrefix(p, "/home/agent/.local/share/mise/shims:") || !strings.HasPrefix(s.tmuxEnv["DOCKER_HOST"], "unix:///run/user/") || !strings.HasSuffix(s.tmuxEnv["DOCKER_HOST"], "/docker.sock") || s.tmuxEnv["PNPM_HOME"] != "/home/agent/.local/share/pnpm" {
		t.Errorf("tmux env: %v", s.tmuxEnv)
	}
	if _, ok := s.Files["/home/agent/.config/environment.d/10-keelbox.conf"]; ok {
		t.Error("old environment.d file left")
	}
	for _, c := range s.Calls {
		if cmd := c.String(); strings.Contains(cmd, "kill-server") || strings.Contains(cmd, "restart "+tmuxService) {
			t.Errorf("ended the agent's sessions: %s", cmd)
		}
	}
	for _, it := range engine.Plan(ctx, e, All()) {
		if !it.OK {
			t.Errorf("not converged: %+v", it)
		}
	}
}

// tmuxOption is the value of a "set -g NAME" line in a tmux config.
func tmuxOption(conf, name string) string {
	for _, l := range strings.Split(conf, "\n") {
		if v, ok := strings.CutPrefix(l, "set -g "+name+" "); ok {
			return strings.Trim(v, `"`)
		}
	}
	return ""
}

// The status line of the agent's sessions says where you are (the box, in
// brass, and keelbox) and how to leave without stopping anything, in the
// brand's colours on the bar's own ground. keelbox keeps the prefix as tmux
// has it, and a second apply leaves the file alone.
func TestTmuxStatusLineNamesTheBoxAndHowToLeave(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	conf := s.content("/home/agent/.config/tmux/keelbox.conf")
	left, right := tmuxOption(conf, "status-left"), tmuxOption(conf, "status-right")
	if !strings.HasPrefix(left, "#[fg=#e0a458,bold] testbox #[fg=#95a1b5,nobold]keelbox") {
		t.Errorf("status-left does not start with the box name in brass, then keelbox: %s", left)
	}
	if !strings.Contains(left, "#{?#{==:#{session_name},main},,") || !strings.Contains(left, "#{session_name}} ") {
		t.Errorf("status-left names session main, or no other session: %s", left)
	}
	// In mist: whole from 80 columns, the keys and "leave" under that.
	if !strings.Contains(right, "#[fg=#95a1b5] #{?#{e|>=:#{client_width},80},Ctrl-b d  leave#, everything keeps running,Ctrl-b d  leave} ") {
		t.Errorf("status-right lacks the way to leave: %s", right)
	}
	if got := tmuxOption(conf, "status-style"); got != "bg=#1b2330,fg=#95a1b5" {
		t.Errorf("status-style: %s", got)
	}
	for _, l := range strings.Split(conf, "\n") {
		f := strings.Fields(l)
		if len(f) > 2 && strings.HasPrefix(f[0], "set") && (f[1] == "prefix" || f[2] == "prefix") || len(f) > 1 && f[0] == "unbind" && f[1] == "C-b" {
			t.Errorf("the tmux prefix is changed: %s", l)
		}
		if strings.Contains(l, "{box}") || strings.Contains(l, "{keys}") || strings.Contains(l, "{hint}") || strings.Contains(l, "{ground}") {
			t.Errorf("a placeholder was left in the config: %s", l)
		}
	}
	if !s.Ran("(as agent) tmux -L keelbox-check -f /dev/null start-server ; source-file /home/agent/.config/tmux/.keelbox-check.conf") {
		t.Error("the config was not loaded into a throwaway tmux server before it was kept")
	}
	res, err := engine.Apply(ctx, e, All())
	if err != nil || len(res.Changes) != 0 {
		t.Errorf("second apply: %v %v", res.Changes, err)
	}
}

// sessionsBlock is keelbox's "sessions" block in the agent's .bashrc.
func sessionsBlock(t *testing.T, s *sim) string {
	t.Helper()
	rc := s.content("/home/agent/.bashrc")
	begin, end := "# >>> keelbox sessions >>>\n", "# <<< keelbox sessions <<<\n"
	i, j := strings.Index(rc, begin), strings.Index(rc, end)
	if i < 0 || j < i || strings.Count(rc, begin) != 1 {
		t.Fatalf("no single sessions block in .bashrc:\n%s", rc)
	}
	return rc[i+len(begin) : j]
}

// A new session's first pane starts with keelbox's welcome: tmux marks that
// pane, and the agent's interactive shell in it runs keelbox welcome once,
// with what the config says. sessions.welcome: false takes both out again,
// and each state is one apply away and then stable.
func TestSessionWelcomeIsWiredAndTurnsOff(t *testing.T) {
	s := newSim(t)
	// Ubuntu's own .bashrc: nothing after its guard runs in a shell that
	// is not interactive (ssh box command, an agent CLI's commands).
	s.Files["/home/agent/.bashrc"] = &system.FakeFile{Data: []byte("# ~/.bashrc\ncase $- in\n    *i*) ;;\n      *) return;;\nesac\n"), Mode: 0o644, Owner: "agent", Group: "agent"}
	ctx := context.Background()
	on := simEnv(t, s, testConfig(t, ""))
	if _, err := engine.Apply(ctx, on, All()); err != nil {
		t.Fatal(err)
	}
	conf, rc := s.content("/home/agent/.config/tmux/keelbox.conf"), s.content("/home/agent/.bashrc")
	for _, want := range []string{
		// The mark: the new session's first pane, and no other.
		`set-hook -g session-created[90] 'set -F @keelbox-welcome "#{pane_id}"'`,
		// The second try, only for a shell that has done nothing.
		"set-hook -g client-attached[90] {",
		`#{==:#{@keelbox-welcome},retry#{pane_id}}`, `#{==:#{pane_current_command},bash}`, `#{==:#{history_size},0}`, `#{==:#{cursor_y},0}`,
		`set -F @keelbox-welcome "again#{pane_id}"`,
		"respawn-pane -k",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("tmux config lacks %q", want)
		}
	}
	block := sessionsBlock(t, s)
	for _, want := range []string{
		"tmux list-sessions", // the login hint stays
		`if [ -n "$TMUX" ] && [ -n "$TMUX_PANE" ] && [ -t 1 ]; then`,
		`tmux display-message -p '#{?@keelbox-welcome,#{@keelbox-welcome},-} #{session_created} #{session_name}'`,
		// A mark set long after the session began is dropped, not acted on.
		`if [ "$keelbox_mark" = "$TMUX_PANE" ] && [ $(( ${EPOCHSECONDS:-0} - ${keelbox_welcome%% *} )) -ge 30 ]; then`,
		`elif [ "$keelbox_mark" = "$TMUX_PANE" ] || [ "$keelbox_mark" = "again$TMUX_PANE" ]; then`,
		`if /usr/local/bin/keelbox welcome --box 'testbox' --agent 'agent' --projects 'projects' --session "${keelbox_welcome#* }" 2>/dev/null; then`,
		"tmux set-option -qu @keelbox-welcome",
		`tmux set-option -q @keelbox-welcome "retry$TMUX_PANE"`,
	} {
		if !strings.Contains(block, want) {
			t.Errorf("the sessions block lacks %q:\n%s", want, block)
		}
	}
	if strings.Contains(block, "--sudo") {
		t.Errorf("an agent without sudo is announced as having it:\n%s", block)
	}
	if strings.Index(rc, "return;;") > strings.Index(rc, "keelbox welcome") {
		t.Errorf("the welcome runs before the interactive guard:\n%s", rc)
	}
	// What apply loads into a throwaway server to check is the config with
	// these hooks, and apply itself restarts nothing in the agent's tmux.
	for _, c := range s.Commands() {
		if strings.Contains(c, "respawn-pane") || strings.Contains(c, "kill-session") || strings.Contains(c, "restart "+tmuxService) {
			t.Errorf("apply touched the agent's sessions: %s", c)
		}
	}
	if res, err := engine.Apply(ctx, on, All()); err != nil || len(res.Changes) != 0 {
		t.Errorf("second apply: %v %v", res.Changes, err)
	}

	off := simEnv(t, s, testConfig(t, "sessions:\n  welcome: false\n"))
	res, err := engine.Apply(ctx, off, All())
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, c := range res.Changes {
		steps = append(steps, c.Step)
	}
	// The two files that carry the welcome, and the box's copy of the config.
	if got := strings.Join(steps, " "); got != "tmux-config tmux-bashrc config-copy" {
		t.Errorf("turning the welcome off changed: %s", got)
	}
	conf, block = s.content("/home/agent/.config/tmux/keelbox.conf"), sessionsBlock(t, s)
	if strings.Contains(conf, "keelbox-welcome") || strings.Contains(conf, "set-hook") || strings.Contains(conf, "respawn-pane") {
		t.Errorf("tmux config still marks sessions for the welcome:\n%s", conf)
	}
	if strings.Contains(block, "welcome") || !strings.Contains(block, "tmux list-sessions") {
		t.Errorf("the sessions block with the welcome off:\n%s", block)
	}
	if !strings.Contains(tmuxOption(conf, "status-right"), "Ctrl-b d") {
		t.Error("the status line went with the welcome")
	}
	if res, err := engine.Apply(ctx, off, All()); err != nil || len(res.Changes) != 0 {
		t.Errorf("second apply with the welcome off: %v %v", res.Changes, err)
	}
	if res, err := engine.Apply(ctx, on, All()); err != nil || len(res.Changes) != 3 || !strings.Contains(sessionsBlock(t, s), "keelbox welcome") {
		t.Errorf("turning the welcome on again: %v %v", res.Changes, err)
	}
}

// The shell hands keelbox welcome what only the config knows: the agent's
// name, its projects directory and that it has sudo.
func TestSessionWelcomeCarriesTheConfig(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, "  agent:\n    sudo: true\nsessions:\n  remote_control:\n    enabled: true\n    directory: src/work\n"))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	want := `if /usr/local/bin/keelbox welcome --box 'testbox' --agent 'agent' --projects 'src/work' --sudo --session "${keelbox_welcome#* }" 2>/dev/null; then`
	if block := sessionsBlock(t, s); !strings.Contains(block, want) {
		t.Errorf("the sessions block lacks %q:\n%s", want, block)
	}
	if got := SessionWelcome(WelcomeOptions{Box: "b", Agent: "a", Projects: "it's here"}); !strings.Contains(got, `--projects 'it'\''s here' --session`) {
		t.Errorf("a directory with a quote is not one shell word:\n%s", got)
	}
}

// Without the sessions module there is no tmux config to mark sessions in
// and no block: sessions.tmux: false wins over sessions.welcome.
func TestSessionWelcomeNeedsTmux(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, "sessions:\n  tmux: false\n  recovery: false\n  welcome: true\n"))
	if e.Cfg.SessionWelcome() {
		t.Error("the welcome is on without tmux")
	}
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.content("/home/agent/.bashrc"), "keelbox welcome") {
		t.Error("a welcome was installed without tmux")
	}
}

// A tmux server that is running reads keelbox's config again when apply
// changes it, so the status line and the welcome hooks reach a box that is
// in use; with no server there is nothing to reload, and nothing is run.
func TestTmuxConfigChangeReachesARunningServer(t *testing.T) {
	const conf = "/home/agent/.config/tmux/keelbox.conf"
	reloaded := func(s *sim) bool { return s.Ran("(as agent) tmux source-file " + conf) }
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if reloaded(s) {
		t.Error("the config was reloaded with no tmux server running")
	}
	// A server is running and the file is an older keelbox's.
	s.tmuxEnv = map[string]string{}
	s.Files[conf].Data = []byte("# an older config\n")
	s.Calls = nil
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if !reloaded(s) {
		t.Error("a running tmux server was not told to read the changed config")
	}
	// Nothing changed: nothing is reloaded.
	s.Calls = nil
	if _, err := engine.Apply(ctx, e, All()); err != nil || reloaded(s) {
		t.Errorf("an apply that changed nothing reloaded tmux: %v", err)
	}
}
