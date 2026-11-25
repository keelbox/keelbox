package hetzner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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
				"Hetzner may be limiting new servers for this account or in fsn1",
				"No server was created, and keelbox deleted the firewall it had made for it",
			} {
				if !strings.Contains(msg, want) {
					t.Errorf("message lacks %q:\n%s", want, msg)
				}
			}
			if len(a.Servers) != 0 || len(a.Firewalls) != 0 || !strings.Contains(strings.Join(said, "\n"), "deleted firewall keelbox-acme") {
				t.Errorf("left %d servers, %d firewalls", len(a.Servers), len(a.Firewalls))
			}
			if strings.Join(re.OtherLocations, ",") != "ash" || strings.Join(re.OtherTypes, ",") != "cax11,cpx02,cpx22,ccx13" {
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
