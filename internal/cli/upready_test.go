package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/ui"
)

// saysSetUp reports whether out carries the last screen of keelbox up for
// box: the headline on a line of its own, and the card under it.
func saysSetUp(out, box string) bool {
	return strings.Contains(out, "\n"+box+" is set up") && strings.Contains(out, "Start working:")
}

// readyConfig is the config of a box keelbox up has set up, from a starter
// and what the starter has no field for.
func readyConfig(t *testing.T, s config.Starter, edit ...func(*config.Config)) *config.Config {
	t.Helper()
	if s.GitName == "" {
		s.GitName, s.GitEmail = "Box Agent", "agent@example.com"
	}
	if s.GitName == "none" {
		s.GitName, s.GitEmail = "", ""
	}
	y, err := s.Render()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte(y), s.Box+".yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range edit {
		f(cfg)
	}
	return cfg
}

func cloudBox(box string) config.Starter {
	return config.StarterFor(box, config.ProviderHetzner)
}

func serverBox(box, host string) config.Starter {
	return config.Starter{Box: box, Provider: config.ProviderSSH, SSHHost: host}
}

func thisMachine(box string) config.Starter {
	return config.Starter{Box: box, Provider: config.ProviderLocal}
}

func agentSudo(cfg *config.Config) { yes := true; cfg.Users.Agent.Sudo = &yes }

func agentPermissions(mode string) func(*config.Config) {
	return func(cfg *config.Config) { cfg.Users.Agent.Permissions = mode }
}

func noTmux(cfg *config.Config) { no := false; cfg.Sessions.Tmux = &no }

// readyState is one end of keelbox up: the box, what is left, and whether
// this computer has applied to it over the tailnet.
type readyState struct {
	cfg     *config.Config
	st      upState
	reached bool
	took    string
}

func (r readyState) render(ss ui.Style) string {
	return readyScreen(ss, r.cfg, r.st, r.reached, r.took)
}

// readyStates are the three ends the docs show: everything done, the
// sign-ins skipped with the tailnet still to join, and this machine.
func readyStates(t *testing.T) map[string]readyState {
	t.Helper()
	isolate(t) // the config's path in the git identity row is under HOME
	done := cloudBox("devbox-2")
	done.RemoteControl = true
	skipped := serverBox("devbox-2", "203.0.113.7")
	skipped.RemoteControl, skipped.GitName = true, "none"
	return map[string]readyState{
		"done": {cfg: readyConfig(t, done), reached: true, took: "in 4m48s"},
		"skipped": {cfg: readyConfig(t, skipped), took: "in 3m12s",
			st: upState{Address: "203.0.113.7", Left: []string{"tailscale", config.AgentClaudeCode, config.AgentCodex, "gh"}}},
		"local": {cfg: readyConfig(t, thisMachine("workstation")), took: "in 2m05s"},
	}
}

func plainText(r readyState) string { return r.render(ui.Style{}) }

func liveStyle(width int) ui.Style { return ui.Style{Live: true, UTF8: true, Width: width} }

func colourStyle(width int) ui.Style {
	return ui.Style{Live: true, UTF8: true, Colour: true, Depth: ui.Depth256, Links: true, Width: width}
}

// readyGolden compares got with testdata/golden/name, or rewrites it with
// -update.
func readyGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join(wd, "testdata", "golden", name)
	if *updateDocs {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/cli -run TestReadyGolden -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from the golden file:\n%s\nwant:\n%s", name, got, want)
	}
}

// wd is the package directory: the tests change directory (isolate).
var wd, _ = os.Getwd()

func TestReadyGolden(t *testing.T) {
	for name, r := range readyStates(t) {
		readyGolden(t, "ready-"+name+"-plain.txt", plainText(r))
		for _, w := range []int{60, 80, 100} {
			readyGolden(t, fmt.Sprintf("ready-%s-%d.txt", name, w), r.render(liveStyle(w)))
		}
		readyGolden(t, "ready-"+name+"-100-colour.txt", r.render(colourStyle(100)))
	}
}

