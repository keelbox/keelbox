package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/manifest"
	"github.com/keelbox/keelbox/internal/system"
)

type testModule struct {
	name    string
	enabled bool
	steps   []Step
}

func (m testModule) Name() string                           { return m.name }
func (m testModule) Enabled(*config.Config) bool            { return m.enabled }
func (m testModule) Steps(*Env) []Step                      { return m.steps }
func (m testModule) Verify(context.Context, *Env) []Finding { return nil }

func env(x system.Executor) *Env { return &Env{X: x, Cfg: &config.Config{}} }

func flag(name string, state *bool) Step {
	return Step{Name: name,
		Check: func(context.Context, *Env) (Status, error) {
			if *state {
				return Converged(), nil
			}
			return Pending("set %s", name), nil
		},
		Apply: func(context.Context, *Env) error { *state = true; return nil },
	}
}

func TestApplyThenNoChanges(t *testing.T) {
	a, b := false, true
	mods := []Module{testModule{"m", true, []Step{flag("a", &a), flag("b", &b)}}, testModule{"off", false, []Step{flag("x", new(bool))}}}
	e := env(system.NewFake())
	res, err := Apply(context.Background(), e, mods)
	if err != nil || len(res.Changes) != 1 || res.Changes[0].Step != "a" || res.Checked != 2 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	res, err = Apply(context.Background(), e, mods)
	if err != nil || len(res.Changes) != 0 {
		t.Fatalf("second apply: %+v %v", res, err)
	}
	if items := Plan(context.Background(), e, mods); len(items) != 2 {
		t.Errorf("plan included disabled module: %v", items)
	}
}

func TestApplyStopsAtNonConvergingStep(t *testing.T) {
	later := false
	stuck := Step{Name: "stuck",
		Check: func(context.Context, *Env) (Status, error) { return Pending("never"), nil },
		Apply: func(context.Context, *Env) error { return nil }}
	_, err := Apply(context.Background(), env(system.NewFake()), []Module{testModule{"m", true, []Step{stuck, flag("later", &later)}}})
	if err == nil || !strings.Contains(err.Error(), "m/stuck: still not converged") {
		t.Fatalf("got %v", err)
	}
	if later {
		t.Error("ran a step after a failure")
	}
}

// Ctrl-C during a step lets that step finish with a live context, then
// stops before the next one.
func TestApplyInterruptStopsBetweenSteps(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var sawCancelled, later bool
	slow := Step{Name: "slow",
		Check: func(_ context.Context, e *Env) (Status, error) { return Status{OK: e.Facts.Arch == "done"}, nil },
		Apply: func(c context.Context, e *Env) error {
			cancel()
			sawCancelled = c.Err() != nil
			e.Facts.Arch = "done"
			return nil
		}}
	res, err := Apply(ctx, env(system.NewFake()), []Module{testModule{"m", true, []Step{slow, flag("later", &later)}}})
	if !errors.Is(err, ErrInterrupted) || !strings.Contains(err.Error(), "before m/later") {
		t.Fatalf("got %v", err)
	}
	if sawCancelled {
		t.Error("the running step saw the cancellation")
	}
	if later {
		t.Error("ran a step after the interrupt")
	}
	if len(res.Changes) != 1 {
		t.Errorf("changes: %+v", res.Changes)
	}
}

func TestApplyReportsApplyError(t *testing.T) {
	boom := Step{Name: "boom",
		Check: func(context.Context, *Env) (Status, error) { return Pending("x"), nil },
		Apply: func(context.Context, *Env) error { return errors.New("kaput") }}
	_, err := Apply(context.Background(), env(system.NewFake()), []Module{testModule{"m", true, []Step{boom}}})
	if err == nil || err.Error() != "m/boom: kaput" {
		t.Fatalf("got %v", err)
	}
}

func TestFileStep(t *testing.T) {
	x := system.NewFake()
	e := env(x)
	ctx := context.Background()
	s := FileStep("f", File{Path: "/etc/x.conf", Content: []byte("a\n"), Mode: 0o640})
	if st, _ := s.Check(ctx, e); st.OK || !strings.Contains(st.Detail, "create") {
		t.Fatalf("%+v", st)
	}
	if err := s.Apply(ctx, e); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Check(ctx, e); !st.OK {
		t.Fatalf("after apply: %+v", st)
	}
	x.Files["/etc/x.conf"].Mode = 0o644
	x.Files["/etc/x.conf"].Owner = "agent"
	if st, _ := s.Check(ctx, e); st.OK || !strings.Contains(st.Detail, "mode 644->640") || !strings.Contains(st.Detail, "owner") {
		t.Fatalf("drift not detected: %+v", st)
	}
}

func TestFileStepRollsBackOnValidateFailure(t *testing.T) {
	x := system.NewFake()
	x.AddFile("/etc/x", "old", 0o644)
	ran := false
	s := FileStep("f", File{Path: "/etc/x", Content: []byte("new"), Mode: 0o644,
		Validate: func(context.Context, *Env) error { return errors.New("bad") },
		OnChange: func(context.Context, *Env) error { ran = true; return nil }})
	if err := s.Apply(context.Background(), env(x)); err == nil {
		t.Fatal("want error")
	}
	if d, _ := x.ReadFile("/etc/x"); string(d) != "old" || ran {
		t.Errorf("content %q, onchange ran %v", d, ran)
	}
}

