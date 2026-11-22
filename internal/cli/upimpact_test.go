package cli

import (
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/inventory"
)

// inUseReport is plan --impact on a home server: a password login and a
// container without a restart policy (both gated), a port up asks about
// (gated, answered by the LAN-ports question), and Docker kept.
func inUseReport() *impactReport {
	return &impactReport{Impact: 1, FirstApply: true, Machine: &inventory.Inventory{Hostname: "lab"}, Items: []engine.ImpactItem{
		{Group: engine.ImpactCuts, Module: "host", Subject: "8096/tcp", Gate: true, Detail: "a Python app listens on the network now", Keep: &config.Allow{Name: "python", Port: config.Port(8096)}},
		{Group: engine.ImpactCuts, Module: "host", Subject: "sam", Gate: true, Detail: "logs in over SSH with a password now; keelbox allows keys only"},
		{Group: engine.ImpactRestarts, Module: "runtime", Subject: "docker", Gate: true, Detail: "the system daemon restarts: scratch has no restart policy and stays stopped",
			Hint: "docker update --restart unless-stopped scratch brings them back"},
		{Group: engine.ImpactRestarts, Module: "host", Subject: "sshd", Detail: "reloads with keys only"},
		{Group: engine.ImpactKeeps, Module: "runtime", Subject: "docker", Detail: "the system Docker daemon and its 2 containers stay"},
	}}
}

const listeningProbe = ubuntuProbe + "listen=tcp LISTEN 0 5 0.0.0.0:8096 0.0.0.0:* users:((\"python3\",pid=2,fd=3))\n"

