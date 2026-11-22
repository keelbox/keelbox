package hetzner

import (
	"context"
	"strings"
	"testing"
)

func TestCreateAttachesAFirewallFromFirstBoot(t *testing.T) {
	cl, a := setup(t)
	ctx := context.Background()
	cfg := boxConfig(t, "ubuntu-26.04")
	if _, err := Create(ctx, cl, cfg, testHostKey, t.Logf); err != nil {
		t.Fatal(err)
	}
	if len(a.firewalls) != 1 {
		t.Fatalf("%d firewalls", len(a.firewalls))
	}
	var f *Firewall
	for _, x := range a.firewalls {
		f = x
	}
	if f.Name != "keelbox-acme" || f.Labels[LabelManaged] != "true" || f.Labels[LabelBox] != "acme" || f.Labels["keelbox-e2e"] != "true" {
		t.Errorf("firewall %+v", f)
	}
	if !SameRules(f.Rules, FirewallRules(true)) || !HasPublicSSH(f) {
		t.Errorf("new firewall rules %+v", f.Rules)
	}
	for _, r := range f.Rules {
		if r.Direction != "in" {
			t.Errorf("outbound rule %+v: outbound must stay unrestricted", r)
		}
	}
	if fws := a.created[0].Firewalls; len(fws) != 1 || fws[0].Firewall != f.ID {
		t.Errorf("server created without the firewall: %+v", a.created[0].Firewalls)
	}
}

func TestEnsureFirewallReconciles(t *testing.T) {
	cl, a := setup(t)
	ctx := context.Background()
	cfg := boxConfig(t, "ubuntu-26.04")
	// An existing box without a firewall: created with SSH, attached.
	f, changes, err := EnsureFirewall(ctx, cl, cfg, 42, nil)
	if err != nil || !f.AppliedToServer(42) || !HasPublicSSH(f) || len(changes) != 2 {
		t.Fatalf("create: %+v %v %v", f, changes, err)
	}
	// Nothing to do: no changes, SSH kept.
	if _, changes, err := EnsureFirewall(ctx, cl, cfg, 42, nil); err != nil || len(changes) != 0 {
		t.Fatalf("second ensure: %v %v", changes, err)
	}
	// The box closed public SSH: the SSH rule goes, Tailscale's stays.
	closed := false
	if f, changes, err = EnsureFirewall(ctx, cl, cfg, 42, &closed); err != nil || len(changes) != 1 || HasPublicSSH(a.firewalls[f.ID]) {
		t.Fatalf("close: %v %v %+v", changes, err, a.firewalls[f.ID].Rules)
	}
	if got := DescribeRules(a.firewalls[f.ID].Rules); got != "in udp 41641" {
		t.Errorf("closed rules: %s", got)
	}
	// nil keeps the closed state.
	if _, changes, _ := EnsureFirewall(ctx, cl, cfg, 42, nil); len(changes) != 0 || HasPublicSSH(a.firewalls[f.ID]) {
		t.Errorf("keep: %v", changes)
	}
	// Someone edits the rules and detaches it: apply restores both.
	a.firewalls[f.ID].Rules = append(a.firewalls[f.ID].Rules, FirewallRule{Direction: "in", Protocol: "tcp", Port: "80", SourceIPs: anywhere})
	a.firewalls[f.ID].AppliedTo = nil
	if _, changes, err := EnsureFirewall(ctx, cl, cfg, 42, &closed); err != nil || len(changes) != 2 {
		t.Fatalf("reconcile: %v %v", changes, err)
	}
	if !SameRules(a.firewalls[f.ID].Rules, FirewallRules(false)) || !a.firewalls[f.ID].AppliedToServer(42) {
		t.Errorf("not reconciled: %+v", a.firewalls[f.ID])
	}
}

func TestFirewallsWithoutKeelboxLabelsAreLeftAlone(t *testing.T) {
	cl, a := setup(t)
	a.firewalls[7] = &Firewall{ID: 7, Name: "keelbox-acme", Labels: map[string]string{}}
	if _, _, err := EnsureFirewall(context.Background(), cl, boxConfig(t, "ubuntu-26.04"), 42, nil); err == nil || !strings.Contains(err.Error(), "without keelbox's labels") {
		t.Fatalf("got %v", err)
	}
	if f, err := DestroyFirewall(context.Background(), cl, "acme"); err != nil || f != nil || len(a.fwDeleted) != 0 {
		t.Fatalf("destroy touched an unlabelled firewall: %v %v", f, err)
	}
	// Labelled for another box: not this box's either.
	a.firewalls[8] = &Firewall{ID: 8, Name: "keelbox-other", Labels: map[string]string{LabelManaged: "true", LabelBox: "other"}}
	if f, _ := DestroyFirewall(context.Background(), cl, "acme"); f != nil || len(a.fwDeleted) != 0 {
		t.Fatal("destroy deleted another box's firewall")
	}
}

func TestDestroyFirewallDetachesAndRetries(t *testing.T) {
	cl, a := setup(t)
	ctx := context.Background()
	cfg := boxConfig(t, "ubuntu-26.04")
	f, _, err := EnsureFirewall(ctx, cl, cfg, 42, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.inUse = 1
	got, err := DestroyFirewall(ctx, cl, "acme")
	if err != nil || got == nil || got.ID != f.ID || len(a.fwDeleted) != 1 {
		t.Fatalf("destroy: %+v %v %v", got, err, a.fwDeleted)
	}
	if f, err := DestroyFirewall(ctx, cl, "acme"); f != nil || err != nil {
		t.Errorf("second destroy: %v %v", f, err)
	}
}