func TestBlockRender(t *testing.T) {
	b := Block{ID: "keys", Content: "k1\nk2"}
	out := b.Render("mine\n")
	if out != "mine\n# >>> keelbox keys >>>\nk1\nk2\n# <<< keelbox keys <<<\n" {
		t.Fatalf("%q", out)
	}
	if b.Render(out) != out {
		t.Error("not idempotent")
	}
	b.Content = "k3"
	out2 := b.Render(out + "after\n")
	if !strings.HasPrefix(out2, "mine\n") || !strings.Contains(out2, "k3\n") || strings.Contains(out2, "k1") || !strings.HasSuffix(out2, "after\n") {
		t.Errorf("replace lost content: %q", out2)
	}
	top := Block{ID: "p", Content: "x", Top: true}.Render("rest\n")
	if !strings.HasPrefix(top, "# >>> keelbox p >>>") {
		t.Errorf("top block: %q", top)
	}
}

func TestLineStep(t *testing.T) {
	x := system.NewFake()
	x.AddFile("/f", "a", 0o600)
	e := env(x)
	s := LineStep("l", "/f", "b", 0o644, "root")
	if err := s.Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if d, _ := x.ReadFile("/f"); string(d) != "a\nb\n" {
		t.Errorf("%q", d)
	}
	if x.Files["/f"].Mode != 0o600 {
		t.Error("existing mode not kept")
	}
	if st, _ := s.Check(context.Background(), e); !st.OK {
		t.Error("not converged")
	}
}

func TestPackagesStepInstallsOnlyMissing(t *testing.T) {
	x := system.NewFake()
	x.OK("dpkg-query", "curl ii \njq un \n")
	x.OK("apt-get", "")
	e := env(x)
	e.AptStale = true
	s := PackagesStep("p", func(*Env) []string { return []string{"jq", "curl", "git"} })
	st, _ := s.Check(context.Background(), e)
	if st.Detail != "install git jq" {
		t.Errorf("detail %q", st.Detail)
	}
	if err := s.Apply(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	cmds := strings.Join(x.Commands(), "\n")
	if !strings.Contains(cmds, "apt-get -o DPkg::Lock::Timeout=600") || !strings.Contains(cmds, " update") || !strings.Contains(cmds, "install -y git jq") {
		t.Errorf("commands:\n%s", cmds)
	}
}

func TestCheckPlatform(t *testing.T) {
	m, err := manifest.ForChannel("stable")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []Facts{{OSID: "debian", OSVersion: "13", Codename: "trixie", Arch: "amd64"}, {OSID: "ubuntu", OSVersion: "22.04", Codename: "jammy", Arch: "amd64"}} {
		if err := CheckPlatform(f, m); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%+v accepted: %v", f, err)
		}
	}
	if err := CheckPlatform(Facts{OSID: "ubuntu", OSVersion: "26.04", Codename: "resolute", Arch: "amd64"}, m); err != nil {
		t.Error(err)
	}
}

func TestParseEnvFile(t *testing.T) {
	kv := ParseEnvFile("# c\nID=ubuntu\nVERSION_ID=\"26.04\"\nBAD\n")
	if kv["ID"] != "ubuntu" || kv["VERSION_ID"] != "26.04" || len(kv) != 2 {
		t.Errorf("%v", kv)
	}
}

func TestSoftFailureDoesNotStopTheRun(t *testing.T) {
	later := false
	soft := Step{Name: "vendor", Soft: true,
		Check: func(context.Context, *Env) (Status, error) { return Pending("install"), nil },
		Apply: func(context.Context, *Env) error { return errors.New("installer moved") }}
	res, err := Apply(context.Background(), env(system.NewFake()), []Module{testModule{"m", true, []Step{soft, flag("later", &later)}}})
	if err != nil {
		t.Fatal(err)
	}
	if !later || len(res.Failed) != 1 || res.Failed[0].Detail != "m/vendor: installer moved" || len(res.Changes) != 1 {
		t.Errorf("res=%+v later=%v", res, later)
	}
}

func TestDeferredStepStaysPendingWithoutFailing(t *testing.T) {
	later := false
	wait := Step{Name: "lock",
		Check: func(context.Context, *Env) (Status, error) { return Pending("lock ubuntu"), nil },
		Apply: func(context.Context, *Env) error { return Defer("running as %s", "ubuntu") }}
	res, err := Apply(context.Background(), env(system.NewFake()), []Module{testModule{"m", true, []Step{wait, flag("later", &later)}}})
	if err != nil {
		t.Fatal(err)
	}
	if !later || len(res.Deferred) != 1 || res.Deferred[0].Detail != "running as ubuntu" || len(res.Changes) != 1 {
		t.Errorf("res=%+v", res)
	}
}