// At every width the screen fits the terminal, the card's border is one
// straight line, and the words are the plain text's in the same order:
// nothing is cut, dropped or broken inside a word.
func TestReadyFitsEveryWidth(t *testing.T) {
	words := func(s string) []string {
		s = strings.NewReplacer("╭", " ", "╮", " ", "╰", " ", "╯", " ", "│", " ", "─", " ", "✓", " ").Replace(ui.Strip(s))
		s = strings.NewReplacer("Start working:", "Start working", "s.\n\n", "s\n\n").Replace(s)
		return strings.Fields(s)
	}
	for name, r := range readyStates(t) {
		want := strings.Join(words(plainText(r)), " ")
		for w := 48; w <= 200; w++ {
			for _, ss := range []ui.Style{liveStyle(w), colourStyle(w)} {
				got := r.render(ss)
				border := 0
				for _, l := range strings.Split(got, "\n") {
					if ui.Width(l) >= w {
						t.Fatalf("%s at %d columns: a line is %d: %q", name, w, ui.Width(l), l)
					}
					if plain := ui.Strip(l); strings.HasPrefix(plain, "│") || strings.HasPrefix(plain, "╭") || strings.HasPrefix(plain, "╰") {
						if border != 0 && ui.Width(l) != border {
							t.Fatalf("%s at %d columns: the border is %d wide, then %d: %q", name, w, border, ui.Width(l), l)
						}
						border = ui.Width(l)
					}
				}
				if flat := strings.Join(words(got), " "); flat != want {
					t.Fatalf("%s at %d columns: the words differ from the plain text:\n%s\nwant:\n%s", name, w, flat, want)
				}
			}
		}
	}
}

// The headline says the set-up is finished, once, with the one total; the
// plain form is a sentence, and a box printed again has no time.
func TestReadyHeadline(t *testing.T) {
	r := readyStates(t)["done"]
	if got := plainText(r); !strings.HasPrefix(got, "\ndevbox-2 is set up in 4m48s.\n\n") {
		t.Errorf("plain headline:\n%s", got)
	}
	r.took = ""
	if got := plainText(r); !strings.HasPrefix(got, "\ndevbox-2 is set up.\n\n") {
		t.Errorf("plain headline without a time:\n%s", got)
	}
	if got := r.render(liveStyle(100)); !strings.HasPrefix(got, "\n  ✓ devbox-2 is set up\n\n") {
		t.Errorf("headline without a time:\n%s", got)
	}
	r.took = "in 4m48s"
	ss := colourStyle(100)
	lines := strings.Split(r.render(ss), "\n")
	head, top := lines[1], ""
	for _, l := range lines {
		if strings.HasPrefix(ui.Strip(l), "╭─ Start working ") {
			top = l
		}
	}
	if !strings.HasPrefix(head, "  "+ss.Tick()+" "+ss.Bold("devbox-2 is set up")+" ") || !strings.HasSuffix(head, " "+ss.Dim("in 4m48s")) {
		t.Errorf("headline: tick in the OK colour, bold, the time dim: %q", head)
	}
	if top == "" || ui.Width(head) != ui.Width(top) {
		t.Errorf("the time does not end at the card's edge:\n%s\n%s", head, top)
	}
	if n := strings.Count(strings.Join(lines, "\n"), "4m48s"); n != 1 {
		t.Errorf("the time is there %d times", n)
	}
	// A terminal too narrow for both puts the time under the headline.
	long := readyConfig(t, serverBox("a-box-with-a-very-long-name-indeed-for-a-terminal", "203.0.113.7"))
	if got := readyScreen(liveStyle(60), long, upState{}, false, "in 4m48s"); !strings.HasPrefix(got, "\n  ✓ a-box-with-a-very-long-name-indeed-for-a-terminal is set up\n    in 4m48s\n") {
		t.Errorf("long headline:\n%s", got)
	}
}

