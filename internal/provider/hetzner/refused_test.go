package hetzner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/provider/hetzner/hetznertest"
)

// refusals are the shapes Hetzner's refusal of an order may take. The
// code for an account Hetzner limits is not documented; the first three
// are the ones reported for a type out of stock, a project limit and a
// placement failure.
var refusals = []hetznertest.Error{
	{Status: 412, Code: "resource_unavailable", Message: `server type "cx23" is not available in location "fsn1"`},
	{Status: 403, Code: "resource_limit_exceeded", Message: "server limit exceeded for this project"},
	{Status: 422, Code: "placement_error", Message: "error during placement"},
	{Status: 403, Code: "forbidden", Message: "creating servers in this location is not possible for this account at the moment"},
	{Status: 422, Code: "location_unavailable", Message: "a code keelbox has not seen"},
}

func TestCreateRefusedKeepsHetznersAnswerAndRemovesTheFirewall(t *testing.T) {
	for _, e := range refusals {
		t.Run(e.Code, func(t *testing.T) {
			cl, a := fake(t)
			a.Keys = []hetznertest.Key{{ID: 7, Name: "laptop"}}
			a.Refuse = map[string]hetznertest.Error{"create_server": e}
			var said []string
			logf := func(f string, v ...any) { said = append(said, fmt.Sprintf(f, v...)) }
			_, err := Create(context.Background(), cl, boxConfig(t, "ubuntu-26.04"), testHostKey, logf)
			var re *RefusedError
			if !errors.As(err, &re) {
				t.Fatalf("not a refusal: %v", err)
			}
			msg := err.Error()
			for _, want := range []string{
				"Hetzner refused to create acme's server, cx23 in fsn1",
				fmt.Sprintf("hetzner api: %d %s: %s", e.Status, e.Code, e.Message),
				"No server was created, and keelbox deleted the firewall it had made for it",
			} {
				if !strings.Contains(msg, want) {
					t.Errorf("message lacks %q:\n%s", want, msg)
				}
			}
			why := "Hetzner may be limiting new servers for this account or in fsn1 (status.hetzner.com)"
			if e.Code == "resource_limit_exceeded" {
				why = "The project is at one of its Hetzner limits, such as its number of servers or cores, in every location. Ask Hetzner for a higher limit in the Hetzner Console, or delete a server you no longer need"
			}
			if !strings.Contains(msg, why) || strings.Contains(msg, "by itself") || re.Quota() != (e.Code == "resource_limit_exceeded") {
				t.Errorf("message lacks %q:\n%s", why, msg)
			}
			if len(a.Servers) != 0 || len(a.Firewalls) != 0 || !strings.Contains(strings.Join(said, "\n"), "deleted firewall keelbox-acme") {
				t.Errorf("left %d servers, %d firewalls", len(a.Servers), len(a.Firewalls))
			}
			if strings.Join(re.OtherLocations, ",") != "ash" || strings.Join(re.OtherTypes, ",") != "cpx22,ccx13,cpx02" {
				t.Errorf("alternatives: locations %v, types %v", re.OtherLocations, re.OtherTypes)
			}
			var ae *APIError
			if !errors.As(err, &ae) || ae.Code != e.Code {
				t.Errorf("Hetzner's error is not wrapped: %v", ae)
			}
			if strings.Contains(msg, token) {
				t.Error("token in the message")
			}
		})
	}
}

// A firewall that was there before the create stays as it is, attached
// to whatever it is attached to.
func TestCreateRefusedLeavesAFirewallItDidNotMake(t *testing.T) {
	cl, a := fake(t)
	a.Keys = []hetznertest.Key{{ID: 7, Name: "laptop"}}
	a.Firewalls = []map[string]any{hetznertest.Firewall(7, "acme", 42)}
	a.Refuse = map[string]hetznertest.Error{"create_server": refusals[0]}
	_, err := Create(context.Background(), cl, boxConfig(t, "ubuntu-26.04"), testHostKey, t.Logf)
	want := "No server was created. The firewall keelbox-acme stays as it is: something is attached to it; your SSH key stays in the project for the next try"
	if err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Fatalf("got %v", err)
	}
	if len(a.Firewalls) != 1 || len(a.Firewalls[0]["applied_to"].([]any)) != 1 {
		t.Errorf("firewall changed: %v", a.Firewalls)
	}
	for _, c := range a.Calls() {
		if strings.Contains(c, "/firewalls/7") && !strings.HasPrefix(c, "GET ") {
			t.Errorf("sent %s", c)
		}
	}
}

