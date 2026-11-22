package modules

import (
	"bytes"
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// fileState is what an attacker would want to change about a file.
type fileState struct {
	data         string
	mode         fs.FileMode
	owner, group string
	dir          bool
	target       string
}

func snapshot(s *sim, keep func(string) bool) map[string]fileState {
	out := map[string]fileState{}
	for p, f := range s.Files {
		if keep(p) {
			out[p] = fileState{string(f.Data), f.Mode, f.Owner, f.Group, f.Dir, f.Target}
		}
	}
	return out
}

func outsideAgentHome(p string) bool {
	return p != "/home/agent" && !strings.HasPrefix(p, "/home/agent/") && !strings.HasPrefix(p, "/tmp/")
}

// TestAgentSymlinksGainNothing plants, as the agent would between applies,
// a symlink in its home that points at a root-owned place, then re-applies.
// Whether apply succeeds or stops, nothing outside the agent's home may
// change, nothing there may become the agent's, and no root-only content may
// land where the agent can read it. ~/.config -> /etc is the attack where
// an apply that chowned through the link would give the agent /etc/docker,
// and with it the system Docker daemon's config.
func TestAgentSymlinksGainNothing(t *testing.T) {
	for _, tc := range []struct {
		name, link, target string
		failsAt            string // a step apply must stop at, or "" if apply may succeed
	}{
		{"config dir to /etc", "/home/agent/.config", "/etc", "/home/agent/.config/"},
		{"docker config dir to /etc/docker", "/home/agent/.config/docker", "/etc/docker", "agent-docker-config-dir"},
		{"user units to /etc/systemd/system", "/home/agent/.config/systemd/user", "/etc/systemd/system", "/home/agent/.config/systemd/user/"},
		{"ssh dir to /root", "/home/agent/.ssh", "/root", "agent-ssh-dir"},
		{".bashrc to /etc/shadow", "/home/agent/.bashrc", "/etc/shadow", ".bashrc"},
		{".claude to /etc/keelbox", "/home/agent/.claude", "/etc/keelbox", "claude-dir"},
		{".codex to /etc", "/home/agent/.codex", "/etc", "codex-dir"},
		{".tmux.conf to /etc/hosts", "/home/agent/.tmux.conf", "/etc/hosts", ""},
		{"events dir to /var/lib/keelbox", "/home/agent/.local/state", "/var/lib/keelbox", "agent-events-dir"},
		{"relative climb out of home", "/home/agent/.config", "../../etc", "/home/agent/.config/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSim(t)
			e := simEnv(t, s, testConfig(t, ""))
			ctx := context.Background()
			if _, err := engine.Apply(ctx, e, All()); err != nil {
				t.Fatalf("first apply: %v", err)
			}
			s.Files["/root"] = &system.FakeFile{Dir: true, Mode: 0o700, Owner: "root", Group: "root"}
			s.Files["/root/.ssh/authorized_keys"] = &system.FakeFile{Data: []byte("root key\n"), Mode: 0o600, Owner: "root", Group: "root"}
			// The agent replaces the path with a symlink.
			for p := range s.Files {
				if p == tc.link || strings.HasPrefix(p, tc.link+"/") {
					delete(s.Files, p)
				}
			}
			s.Files[tc.link] = &system.FakeFile{Target: tc.target, Mode: 0o777, Owner: "agent", Group: "agent"}
			before := snapshot(s, outsideAgentHome)

			res, err := engine.Apply(ctx, e, All())
			if err != nil {
				t.Fatalf("a link in the agent's home stopped the whole run: %v", err)
			}
			if tc.failsAt != "" && !failedAt(res, tc.failsAt) {
				t.Errorf("apply through the planted link: want a failure at %s, got %+v", tc.failsAt, res.Failed)
			}
			after := snapshot(s, outsideAgentHome)
			for p, was := range before {
				if now, ok := after[p]; !ok {
					t.Errorf("%s removed", p)
				} else if now != was {
					t.Errorf("%s changed: %+v -> %+v", p, was, now)
				}
			}
			for p, now := range after {
				if _, ok := before[p]; !ok {
					t.Errorf("%s created outside the agent's home: %+v", p, now)
				}
			}
			// Nothing root-only may become readable by the agent.
			shadow := s.content("/etc/shadow")
			for p, f := range s.Files {
				readable := f.Owner == "agent" || f.Mode&0o004 != 0
				if readable && p != "/etc/shadow" && len(f.Data) > 0 && bytes.Contains(f.Data, []byte(shadow)) {
					t.Errorf("%s holds /etc/shadow and the agent can read it", p)
				}
				if readable && bytes.Contains(f.Data, []byte("root key")) && p != "/root/.ssh/authorized_keys" {
					t.Errorf("%s holds root's authorized_keys and the agent can read it", p)
				}
			}
		})
	}
}