// The first sentence names the agent CLIs the config installs and says
// what the agent account is, with sudo only as the config has it.
func TestReadyAgentsSentence(t *testing.T) {
	both, one, none := serverBox("lab", "203.0.113.7"), serverBox("lab", "203.0.113.7"), serverBox("lab", "203.0.113.7")
	one.Agents, none.Agents = []string{config.AgentCodex}, []string{}
	for name, c := range map[string]struct {
		cfg  *config.Config
		want string
	}{
		"both":         {readyConfig(t, both), "Claude Code and Codex are installed and run as the agent, an account without sudo that can install Ubuntu packages (keelbox install)."},
		"one":          {readyConfig(t, one), "Codex is installed and runs as the agent, an account without sudo that can install Ubuntu packages (keelbox install)."},
		"none":         {readyConfig(t, none), "No agent CLI is installed; the agent is an account without sudo that can install Ubuntu packages (keelbox install)."},
		"auto":         {readyConfig(t, both, agentPermissions(config.PermissionsAuto)), "Claude Code and Codex are installed and run as the agent, an account without sudo that can install Ubuntu packages (keelbox install)."},
		"manual":       {readyConfig(t, both, agentPermissions(config.PermissionsManual)), "Claude Code and Codex are installed and run as the agent, an account without sudo."},
		"none, manual": {readyConfig(t, none, agentPermissions(config.PermissionsManual)), "No agent CLI is installed; the agent is an account without sudo."},
		"full":         {readyConfig(t, both, agentPermissions(config.PermissionsFull)), "Claude Code and Codex are installed and run as the agent, an account with sudo."},
		"sudo":         {readyConfig(t, both, agentSudo), "Claude Code and Codex are installed and run as the agent, an account with sudo."},
		"none, sudo":   {readyConfig(t, none, agentSudo), "No agent CLI is installed; the agent is an account with sudo."},
	} {
		if got := agentsSentence(c.cfg); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
		if out := readyScreen(ui.Style{}, c.cfg, upState{}, true, ""); !strings.Contains(out, "\n\n"+c.want+" ") {
			t.Errorf("%s: the screen lacks %q:\n%s", name, c.want, out)
		}
	}
}

// The second sentence follows the state of SSH and the tailnet: closed is
// said only with this computer's proof, and every other state says what
// closes SSH or why it stays open.
func TestReadyReachSentence(t *testing.T) {
	isolate(t)
	ssh := func(edit func(*config.Starter)) *config.Config {
		s := serverBox("lab", "203.0.113.7")
		if edit != nil {
			edit(&s)
		}
		return readyConfig(t, s)
	}
	local := func(edit func(*config.Starter)) *config.Config {
		s := thisMachine("lab")
		if edit != nil {
			edit(&s)
		}
		return readyConfig(t, s)
	}
	mode := func(m config.PublicSSHMode) func(*config.Starter) {
		return func(s *config.Starter) { s.PublicSSH = string(m) }
	}
	noTailscale := func(s *config.Starter) { s.NoTailscale = true }
	pending := upState{Left: []string{"tailscale"}}
	for name, c := range map[string]struct {
		cfg     *config.Config
		st      upState
		reached bool
		want    string
	}{
		"joined, closed": {ssh(nil), upState{}, true,
			"SSH from the internet is closed; the box is on your tailnet."},
		"joined, closed, LAN kept": {ssh(mode(config.PublicSSHLAN)), upState{}, true,
			"SSH from the internet is closed and your LAN keeps it; the box is on your tailnet."},
		"joined, no proof": {ssh(nil), upState{}, false,
			"The box is on your tailnet. SSH from the internet closes once keelbox apply lab reaches the box over the tailnet; keelbox doctor lab says whether it has."},
		"joined, no proof, LAN kept": {ssh(mode(config.PublicSSHLAN)), upState{}, false,
			"The box is on your tailnet. SSH from the internet closes once keelbox apply lab reaches the box over the tailnet; keelbox doctor lab says whether it has. Your LAN keeps SSH."},
		"pending": {ssh(nil), pending, false,
			"The box is not on your tailnet yet, so SSH from the internet is still open. It closes after keelbox login lab tailscale, then keelbox apply lab."},
		"pending, LAN kept": {ssh(mode(config.PublicSSHLAN)), pending, false,
			"The box is not on your tailnet yet, so SSH from the internet is still open. It closes after keelbox login lab tailscale, then keelbox apply lab; your LAN keeps SSH."},
		// A record from an earlier box of the name proves nothing while the
		// sign-in is left.
		"pending, stale proof": {ssh(nil), pending, true,
			"The box is not on your tailnet yet, so SSH from the internet is still open. It closes after keelbox login lab tailscale, then keelbox apply lab."},
		"no Tailscale": {ssh(noTailscale), upState{}, false,
			"Without Tailscale you reach the box over SSH, which stays open to the LAN and the internet."},
		"kept open": {ssh(mode(config.PublicSSHOpen)), upState{}, true,
			"The box is on your tailnet; SSH stays open to the LAN and the internet (host.firewall.public_ssh: open)."},
		"kept open, pending": {ssh(mode(config.PublicSSHOpen)), pending, false,
			"The box is not on your tailnet yet; SSH stays open to the LAN and the internet (host.firewall.public_ssh: open)."},
		"local": {local(nil), upState{}, false,
			"The box is this machine. It is on your tailnet. SSH from the internet closes once keelbox apply lab reaches it over the tailnet from another computer; keelbox doctor lab says whether it has."},
		"local, pending": {local(nil), pending, false,
			"The box is this machine. It is not on your tailnet yet, so SSH from the internet is still open. It closes at the next keelbox apply from another machine on the tailnet, after keelbox login lab tailscale."},
		"local, no Tailscale": {local(noTailscale), upState{}, false,
			"The box is this machine. Without Tailscale its SSH stays open to the LAN and the internet."},
		"local, kept open": {local(mode(config.PublicSSHOpen)), upState{}, false,
			"The box is this machine. It is on your tailnet; SSH stays open to the LAN and the internet (host.firewall.public_ssh: open)."},
	} {
		if got := reachSentence(ui.Style{}, c.cfg, c.st, c.reached); got != c.want {
			t.Errorf("%s:\n%q, want\n%q", name, got, c.want)
		}
		out := readyScreen(ui.Style{}, c.cfg, c.st, c.reached, "")
		if !strings.Contains(out, " "+c.want+"\n\nStart working:") {
			t.Errorf("%s: the screen lacks the sentence:\n%s", name, out)
		}
		for _, word := range []string{"secure", "safe", "sandbox"} {
			if strings.Contains(strings.ToLower(out), word) {
				t.Errorf("%s: the screen says %q:\n%s", name, word, out)
			}
		}
	}
}

