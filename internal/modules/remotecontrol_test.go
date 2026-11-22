package modules

import (
	"context"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
)

func remoteControlFinding(e *engine.Env) engine.Finding {
	for _, f := range engine.Doctor(context.Background(), e, All()) {
		if f.Check == "claude-remote-control" && f.Module == "agents" {
			return f
		}
	}
	return engine.Finding{Detail: "missing"}
}

func TestRemoteControlWaitsForTheUsersSignIn(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, "sessions:\n  remote_control:\n    enabled: true\n"))
	ctx := context.Background()
	if res, err := engine.Apply(ctx, e, All()); err != nil || len(res.Failed) > 0 {
		t.Fatal(err, res.Failed)
	}
	unit := s.content("/home/agent/.config/systemd/user/" + RemoteControlUnit)
	for _, want := range []string{
		"WorkingDirectory=/home/agent/projects",
		"ExecCondition=/bin/sh -c 'exec claude auth status >/dev/null 2>&1'",
		"ExecStart=/bin/sh -c 'exec claude remote-control --spawn same-dir --remote-control-session-name-prefix testbox'",
		"Restart=always", "RestartSteps=5", "RestartMaxDelaySec=10min", "KillSignal=SIGINT", "StandardOutput=null", "StandardError=journal", "WantedBy=default.target", "Managed by keelbox",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit lacks %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, ".credentials") || strings.Contains(unit, "login") && !strings.Contains(unit, "keelbox login") {
		t.Errorf("unit touches sign-in:\n%s", unit)
	}
	if d, ok := s.Files["/home/agent/projects"]; !ok || !d.Dir || d.Owner != "agent" {
		t.Errorf("projects directory: %+v", d)
	}
	if again, err := engine.Apply(ctx, e, All()); err != nil || len(again.Changes) != 0 {
		t.Errorf("second apply before sign-in: %v %v", again.Changes, err)
	}
	if f := remoteControlFinding(e); f.Level != engine.LevelWarn || !strings.Contains(f.Detail, "cannot start until you run keelbox login") {
		t.Errorf("doctor before sign-in: %+v", f)
	}

	// The user signs in with keelbox login: apply starts the service.
	s.claudeSignedIn = true
	res, err := engine.Apply(ctx, e, All())
	if err != nil || len(res.Changes) != 1 || res.Changes[0].Step != "claude-remote-control-service" {
		t.Fatalf("apply after sign-in: %+v %v", res.Changes, err)
	}
	if f := remoteControlFinding(e); f.Level != engine.LevelOK || !strings.Contains(f.Detail, "running in ~/projects") {
		t.Errorf("doctor after sign-in: %+v", f)
	}
	if again, err := engine.Apply(ctx, e, All()); err != nil || len(again.Changes) != 0 {
		t.Errorf("third apply: %v %v", again.Changes, err)
	}

	// The server stops (its first-run questions unanswered, say).
	s.units["user:"+RemoteControlUnit] = false
	if f := remoteControlFinding(e); f.Level != engine.LevelWarn || !strings.Contains(f.Detail, "needs you") || !strings.Contains(f.Detail, "keelbox login testbox claude-code") {
		t.Errorf("doctor with the server down: %+v", f)
	}
}

// Declining Claude Code's trust question is the user's choice about an
// optional feature: apply defers the step with the command that finishes
// it, and does not fail.
func TestRemoteControlDeclinedDoesNotFailApply(t *testing.T) {
	old := rcSettle
	rcSettle = 0
	t.Cleanup(func() { rcSettle = old })
	s := newSim(t)
	s.claudeSignedIn, s.rcUnanswered = true, true
	e := simEnv(t, s, testConfig(t, "sessions:\n  remote_control:\n    enabled: true\n"))
	res, err := engine.Apply(context.Background(), e, All())
	if err != nil || len(res.Failed) != 0 {
		t.Fatalf("apply: %v failed %v", err, res.Failed)
	}
	if len(res.Deferred) != 1 || res.Deferred[0].Step != "claude-remote-control-service" || !strings.Contains(res.Deferred[0].Detail, "keelbox login testbox claude-code") {
		t.Errorf("deferred %+v", res.Deferred)
	}
	if f := remoteControlFinding(e); f.Level != engine.LevelWarn {
		t.Errorf("doctor: %+v", f)
	}
	// Answered on the terminal: the next apply starts it.
	s.rcUnanswered = false
	if res, err := engine.Apply(context.Background(), e, All()); err != nil || len(res.Deferred) != 0 || len(res.Changes) != 1 {
		t.Errorf("apply after answering: %v %+v", err, res)
	}
}

// On a real box the unanswered server reads as running for a moment after
// each start. Apply must not take that for up: it defers with the command,
// rather than pass the step and then fail it as "still not converged".
func TestRemoteControlThatStartsAndExitsIsDeferred(t *testing.T) {
	oldSettle, oldSteady := rcSettle, rcSteady
	rcSettle, rcSteady = 0, 0
	t.Cleanup(func() { rcSettle, rcSteady = oldSettle, oldSteady })
	s := newSim(t)
	s.claudeSignedIn, s.rcUnanswered, s.rcFlaps = true, true, true
	e := simEnv(t, s, testConfig(t, "sessions:\n  remote_control:\n    enabled: true\n"))
	res, err := engine.Apply(context.Background(), e, All())
	if err != nil || len(res.Failed) != 0 {
		t.Fatalf("apply: %v failed %v", err, res.Failed)
	}
	if len(res.Deferred) != 1 || res.Deferred[0].Step != "claude-remote-control-service" || !strings.Contains(res.Deferred[0].Detail, "keelbox login testbox claude-code") {
		t.Errorf("deferred %+v", res.Deferred)
	}
}

func TestRemoteControlOptionsReachTheUnit(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, "sessions:\n  remote_control:\n    enabled: true\n    directory: work/repo\n    spawn: worktree\n"))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	unit := s.content("/home/agent/.config/systemd/user/" + RemoteControlUnit)
	if !strings.Contains(unit, "WorkingDirectory=/home/agent/work/repo") || !strings.Contains(unit, "--spawn worktree") {
		t.Errorf("unit:\n%s", unit)
	}
}

func TestRemoteControlOffByDefault(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	if s.has("/home/agent/.config/systemd/user/" + RemoteControlUnit) {
		t.Error("remote control unit written without sessions.remote_control.enabled")
	}
}