func failedAt(res engine.Applied, what string) bool {
	for _, f := range res.Failed {
		if strings.Contains(f.Step+": "+f.Detail, what) {
			return true
		}
	}
	return false
}

// TestBrokenHomeDoesNotHoldBackTheBox plants the earliest-failing link, then
// drifts the root security config that the host and runtime modules apply
// after the agent's steps. Those must still converge; only the agent's own
// later steps are skipped, and doctor fails until the home is repaired.
func TestBrokenHomeDoesNotHoldBackTheBox(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	security := []string{sshdDropIn, agentNetRules, agentNetUnit, dockerGuardUnit, dockerDaemon}
	want := map[string]string{}
	for _, p := range security {
		want[p] = s.content(p)
		delete(s.Files, p)
	}
	for p := range s.Files {
		if strings.HasPrefix(p, "/home/agent/.ssh") {
			delete(s.Files, p)
		}
	}
	s.Files["/home/agent/.ssh"] = &system.FakeFile{Target: "/root", Mode: 0o777, Owner: "agent", Group: "agent"}
	s.Files[agentSudoers] = &system.FakeFile{Data: []byte("agent ALL=(ALL:ALL) NOPASSWD:ALL\n"), Mode: 0o440, Owner: "root", Group: "root"}

	res, err := engine.Apply(ctx, e, All())
	if err != nil {
		t.Fatalf("apply stopped: %v", err)
	}
	if len(res.Failed) == 0 || res.Failed[0].Step != "agent-ssh-dir" {
		t.Fatalf("first failure: want host/agent-ssh-dir, got %+v", res.Failed)
	}
	for _, p := range security {
		if got := s.content(p); got == "" || got != want[p] {
			t.Errorf("%s did not converge after the agent's step failed", p)
		}
	}
	if s.has(agentSudoers) {
		t.Error("agent sudoers drop-in survived")
	}
	for _, step := range []string{"agent-ssh-keys", "agent-docker-daemon-json", "agent-shell-path", "tmux-config", "claude-dir"} {
		found := false
		for _, f := range res.Failed {
			if f.Step == step && strings.HasPrefix(f.Detail, "skipped: agent's step host/agent-ssh-dir failed") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: want it skipped after agent-ssh-dir failed, got %+v", step, res.Failed)
		}
	}
	for _, f := range res.Changes {
		if f.Step == "agent-ssh-keys" {
			t.Error("an agent step ran after that account's earlier step failed")
		}
	}
	if fs := engine.Doctor(ctx, e, All()); !engine.Failed(fs) {
		t.Error("doctor passes with the agent's home broken")
	}
}

// TestUserFilesAreWrittenAsTheirOwner checks that every managed file in a
// user's home is written through that user's own file access, never root's:
// root's refuses paths whose directories a user owns.
func TestUserFilesAreWrittenAsTheirOwner(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	for p, f := range s.Files {
		for _, home := range []string{"/home/agent/", "/home/admin/"} {
			owner := strings.TrimSuffix(strings.TrimPrefix(home, "/home/"), "/")
			if strings.HasPrefix(p, home) && f.Owner != owner && !strings.HasPrefix(p, home+".local/bin/") {
				t.Errorf("%s is owned by %s, not %s", p, f.Owner, owner)
			}
		}
	}
	if _, err := s.ReadFile("/home/agent/.bashrc"); err == nil {
		t.Error("root read a file in the agent's home")
	}
	if err := s.WriteFile("/home/agent/.x", nil, 0o644, "agent", ""); err == nil {
		t.Error("root wrote into the agent's home")
	}
}
