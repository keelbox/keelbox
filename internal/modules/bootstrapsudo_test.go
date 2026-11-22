package modules

import (
	"context"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
)

// A bootstrap sudoers file goes once the admin works: deferred while
// keelbox runs over SSH as the account it serves, removed by the admin's
// apply, and doctor fails while it is there.
func TestBootstrapSudoersRemovedByTheAdmin(t *testing.T) {
	s := newSim(t)
	const f = "/etc/sudoers.d/90-keelbox-bootstrap"
	s.AddFile(f, "sam ALL=(ALL) NOPASSWD:ALL\n", 0o440)
	e := simEnv(t, s, testConfig(t, ""))
	e.SSHConnection, e.Facts.InvokingUser = "192.168.1.5 50000 192.168.1.20 22", "sam"
	ctx := context.Background()
	res, err := engine.Apply(ctx, e, All())
	if err != nil {
		t.Fatal(err)
	}
	var deferred bool
	for _, d := range res.Deferred {
		deferred = deferred || d.Step == BootstrapSudoersStep
	}
	if st, _ := s.Stat(f); !deferred || !st.Exists {
		t.Fatalf("removed in sam's own session: %+v", res.Deferred)
	}
	found := false
	for _, x := range engine.Doctor(ctx, e, All()) {
		if x.Check == "bootstrap-sudo" {
			found = true
			if x.Level != engine.LevelFail || !strings.Contains(x.Detail, "gives sam sudo without a password") || !strings.Contains(x.Detail, "keelbox apply "+e.Cfg.Box) {
				t.Errorf("doctor: %+v", x)
			}
		}
	}
	if !found {
		t.Error("doctor has no bootstrap-sudo check")
	}
	e.Facts.InvokingUser = "admin"
	res, err = engine.Apply(ctx, e, All())
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Stat(f); st.Exists || len(res.Changes) != 1 || res.Changes[0].Step != BootstrapSudoersStep || !strings.Contains(res.Changes[0].Detail, f+", which gives sam sudo without a password") {
		t.Fatalf("admin apply: %+v", res.Changes)
	}
	for _, x := range engine.Doctor(ctx, e, All()) {
		if x.Check == "bootstrap-sudo" && x.Level != engine.LevelOK {
			t.Errorf("doctor after removal: %+v", x)
		}
	}
}

// On the machine itself sudo can ask, so the file goes in the first apply.
func TestBootstrapSudoersRemovedLocally(t *testing.T) {
	s := newSim(t)
	s.AddFile("/etc/sudoers.d/90-keelbox-bootstrap", "alex ALL=(ALL) NOPASSWD:ALL\n", 0o440)
	e := simEnv(t, s, testConfig(t, ""))
	e.Facts.InvokingUser = "alex"
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.Stat("/etc/sudoers.d/90-keelbox-bootstrap"); st.Exists {
		t.Error("left in place on a local apply")
	}
}
