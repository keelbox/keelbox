package modules

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
)

const (
	overTailnet = "100.80.1.2 50000 100.101.102.103 22"
	overPublic  = "203.0.113.5 50000 192.0.2.10 22"
)

func TestDecidePublicSSH(t *testing.T) {
	up := tailnetFacts{running: true, ips: []string{"100.101.102.103"}, iface: []string{"100.101.102.103"}}
	down := tailnetFacts{}
	moved := tailnetFacts{running: true, ips: []string{"100.99.0.1"}, iface: []string{"100.99.0.1"}}
	notOnIface := tailnetFacts{running: true, ips: []string{"100.101.102.103"}, iface: nil}
	rec := &PublicSSHProof{Address: "100.101.102.103", User: "admin", Time: time.Unix(0, 0)}
	now := time.Now()
	for _, tc := range []struct {
		name     string
		mode     config.PublicSSHMode
		t        tailnetFacts
		rec      *PublicSSHProof
		conn     string
		invoking string
		closed   bool
		deferred bool
	}{
		{"fresh box, run on the box", config.PublicSSHAuto, down, nil, "", "", false, false},
		{"on the tailnet, apply over the public address", config.PublicSSHAuto, up, nil, overPublic, "admin", false, false},
		{"apply over the tailnet as the admin", config.PublicSSHAuto, up, nil, overTailnet, "admin", true, false},
		{"apply over the tailnet as the image's user", config.PublicSSHAuto, up, nil, overTailnet, "ubuntu", false, false},
		{"tailnet address not on tailscale0", config.PublicSSHAuto, notOnIface, nil, overTailnet, "admin", false, false},
		{"proven, run on the box", config.PublicSSHAuto, up, rec, "", "", true, false},
		{"proven, apply over the public address again", config.PublicSSHAuto, up, rec, overPublic, "admin", false, false},
		{"proven, tailnet down", config.PublicSSHAuto, down, rec, "", "", false, false},
		{"proven, tailnet address changed", config.PublicSSHAuto, moved, rec, "", "", false, false},
		{"closed without proof waits", config.PublicSSHClosed, up, nil, overPublic, "admin", false, true},
		{"closed, proven over the tailnet", config.PublicSSHClosed, up, nil, overTailnet, "admin", true, false},
		{"closed keeps a proof while the tailnet is down", config.PublicSSHClosed, down, rec, "", "", true, false},
		{"closed keeps a proof over the public address", config.PublicSSHClosed, up, rec, overPublic, "admin", true, false},
		{"closed, address changed, waits", config.PublicSSHClosed, moved, rec, "", "", false, true},
	} {
		p := decidePublicSSH(tc.mode, tc.t, tc.rec, tc.conn, tc.invoking, "admin", now)
		if p.closed != tc.closed || (p.deferred != "") != tc.deferred {
			t.Errorf("%s: closed=%v deferred=%q, want closed=%v deferred=%v (%s)", tc.name, p.closed, p.deferred, tc.closed, tc.deferred, p.reason)
		}
		if p.closed && p.proof == nil {
			t.Errorf("%s: closed without a proof", tc.name)
		}
	}
}

func applyVia(t *testing.T, e *engine.Env, conn, invoking string) engine.Applied {
	t.Helper()
	e.SSHConnection, e.Facts.InvokingUser = conn, invoking
	res, err := engine.Apply(context.Background(), e, All())
	if err != nil {
		t.Fatalf("apply via %q: %v", conn, err)
	}
	return res
}

func publicSSHFinding(e *engine.Env, check string) engine.Finding {
	for _, f := range engine.Doctor(context.Background(), e, All()) {
		if f.Module == "host" && f.Check == check {
			return f
		}
	}
	return engine.Finding{Detail: "missing"}
}

func (s *sim) publicSSHClosed(t *testing.T) bool {
	t.Helper()
	ufwClosed, sshdClosed := !s.ufw["ssh"], s.has(sshdTailnetOnly)
	if ufwClosed != sshdClosed {
		t.Fatalf("ufw closed=%v but sshd closed=%v", ufwClosed, sshdClosed)
	}
	st, _ := ReadPublicSSHStatus(s)
	if (st.State == "closed") != ufwClosed {
		t.Fatalf("recorded %q, ufw closed=%v", st.State, ufwClosed)
	}
	return ufwClosed
}

