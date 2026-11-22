package hetzner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/provider/hetzner/hetznertest"
)

const laptopKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGq2XkqzU0W2C0E3dX1rN4oG2b6yXn0Q0bQ1zQ1zQ1zQ you@laptop"

func fake(t *testing.T) (*Client, *hetznertest.API) {
	a := hetznertest.New(t, token)
	c := New(token)
	c.Endpoint = a.URL
	return c, a
}

func TestCheckToken(t *testing.T) {
	c, a := fake(t)
	a.Servers = []map[string]any{{"id": 1, "name": "web", "labels": map[string]string{}}, {"id": 2, "name": "acme", "labels": map[string]string{"keelbox": "true"}}}
	tc, err := c.CheckToken(context.Background())
	if err != nil || tc.Servers != 2 || tc.Unmanaged != 1 {
		t.Fatalf("%+v %v", tc, err)
	}
	if len(a.Keys) != 0 {
		t.Error("the write check created a key")
	}
	ro := New(a.ReadOnlyToken)
	ro.Endpoint = a.URL
	if _, err := ro.CheckToken(context.Background()); !errors.Is(err, ErrTokenReadOnly) {
		t.Errorf("read-only token: %v", err)
	}
	bad := New("wrong-token-xyz")
	bad.Endpoint = a.URL
	_, err = bad.CheckToken(context.Background())
	if !errors.Is(err, ErrTokenRefused) || strings.Contains(err.Error(), "wrong-token-xyz") {
		t.Errorf("refused token: %v", err)
	}
}

func TestMD5FingerprintMatchesSSHKeygen(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	out, err := exec.Command("ssh-keygen", "-l", "-E", "md5", "-f", key+".pub").Output()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimPrefix(strings.Fields(string(out))[1], "MD5:")
	pub, _ := os.ReadFile(key + ".pub")
	if got, err := MD5Fingerprint(string(pub)); err != nil || got != want {
		t.Errorf("got %s, ssh-keygen says %s (%v)", got, want, err)
	}
	if _, err := MD5Fingerprint("not a key"); err == nil {
		t.Error("accepted a non-key")
	}
}

func TestEnsureSSHKeyUploadsOnceAndReuses(t *testing.T) {
	c, a := fake(t)
	ctx := context.Background()
	k, created, err := EnsureSSHKey(ctx, c, "keelbox-mac", laptopKey)
	if err != nil || !created || k.Name != "keelbox-mac" || a.Keys[0].Labels[LabelManaged] != "true" {
		t.Fatalf("upload: %+v %v %v", k, created, err)
	}
	k, created, err = EnsureSSHKey(ctx, c, "keelbox-other-name", laptopKey)
	if err != nil || created || k.Name != "keelbox-mac" || len(a.Keys) != 1 {
		t.Fatalf("same key again: %+v %v %v (%d keys)", k, created, err, len(a.Keys))
	}
	// A different key under a taken name gets a suffix.
	other := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBq2XkqzU0W2C0E3dX1rN4oG2b6yXn0Q0bQ1zQ1zQ1zQ other@mac"
	k, created, err = EnsureSSHKey(ctx, c, "keelbox-mac", other)
	if err != nil || !created || k.Name != "keelbox-mac-2" {
		t.Fatalf("taken name: %+v %v %v", k, created, err)
	}
	if err := c.DeleteSSHKey(ctx, k.ID); err != nil || len(a.Keys) != 1 || a.Keys[0].Name != "keelbox-mac" {
		t.Fatalf("delete: %v, left %+v", err, a.Keys)
	}
}

func TestOffersListWhatCanBeOrderedCheapestFirst(t *testing.T) {
	c, _ := fake(t)
	types, err := c.ServerTypes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	offers := Offers(types, "fsn1")
	var names []string
	for _, o := range offers {
		names = append(names, o.Type.Name)
	}
	// cpx11 is not sold in fsn1 and cx33 is being retired there. cax11 is
	// Arm64 and is offered beside the x86 types (ADR 0087).
	if got := strings.Join(names, ","); !strings.Contains(","+got+",", ",cax11,") || strings.Contains(got, "cpx11") || strings.Contains(got, "cx33") {
		t.Errorf("offers %s", got)
	}
	if d := offers[DefaultOffer(offers)]; d.Type.Name != "cx23" {
		t.Errorf("default %s", d.Type.Name)
	}
	for _, o := range offers {
		if o.Type.Name == "cax11" && (!o.Arm() || !strings.Contains(o.Describe(), "(shared, Arm64)")) {
			t.Errorf("an Arm64 type is not marked: %q", o.Describe())
		}
	}
	// x86 is the default while there is one of the size; with none, the
	// cheapest Arm64 type of the size is.
	var armOnly []Offer
	for _, o := range offers {
		if o.Arm() || o.Small() {
			armOnly = append(armOnly, o)
		}
	}
	if d := armOnly[DefaultOffer(armOnly)]; d.Type.Name != "cax11" {
		t.Errorf("default with no x86 type of the size: %s", d.Type.Name)
	}
	if got := offers[0].Describe(); got != "2 vCPU (shared), 4 GB, 40 GB disk, EUR 3.79/month (EUR 0.0060/h)" {
		t.Errorf("describe %q", got)
	}
	// Below 2 vCPU or 4 GB: marked in up's size menu.
	for _, o := range offers {
		if o.Type.Name == "cpx02" && !strings.HasSuffix(o.Describe(), "; small for agents") {
			t.Errorf("small type %q", o.Describe())
		}
	}
	if offers[0].Small() {
		t.Errorf("the first offer reads as small: %q", offers[0].Describe())
	}
	if n := len(Offers(types, "ash")); n != 2 {
		t.Errorf("ash: %d offers", n)
	}
	if n := len(Offers(types, "nowhere")); n != 0 {
		t.Errorf("unknown location: %d offers", n)
	}
}