// A firewall that was there before, with nothing attached, stays too; the
// message says where to delete it.
func TestCreateRefusedLeavesAnUnattachedFirewallItDidNotMake(t *testing.T) {
	cl, a := fake(t)
	a.Keys = []hetznertest.Key{{ID: 7, Name: "laptop"}}
	a.Firewalls = []map[string]any{hetznertest.Firewall(7, "acme")}
	a.Refuse = map[string]hetznertest.Error{"create_server": refusals[0]}
	_, err := Create(context.Background(), cl, boxConfig(t, "ubuntu-26.04"), testHostKey, t.Logf)
	want := "No server was created. The firewall keelbox-acme stays (it was there before this create), free of charge; should acme move to another cloud, delete it in the Hetzner Console (Firewalls); your SSH key stays in the project for the next try"
	if err == nil || !strings.HasSuffix(err.Error(), want) || len(a.Firewalls) != 1 {
		t.Fatalf("got %v, firewalls %v", err, a.Firewalls)
	}
}

// The firewall this create made stays when it cannot be shown to be
// unused, and the message says why.
func TestCreateRefusedKeepsItsFirewallWhenUnsure(t *testing.T) {
	const tail = "), free of charge; should acme move to another cloud, delete it in the Hetzner Console (Firewalls); your SSH key stays in the project for the next try"
	for _, c := range []struct {
		name, why string
		onRefuse  func(a *hetznertest.API)
	}{
		{"attached", "", func(a *hetznertest.API) {
			a.Firewalls[0]["applied_to"] = []any{map[string]any{"type": "server", "server": map[string]any{"id": 99}}}
		}},
		{"lookup fails", "looking it up again failed: ", func(a *hetznertest.API) { a.Fail = map[string]bool{"list_firewalls": true} }},
		{"delete fails", "deleting it failed: ", func(a *hetznertest.API) { a.Fail = map[string]bool{"delete_firewall": true} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			cl, a := fake(t)
			a.Keys = []hetznertest.Key{{ID: 7, Name: "laptop"}}
			a.Refuse = map[string]hetznertest.Error{"create_server": refusals[0]}
			a.OnCall = func(call string) {
				if call == "POST /servers" {
					c.onRefuse(a)
				}
			}
			_, err := Create(context.Background(), cl, boxConfig(t, "ubuntu-26.04"), testHostKey, t.Logf)
			var re *RefusedError
			if !errors.As(err, &re) {
				t.Fatalf("not a refusal: %v", err)
			}
			msg := err.Error()
			ok := strings.Contains(msg, "No server was created. The firewall keelbox-acme stays ("+c.why) && strings.HasSuffix(msg, tail)
			if c.why == "" {
				// Not to be deleted by hand: it is in use.
				ok = strings.HasSuffix(msg, "No server was created. The firewall keelbox-acme stays as it is: something is attached to it; your SSH key stays in the project for the next try")
			}
			if !ok {
				t.Errorf("got %s", msg)
			}
			if len(a.Firewalls) != 1 {
				t.Errorf("firewall deleted: %v", a.Firewalls)
			}
		})
	}
}

// Failures before the order say no server was ordered: where the box is
// created may still change.
func TestCreateFailuresBeforeTheOrder(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(cfg *config.Config, a *hetznertest.API)
	}{
		{"no such type", func(cfg *config.Config, a *hetznertest.API) { cfg.Target.Hetzner.ServerType = "cpx222" }},
		{"no SSH key", func(cfg *config.Config, a *hetznertest.API) { a.Keys = nil }},
		{"firewall", func(cfg *config.Config, a *hetznertest.API) { a.Fail = map[string]bool{"list_firewalls": true} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			cl, a := fake(t)
			a.Keys = []hetznertest.Key{{ID: 7, Name: "laptop"}}
			cfg := boxConfig(t, "ubuntu-26.04")
			c.set(cfg, a)
			_, err := Create(context.Background(), cl, cfg, testHostKey, t.Logf)
			if !provider.NotOrdered(err) {
				t.Errorf("not marked as not ordered: %v", err)
			}
			if len(a.Created) != 0 {
				t.Errorf("ordered: %v", a.Created)
			}
		})
	}
	// An order Hetzner answered with an error is not certain to have
	// created nothing.
	cl, a := fake(t)
	a.Keys = []hetznertest.Key{{ID: 7, Name: "laptop"}}
	a.Fail = map[string]bool{"create_server": true}
	if _, err := Create(context.Background(), cl, boxConfig(t, "ubuntu-26.04"), testHostKey, t.Logf); err == nil || provider.NotOrdered(err) {
		t.Errorf("got %v", err)
	}
}

// After the types at least as large as the one asked for come the smaller
// ones, the largest first, then the other architecture.
func TestNearestOffersOrder(t *testing.T) {
	ty := func(name, arch string, cores int, mem float64) Offer {
		return Offer{Type: ServerType{Name: name, Architecture: arch, Cores: cores, Memory: mem}}
	}
	offers := []Offer{
		ty("tiny", "x86", 1, 1), ty("arm", "arm", 2, 4), ty("small", "x86", 2, 2), ty("same", "x86", 2, 4), ty("big", "x86", 4, 8), ty("mid", "x86", 1, 2),
	}
	var got []string
	for _, o := range NearestOffers(offers, ServerType{Name: "cx23", Architecture: "x86", Cores: 2, Memory: 4}) {
		got = append(got, o.Type.Name)
	}
	if strings.Join(got, ",") != "same,big,small,mid,tiny,arm" {
		t.Errorf("got %v", got)
	}
}

