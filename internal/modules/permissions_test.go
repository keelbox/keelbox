package modules

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/installs"
	"github.com/keelbox/keelbox/internal/logbook"
)

func permissionsYAML(mode string) string { return "  agent:\n    permissions: " + mode + "\n" }

// applied applies a config with users.agent.permissions mode to s and
// records the apply in the logbook, as keelbox apply does.
func applied(t *testing.T, s *sim, mode string) *engine.Env {
	t.Helper()
	e := simEnv(t, s, testConfig(t, permissionsYAML(mode)))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatalf("apply (%s): %v", mode, err)
	}
	if err := logbook.Append(s, logbook.Record{Time: time.Now().UTC(), Box: e.Cfg.Box, Keelbox: "test", Release: e.M.Release, Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	return e
}

// agentAsks writes a package request to the agent's file, as the agent
// (keelbox install does the same), and returns its ID.
func agentAsks(t *testing.T, s *sim, pkgs ...string) string {
	t.Helper()
	id := installs.NewID()
	data, err := json.Marshal(installs.Request{V: 1, ID: id, Time: time.Now().UTC(), Packages: pkgs, Why: "the build needs it"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UserFS("agent").WriteFile("/home/agent/"+installs.AgentFile, data, 0o600, "agent", "agent"); err != nil {
		t.Fatal(err)
	}
	return id
}

// serve is one run of keelbox-install.service.
func serve(t *testing.T, e *engine.Env) {
	t.Helper()
	if _, err := installs.Run(context.Background(), e, installs.Options{}); err != nil {
		t.Fatal(err)
	}
}

func requestState(t *testing.T, s *sim, id string) installs.Entry {
	t.Helper()
	st, err := installs.ReadState(s)
	if err != nil {
		t.Fatal(err)
	}
	en := st.FindRef(id)
	if en == nil {
		t.Fatalf("root has no record of request %s", id)
	}
	return *en
}

// The limits on the agent that hold in manual and auto: no sudo rule, not
// in the sudo or docker group, the network table loaded for it.
func agentLimitsHold(t *testing.T, s *sim, mode string) {
	t.Helper()
	if s.has(agentSudoers) {
		t.Errorf("%s: %s exists", mode, agentSudoers)
	}
	for _, g := range []string{"sudo", "docker"} {
		if contains(s.groups[g], "agent") {
			t.Errorf("%s: the agent is in the %s group", mode, g)
		}
	}
	if !s.units[agentNetName] || !strings.Contains(s.nftLoaded, "reject") {
		t.Errorf("%s: the agent network table is not loaded", mode)
	}
	for _, p := range []string{BinaryPath, ConfigPath, installs.ModePath, unitDir + installs.Service, unitDir + installs.PathUnit} {
		if f, ok := s.Files[p]; !ok || f.Owner != "root" || f.Mode&0o022 != 0 {
			t.Errorf("%s: %s is not root's alone to write: %+v", mode, p, f)
		}
	}
}

func TestPermissionsConvergeInEachMode(t *testing.T) {
	ctx := context.Background()
	for _, mode := range config.PermissionModes {
		s := newSim(t)
		e := applied(t, s, mode)
		for _, it := range engine.Plan(ctx, e, All()) {
			if !it.OK {
				t.Errorf("%s: after apply, %s/%s still pending: %s (%v)", mode, it.Module, it.Step, it.Detail, it.Err)
			}
		}
		second, err := engine.Apply(ctx, e, All())
		if err != nil || len(second.Changes) != 0 {
			t.Errorf("%s: second apply changed %v, %v", mode, second.Changes, err)
		}
		m, err := installs.ParseMode([]byte(s.content(installs.ModePath)))
		if err != nil || m.Mode != mode || m.Agent != "agent" || m.Box != "testbox" {
			t.Errorf("%s: mode file %+v, %v", mode, m, err)
		}
		if !s.units[installs.PathUnit] {
			t.Errorf("%s: %s is not running", mode, installs.PathUnit)
		}
		if f := s.Files["/home/agent/"+installs.AgentDir]; f == nil || f.Owner != "agent" || f.Mode != 0o700 {
			t.Errorf("%s: the agent's request directory: %+v", mode, f)
		}
		rows := map[string]engine.Finding{}
		for _, f := range engine.Doctor(ctx, e, All()) {
			if f.Level == engine.LevelFail {
				t.Errorf("%s: doctor failure %s/%s: %s", mode, f.Module, f.Check, f.Detail)
			}
			if f.Module == "permissions" {
				rows[f.Check] = f
			}
		}
		if r := rows["mode"]; r.Level != engine.LevelOK || !strings.HasPrefix(r.Detail, mode+": ") {
			t.Errorf("%s: doctor's mode row %+v", mode, r)
		}
		if r := rows["request-service"]; r.Level != engine.LevelOK || !strings.Contains(r.Detail, installs.PathUnit+" active") {
			t.Errorf("%s: doctor's request-service row %+v", mode, r)
		}
		primer := s.content(PrimerPath)
		if !strings.Contains(primer, `keelbox install NAME --why "what needs it"`) || !strings.Contains(primer, "never work around a refusal") && !strings.Contains(primer, "Never work around a refusal") {
			t.Errorf("%s: the primer does not say how to get a package:\n%s", mode, primer)
		}
		switch mode {
		case config.PermissionsFull:
			if !strings.Contains(s.content(agentSudoers), "agent ALL=(ALL:ALL) NOPASSWD:ALL") {
				t.Errorf("full: no sudo rule: %q", s.content(agentSudoers))
			}
			if r := rows["agent-sudo"]; r.Level != engine.LevelWarn || !strings.Contains(r.Detail, "none of keelbox's limits on the agent bind") {
				t.Errorf("full: doctor's agent-sudo row %+v", r)
			}
			if !strings.Contains(primer, "You have sudo on this box") || strings.Contains(primer, "Never try sudo") {
				t.Errorf("full: primer:\n%s", primer)
			}
		default:
			agentLimitsHold(t, s, mode)
			if _, warned := rows["agent-sudo"]; warned {
				t.Errorf("%s: doctor warns about sudo", mode)
			}
			if !strings.Contains(primer, "**Never try sudo, and never work around a refusal**") || !strings.Contains(primer, "which has **no sudo**") {
				t.Errorf("%s: primer:\n%s", mode, primer)
			}
		}
		if mode == config.PermissionsManual && (!strings.Contains(primer, "keelbox approve testbox") || !strings.Contains(primer, "installs nothing")) {
			t.Errorf("manual: the primer does not say the human approves:\n%s", primer)
		}
		if mode == config.PermissionsAuto && !strings.Contains(primer, "installs it straight away") {
			t.Errorf("auto: primer:\n%s", primer)
		}
	}
}

// The install service's unit: started for the agent's file, skipped by a
// keelbox that does not know the command, never start-limited, below
// system services in CPU and I/O, and free to gain privileges as apt-get
// run by hand is.
func TestInstallUnits(t *testing.T) {
	svc := installService.text()
	for _, want := range []string{
		"# Managed by keelbox", "StartLimitIntervalSec=0\n", "Type=oneshot\n",
		"ExecCondition=/usr/local/bin/keelbox has-command install-run\n",
		"ExecStart=/usr/local/bin/keelbox install-run --local /etc/keelbox/keelbox.yaml\n",
		"TimeoutStartSec=1h\n", "Nice=10\n", "CPUWeight=50\n", "IOWeight=50\n",
	} {
		if !strings.Contains(svc, want) {
			t.Errorf("service unit has no %q:\n%s", want, svc)
		}
	}
	if strings.Contains(svc, "NoNewPrivileges") {
		t.Errorf("the install service sets NoNewPrivileges; maintainer scripts run under it:\n%s", svc)
	}
	// The other day-two services are as they were.
	if w := watchService.text(); !strings.Contains(w, "Nice=5\nCPUWeight=200\nIOWeight=200\nNoNewPrivileges=yes\n") {
		t.Errorf("the watch service changed:\n%s", w)
	}
	path := installPathUnit("/home/agent").text()
	if !strings.Contains(path, "PathModified=/home/agent/.local/state/keelbox/install-request.json\n") || !strings.Contains(path, "Unit=keelbox-install.service\n") {
		t.Errorf("path unit:\n%s", path)
	}
}

func TestAutoInstallsThroughApt(t *testing.T) {
	s := newSim(t)
	e := applied(t, s, config.PermissionsAuto)
	id := agentAsks(t, s, "libpq-dev", "sl")
	serve(t, e)
	if en := requestState(t, s, id); en.State != installs.StateInstalled || strings.Join(en.Installed, " ") != "libpq-dev sl" {
		t.Fatalf("request: %+v", en)
	}
	for _, p := range []string{"libpq-dev", "sl"} {
		if _, ok := s.pkgs[p]; !ok {
			t.Errorf("%s is not installed", p)
		}
	}
	if !s.Ran("apt-get -o DPkg::Lock::Timeout=600 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold install -y --no-remove -- libpq-dev sl") {
		t.Errorf("apt-get did not run with keelbox's options and --no-remove: %v", s.Commands())
	}
	agentLimitsHold(t, s, "auto, after an install")

	// The record and the packages survive the next apply, which changes
	// nothing.
	second, err := engine.Apply(context.Background(), e, All())
	if err != nil || len(second.Changes) != 0 {
		t.Errorf("apply after an install changed %v, %v", second.Changes, err)
	}
	pkgs, err := installs.ReadInstalled(s)
	if err != nil || len(pkgs) != 2 || pkgs[0].Name != "libpq-dev" || pkgs[0].Request != requestState(t, s, id).ID {
		t.Errorf("installed record after apply: %+v, %v", pkgs, err)
	}
	if _, ok := s.pkgs["sl"]; !ok {
		t.Error("apply removed a package installed for the agent")
	}
	if got := installs.Unlisted(installs.StillInstalled(context.Background(), e), BasePackages, e.Cfg); strings.Join(got, " ") != "libpq-dev sl" {
		t.Errorf("packages host.packages lacks: %q", got)
	}
	// A base package the agent asks for is there already: no record.
	id = agentAsks(t, s, "jq")
	serve(t, e)
	if en := requestState(t, s, id); en.State != installs.StateInstalled || len(en.Installed) != 0 {
		t.Errorf("a base package: %+v", en)
	}
}

func TestInstallThatWouldRemoveAPackageFails(t *testing.T) {
	s := newSim(t)
	e := applied(t, s, config.PermissionsAuto)
	s.conflicts = map[string][]string{"other-firewall-helper": {"ufw"}}
	id := agentAsks(t, s, "other-firewall-helper")
	serve(t, e)
	en := requestState(t, s, id)
	if en.State != installs.StateRefused || !strings.Contains(en.Detail, "would remove ufw") {
		t.Errorf("request: %+v", en)
	}
	if _, ok := s.pkgs["ufw"]; !ok {
		t.Error("ufw was removed")
	}
	if _, ok := s.pkgs["other-firewall-helper"]; ok {
		t.Error("the package was installed")
	}
	// Deny-listed names and names apt does not have are refused too, and
	// nothing is recorded as installed.
	s.noPackage = map[string]bool{"no-such-package": true}
	for _, p := range []string{"sudo", "docker.io", "openssh-server", "no-such-package", "jq=1.7", "--reinstall"} {
		id := agentAsks(t, s, p)
		serve(t, e)
		if en := requestState(t, s, id); en.State != installs.StateRefused {
			t.Errorf("%s: %+v", p, en)
		}
	}
	if pkgs, _ := installs.ReadInstalled(s); len(pkgs) != 0 {
		t.Errorf("installed record: %+v", pkgs)
	}
}

func TestManualWaitsUntilApproved(t *testing.T) {
	s := newSim(t)
	e := applied(t, s, config.PermissionsManual)
	id := agentAsks(t, s, "sl")
	var notified []installs.Entry
	if _, err := installs.Run(context.Background(), e, installs.Options{Notify: func(en installs.Entry) { notified = append(notified, en) }}); err != nil {
		t.Fatal(err)
	}
	if en := requestState(t, s, id); en.State != installs.StatePending {
		t.Fatalf("request: %+v", en)
	}
	if _, ok := s.pkgs["sl"]; ok {
		t.Fatal("manual installed without approval")
	}
	if len(notified) != 1 {
		t.Errorf("the owner was told %d times", len(notified))
	}
	// An apply in between leaves it waiting.
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	if en := requestState(t, s, id); en.State != installs.StatePending {
		t.Errorf("after an apply: %+v", en)
	}
	if got, err := installs.Approve(e, requestState(t, s, id).ID, false, installs.Options{}); err != nil || len(got) != 1 {
		t.Fatalf("approve: %+v, %v", got, err)
	}
	serve(t, e)
	if en := requestState(t, s, id); en.State != installs.StateInstalled || !en.Approved {
		t.Fatalf("after approve: %+v", en)
	}
	if _, ok := s.pkgs["sl"]; !ok {
		t.Error("approve did not install")
	}
	if !s.Ran("apt-get -o DPkg::Lock::Timeout=600 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold install -y --no-remove -- sl") {
		t.Error("approve did not run apt-get with --no-remove")
	}
	agentLimitsHold(t, s, "manual, after an approved install")
}

// Changing users.agent.permissions and applying converges both ways.
func TestSwitchingPermissionsConverges(t *testing.T) {
	ctx := context.Background()
	s := newSim(t)

	// full: the sudo rule is there.
	applied(t, s, config.PermissionsFull)
	if !s.has(agentSudoers) {
		t.Fatal("full wrote no sudo rule")
	}

	// full to auto removes it, and the agent installs through the service.
	e := applied(t, s, config.PermissionsAuto)
	agentLimitsHold(t, s, "full to auto")
	if again, err := engine.Apply(ctx, e, All()); err != nil || len(again.Changes) != 0 {
		t.Errorf("full to auto: a second apply changed %v, %v", again.Changes, err)
	}
	id := agentAsks(t, s, "sl")
	serve(t, e)
	if en := requestState(t, s, id); en.State != installs.StateInstalled {
		t.Errorf("auto: %+v", en)
	}

	// auto to manual stops installs without approval.
	e = applied(t, s, config.PermissionsManual)
	agentLimitsHold(t, s, "auto to manual")
	if m, _ := installs.ParseMode([]byte(s.content(installs.ModePath))); m.Mode != config.PermissionsManual {
		t.Errorf("auto to manual: mode file %+v", m)
	}
	id = agentAsks(t, s, "cowsay")
	serve(t, e)
	if en := requestState(t, s, id); en.State != installs.StatePending {
		t.Errorf("manual: %+v", en)
	}
	if _, ok := s.pkgs["cowsay"]; ok {
		t.Error("manual installed without approval")
	}
	if _, ok := s.pkgs["sl"]; !ok {
		t.Error("the package installed under auto is gone")
	}

	// manual back to auto: what waits still waits for the owner, and new
	// requests are installed.
	e = applied(t, s, config.PermissionsAuto)
	if en := requestState(t, s, id); en.State != installs.StatePending {
		t.Errorf("manual to auto: the waiting request: %+v", en)
	}
	id2 := agentAsks(t, s, "tree")
	serve(t, e)
	if en := requestState(t, s, id2); en.State != installs.StateInstalled {
		t.Errorf("auto again: %+v", en)
	}

	// auto to full writes the rule again; users.agent.sudo: true is the
	// same mode.
	applied(t, s, config.PermissionsFull)
	if !s.has(agentSudoers) {
		t.Error("auto to full wrote no sudo rule")
	}
	old := simEnv(t, s, testConfig(t, "  agent:\n    sudo: true\n"))
	for _, it := range engine.Plan(ctx, old, All()) {
		// The copy of the config differs: it is the config's own text.
		if !it.OK && it.Step != "config-copy" {
			t.Errorf("users.agent.sudo: true after permissions: full leaves %s/%s pending: %s", it.Module, it.Step, it.Detail)
		}
	}
}
