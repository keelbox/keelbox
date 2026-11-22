//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestWorkOnBox: nobody loses work on a box without being told (feedback
// item 35, ADR 0064). On a real VM: apply sets the agent's git identity
// through git and leaves the rest of its config alone; projects the agent
// makes in each state show in keelbox status from the laptop, in the
// agent's own status, in doctor's one warning and in what keelbox destroy
// reads before it deletes a server; names the agent wrote reach no
// terminal as escape sequences; and a box without an identity gets none
// written and a doctor warning.
func TestWorkOnBox(t *testing.T) {
	ensureKey(t)
	build(t)
	cfg, host := minimalBox(t, "agents: []\nruntime: {docker: false, node: false, gh: false}\naccess: {tailscale: {enabled: false}}\n",
		"    git: {name: E2E Agent, email: agent@example.com}\n")

	// Identity: the three keys, set with git as the agent.
	must(t, host, "agent", "git config --global user.name; git config --global user.email; git config --global init.defaultBranch",
		"E2E Agent\nagent@example.com\nmain")
	must(t, host, "agent", "stat -c '%U %a' ~/.gitconfig", "agent ")
	// What the agent put in its config stays; a changed identity is put
	// back; a default branch someone chose is kept.
	must(t, host, "agent", "git config --global alias.st status && git config --global init.defaultBranch trunk && git config --global user.email other@example.com")
	if out, err := kb(t, "apply", cfg); err != nil {
		t.Fatalf("second apply: %v\n%s", err, out)
	}
	must(t, host, "agent", "git config --global alias.st; git config --global init.defaultBranch; git config --global user.email",
		"status\ntrunk\nagent@example.com")
	if out, err := kb(t, "apply", cfg); err != nil || !strings.Contains(out, "No changes") {
		t.Fatalf("third apply changed something: %v\n%s", err, out)
	}
	logf(t, "PASS apply sets the agent's git identity with git config and keeps its other settings")

	// One project in each state, made by the agent with the identity
	// keelbox set (a commit fails without one), plus what an account can
	// plant: a name with an escape sequence, a symlink out of the directory.
	must(t, host, "agent", `set -e
mkdir -p ~/projects ~/remotes && cd ~/projects
git init -q --bare ~/remotes/origin.git
mkdir plain && echo notes > plain/notes.txt
git init -q noremote && (cd noremote && echo a > a && git add a && git commit -qm first)
git init -q pushed && (cd pushed && echo a > a && git add a && git commit -qm first && git remote add origin ~/remotes/origin.git && git push -q -u origin HEAD)
git clone -q ~/remotes/origin.git ahead && (cd ahead && echo b > b && git add b && git commit -qm second && echo c > c && git add c && git commit -qm third)
git clone -q ~/remotes/origin.git dirty && (cd dirty && echo more >> a && echo new > untracked)
mkdir "$(printf 'evil\033[2Jname')"
ln -s /etc way-out
git -C noremote log -1 --format='%an <%ae>'
`, "E2E Agent <agent@example.com>")

	rows := []string{
		"projects   6 in ~/projects, 5 with work that is only on this box",
		"           ahead: 2 commits not pushed",
		"           dirty: 2 uncommitted changes",
		`           evil\x1b[2Jname: not a git repository`,
		"           noremote: no remote",
		"           plain: not a git repository",
		"           pushed: clean and pushed",
	}
	plain := func(what, out string) {
		t.Helper()
		if strings.ContainsRune(out, '\x1b') {
			t.Errorf("%s carries an escape sequence from a project's name:\n%q", what, out)
		}
		if strings.Contains(out, "way-out") {
			t.Errorf("%s lists the symlink out of the projects directory:\n%s", what, out)
		}
	}
	// From the laptop: read now, as the agent, by root's status.
	out, err := kb(t, "status", cfg)
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	for _, want := range rows {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	plain("status", out)
	logf(t, "PASS keelbox status lists the projects:\n%s", out[strings.Index(out, "projects "):])

	// The probe ran as the agent and wrote nothing as root in its projects.
	must(t, host, "admin", "! sudo find /home/agent/projects /home/agent/remotes -user root | grep .")

	// The agent's own status, without sudo, from the snapshot the watch
	// run writes every two minutes.
	deadline := time.Now().Add(5 * time.Minute)
	for {
		out, err = sshRun(t, host, "agent", nil, "keelbox status 2>&1")
		if err == nil && strings.Contains(out, rows[0]) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the agent's status has no projects: %v\n%s", err, out)
		}
		time.Sleep(10 * time.Second)
	}
	for _, want := range rows {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("the agent's status lacks %q:\n%s", want, out)
		}
	}
	plain("the agent's status", out)
	must(t, host, "admin", "stat -c '%U %a' /var/lib/keelbox/status.json", "root 644")
	logf(t, "PASS the agent's keelbox status lists them from the snapshot, without sudo")

	// Doctor: one warning with counts and no names, never a failure.
	out, _ = kb(t, "doctor", cfg)
	warn := regexp.MustCompile(`(?m)^  warn  projects +5 of 6 in ~/projects hold work that exists only on this box \(2 not a git repository, 1 with no remote, 1 with uncommitted changes, 1 with commits not pushed\)\. List them: keelbox status \S+; then commit and push as the agent, or copy them off$`)
	if !warn.MatchString(out) || strings.Contains(out, "FAIL  projects") || strings.Contains(out, "noremote") {
		t.Errorf("doctor's projects warning:\n%s", out)
	}
	if !regexp.MustCompile(`(?m)^  ok    git-identity `).MatchString(out) {
		t.Errorf("doctor's git-identity:\n%s", out)
	}
	plain("doctor", out)
	logf(t, "PASS doctor warns once: %s", strings.TrimSpace(warn.FindString(out)))

	// What keelbox destroy reads before it deletes a server: the same
	// state, collected now, as JSON on one line.
	out, err = kb(t, "status", cfg, "--json")
	if err != nil {
		t.Fatalf("status --json: %v\n%s", err, out)
	}
	var snap struct {
		Projects struct {
			Dir   string
			Total int
			List  []struct {
				Name                  string
				Repo                  bool
				Uncommitted, Unpushed int
				NoRemote              bool `json:"no_remote"`
				Unchecked             bool
			}
		}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &snap); err != nil {
		t.Fatalf("status --json: %v\n%s", err, out)
	}
	got := map[string]string{}
	for _, p := range snap.Projects.List {
		state := "clean"
		switch {
		case !p.Repo:
			state = "norepo"
		case p.Unchecked:
			state = "unchecked"
		case p.NoRemote:
			state = "noremote"
		case p.Uncommitted > 0:
			state = "dirty"
		case p.Unpushed > 0:
			state = "ahead"
		}
		got[p.Name] = state
	}
	want := map[string]string{"plain": "norepo", `evil\x1b[2Jname`: "norepo", "noremote": "noremote", "dirty": "dirty", "ahead": "ahead", "pushed": "clean"}
	if snap.Projects.Dir != "~/projects" || snap.Projects.Total != 6 || len(got) != len(want) {
		t.Errorf("status --json projects: %+v", snap.Projects)
	}
	for name, state := range want {
		if got[name] != state {
			t.Errorf("status --json: %s is %q, want %q", name, got[name], state)
		}
	}
	plain("status --json", out)
	logf(t, "PASS status --json carries the projects keelbox destroy checks: %v", got)

	// Everything pushed or removed: the warning goes.
	must(t, host, "agent", `set -e
cd ~/projects
rm -rf plain evil* noremote
(cd ahead && git push -q)
(cd dirty && git checkout -q -- a && rm untracked)
`)
	out, _ = kb(t, "doctor", cfg)
	if !regexp.MustCompile(`(?m)^  ok    projects +3 in ~/projects, all clean and pushed$`).MatchString(out) {
		t.Errorf("doctor with everything pushed:\n%s", out)
	}
	logf(t, "PASS doctor is ok once everything is pushed")

	// A box without users.agent.git: nothing is written, and doctor says
	// commits will fail, with the fix.
	y, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(strings.Replace(string(y), "    git: {name: E2E Agent, email: agent@example.com}\n", "", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	must(t, host, "agent", "git config --global --unset user.name && git config --global --unset user.email")
	if out, err := kb(t, "apply", cfg); err != nil {
		t.Fatalf("apply without an identity: %v\n%s", err, out)
	}
	must(t, host, "agent", "! git config --global user.name && ! git config --global user.email && git config --global alias.st", "status")
	out, _ = kb(t, "doctor", cfg)
	if !regexp.MustCompile(`(?m)^  warn  git-identity +no git identity: commits will fail\. Add users\.agent\.git: \{name: Your Name, email: you@example\.com\} to the config, then keelbox apply \S+$`).MatchString(out) {
		t.Errorf("doctor without an identity:\n%s", out)
	}
	// git refuses unless it can guess an address from a fully qualified
	// host name; which this VM has is logged, not required.
	out, err = sshRun(t, host, "agent", nil, "cd ~/projects/pushed && echo z > z && git add z && git commit -m z 2>&1")
	logf(t, "a commit without an identity: refused %v: %s", err != nil && strings.Contains(out, "tell me who you are"), firstLine(out))
	logf(t, "PASS without users.agent.git nothing is written and doctor warns that commits will fail")
}
