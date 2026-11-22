package projects

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/system"
)

func hx(s string) string { return hex.EncodeToString([]byte(s)) }

func TestParse(t *testing.T) {
	out := strings.Join([]string{
		"P " + hx("site") + " repo 0 0 1",
		"P " + hx("api") + " repo 3 2 1",
		"P " + hx("shop") + " norepo 0 0 0",
		"P " + hx("notes") + " repo 0 1 0",
		"P " + hx("slow") + " unknown 0 0 0",
		"P " + hx("evil\x1b[31m\nname") + " repo 1 0 1",
		"P zz repo 0 0 1",            // not hex
		"P " + hx("short") + " repo", // not a full line
		"something else",
		"T 9",
	}, "\n")
	p, ok := Parse(out)
	if !ok || p.Total != 9 || len(p.List) != 6 {
		t.Fatalf("%v %+v", ok, p)
	}
	var got []string
	for _, pr := range p.List {
		got = append(got, pr.Name+": "+pr.State())
	}
	// Work that is only on the box first, each group by name; the hostile
	// name is one plain line.
	want := []string{
		"api: 3 uncommitted changes, 2 commits not pushed",
		`evil\x1b[31m name: 1 uncommitted change`,
		"notes: no remote",
		"shop: not a git repository",
		"slow: could not be checked (git did not answer)",
		"site: clean and pushed",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("list:\n got %q\nwant %q", got, want)
	}
	if len(p.OnlyHere()) != 5 || p.Unlisted() != 3 {
		t.Errorf("only here %d, unlisted %d", len(p.OnlyHere()), p.Unlisted())
	}
	if _, ok := Parse("P " + hx("a") + " repo 0 0 1\n"); ok {
		t.Error("output cut before its total line parsed")
	}
	long, _ := Parse("P " + hx(strings.Repeat("n", 200)) + " norepo 0 0 0\nT 1\n")
	if n := len([]rune(long.List[0].Name)); n != nameMax {
		t.Errorf("a long name kept %d characters", n)
	}
}

func cfg(t *testing.T, extra string) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte("version: 1\nbox: acme\nusers:\n  admin:\n    ssh_authorized_keys: [\"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAdmin a@b\"]\n"+extra), "t.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The probe is one bounded command run as the agent, in the directory the
// config names, with the script on stdin and nothing of it in arguments.
func TestProbeRunsAsTheAgent(t *testing.T) {
	x := system.NewFake()
	x.AddUser("agent", 1001)
	var ran system.Cmd
	x.OnFunc("(as agent) sh -s --", func(c system.Cmd) (system.Result, error) {
		ran = c
		return system.Result{Stdout: "P " + hx("shop") + " norepo 0 0 0\nT 1\n"}, nil
	})
	p := Probe(context.Background(), x, cfg(t, "sessions:\n  remote_control: {enabled: true, directory: work}\n"))
	if p == nil || p.Dir != "~/work" || p.Total != 1 || p.List[0].Name != "shop" {
		t.Fatalf("%+v", p)
	}
	if ran.User != "agent" || ran.Timeout != Timeout || string(ran.Stdin) != script || strings.Join(ran.Args, " ") != "-s -- /home/agent/work 50 8 3" {
		t.Errorf("ran %+v", ran)
	}
	// No answer, or no agent account: no projects section, not an error.
	y := system.NewFake()
	if Probe(context.Background(), y, cfg(t, "")) != nil {
		t.Error("a probe without an agent account returned projects")
	}
	y.AddUser("agent", 1001)
	if Probe(context.Background(), y, cfg(t, "")) != nil {
		t.Error("a probe that failed returned projects")
	}
}