// ready reads this computer's record of reaching the box over the tailnet:
// with it the screen says SSH is closed, without it what closes it.
func TestReadyReadsTheTailnetRecord(t *testing.T) {
	isolate(t)
	cfg := readyConfig(t, serverBox("lab", "203.0.113.7"))
	screen := func() string {
		var b strings.Builder
		(&wizard{out: &b, start: time.Now()}).ready(cfg, upState{})
		return b.String()
	}
	if out := screen(); strings.Contains(out, "is closed") || !strings.Contains(out, "SSH from the internet closes once keelbox apply lab reaches the box over the tailnet") {
		t.Errorf("without a record:\n%s", out)
	}
	if err := saveRecord(cfg, boxRecord{TailnetHost: "100.64.0.7", TailnetPort: 22}); err != nil {
		t.Fatal(err)
	}
	if out := screen(); !strings.Contains(out, "SSH from the internet is closed; the box is on your tailnet.") || !strings.HasPrefix(out, "\nlab is set up.\n") {
		t.Errorf("with a record:\n%s", out)
	}
	// A run that completed a stage says how long it took.
	var b strings.Builder
	(&wizard{out: &b, start: time.Now().Add(-288 * time.Second), progressed: true}).ready(cfg, upState{})
	if !strings.HasPrefix(b.String(), "\nlab is set up in 4m48s.\n") {
		t.Errorf("with a time:\n%s", b.String())
	}
}

// The card's rows: this computer always, the Claude app only while Remote
// Control runs, and the phone over the tailnet or at the box's address.
func TestReadyStartRows(t *testing.T) {
	isolate(t)
	rc := serverBox("lab", "203.0.113.7")
	rc.RemoteControl = true
	noTS := serverBox("lab", "203.0.113.7")
	noTS.NoTailscale = true
	const (
		attach    = "  This computer   keelbox attach lab\n                  opens the agent's terminal on the box. Close it and the work carries on; run it again to come back.\n"
		app       = "\n  Claude app      Remote Control: the Code tab in the Claude app, or claude.ai/code. Sessions run on lab.\n"
		phone     = "\n  Phone over SSH  add your phone's key to the box first:\n                  https://keelbox.dev/docs/guides/sessions\n"
		phoneAddr = "\n  Phone over SSH  ssh agent@203.0.113.7 from an SSH app, once your phone's key is on the box:\n                  https://keelbox.dev/docs/guides/sessions\n"
	)
	for name, c := range map[string]struct {
		cfg  *config.Config
		st   upState
		want string // the card, after its title
	}{
		"Remote Control off": {readyConfig(t, serverBox("lab", "203.0.113.7")), upState{}, attach + phone},
		"Remote Control on":  {readyConfig(t, rc), upState{}, attach + app + phone},
		"Remote Control waits on the sign-in": {readyConfig(t, rc), upState{Left: []string{config.AgentClaudeCode}},
			attach + phone},
		"Remote Control waits on the first run": {readyConfig(t, rc), upState{Left: []string{"remote-control"}},
			attach + phone},
		"tailnet pending": {readyConfig(t, rc), upState{Address: "203.0.113.7", Left: []string{"tailscale"}},
			attach + app + phoneAddr},
		"no Tailscale": {readyConfig(t, noTS), upState{}, attach + phoneAddr},
		"no tmux": {readyConfig(t, serverBox("lab", "203.0.113.7"), noTmux), upState{},
			"  This computer   keelbox ssh lab\n                  opens a shell as the agent on the box. sessions.tmux is off, so there is no session to come back to.\n" + phone},
		"local": {readyConfig(t, thisMachine("lab")), upState{},
			strings.Replace(attach, "on the box", "on this machine", 1) + phone},
	} {
		out := readyScreen(ui.Style{}, c.cfg, c.st, false, "")
		if !strings.Contains(out, "\n\nStart working:\n"+c.want+"\n") {
			t.Errorf("%s: the card is not\n%s\nin:\n%s", name, c.want, out)
		}
	}
	// While Remote Control waits, Left to do says on what, with the command.
	for left, want := range map[string]string{
		config.AgentClaudeCode: "  Left to do      sign in to Claude Code (Remote Control starts after): keelbox login lab claude-code\n",
		"remote-control":       "  Left to do      Claude Code Remote Control: keelbox login lab claude-code, and answer y to both of Claude Code's first-run questions\n",
	} {
		if out := readyScreen(ui.Style{}, readyConfig(t, rc), upState{Left: []string{left}}, false, ""); !strings.Contains(out, want) {
			t.Errorf("%s left: no %q in:\n%s", left, want, out)
		}
	}
}

