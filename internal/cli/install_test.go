package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/installs"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/manifest"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/system"
	"github.com/keelbox/keelbox/internal/ui"
)

// installBox makes this process the agent on box devbox with
// users.agent.permissions mode: the mode file and root's record of the
// requests are in a temporary directory, the agent's home in another, and
// keelbox install's waits are short. It returns the home.
func installBox(t *testing.T, mode string) string {
	t.Helper()
	onBoxAs(t, "agent")
	dir, home := t.TempDir(), t.TempDir()
	oldMode, oldReq, oldHome := installModePath, installRequestsPath, eventHome
	oldAck, oldNudge, oldWait, oldPoll := installAckWait, installNudge, installWait, installPoll
	t.Cleanup(func() {
		installModePath, installRequestsPath, eventHome = oldMode, oldReq, oldHome
		installAckWait, installNudge, installWait, installPoll = oldAck, oldNudge, oldWait, oldPoll
	})
	installModePath, installRequestsPath = filepath.Join(dir, "mode.json"), filepath.Join(dir, "requests.json")
	eventHome = func() string { return home }
	installAckWait, installNudge, installWait, installPoll = 5*time.Second, 20*time.Millisecond, 5*time.Second, 2*time.Millisecond
	m, _ := json.Marshal(installs.Mode{Box: "devbox", Agent: "agent", Mode: mode})
	if err := os.WriteFile(installModePath, m, 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// rootAnswers stands in for root's install service: it waits for the
// agent's request file and writes, one after another, the states answer
// returns for it into root's record. It returns the request it read.
func rootAnswers(t *testing.T, home string, answer func(installs.Request) []installs.Entry) <-chan installs.Request {
	t.Helper()
	got := make(chan installs.Request, 1)
	go func() {
		file := filepath.Join(home, installs.AgentFile)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			data, err := os.ReadFile(file)
			var req installs.Request
			if err != nil || json.Unmarshal(data, &req) != nil {
				time.Sleep(time.Millisecond)
				continue
			}
			for _, en := range answer(req) {
				en.ID, en.Ref, en.By, en.Asked = req.ID, req.ID, "agent", time.Now().UTC()
				b, _ := json.Marshal(installs.State{Requests: []installs.Entry{en}})
				tmp := installRequestsPath + ".new"
				_ = os.WriteFile(tmp, b, 0o644)
				_ = os.Rename(tmp, installRequestsPath)
				time.Sleep(10 * time.Millisecond)
			}
			got <- req
			return
		}
		close(got)
	}()
	return got
}

func runInstall(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Main(append([]string{"install"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

// keelbox install as the agent, in each mode: what it prints and how it
// exits. Every decision is the root side's; the command asks and reports.
func TestInstallAsTheAgent(t *testing.T) {
	for name, tc := range map[string]struct {
		mode   string
		args   []string
		states func(req installs.Request) []installs.Entry
		code   int
		stdout []string
		stderr []string
	}{
		"auto installs": {
			mode: config.PermissionsAuto, args: []string{"libpq-dev", "jq", "--why", "psycopg builds against it"},
			states: func(req installs.Request) []installs.Entry {
				return []installs.Entry{
					{State: installs.StateInstalling, Packages: req.Packages},
					{State: installs.StateInstalled, Packages: req.Packages, Installed: []string{"libpq-dev"}},
				}
			},
			code: 0, stdout: []string{"Installing libpq-dev, jq (request ", "Installed libpq-dev. Already installed: jq.\n"},
		},
		"auto, already installed": {
			mode: config.PermissionsAuto, args: []string{"jq"},
			states: func(req installs.Request) []installs.Entry {
				return []installs.Entry{{State: installs.StateInstalled, Packages: req.Packages}}
			},
			code: 0, stdout: []string{"Already installed: jq. Nothing to do.\n"},
		},
		"full installs the same way": {
			mode: config.PermissionsFull, args: []string{"jq"},
			states: func(req installs.Request) []installs.Entry {
				return []installs.Entry{{State: installs.StateInstalling, Packages: req.Packages}, {State: installs.StateInstalled, Packages: req.Packages, Installed: req.Packages}}
			},
			code: 0, stdout: []string{"Installed jq.\n"},
		},
		"manual records and returns": {
			mode: config.PermissionsManual, args: []string{"libpq-dev"},
			states: func(req installs.Request) []installs.Entry {
				return []installs.Entry{{State: installs.StatePending, Packages: req.Packages}}
			},
			code: 3, stdout: []string{
				"is recorded: libpq-dev. Nothing is installed yet.\n",
				"On devbox the owner approves each request (users.agent.permissions: manual). Tell the user to run, from their computer:\n  keelbox approve devbox ",
				"(keelbox requests devbox lists what waits)",
				"Carry on with other work meanwhile. keelbox install --status shows what they decided.\n",
			},
		},
		"refused with the reason": {
			mode: config.PermissionsAuto, args: []string{"sudo"},
			states: func(req installs.Request) []installs.Entry {
				return []installs.Entry{{State: installs.StateRefused, Packages: req.Packages, Detail: "sudo is on keelbox's deny-list (privilege tools): they run commands as another account"}}
			},
			code: 1, stderr: []string{
				"keelbox: not installed: sudo is on keelbox's deny-list (privilege tools)",
				"Do not work around a refusal.",
				"as the admin: keelbox ssh devbox --admin, then sudo apt-get install sudo\n",
			},
		},
		"refused before the names passed": {
			mode: config.PermissionsAuto, args: []string{"jq=1.7"},
			states: func(installs.Request) []installs.Entry {
				return []installs.Entry{{State: installs.StateRefused, Detail: `"jq=1.7" is not a package name`}}
			},
			// No command is made of names root did not pass.
			code: 1, stderr: []string{`keelbox: not installed: "jq=1.7" is not a package name`, "as the admin: keelbox ssh devbox --admin\n"},
		},
		"apt failed": {
			mode: config.PermissionsAuto, args: []string{"brokenthing"},
			states: func(req installs.Request) []installs.Entry {
				return []installs.Entry{{State: installs.StateInstalling, Packages: req.Packages}, {State: installs.StateFailed, Packages: req.Packages, Detail: "apt-get did not install brokenthing: Sub-process /usr/bin/dpkg returned an error code (1)"}}
			},
			code: 1, stderr: []string{"keelbox: not installed: apt-get did not install brokenthing", "keelbox ssh devbox --admin, then sudo journalctl -u keelbox-install.service\n"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			home := installBox(t, tc.mode)
			got := rootAnswers(t, home, tc.states)
			code, out, errOut := runInstall(t, tc.args...)
			if code != tc.code {
				t.Errorf("exit %d, want %d\n%s%s", code, tc.code, out, errOut)
			}
			for _, w := range tc.stdout {
				if !strings.Contains(out, w) {
					t.Errorf("stdout lacks %q:\n%s", w, out)
				}
			}
			for _, w := range tc.stderr {
				if !strings.Contains(errOut, w) {
					t.Errorf("stderr lacks %q:\n%s", w, errOut)
				}
			}
			if tc.code != 0 && tc.code != 3 && out != "" && !strings.HasPrefix(out, "Installing ") {
				t.Errorf("a request that was not installed printed to stdout: %q", out)
			}
			req, ok := <-got
			if !ok {
				t.Fatal("no request file was written")
			}
			var names []string
			for _, a := range tc.args {
				if a == "--why" {
					break
				}
				names = append(names, a)
			}
			if req.V != 1 || !installs.ValidID(req.ID) || strings.Join(req.Packages, " ") != strings.Join(names, " ") || time.Since(req.Time) > time.Minute {
				t.Errorf("request %+v", req)
			}
			if fi, err := os.Stat(filepath.Join(home, installs.AgentFile)); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("the request file: %v %v", fi, err)
			}
		})
	}
}

// The reason goes to root as one line of plain text, whatever was typed.
func TestInstallWhyIsCleaned(t *testing.T) {
	home := installBox(t, config.PermissionsManual)
	got := rootAnswers(t, home, func(req installs.Request) []installs.Entry {
		return []installs.Entry{{State: installs.StatePending, Packages: req.Packages}}
	})
	if code, _, errOut := runInstall(t, "jq", "--why", "tests\x1b[2J need\nit"); code != 3 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if req := <-got; req.Why != "tests[2J need it" {
		t.Errorf("why %q", req.Why)
	}
}

// When root's service takes nothing up, keelbox install writes the request
// again while it waits, then says what to do.
func TestInstallWithNoAnswer(t *testing.T) {
	home := installBox(t, config.PermissionsAuto)
	installAckWait = 150 * time.Millisecond
	file := filepath.Join(home, installs.AgentFile)
	writes := make(chan int, 1)
	stop := make(chan struct{})
	go func() {
		n, last := 0, time.Time{}
		for {
			select {
			case <-stop:
				writes <- n
				return
			default:
			}
			if fi, err := os.Stat(file); err == nil && !fi.ModTime().Equal(last) {
				n, last = n+1, fi.ModTime()
			}
			time.Sleep(time.Millisecond)
		}
	}()
	code, out, errOut := runInstall(t, "jq")
	close(stop)
	if code != 1 || out != "" {
		t.Errorf("exit %d, stdout %q", code, out)
	}
	for _, w := range []string{"keelbox: not installed: request ", "root's install service did not take it up within 1", "run the same command again in a few minutes", "keelbox doctor devbox\n"} {
		if !strings.Contains(errOut, w) {
			t.Errorf("stderr lacks %q:\n%s", w, errOut)
		}
	}
	if n := <-writes; n < 2 {
		t.Errorf("the request was written %d time(s); want it written again while root does not answer", n)
	}
}

// The service takes one request at a time: while it installs another, a
// new request waits its turn past the usual wait, with a time that stays
// current.
func TestInstallWaitsBehindAnotherInstall(t *testing.T) {
	home := installBox(t, config.PermissionsAuto)
	installAckWait = 100 * time.Millisecond
	other := installs.Entry{ID: "0a1b2c3d", By: "agent", Asked: time.Now().UTC(), Packages: []string{"texlive"}, State: installs.StateInstalling}
	b, _ := json.Marshal(installs.State{Requests: []installs.Entry{other}})
	if err := os.WriteFile(installRequestsPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	read := make(chan installs.Request, 1)
	record := installRequestsPath
	go func() {
		// Root answers well after the usual wait, once the other is done.
		time.Sleep(400 * time.Millisecond)
		var req installs.Request
		data, _ := os.ReadFile(filepath.Join(home, installs.AgentFile))
		_ = json.Unmarshal(data, &req)
		read <- req
		other.State = installs.StateInstalled
		mine := installs.Entry{ID: req.ID, Ref: req.ID, By: "agent", Asked: time.Now().UTC(), Packages: req.Packages, State: installs.StateInstalled, Installed: req.Packages}
		b, _ := json.Marshal(installs.State{Requests: []installs.Entry{other, mine}})
		_ = os.WriteFile(record+".new", b, 0o644)
		_ = os.Rename(record+".new", record)
	}()
	code, out, errOut := runInstall(t, "sl")
	if code != 0 || !strings.Contains(out, "Installed sl.") {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	// Re-dated past the usual wait: a fixed margin closer to root's read
	// fails on a loaded machine, where the loop can stall for a while.
	if last := <-read; last.Time.Sub(began) <= installAckWait {
		t.Errorf("the request root read was dated %s after the command began; want it written again after the usual wait of %s", last.Time.Sub(began), installAckWait)
	}
}

func TestInstallUsageAndWhoRunsIt(t *testing.T) {
	home := installBox(t, config.PermissionsAuto)
	file := filepath.Join(home, installs.AgentFile)
	many := make([]string, installs.MaxNames+1)
	for i := range many {
		many[i] = fmt.Sprintf("pkg%d", i)
	}
	for name, tc := range map[string]struct {
		args []string
		code int
		says string
	}{
		"no package":     {nil, 2, `keelbox install libpq-dev --why "psycopg builds against it"`},
		"an option":      {[]string{"-y", "jq"}, 2, "install has no flag --y; did you mean --why? List its flags: keelbox help install"},
		"apt's option":   {[]string{"--no-install-recommends", "jq"}, 2, "install has no flag --no-install-recommends; list its flags: keelbox help install"},
		"21 names":       {many, 1, "21 package names in one request; the most is 20"},
		"not names":      {[]string{strings.Repeat("a", 5000)}, 1, "those are not package names"},
		"status and one": {[]string{"--status", "jq"}, 2, "install --status takes no package name"},
	} {
		code, out, errOut := runInstall(t, tc.args...)
		if code != tc.code || !strings.Contains(errOut, tc.says) || out != "" {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want %d saying %q", name, code, out, errOut, tc.code, tc.says)
		}
		if _, err := os.Stat(file); err == nil {
			t.Errorf("%s: a request was written", name)
		}
	}

	// Another account on the box is told what it can do instead.
	currentUser = func() string { return "admin" }
	if code, _, errOut := runInstall(t, "jq"); code != 2 || !strings.Contains(errOut, "keelbox install is for the agent account agent on devbox; you are admin") || !strings.Contains(errOut, "sudo apt-get install NAME") {
		t.Errorf("as the admin: exit %d %s", code, errOut)
	}
	// Off a box there is no install service to ask.
	currentUser = func() string { return "agent" }
	installModePath = filepath.Join(t.TempDir(), "none.json")
	if code, _, errOut := runInstall(t, "jq"); code != 1 || !strings.Contains(errOut, "this machine has no install service set up") || !strings.HasSuffix(errOut, "keelbox ssh BOX, then keelbox install NAME\n") {
		t.Errorf("off a box: exit %d %s", code, errOut)
	}
}

func TestInstallStatus(t *testing.T) {
	installBox(t, config.PermissionsManual)
	code, out, _ := runInstall(t, "--status")
	if code != 0 || !strings.Contains(out, "No package requests on devbox yet (users.agent.permissions: manual).") {
		t.Errorf("no requests: exit %d\n%s", code, out)
	}
	at := time.Date(2026, 11, 15, 14, 2, 0, 0, time.Local)
	st := installs.State{Requests: []installs.Entry{
		{ID: "3f2a9c1e", Asked: at, By: "agent", Packages: []string{"jq"}, State: installs.StateInstalled, Installed: []string{"jq"}},
		{ID: "8c1d2e3f", Asked: at, By: "agent", Packages: []string{"sudo"}, State: installs.StateRefused, Detail: "sudo is on keelbox's deny-list (privilege tools)"},
		{ID: "0a1b2c3d", Asked: at, By: "agent", Packages: []string{"libpq-dev"}, State: installs.StatePending, Why: "psycopg"},
	}}
	b, _ := json.Marshal(st)
	if err := os.WriteFile(installRequestsPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runInstall(t, "--status")
	want := "Package requests on devbox (users.agent.permissions: manual), oldest first:\n" +
		"ID        ASKED             BY        STATE       PACKAGES\n" +
		"3f2a9c1e  2026-11-15 14:02  agent     installed   jq\n" +
		"8c1d2e3f  2026-11-15 14:02  agent     refused     sudo\n" +
		"          sudo is on keelbox's deny-list (privilege tools)\n" +
		"0a1b2c3d  2026-11-15 14:02  agent     pending     libpq-dev\n" +
		"\n1 waiting for approval: the user runs keelbox approve devbox from their computer.\n"
	if code != 0 || out != want {
		t.Errorf("exit %d\n%s\nwant\n%s", code, out, want)
	}
	// The agent's own status says a request waits.
	var sout, serr bytes.Buffer
	if code := Main([]string{"status"}, &sout, &serr); code != 0 || !strings.Contains(sout.String(), "requests   1 waiting for the owner's approval (keelbox install --status)") {
		t.Errorf("status: exit %d\n%s%s", code, sout.String(), serr.String())
	}
}

// The agent does not approve its own requests: the owner's commands say
// where they run, and never call sudo.
func TestAgentCannotApprove(t *testing.T) {
	installBox(t, config.PermissionsManual)
	for _, cmd := range [][]string{{"approve", "3f2a9c1e"}, {"approve", "--all"}, {"deny", "3f2a9c1e"}, {"requests"}} {
		var out, errb bytes.Buffer
		if code := Main(cmd, &out, &errb); code != 2 || !strings.Contains(errb.String(), "keelbox "+cmd[0]+" runs on the owner's laptop") {
			t.Errorf("%v: exit %d %s", cmd, code, errb.String())
		}
	}
	if !strings.Contains(agentHelp("devbox"), "keelbox install NAME    ask root's install service for an Ubuntu package") {
		t.Errorf("the agent's help does not list install:\n%s", agentHelp("devbox"))
	}
}

// aptBox is a root-side box for the owner's commands: a fake apt that has
// every package, and one apply in the logbook.
type aptBox struct {
	*system.Fake
	installed map[string]bool
	removes   map[string][]string
}

func ownerEnv(t *testing.T, mode string, packages ...string) (*engine.Env, *aptBox) {
	t.Helper()
	b := &aptBox{Fake: system.NewFake(), installed: map[string]bool{"ufw": true}, removes: map[string][]string{}}
	b.AddUser("agent", 1001)
	ok := func(c system.Cmd, out string) (system.Result, error) { return system.Result{Stdout: out}, nil }
	b.OnFunc("apt-cache policy", func(c system.Cmd) (system.Result, error) {
		var out strings.Builder
		for _, n := range c.Args[2:] {
			inst := "(none)"
			if b.installed[n] {
				inst = "1"
			}
			fmt.Fprintf(&out, "%s:\n  Installed: %s\n  Candidate: 1\n", n, inst)
		}
		return ok(c, out.String())
	})
	b.OnFunc("dpkg-query", func(c system.Cmd) (system.Result, error) {
		var out strings.Builder
		for _, n := range c.Args[2:] {
			if b.installed[n] {
				fmt.Fprintf(&out, "%s ii \n", n)
			}
		}
		return ok(c, out.String())
	})
	b.OnFunc("apt-get", func(c system.Cmd) (system.Result, error) {
		s := c.String()
		i := strings.Index(s, " -- ")
		if !strings.Contains(s, " install ") || i < 0 {
			return ok(c, "")
		}
		names := strings.Fields(s[i+4:])
		var out strings.Builder
		for _, n := range names {
			if r := b.removes[n]; len(r) > 0 {
				res := system.Result{ExitCode: 100, Stdout: "The following packages will be REMOVED:\n  " + strings.Join(r, " ") + "\n", Stderr: "E: Packages need to be removed but remove is disabled."}
				return res, &system.ExitError{Cmd: c, Result: res}
			}
			fmt.Fprintf(&out, "Inst %s (1 Ubuntu:26.04/resolute [amd64])\n", n)
			if !strings.Contains(s, " -s ") {
				b.installed[n] = true
			}
		}
		return ok(c, out.String())
	})
	y := fmt.Sprintf("version: 1\nbox: acme\nhost:\n  packages: [%s]\nusers:\n  admin:\n    ssh_authorized_keys: [\"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAdmin admin@laptop\"]\n  agent:\n    permissions: %s\n",
		strings.Join(packages, ", "), mode)
	cfg, err := config.Parse([]byte(y), "acme.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := logbook.Append(b, logbook.Record{Time: time.Now().UTC().Add(-time.Hour), Box: "acme", Keelbox: "test", Release: "2026.11.4", Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	e := &engine.Env{X: b, Cfg: cfg, M: &manifest.Manifest{Release: "2026.11.4"}, Facts: engine.Facts{OSVersion: "26.04", Arch: "amd64"}}
	// keelbox approve starts the service; here the service is a call.
	old, oldSettle := startInstalls, approveSettle
	startInstalls = func(ctx context.Context, e *engine.Env) error {
		_, err := installs.Run(ctx, e, installs.Options{})
		return err
	}
	approveSettle = time.Millisecond
	t.Cleanup(func() { startInstalls, approveSettle = old, oldSettle })
	return e, b
}

// asks has the agent ask for packages and root's service take it up; it
// returns root's ID for the request, the one the owner approves.
func (b *aptBox) asks(t *testing.T, e *engine.Env, why string, pkgs ...string) string {
	t.Helper()
	ref := installs.NewID()
	data, _ := json.Marshal(installs.Request{V: 1, ID: ref, Time: time.Now().UTC(), Packages: pkgs, Why: why})
	if err := b.UserFS("agent").WriteFile("/home/agent/"+installs.AgentFile, data, 0o600, "agent", "agent"); err != nil {
		t.Fatal(err)
	}
	if _, err := installs.Run(context.Background(), e, installs.Options{}); err != nil {
		t.Fatal(err)
	}
	st, err := installs.ReadState(b)
	if err != nil || st.FindRef(ref) == nil {
		t.Fatalf("root has no record of the request: %v", err)
	}
	return st.FindRef(ref).ID
}

func TestRequestsApproveDeny(t *testing.T) {
	ctx := context.Background()
	e, b := ownerEnv(t, config.PermissionsManual)
	var out, errb bytes.Buffer
	run := func(f func() int) (int, string, string) {
		out.Reset()
		errb.Reset()
		code := f()
		return code, out.String(), errb.String()
	}
	requests := func(o options) func() int { return func() int { return Requests(e, o, &out, &errb) } }
	approve := func(o options) func() int {
		return func() int { return approveRequests(ctx, e, o, installs.Options{}, &out, &errb) }
	}
	deny := func(o options) func() int {
		return func() int { return denyRequest(e, o, installs.Options{}, &out, &errb) }
	}

	// Nothing waiting.
	code, got, _ := run(requests(options{}))
	if code != 0 || !strings.HasPrefix(got, "0 waiting for approval on acme.\nThe agent's package requests wait here for you. (users.agent.permissions: manual)") {
		t.Errorf("no requests: exit %d\n%s", code, got)
	}
	if code, got, _ := run(approve(options{})); code != 0 || got != "0 waiting for approval on acme.\n" {
		t.Errorf("approve with nothing waiting: exit %d %q", code, got)
	}

	// One request, with a reason that tries to draw on the terminal.
	id1 := b.asks(t, e, "psycopg builds \x1b]0;owned\x07against it\nAPPROVED", "libpq-dev")
	code, got, _ = run(requests(options{}))
	if code != 0 || !strings.HasPrefix(got, "1 waiting for approval on acme.\n\n") {
		t.Errorf("requests: exit %d\n%s", code, got)
	}
	for _, w := range []string{
		"ID        ASKED             BY        STATE       PACKAGES\n" + id1 + "  ",
		"  agent     pending     libpq-dev\n",
		"          the agent's reason: psycopg builds ]0;ownedagainst it APPROVED\n",
		"The package names passed keelbox's checks; a reason is the agent's own text. Approve: keelbox approve acme ID (or --all). Deny: keelbox deny acme ID.\n",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("requests lacks %q:\n%s", w, got)
		}
	}
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("requests printed a control character: %q", got)
	}

	// Approve it by ID: the service installs it.
	code, got, errOut := run(approve(options{request: id1}))
	want := "Approved request " + id1 + ": installing libpq-dev for agent...\nRequest " + id1 + ": installed libpq-dev.\n0 waiting for approval on acme.\n"
	if code != 0 || got != want || errOut != "" {
		t.Errorf("approve: exit %d\n%s%s\nwant\n%s", code, got, errOut, want)
	}
	if !b.installed["libpq-dev"] || !b.Ran("apt-get -o DPkg::Lock::Timeout=600 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold install -y --no-remove -- libpq-dev") {
		t.Error("the approved request was not installed with --no-remove")
	}
	if code, _, errOut := run(approve(options{request: id1})); code != 1 || !strings.HasSuffix(errOut, "List the ones that do: keelbox requests acme\n") {
		t.Errorf("approve again: exit %d %s", code, errOut)
	}

	// Two waiting: approve needs to know which; deny one, approve --all.
	id2, id3 := b.asks(t, e, "", "jq"), b.asks(t, e, "", "tree")
	code, got, errOut = run(approve(options{}))
	if code != 2 || !strings.HasPrefix(got, "2 waiting for approval on acme.\n") || !strings.Contains(errOut, "keelbox approve acme ID, or all of them: keelbox approve acme --all\n") {
		t.Errorf("approve with two waiting: exit %d\n%s%s", code, got, errOut)
	}
	code, got, _ = run(deny(options{request: id2}))
	if code != 0 || got != "Denied request "+id2+" (jq); nothing was installed.\n1 waiting for approval on acme.\n" {
		t.Errorf("deny: exit %d %q", code, got)
	}
	if code, _, errOut := run(deny(options{request: "ffffffff"})); code != 1 || !strings.Contains(errOut, "no request ffffffff waits on acme. List the ones that do: keelbox requests acme\n") {
		t.Errorf("deny of an unknown ID: exit %d %s", code, errOut)
	}
	code, got, _ = run(approve(options{all: true}))
	if code != 0 || !strings.Contains(got, "Request "+id3+": installed tree.\n0 waiting for approval on acme.\n") || b.installed["jq"] {
		t.Errorf("approve --all: exit %d\n%s", code, got)
	}

	// --all lists what was decided, the denied one included.
	code, got, _ = run(requests(options{all: true}))
	if code != 0 || !strings.Contains(got, "Decided, oldest first:\n") || !strings.Contains(got, "  agent     denied      jq\n") || !strings.Contains(got, "  agent     installed   tree\n") {
		t.Errorf("requests --all: exit %d\n%s", code, got)
	}

	// A request the box no longer allows is refused at approval.
	id4 := b.asks(t, e, "", "some-tool")
	b.removes["some-tool"] = []string{"ufw"}
	code, got, errOut = run(approve(options{request: id4}))
	if code != 1 || !strings.Contains(errOut, "keelbox: request "+id4+" was not installed: installing some-tool would remove ufw") ||
		!strings.HasSuffix(errOut, "To install it yourself: keelbox ssh acme --admin, then sudo apt-get install some-tool\n") || !b.installed["ufw"] {
		t.Errorf("approve of a request that would remove a package: exit %d\n%s%s", code, got, errOut)
	}

	// The service does not run: the approval stands and the fix is said.
	id5 := b.asks(t, e, "", "cowsay")
	startInstalls = func(context.Context, *engine.Env) error { return errors.New("Unit keelbox-install.service not found") }
	code, _, errOut = run(approve(options{request: id5}))
	if code != 1 || !strings.Contains(errOut, "keelbox-install.service did not run") || !strings.HasSuffix(errOut, "start it again: keelbox approve acme "+id5+"\n") {
		t.Errorf("approve without a service: exit %d %s", code, errOut)
	}
	// A run that leaves it (an apply holds the lock): asked again, then said.
	starts := 0
	startInstalls = func(context.Context, *engine.Env) error { starts++; return nil }
	code, _, errOut = run(approve(options{request: id5}))
	if code != 1 || starts != approveTries || !strings.Contains(errOut, "request "+id5+" is approved, and keelbox-install.service has not installed it yet") ||
		!strings.HasSuffix(errOut, "Start it again: keelbox approve acme "+id5+"\n") {
		t.Errorf("approve while the service leaves it: exit %d, %d starts\n%s", code, starts, errOut)
	}
	// An approved request the service has not finished is listed without
	// --all, and can still be denied.
	code, got, _ = run(requests(options{}))
	if code != 0 || !strings.HasPrefix(got, "0 waiting for approval on acme.\n\nApproved and not installed yet;") ||
		!strings.Contains(got, "To start it again: keelbox approve acme ID. To stop one it has not begun: keelbox deny acme ID.\n") || !strings.Contains(got, id5+"  ") || !strings.Contains(got, "  approved    cowsay\n") {
		t.Errorf("requests with an approved request left: exit %d\n%s", code, got)
	}
	if code, got, _ := run(deny(options{request: id5})); code != 0 || !strings.HasPrefix(got, "Denied request "+id5+" (cowsay); nothing was installed.\n") || b.installed["cowsay"] {
		t.Errorf("deny of an approved request: exit %d %q", code, got)
	}
}

// With permissions auto, keelbox requests says requests are not held here.
func TestRequestsOnAnAutoBox(t *testing.T) {
	e, b := ownerEnv(t, config.PermissionsAuto)
	b.asks(t, e, "", "jq")
	var out, errb bytes.Buffer
	if code := Requests(e, options{}, &out, &errb); code != 0 || !strings.Contains(out.String(), "0 waiting for approval on acme.\nPackages the agent asks for with keelbox install are checked and installed without asking. (users.agent.permissions: auto)") {
		t.Errorf("exit %d\n%s", code, out.String())
	}
}

// keelbox status shows what was installed for the agent, the YAML that
// keeps it when host.packages does not name it, and how many requests wait;
// keelbox apply says the YAML once.
func TestStatusAndApplyNameAgentPackages(t *testing.T) {
	ctx := context.Background()
	e, b := ownerEnv(t, config.PermissionsAuto, "htop", "libpq-dev")
	var out bytes.Buffer
	installRows(ctx, e, &out)
	if out.String() != "" {
		t.Errorf("rows on a box with no requests: %q", out.String())
	}
	b.asks(t, e, "", "sl", "libpq-dev")
	installRows(ctx, e, &out)
	want := "packages   agent-installed: libpq-dev, sl (2)\n" +
		"           not in host.packages: sl. A box rebuilt from the config would not have them; to keep them, acme's config should read:\n" +
		"           host:\n             packages:\n               - htop\n               - libpq-dev\n               - sl\n"
	if out.String() != want {
		t.Errorf("status rows:\n%s\nwant:\n%s", out.String(), want)
	}
	// The styled status still reads the row as a row.
	var styled bytes.Buffer
	r := ui.NewReport(&styled, ui.Style{}, true)
	_, _ = r.Write([]byte(want))
	r.Close()
	// It may wrap a long row under itself; the words and their order stay.
	if got := styled.String(); strings.Join(strings.Fields(got), " ") != strings.Join(strings.Fields(want), " ") || !strings.HasPrefix(got, "packages   agent-installed: libpq-dev, sl (2)\n") {
		t.Errorf("the status report changed the rows:\n%s", got)
	}

	out.Reset()
	applyPackagesNote(ctx, e, &out)
	note := "Installed for the agent and not in host.packages: sl. A box rebuilt from the config would not have them; to keep them, acme's config should read:\n" +
		"    host:\n      packages:\n        - htop\n        - libpq-dev\n        - sl\n"
	if out.String() != note {
		t.Errorf("after apply:\n%s\nwant:\n%s", out.String(), note)
	}
	out.Reset()
	if applyPackagesNote(ctx, e, &out); out.String() != "" {
		t.Errorf("the next apply said it again: %q", out.String())
	}
	// Another package installed since: said again, once.
	b.asks(t, e, "", "tree")
	if applyPackagesNote(ctx, e, &out); !strings.Contains(out.String(), "not in host.packages: sl, tree.") {
		t.Errorf("after another install: %q", out.String())
	}
	// A package keelbox installs on every box needs no line in the config.
	b.asks(t, e, "", "jq")
	if _, unlisted := unlistedPackages(ctx, e); strings.Join(unlisted, " ") != "sl tree" {
		t.Errorf("unlisted with a base package installed for the agent: %q", unlisted)
	}
	// With the config naming them, nothing is said.
	e.Cfg.Host.Packages = append(e.Cfg.Host.Packages, "sl", "tree")
	out.Reset()
	installRows(ctx, e, &out)
	if out.String() != "packages   agent-installed: jq, libpq-dev, sl, tree (4)\n" {
		t.Errorf("rows once the config names them: %q", out.String())
	}
	if applyPackagesNote(ctx, e, &out); strings.Contains(out.String(), "host:") {
		t.Errorf("apply note once the config names them: %q", out.String())
	}

	// A manual box says how many wait, none included.
	m, mb := ownerEnv(t, config.PermissionsManual)
	out.Reset()
	installRows(ctx, m, &out)
	if out.String() != "requests   0 waiting for approval\n" {
		t.Errorf("manual, nothing waiting: %q", out.String())
	}
	mb.asks(t, m, "", "jq")
	mb.asks(t, m, "", "tree")
	out.Reset()
	installRows(ctx, m, &out)
	if out.String() != "requests   2 waiting for approval: keelbox requests acme\n" {
		t.Errorf("manual, two waiting: %q", out.String())
	}
}

func TestRequestArg(t *testing.T) {
	isolate(t)
	register(t, serverBox("deadbeef", "203.0.113.7"))
	for _, tc := range []struct {
		pos  []string
		rest []string
		id   string
	}{
		{[]string{"acme", "3f2a9c1e"}, []string{"acme"}, "3f2a9c1e"},
		{[]string{"3f2a9c1e", "acme"}, []string{"acme"}, "3f2a9c1e"},
		{[]string{"3f2a9c1e"}, []string{}, "3f2a9c1e"},
		{[]string{"acme"}, []string{"acme"}, ""},
		{[]string{"acme", "3f2a"}, []string{"acme", "3f2a"}, ""},
		{[]string{"acme", "3F2A9C1E"}, []string{"acme", "3F2A9C1E"}, ""},
		{[]string{"deadbeef"}, []string{"deadbeef"}, ""},
		{[]string{"deadbeef", "3f2a9c1e"}, []string{"deadbeef"}, "3f2a9c1e"},
		{[]string{"/run/keelbox/keelbox.yaml", "3f2a9c1e"}, []string{"/run/keelbox/keelbox.yaml"}, "3f2a9c1e"},
		{nil, nil, ""},
	} {
		rest, id := requestArg(tc.pos)
		if id != tc.id || strings.Join(rest, " ") != strings.Join(tc.rest, " ") {
			t.Errorf("requestArg(%q) = %q, %q; want %q, %q", tc.pos, rest, id, tc.rest, tc.id)
		}
	}
	// What is not a box and an ID is said before any box is reached.
	for _, args := range [][]string{{"deny", "deadbeef"}, {"approve", "deadbeef", "3f2a", "x"}, {"approve", "deadbeef", "3f2a9c1e", "--all"}, {"deny", "deadbeef", "12"}} {
		var out, errb bytes.Buffer
		if code := Main(args, &out, &errb); code != 2 || !strings.Contains(errb.String(), "takes a box and a request ID, 8 hex digits as keelbox requests BOX lists them: keelbox "+args[0]+" acme 3f2a9c1e") {
			t.Errorf("%v: exit %d %s", args, code, errb.String())
		}
	}
}

// The ID and --all reach the box as the laptop gave them.
func TestRequestFlagsReachTheBox(t *testing.T) {
	if got := strings.Join(remoteExtra(options{request: "3f2a9c1e"}), " "); got != "3f2a9c1e" {
		t.Errorf("approve ID: %q", got)
	}
	if got := strings.Join(remoteExtra(options{all: true}), " "); got != "--all" {
		t.Errorf("--all: %q", got)
	}
}

// What a box says of waiting requests is kept on this computer, and
// keelbox ls shows it with the time it was heard.
func TestLsShowsWaitingRequests(t *testing.T) {
	isolate(t)
	register(t, serverBox("acme", "203.0.113.7"))
	cfg, err := config.Load(boxConfigPath("acme"))
	if err != nil {
		t.Fatal(err)
	}
	if !notesRequests("requests", cfg, options{}, boxConfigPath("acme"), config.ProviderSSH) || notesRequests("doctor", cfg, options{}, boxConfigPath("acme"), config.ProviderSSH) ||
		notesRequests("status", cfg, options{json: true}, boxConfigPath("acme"), config.ProviderSSH) || notesRequests("status", cfg, options{}, "/etc/keelbox/keelbox.yaml", config.ProviderSSH) {
		t.Error("the wrong commands keep the count")
	}
	if c := listRow("acme").status; strings.Contains(c, "request") {
		t.Errorf("ls before anything was heard: %q", c)
	}
	for _, tc := range []struct {
		cmd, out string
		code     int
		waiting  int
	}{
		{"requests", "2 waiting for approval on acme.\n\nID        ASKED  ...\n", 0, 2},
		{"status", "box        acme (Ubuntu 26.04 amd64)\nrequests   3 waiting for approval: keelbox requests acme\nwatchdog   ok\n", 0, 3},
		{"approve", "Approved request 3f2a9c1e: installing jq for agent...\nRequest 3f2a9c1e: installed jq.\n1 waiting for approval on acme.\n", 0, 1},
		{"doctor-like output", "nothing about requests\n", 0, 1},   // no count: what was heard stays
		{"status", "box        acme (Ubuntu 26.04 amd64)\n", 1, 1}, // a status that failed says nothing
		{"deny", "Denied request 3f2a9c1e (jq); nothing was installed.\n0 waiting for approval on acme.\n", 0, 0},
		{"requests", "4 waiting for approval on acme.\n", 0, 4},
		{"status", "box        acme (Ubuntu 26.04 amd64)\nwatchdog   ok\n", 0, 0}, // a status without the row: none wait
	} {
		noteRequests("acme", tc.cmd, tc.code, tc.out)
		n, _ := loadRequestsNote("acme")
		if n.Waiting != tc.waiting {
			t.Errorf("after %s (%q): %d waiting, want %d", tc.cmd, tc.out, n.Waiting, tc.waiting)
		}
	}
	saveRequestsNote("acme", 2, time.Date(2026, 11, 15, 14, 2, 0, 0, time.Local))
	if c := listRow("acme").status; c != "2 package requests waiting as of Nov 15 14:02 (keelbox requests acme)" {
		t.Errorf("ls: %q", c)
	}
	var out, errb bytes.Buffer
	if code := Main([]string{"ls"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "2 package requests waiting as of Nov 15 14:02 (keelbox requests acme)") {
		t.Errorf("keelbox ls: exit %d\n%s", code, out.String())
	}
	// keelbox status --all hears it from each box's snapshot.
	rows := []statusAllRow{{Box: "acme", Reach: reachOK, Status: &snapshot.Status{Schema: 1, Written: time.Now(), Box: "acme", RequestsWaiting: 1}}}
	out.Reset()
	printStatusAllStyled(&out, ui.Style{}, rows, time.Now())
	if !strings.Contains(out.String(), "acme: 1 package request of the agent's waits for approval: keelbox requests acme\n") {
		t.Errorf("status --all:\n%s", out.String())
	}
	// keelbox forget removes the note with the box.
	if code := Main([]string{"forget", "acme", "--yes"}, &out, &errb); code != 0 {
		t.Fatalf("forget: exit %d %s", code, errb.String())
	}
	if _, err := os.Stat(requestsNotePath("acme")); err == nil {
		t.Error("forget left the requests note")
	}
}

// The install service is a command the box's keelbox knows, for the unit's
// ExecCondition, and is in no help.
func TestInstallRunIsInternal(t *testing.T) {
	var errb bytes.Buffer
	if code := hasCommand([]string{installs.Command}, &errb); code != 0 {
		t.Errorf("has-command %s: exit %d %s", installs.Command, code, errb.String())
	}
	if _, listed := findCmd(installs.Command); listed {
		t.Errorf("%s is in help", installs.Command)
	}
	for _, c := range []string{"install", "requests", "approve", "deny"} {
		if _, listed := findCmd(c); !listed {
			t.Errorf("%s is not in help", c)
		}
	}
}