func TestUpInUseNeedsAdoptWithoutATerminal(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	r := &fakeRunner{report: inUseReport(), probe: listeningProbe}
	code, out := upWith(t, r, nil, "up", "lab", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-user", "ubuntu", "--agents", "none", "--tailscale=false", "--lan-ports", "none", "--ssh-key", key, "--yes")
	if code != 2 || !strings.Contains(out, "what the first apply would cut off or replace there (sam, docker)") || !strings.Contains(out, "keelbox up lab --provider ssh --adopt --yes") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if len(r.calls) != 0 {
		t.Errorf("changed something: %v", r.calls)
	}
	for _, want := range []string{"In use there now:", "  Cuts off:\n  *  sam         logs in", "  *  docker      the system daemon restarts: scratch has no restart policy and stays stopped (docker update --restart unless-stopped scratch brings them back)",
		"  Keeps:\n     docker      the system Docker daemon and its 2 containers stay"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "8096/tcp   a Python app") || strings.Contains(out, "sshd       reloads") {
		t.Errorf("repeats what the summary says:\n%s", out)
	}

	r = &fakeRunner{report: inUseReport(), probe: listeningProbe}
	code, out = upWith(t, r, nil, "up", "lab", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-user", "ubuntu", "--agents", "none", "--tailscale=false", "--lan-ports", "none", "--ssh-key", key, "--adopt", "--yes")
	if code != exitNeedsYou || !strings.HasPrefix(strings.Join(r.calls, "; "), "apply lab as ubuntu --adopt") {
		t.Fatalf("exit %d calls %v\n%s", code, r.calls, out)
	}
	if _, st := loadBox(t, "lab"); !st.Adopt {
		t.Error("the consent is not kept for a resumed first apply")
	}
}

func TestUpInUseAnsweredPortsNeedNoAdopt(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	rep := inUseReport()
	rep.Items = rep.Items[:1] // only the port up asks about
	r := &fakeRunner{report: rep, probe: listeningProbe}
	code, out := upWith(t, r, nil, "up", "lab", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-user", "ubuntu", "--agents", "none", "--tailscale=false", "--lan-ports", "none", "--ssh-key", key, "--yes")
	if code != exitNeedsYou || !strings.HasPrefix(strings.Join(r.calls, "; "), "apply lab as ubuntu --adopt") {
		t.Fatalf("exit %d calls %v\n%s", code, r.calls, out)
	}
}

func TestUpInUseAsksOnATerminal(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	args := []string{"up", "lab", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-user", "ubuntu", "--agents", "none", "--tailscale=false", "--lan-ports", "none", "--ssh-key", key, "--gh=false", "--skip-sign-in"}
	r := &fakeRunner{report: inUseReport(), probe: listeningProbe}
	code, out := upWith(t, r, script("", "", "n"), args...) // the host key, limit the agent, go ahead
	if code != 0 || !strings.Contains(out, "Go ahead, accepting the 2 items marked *? [y/N]") || strings.Contains(out, "Go ahead? [Y/n]") || strings.Contains(out, "Accept the items") ||
		!strings.Contains(out, "nothing changed. Your answers are kept") || len(r.calls) != 0 {
		t.Fatalf("exit %d calls %v\n%s", code, r.calls, out)
	}
	// One yes accepts and goes ahead.
	r = &fakeRunner{report: inUseReport(), probe: listeningProbe}
	code, out = upWith(t, r, script("y"), args...) // the kept answers are used for lab: only the go-ahead
	if code != 0 || !strings.HasPrefix(strings.Join(r.calls, "; "), "apply lab as ubuntu --adopt") || strings.Count(out, "Go ahead") != 1 {
		t.Fatalf("exit %d calls %v\n%s", code, r.calls, out)
	}
}

func TestUpWithoutImpactStillApplies(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	r := &fakeRunner{impactErr: errString("ssh: connection reset")}
	code, out := upWith(t, r, nil, "up", "lab", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-user", "ubuntu", "--agents", "none", "--tailscale=false", "--ssh-key", key, "--yes")
	if code != exitNeedsYou || !strings.Contains(out, "could not list what is in use there (ssh: connection reset); the first apply checks again") ||
		strings.Join(r.calls, "; ") != "apply lab as ubuntu; signed-in?" {
		t.Fatalf("exit %d calls %v\n%s", code, r.calls, out)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestUpAsksOnlyAboutPortsTheFirewallWouldCut(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	// UFW is on already: 8096 is blocked today, and stays so.
	r := &fakeRunner{report: &impactReport{Impact: 1, FirstApply: true, Machine: &inventory.Inventory{}, Items: []engine.ImpactItem{
		{Group: engine.ImpactKeeps, Module: "host", Subject: "ufw", Detail: "UFW is on already: its 2 rules stay"}}}, probe: listeningProbe}
	code, out := upWith(t, r, nil, "up", "lab", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-user", "ubuntu", "--agents", "none", "--tailscale=false", "--ssh-key", key, "--yes")
	if code != exitNeedsYou || strings.Contains(out, "--lan-ports") || strings.Contains(out, "8096") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if cfg, _ := loadBox(t, "lab"); len(cfg.Host.Firewall.Allow) != 0 {
		t.Errorf("opened a port UFW blocks today: %+v", cfg.Host.Firewall.Allow)
	}
}

// On a machine that runs other services, up limits the agent to 75% unless
// told otherwise; --limits sets another share or none.
func TestUpLimitsTheAgentWhereOtherServicesRun(t *testing.T) {
	for flag, want := range map[string]int{"": 75, "60%": 60, "none": 0} {
		home := isolate(t)
		key := laptopKey(t, home)
		rep := inUseReport()
		rep.Items = rep.Items[:1]
		args := []string{"up", "lab", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-user", "ubuntu", "--agents", "none", "--tailscale=false", "--lan-ports", "none", "--ssh-key", key, "--yes"}
		if flag != "" {
			args = append(args, "--limits", flag)
		}
		code, out := upWith(t, &fakeRunner{report: rep, probe: listeningProbe}, nil, args...)
		if code != exitNeedsYou {
			t.Fatalf("%s: exit %d\n%s", flag, code, out)
		}
		cfg, _ := loadBox(t, "lab")
		l, ok := cfg.AgentLimits()
		// none is written as memory: none, since without the key the agent
		// has the default memory ceiling.
		if ok != (want > 0) || l.CPUPercent != want || l.MemoryPercent != want {
			t.Errorf("--limits %q: %+v %v", flag, l, ok)
		}
	}
	home := isolate(t)
	key := laptopKey(t, home)
	if code, out := upWith(t, &fakeRunner{probe: listeningProbe}, nil, "up", "lab", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-key", key, "--yes", "--lan-ports", "none", "--limits", "200%"); code == 0 || !strings.Contains(out, "--limits \"200%\": use a share from 10% to 95%") {
		t.Errorf("bad share: %d\n%s", code, out)
	}
}

// No at the go-ahead keeps every answer, the LAN ports included: the next
// run asks only the go-ahead.
func TestUpDeclinedKeepsTheLANAnswer(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	rep := inUseReport()
	rep.Items = rep.Items[:1]
	args := []string{"up", "lab", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-user", "ubuntu", "--agents", "none", "--tailscale=false", "--ssh-key", key, "--gh=false", "--skip-sign-in"}
	r := &fakeRunner{report: rep, probe: listeningProbe}
	code, out := upWith(t, r, script("", "8096", "", "n"), args...) // host key, LAN ports, limit, go ahead
	if code != 0 || !strings.Contains(out, "Ports to keep open on the LAN") || len(r.calls) != 0 {
		t.Fatalf("exit %d calls %v\n%s", code, r.calls, out)
	}
	r = &fakeRunner{report: rep, probe: listeningProbe}
	code, out = upWith(t, r, script("y"), args...)
	if code != 0 || strings.Contains(out, "Ports to keep open on the LAN") || !strings.Contains(out, "using the answers you gave for lab before") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if cfg, _ := loadBox(t, "lab"); len(cfg.Host.Firewall.Allow) != 1 || cfg.Host.Firewall.Allow[0].Port != config.Port(8096) {
		t.Errorf("allow %+v", cfg.Host.Firewall.Allow)
	}
}

// A port kept open to the LAN inside the preview range is left out of it,
// and the summary says so before the go-ahead.
func TestUpSummarySaysPreviewsSkipALANPort(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	rep := &impactReport{Impact: 1, FirstApply: true, Machine: &inventory.Inventory{Hostname: "lab"}, Items: []engine.ImpactItem{
		{Group: engine.ImpactCuts, Module: "host", Subject: "8450/tcp", Gate: true, Detail: "a Python app listens on the network now", Keep: &config.Allow{Name: "python", Port: config.Port(8450)}},
	}}
	probe := ubuntuProbe + "listen=tcp LISTEN 0 5 0.0.0.0:8450 0.0.0.0:* users:((\"python3\",pid=2,fd=3))\n"
	r := &fakeRunner{report: rep, probe: probe}
	code, out := upWith(t, r, nil, "up", "lab", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-user", "ubuntu", "--agents", "none", "--lan-ports", "8450",
		"--ssh-key", key, "--gh=false", "--skip-sign-in", "--limits", "none", "--adopt", "--yes")
	if code != 0 || !strings.Contains(out, "previews    on the tailnet; skip 8450, kept open to the LAN by host.firewall.allow (python)") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// --yes on a terminal takes the proposed limit instead of asking, as the
// docs' keelbox up --provider local ... --yes does.
func TestUpYesTakesTheProposedLimit(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	r := &fakeRunner{report: &impactReport{Impact: 1, FirstApply: true, Machine: &inventory.Inventory{Hostname: "lab"}}, probe: listeningProbe}
	code, out := upWith(t, r, script(""), "up", "lab", "--provider", "ssh", "--host", "10.0.0.5", "--ssh-user", "ubuntu", "--agents", "none", "--tailscale=false",
		"--lan-ports", "none", "--ssh-key", key, "--gh=false", "--skip-sign-in", "--yes")
	if code != 0 || strings.Contains(out, "Limit the agent to") || !strings.Contains(out, "the agent gets at most 75% of CPU and memory") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}
