package cli

import (
	"io"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
)

// A run over the tailnet still tells the box where it is reached outside
// it: target.ssh.host and, for a cloud box, the provider's public address.
func TestSSHTargetCarriesTheOutsideAddresses(t *testing.T) {
	cfg := &config.Config{}
	cfg.Target.SSH.Host, cfg.Target.SSH.Port, cfg.Target.SSH.User = "box.example", 2222, "admin"
	s := sshTarget(cfg, addr{"100.101.102.103", 22}, options{publicIP: "203.0.113.7"}, io.Discard, io.Discard)
	want := []provider.Addr{{Host: "box.example", Port: 2222}, {Host: "203.0.113.7", Port: 2222}}
	if s.Host != "100.101.102.103" || len(s.Outside) != 2 || s.Outside[0] != want[0] || s.Outside[1] != want[1] {
		t.Errorf("target %s, outside %v, want %v", s.Host, s.Outside, want)
	}
	cfg.Target.SSH.Host = ""
	if s := sshTarget(cfg, addr{"203.0.113.7", 22}, options{}, io.Discard, io.Discard); len(s.Outside) != 0 {
		t.Errorf("nothing known, yet outside is %v", s.Outside)
	}
}