// Left to do lists every sign-in left and the missing git identity, one a
// line under the first, and is not there when nothing is left.
func TestReadyLeftToDo(t *testing.T) {
	isolate(t)
	all := upState{Left: []string{"tailscale", config.AgentClaudeCode, config.AgentCodex, "gh"}}
	noGit := serverBox("lab", "203.0.113.7")
	noGit.GitName = "none"
	out := readyScreen(ui.Style{}, readyConfig(t, noGit), all, false, "")
	want := "\n" +
		"  Left to do      join the tailnet: keelbox login lab tailscale, then keelbox apply lab (closes public SSH)\n" +
		"                  sign in to Claude Code: keelbox login lab claude-code\n" +
		"                  sign in to Codex: keelbox login lab codex\n" +
		"                  sign in to GitHub: keelbox login lab gh (until then the agent's work stays on lab, unless it is pushed another way or copied off)\n" +
		"                  git identity: none in the config, and without one git on lab refuses to commit. Add users.agent.git: {name: Your Name, email: you@example.com} to ~/.config/keelbox/boxes/lab.yaml, then keelbox apply lab\n" +
		"  Check the box   keelbox doctor lab        all boxes: keelbox ls\n"
	if !strings.Contains(out, want) {
		t.Errorf("left to do is not\n%s\nin:\n%s", want, out)
	}
	// On this machine joining the tailnet does not close SSH by itself.
	if out := readyScreen(ui.Style{}, readyConfig(t, thisMachine("lab")), upState{Left: []string{"tailscale"}}, false, ""); !strings.Contains(out,
		"  Left to do      join the tailnet: keelbox login lab tailscale; public SSH closes at the next keelbox apply from a machine on the tailnet\n") {
		t.Errorf("local, tailnet pending:\n%s", out)
	}
	only := readyScreen(ui.Style{}, readyConfig(t, noGit), upState{}, false, "")
	if !strings.Contains(only, "  Left to do      git identity: none in the config, and without one git on lab refuses to commit.") {
		t.Errorf("the git identity alone:\n%s", only)
	}
	if out := readyScreen(ui.Style{}, readyConfig(t, serverBox("lab", "203.0.113.7")), upState{}, false, ""); strings.Contains(out, "Left to do") ||
		!strings.Contains(out, "https://keelbox.dev/docs/guides/sessions\n\n  Check the box   keelbox doctor lab") {
		t.Errorf("nothing left:\n%s", out)
	}
}