func TestPublicSSHClosesOnlyAfterATailnetApply(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	applyVia(t, e, overPublic, "ubuntu")
	if s.publicSSHClosed(t) {
		t.Fatal("closed on a box that is not on the tailnet")
	}
	if f := publicSSHFinding(e, "public-ssh"); f.Level != engine.LevelWarn || !strings.Contains(f.Detail, "not on the tailnet") {
		t.Errorf("doctor before the tailnet: %+v", f)
	}

	s.tailscale = "Running"
	applyVia(t, e, overPublic, "admin")
	if s.publicSSHClosed(t) {
		t.Fatal("closed by an apply that came over the public address")
	}

	res := applyVia(t, e, overTailnet, "admin")
	if !s.publicSSHClosed(t) {
		t.Fatal("not closed by an apply over the tailnet as the admin")
	}
	if !strings.Contains(s.content(sshdTailnetOnly), "Match LocalAddress *,!100.64.0.0/10,!fd7a:115c:a1e0::/48,!127.0.0.0/8,!::1\n\tDenyUsers *") {
		t.Errorf("sshd drop-in:\n%s", s.content(sshdTailnetOnly))
	}
	st, _ := ReadPublicSSHStatus(s)
	if st.Proof == nil || st.Proof.Address != "100.101.102.103" || st.Proof.User != "admin" || st.Proof.Client != "100.80.1.2" {
		t.Errorf("proof: %+v", st.Proof)
	}
	var changed []string
	for _, c := range res.Changes {
		changed = append(changed, c.Step)
	}
	if strings.Join(changed, ",") != "sshd-tailnet-only,firewall,public-ssh-state" {
		t.Errorf("closing changed %v", changed)
	}
	for _, conn := range []string{overTailnet, ""} {
		if again := applyVia(t, e, conn, "admin"); len(again.Changes) != 0 || len(again.Deferred) != 0 {
			t.Errorf("apply via %q after closing changed %v", conn, again.Changes)
		}
	}
	e.SSHConnection = ""
	for check, want := range map[string]string{"public-ssh": "closed: admin reached sshd over the tailnet", "public-ssh-firewall": "only on tailscale0", "public-ssh-sshd": "refuses"} {
		if f := publicSSHFinding(e, check); f.Level != engine.LevelOK || !strings.Contains(f.Detail, want) {
			t.Errorf("doctor %s: %+v", check, f)
		}
	}

	// An apply that reaches the box publicly again (the box was reopened
	// from its console, say) opens public SSH and forgets the proof.
	applyVia(t, e, overPublic, "admin")
	if s.publicSSHClosed(t) {
		t.Fatal("still closed after an apply over the public address")
	}
	if st, _ := ReadPublicSSHStatus(s); st.Proof != nil {
		t.Errorf("proof kept: %+v", st.Proof)
	}

	// Closed again, then the tailnet goes down: a run on the box opens it.
	applyVia(t, e, overTailnet, "admin")
	s.tailscale = "Stopped"
	applyVia(t, e, "", "")
	if s.publicSSHClosed(t) {
		t.Fatal("still closed with the tailnet down")
	}
}

func TestPublicSSHNeedsTheAdminOverTheTailnet(t *testing.T) {
	s := newSim(t)
	s.tailscale = "Running"
	e := simEnv(t, s, testConfig(t, ""))
	applyVia(t, e, overTailnet, "ubuntu")
	if s.publicSSHClosed(t) {
		t.Fatal("closed by the image's account")
	}
	s.tsIface = []string{"10.0.0.9"} // the address is Tailscale's, but not on tailscale0
	applyVia(t, e, overTailnet, "admin")
	if s.publicSSHClosed(t) {
		t.Fatal("closed though the address is not on tailscale0")
	}
}

func TestPublicSSHAddressChangeOpensIt(t *testing.T) {
	s := newSim(t)
	s.tailscale = "Running"
	e := simEnv(t, s, testConfig(t, ""))
	applyVia(t, e, overTailnet, "admin")
	if !s.publicSSHClosed(t) {
		t.Fatal("not closed")
	}
	s.tsIface = []string{"100.99.0.1"}
	applyVia(t, e, "", "")
	if s.publicSSHClosed(t) {
		t.Fatal("still closed after the box's tailnet address changed")
	}
}

