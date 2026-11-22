package modules

import (
	"context"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
)

// On a machine cloud-init identifies as AWS, apply keeps the file that
// tells cloud-init to leave the SSH host keys and the hostname when the
// disk boots on a new instance; anywhere else it writes nothing.
func TestCloudInitKeepsHostKeysOnAWSOnly(t *testing.T) {
	ctx := context.Background()
	changed := func(res engine.Applied) bool {
		for _, c := range res.Changes {
			if c.Step == "cloud-init-keeps-host-keys" {
				return true
			}
		}
		return false
	}
	for _, id := range []string{"", "hetzner\n", "lxd\n"} {
		s := newSim(t)
		if id != "" {
			s.AddFile(cloudIDFile, id, 0o644)
		}
		res, err := engine.Apply(ctx, simEnv(t, s, testConfig(t, "")), All())
		if err != nil {
			t.Fatal(err)
		}
		if s.has(CloudInitKeepFile) || changed(res) {
			t.Errorf("cloud-id %q: %s was written", id, CloudInitKeepFile)
		}
	}

	s := newSim(t)
	s.AddFile(cloudIDFile, "aws\n", 0o644)
	e := simEnv(t, s, testConfig(t, ""))
	res, err := engine.Apply(ctx, e, All())
	if err != nil {
		t.Fatal(err)
	}
	got := s.content(CloudInitKeepFile)
	if !changed(res) || !strings.HasPrefix(got, "# Managed by keelbox.") || !strings.Contains(got, "\nssh_deletekeys: false\npreserve_hostname: true\nusers: []\n") {
		t.Fatalf("%s after apply:\n%s", CloudInitKeepFile, got)
	}
	if f := s.Files[CloudInitKeepFile]; f.Mode != 0o644 || f.Owner != "root" {
		t.Errorf("mode %o, owner %s", f.Mode, f.Owner)
	}
	// A second apply changes nothing; an edited file is put back.
	if res, err := engine.Apply(ctx, e, All()); err != nil || len(res.Changes) != 0 {
		t.Errorf("second apply: %v, %v", res.Changes, err)
	}
	s.AddFile(CloudInitKeepFile, "ssh_deletekeys: true\n", 0o644)
	pending := false
	for _, it := range engine.Plan(ctx, e, All()) {
		pending = pending || (it.Step == "cloud-init-keeps-host-keys" && !it.OK)
	}
	if !pending {
		t.Error("plan does not report the edited file")
	}
	if res, err := engine.Apply(ctx, e, All()); err != nil || !changed(res) || s.content(CloudInitKeepFile) != got {
		t.Errorf("the edited file was not put back: %v\n%s", err, s.content(CloudInitKeepFile))
	}
}