// Under the card: how to check the box, and the admin shell for what the
// agent's account cannot do. On this machine there is no admin shell row.
func TestReadyCheckRows(t *testing.T) {
	isolate(t)
	const check = "  Check the box   keelbox doctor lab        all boxes: keelbox ls\n"
	for name, c := range map[string]struct {
		cfg  *config.Config
		want string
	}{
		"agent without sudo": {readyConfig(t, serverBox("lab", "203.0.113.7")),
			check + "  Admin shell     keelbox ssh lab --admin        for anything that needs root; the agent has no sudo\n"},
		"agent with sudo": {readyConfig(t, serverBox("lab", "203.0.113.7"), agentSudo),
			check + "  Admin shell     keelbox ssh lab --admin        for anything that needs root\n"},
		"local": {readyConfig(t, thisMachine("lab")), check},
	} {
		if out := readyScreen(ui.Style{}, c.cfg, upState{}, false, ""); !strings.HasSuffix(out, "\n"+c.want) || strings.Count(out, "Admin shell") != strings.Count(c.want, "Admin shell") {
			t.Errorf("%s: the screen does not end with\n%s\nit is:\n%s", name, c.want, out)
		}
	}
	// A note that does not fit beside its command goes under it.
	out := readyScreen(liveStyle(80), readyConfig(t, serverBox("lab", "203.0.113.7")), upState{}, false, "")
	if !strings.HasSuffix(out, "\n  Check the box   keelbox doctor lab        all boxes: keelbox ls\n"+
		"  Admin shell     keelbox ssh lab --admin\n"+
		"                  for anything that needs root; the agent has no sudo\n") {
		t.Errorf("80 columns:\n%s", out)
	}
}

// Colour by role: commands in brass, links in sounding blue and as
// hyperlinks where the terminal shows them, labels dim, Left to do in bold
// brass.
func TestReadyColours(t *testing.T) {
	r := readyStates(t)["skipped"]
	ss := colourStyle(100)
	out := r.render(ss)
	for what, styled := range map[string]string{
		"the attach command": ss.Cmd("keelbox attach devbox-2"),
		"the phone command":  ss.Cmd("ssh agent@203.0.113.7"),
		"a sign-in command":  ss.Cmd("keelbox login devbox-2 codex"),
		"the doctor command": ss.Cmd("keelbox doctor devbox-2"),
		"the ls command":     ss.Dim("all boxes: ") + ss.Cmd("keelbox ls"),
		"the admin shell":    ss.Cmd("keelbox ssh devbox-2 --admin"),
		"the admin note":     ss.Dim("for anything that needs root; the agent has no sudo"),
		"the guide link":     "\x1b]8;;https://keelbox.dev/docs/guides/sessions\x1b\\" + ss.Info("https://keelbox.dev/docs/guides/sessions") + "\x1b]8;;\x1b\\",
		"a card label":       ss.Dim("This computer"),
		"a label under it":   ss.Dim("Check the box"),
		"Left to do":         "  " + ss.BoldAccent("Left to do") + "      join the tailnet",
		"the card's title":   ss.Heading("Start working"),
	} {
		if !strings.Contains(out, styled) {
			t.Errorf("%s is not in its colour (%q):\n%s", what, styled, out)
		}
	}
	on := readyStates(t)["done"].render(ss)
	if link := "\x1b]8;;https://claude.ai/code\x1b\\" + ss.Info("claude.ai/code") + "\x1b]8;;\x1b\\"; !strings.Contains(on, link) {
		t.Errorf("claude.ai/code is not a link:\n%s", on)
	}
	// A screen reader's style (no redraw, colour kept) gets the plain layout.
	acc := ui.Style{Colour: true, UTF8: true, Accessible: true}
	if got := ui.Strip(r.render(acc)); got != plainText(r) {
		t.Errorf("accessible:\n%s\nwant:\n%s", got, plainText(r))
	}
}

// The line that ends a cloud create says what its time covers, and in
// keelbox up that the sign-ins follow, so it is not read as the total.
func TestCreatedNote(t *testing.T) {
	cfg := readyConfig(t, cloudBox("lab"))
	bare := cloudBox("lab")
	bare.Agents, bare.NoTailscale, bare.NoGH = []string{}, true, true
	took := 178 * time.Second
	for name, c := range map[string]struct {
		cfg  *config.Config
		o    options
		want string
	}{
		"keelbox create":     {cfg, options{}, "server created and set up in 2m58s"},
		"keelbox up":         {cfg, options{inUp: true}, "server created and set up in 2m58s; the sign-ins come next"},
		"up, sign-ins later": {cfg, options{inUp: true, skipSignIn: true}, "server created and set up in 2m58s"},
		"up, none to do":     {readyConfig(t, bare), options{inUp: true}, "server created and set up in 2m58s"},
	} {
		if got := createdNote(c.cfg, c.o, took); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
}