func TestPublicSSHClosedModeWaitsForProof(t *testing.T) {
	s := newSim(t)
	s.tailscale = "Running"
	e := simEnv(t, s, testConfig(t, "host:\n  firewall:\n    public_ssh: closed\n"))
	res := applyVia(t, e, overPublic, "admin")
	if s.publicSSHClosed(t) {
		t.Fatal("closed without a proof")
	}
	if len(res.Deferred) != 1 || res.Deferred[0].Step != "public-ssh-state" {
		t.Errorf("deferred: %+v", res.Deferred)
	}
	if f := publicSSHFinding(e, "public-ssh"); f.Level != engine.LevelFail {
		t.Errorf("doctor without proof in closed mode: %+v", f)
	}
	applyVia(t, e, overTailnet, "admin")
	if !s.publicSSHClosed(t) {
		t.Fatal("not closed after the proof")
	}
	s.tailscale = "Stopped"
	applyVia(t, e, "", "")
	if !s.publicSSHClosed(t) {
		t.Fatal("closed mode reopened while the tailnet is down")
	}
}

func TestPublicSSHOpenModeNeverCloses(t *testing.T) {
	for _, v := range []string{"open", "true"} {
		s := newSim(t)
		s.tailscale = "Running"
		e := simEnv(t, s, testConfig(t, "host:\n  firewall:\n    public_ssh: "+v+"\n"))
		applyVia(t, e, overTailnet, "admin")
		if s.publicSSHClosed(t) {
			t.Fatalf("public_ssh: %s closed public SSH", v)
		}
		if f := publicSSHFinding(e, "public-ssh"); f.Level != engine.LevelOK {
			t.Errorf("doctor with public_ssh: %s: %+v", v, f)
		}
	}
}

func TestPublicSSHPlanChangesNothing(t *testing.T) {
	s := newSim(t)
	s.tailscale = "Running"
	e := simEnv(t, s, testConfig(t, ""))
	applyVia(t, e, overPublic, "admin")
	e.SSHConnection = overTailnet
	before := s.Paths()
	for _, it := range engine.Plan(context.Background(), e, All()) {
		if it.Module == "host" && it.Step == "public-ssh-state" && it.OK {
			t.Error("plan over the tailnet shows nothing to close")
		}
	}
	if after := s.Paths(); strings.Join(after, ",") != strings.Join(before, ",") || s.publicSSHClosed(t) {
		t.Error("plan changed the box")
	}
}

// public_ssh: lan closes SSH to the internet on the same proof as auto, and
// keeps it open from the LAN: a UFW rule per private range and an sshd
// Match that lets LAN clients in.
func TestPublicSSHLANKeepsTheLAN(t *testing.T) {
	s := newSim(t)
	s.tailscale = "Running"
	e := simEnv(t, s, testConfig(t, "host:\n  firewall:\n    public_ssh: lan\n"))
	applyVia(t, e, overPublic, "admin")
	if s.publicSSHClosed(t) {
		t.Fatal("closed without a tailnet proof")
	}
	applyVia(t, e, overTailnet, "admin")
	if !s.publicSSHClosed(t) {
		t.Fatal("not closed by an apply over the tailnet")
	}
	drop := s.content(sshdTailnetOnly)
	if !strings.Contains(drop, "Match LocalAddress *,!100.64.0.0/10,!fd7a:115c:a1e0::/48,!127.0.0.0/8,!::1 Address *,!10.0.0.0/8,!172.16.0.0/12,!192.168.0.0/16,!fc00::/7\n\tDenyUsers *") {
		t.Errorf("sshd drop-in:\n%s", drop)
	}
	have, _ := haveAllow(context.Background(), e)
	var lan []string
	for _, r := range have {
		if r.label == "ssh-lan" && r.port == config.Port(22) {
			lan = append(lan, r.src)
		}
	}
	if strings.Join(lan, " ") != "10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 fc00::/7" {
		t.Errorf("LAN SSH rules %v (all %+v)", lan, have)
	}
	if again := applyVia(t, e, overTailnet, "admin"); len(again.Changes) != 0 {
		t.Errorf("second apply changed %v", again.Changes)
	}
	e.SSHConnection = ""
	for check, want := range map[string]string{"public-ssh": "closed to the internet, open to the LAN", "public-ssh-firewall": "and from the LAN", "public-ssh-sshd": "or come from the LAN"} {
		if f := publicSSHFinding(e, check); f.Level != engine.LevelOK || !strings.Contains(f.Detail, want) {
			t.Errorf("doctor %s: %+v", check, f)
		}
	}
}