// The script against real git: one project in each state, and the things
// an account could plant.
func TestScript(t *testing.T) {
	for _, bin := range []string{"git", "sh", "timeout", "od"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip("no " + bin)
		}
	}
	root := t.TempDir()
	dir := filepath.Join(root, "projects")
	env := append(os.Environ(), "HOME="+root, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	sh := func(in, script string, args ...string) string {
		t.Helper()
		c := exec.Command("sh", append([]string{"-s", "--"}, args...)...)
		c.Dir, c.Env, c.Stdin = in, env, strings.NewReader(script)
		var out, errb bytes.Buffer
		c.Stdout, c.Stderr = &out, &errb
		if err := c.Run(); err != nil {
			t.Fatalf("%v\n%s", err, errb.String())
		}
		return out.String()
	}
	sh(root, `set -e
mkdir projects outside && cd projects
git init -q --bare -b main ../remote.git
mkdir plain && echo x > plain/file
git init -q -b main fresh
git init -q -b main noremote && (cd noremote && echo a > a && git add a && git commit -qm a)
git init -q -b main pushed && (cd pushed && echo a > a && git add a && git commit -qm a && git remote add origin ../../remote.git && git push -q -u origin main)
git clone -q ../remote.git ahead && (cd ahead && echo b > b && git add b && git commit -qm b && echo c > c && git add c && git commit -qm c)
git clone -q ../remote.git dirty && (cd dirty && echo d >> a && echo new > untracked)
git clone -q ../remote.git detached && (cd detached && git checkout -q --detach && echo z > z && git add z && git commit -qm z)
git clone -q ../remote.git ignored && (cd ignored && echo build > .gitignore && git add .gitignore && git commit -qm i && git push -q && mkdir build && echo o > build/o)
git init -q -b main unfetched && (cd unfetched && echo a > a && git add a && git commit -qm a && git remote add origin ../../remote.git)
mkdir -- '-dash' 'two words' .hidden
mkdir "$(printf 'esc\033[31m\nline')"
git init -q -b main ../outside/repo && ln -s ../outside/repo linked
touch afile
`)
	// Nothing in the directory changes: git status is run without its
	// optional index refresh.
	before := sh(dir, "find . -type f -newer ../remote.git/HEAD | wc -l; ls -la */.git/index | md5sum")
	out := sh(root, script, dir, "50", "8", "3")
	if after := sh(dir, "find . -type f -newer ../remote.git/HEAD | wc -l; ls -la */.git/index | md5sum"); after != before {
		t.Errorf("the probe changed the projects")
	}
	p, ok := Parse(out)
	if !ok {
		t.Fatalf("output:\n%s", out)
	}
	got := map[string]string{}
	for _, pr := range p.List {
		got[pr.Name] = pr.State()
	}
	want := map[string]string{
		"plain":            "not a git repository",
		"fresh":            "no remote",
		"noremote":         "no remote",
		"pushed":           "clean and pushed",
		"ahead":            "2 commits not pushed",
		"dirty":            "2 uncommitted changes",
		"detached":         "1 commit not pushed",
		"ignored":          "clean and pushed",
		"unfetched":        "1 commit not pushed",
		"-dash":            "not a git repository",
		"two words":        "not a git repository",
		`esc\x1b[31m line`: "not a git repository",
	}
	for name, state := range want {
		if got[name] != state {
			t.Errorf("%s: %q, want %q", name, got[name], state)
		}
	}
	// A symlink out of the directory, a hidden directory and a file are
	// not projects.
	if len(got) != len(want) || p.Total != len(want) {
		t.Errorf("%d projects, total %d, want %d:\n%v", len(got), p.Total, len(want), got)
	}

	// Past the limit directories are counted, not read; a directory that
	// is missing, or empty, is no projects.
	p, _ = Parse(sh(root, script, dir, "3", "8", "3"))
	if len(p.List) != 3 || p.Total != len(want) {
		t.Errorf("limit 3: %d listed of %d", len(p.List), p.Total)
	}
	// With no time left, repositories are reported as not checked, and so
	// as work that may be only here.
	p, _ = Parse(sh(root, script, dir, "50", "0", "3"))
	var unchecked []snapshot.Project
	for _, pr := range p.List {
		if pr.Unchecked {
			unchecked = append(unchecked, pr)
		}
	}
	if len(unchecked) != 8 || !unchecked[0].OnlyHere() {
		t.Errorf("no budget: %d unchecked", len(unchecked))
	}
	// A repository whose git does not come back (here, a hook of the
	// account's own that sleeps) costs its time limit and no more.
	sh(root, "mkdir slow && git init -q -b main slow/hang && cd slow/hang && echo a > a && git add a && git commit -qm a && git config core.fsmonitor 'sleep 30 #'")
	began := time.Now()
	p, _ = Parse(sh(root, script, filepath.Join(root, "slow"), "50", "8", "1"))
	if len(p.List) != 1 || !p.List[0].Unchecked || time.Since(began) > 10*time.Second {
		t.Errorf("a hanging git: %+v after %s", p.List, time.Since(began))
	}
	for _, d := range []string{filepath.Join(root, "missing"), filepath.Join(root, "outside", "repo", ".git", "hooks")} {
		if p, ok := Parse(sh(root, script, d, "50", "8", "3")); !ok || p.Total != 0 || len(p.List) != 0 {
			t.Errorf("%s: %+v", d, p)
		}
	}
}