// What is wrong with keelbox's own request, or its token, is not a
// refusal: another location would not change it.
func TestCreateErrorsThatAreNotRefusals(t *testing.T) {
	for _, e := range []hetznertest.Error{
		{Status: 422, Code: "invalid_input", Message: "invalid input in field 'user_data'"},
		{Status: 403, Code: "token_readonly", Message: "the token is read-only"},
		{Status: 429, Code: "rate_limit_exceeded", Message: "limit of 3600 requests per hour reached"},
		{Status: 500, Code: "server_error", Message: "error"},
	} {
		cl, a := fake(t)
		a.Keys = []hetznertest.Key{{ID: 7, Name: "laptop"}}
		a.Refuse = map[string]hetznertest.Error{"create_server": e}
		_, err := Create(context.Background(), cl, boxConfig(t, "ubuntu-26.04"), testHostKey, t.Logf)
		var re *RefusedError
		if err == nil || errors.As(err, &re) {
			t.Errorf("%s: %v", e.Code, err)
		}
	}
}

// A type Hetzner's own list says cannot be ordered there is refused before
// anything is sent.
func TestCreateOfATypeNotOrderableThere(t *testing.T) {
	cl, a := fake(t)
	a.Keys = []hetznertest.Key{{ID: 7, Name: "laptop"}}
	cfg := boxConfig(t, "ubuntu-26.04")
	cfg.Target.Hetzner.ServerType = "cpx11"
	_, err := Create(context.Background(), cl, cfg, testHostKey, t.Logf)
	var re *RefusedError
	if !errors.As(err, &re) || re.API != nil || err.Error() != "Hetzner lists server type cpx11 as not orderable in fsn1 now. Nothing was created" {
		t.Fatalf("got %v", err)
	}
	for _, c := range a.Calls() {
		if strings.HasPrefix(c, "POST ") {
			t.Errorf("sent %s", c)
		}
	}
}

func TestResumeRefusedKeepsTheSnapshotAndTheFirewall(t *testing.T) {
	cl, a, s := parkedProject(t)
	ctx := context.Background()
	cfg := boxConfig(t, "ubuntu-26.04")
	a.Images = []map[string]any{hetznertest.Snapshot(9, "acme", time.Now())}
	img, _ := Parked(ctx, cl, "acme")
	if err := Destroy(ctx, cl, s); err != nil {
		t.Fatal(err)
	}
	a.Refuse = map[string]hetznertest.Error{"create_server fsn1": refusals[0]}
	_, err := Restore(ctx, cl, cfg, img, t.Logf)
	var re *RefusedError
	if !errors.As(err, &re) || re.Action != "resume" {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "Hetzner refused to create acme's server again from its snapshot, cx23 in fsn1 (hetzner api: 412 resource_unavailable: "+refusals[0].Message+")") {
		t.Errorf("message %s", err)
	}
	// Only types that fit the 40 GB snapshot.
	if strings.Join(re.OtherTypes, ",") != "cpx22,ccx13" || strings.Join(re.OtherLocations, ",") != "ash" {
		t.Errorf("alternatives: types %v, locations %v", re.OtherTypes, re.OtherLocations)
	}
	if len(a.Images) != 1 || len(a.Firewalls) != 1 || len(a.Servers) != 0 {
		t.Errorf("images %d, firewalls %d, servers %d", len(a.Images), len(a.Firewalls), len(a.Servers))
	}
}

func TestResizeRefused(t *testing.T) {
	cl, a, s := parkedProject(t)
	ctx := context.Background()
	if err := Stop(ctx, cl, "acme", s, t.Logf); err != nil {
		t.Fatal(err)
	}
	a.Refuse = map[string]hetznertest.Error{"change_type": refusals[3]}
	err := Resize(ctx, cl, "acme", s, "cpx22")
	var re *RefusedError
	if !errors.As(err, &re) {
		t.Fatalf("got %v", err)
	}
	want := "Hetzner refused to change server 42 to cpx22 (hetzner api: 403 forbidden: " + refusals[3].Message + "). Hetzner may be limiting changes to servers for this account or in fsn1"
	if !strings.HasPrefix(err.Error(), want) {
		t.Errorf("message %s", err)
	}
	// A running server is keelbox's mistake, not a refusal.
	a.Refuse = nil
	if _, err := Start(ctx, cl, "acme", s); err != nil {
		t.Fatal(err)
	}
	if err := Resize(ctx, cl, "acme", s, "cpx22"); err == nil || errors.As(err, &re) {
		t.Errorf("resize of a running server: %v", err)
	}
}
