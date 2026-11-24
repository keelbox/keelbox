package modules

import (
	"context"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// containerFindings are host's doctor lines about the container.
func containerFindings(e *engine.Env) map[string]engine.Finding {
	out := map[string]engine.Finding{}
	for _, f := range (Host{}).Verify(context.Background(), e) {
		if strings.HasPrefix(f.Check, "container-") {
			out[f.Check] = f
		}
	}
	return out
}

// Outside a container neither check runs: apply goes ahead and doctor says
// nothing about it.
func TestContainerChecksOnlyInLXC(t *testing.T) {
	s := newSim(t)
	s.nestingOff, s.noTun = true, true
	e := simEnv(t, s, testConfig(t, ""))
	if err := CheckContainer(context.Background(), e); err != nil {
		t.Errorf("not a container: %v", err)
	}
	if f := containerFindings(e); len(f) != 0 {
		t.Errorf("not a container, doctor: %+v", f)
	}
	if s.Ran("unshare") || s.Ran("sh -c") {
		t.Error("probed outside a container")
	}
}

// A container with nesting on and the TUN device passes, and doctor says so.
func TestContainerChecksPassWithNestingAndTun(t *testing.T) {
	s := newSim(t)
	s.lxc = true
	e := simEnv(t, s, testConfig(t, ""))
	if err := CheckContainer(context.Background(), e); err != nil {
		t.Errorf("nesting on, tun present: %v", err)
	}
	f := containerFindings(e)
	if f["container-nesting"].Level != engine.LevelOK || f["container-tun"].Level != engine.LevelOK {
		t.Errorf("doctor: %+v", f)
	}
	// The probes change nothing on the box.
	before := strings.Join(s.Paths(), "\n")
	_ = ContainerLimits(context.Background(), e)
	if after := strings.Join(s.Paths(), "\n"); after != before {
		t.Error("the probes changed files")
	}
}

// On LXD, apply refuses before any change and names the LXD commands with
// the container's name from cloud-init, which stays when apply renames the
// host; doctor fails with the same fix.
func TestContainerChecksOnLXD(t *testing.T) {
	s := newSim(t)
	s.lxc, s.nestingOff, s.noTun = true, true, true
	// As LXD makes it: the host's root, nobody inside.
	s.Files["/dev/lxd"] = &system.FakeFile{Dir: true, Mode: 0o755, Owner: "nobody", Group: "nogroup"}
	s.Files["/dev/lxd/sock"] = &system.FakeFile{Mode: 0o666, Owner: "nobody", Group: "nogroup"}
	s.AddFile("/run/cloud-init/instance-data.json", `{"v1":{"local_hostname":"kb-lxc-1","platform":"lxd"}}`, 0o644)
	s.AddFile("/etc/hostname", "testbox\n", 0o644)
	e := simEnv(t, s, testConfig(t, ""))
	err := CheckContainer(context.Background(), e)
	if err == nil {
		t.Fatal("apply went ahead with nesting off and no tun")
	}
	want := `testbox is an LXC container, and nothing has changed:
  - nesting is off, so rootless Docker and the agents' sandboxes cannot start
  - /dev/net/tun cannot be opened, so Tailscale cannot start
Run these on the LXD host, then keelbox apply testbox again:
  lxc config set kb-lxc-1 security.nesting=true
  lxc config device add kb-lxc-1 tun unix-char path=/dev/net/tun
  lxc restart kb-lxc-1`
	if err.Error() != want {
		t.Errorf("error:\n%s\nwant:\n%s", err, want)
	}
	f := containerFindings(e)
	if n := f["container-nesting"]; n.Level != engine.LevelFail || !strings.HasSuffix(n.Detail, "lxc config set kb-lxc-1 security.nesting=true, then lxc restart kb-lxc-1") {
		t.Errorf("doctor nesting: %+v", n)
	}
	if n := f["container-tun"]; n.Level != engine.LevelFail || !strings.Contains(n.Detail, "lxc config device add kb-lxc-1 tun") {
		t.Errorf("doctor tun: %+v", n)
	}
}

// On Proxmox there is no guest socket and the container's number is not
// known inside it: the fix names pct with CTID. Without Docker or
// Tailscale, only what still applies is named.
func TestContainerChecksOnProxmox(t *testing.T) {
	s := newSim(t)
	s.lxc, s.nestingOff, s.noTun = true, true, true
	e := simEnv(t, s, testConfig(t, "runtime:\n  docker: false\naccess:\n  tailscale:\n    enabled: false\n"))
	err := CheckContainer(context.Background(), e)
	if err == nil {
		t.Fatal("apply went ahead with nesting off")
	}
	msg := err.Error()
	for _, w := range []string{"  - nesting is off, so the agents' sandboxes cannot start\n", "On Proxmox, CTID is the container's number. Run these on the Proxmox host, then keelbox apply testbox again:\n  pct set CTID --features nesting=1,keyctl=1\n  pct reboot CTID"} {
		if !strings.Contains(msg, w) {
			t.Errorf("error lacks %q:\n%s", w, msg)
		}
	}
	if strings.Contains(msg, "tun") {
		t.Errorf("Tailscale is off, yet the error names the TUN device:\n%s", msg)
	}
	if _, ok := containerFindings(e)["container-tun"]; ok {
		t.Error("doctor checks the TUN device with Tailscale off")
	}
	if !strings.HasSuffix(msg, "pct reboot CTID") {
		t.Errorf("the error does not end with the fix:\n%s", msg)
	}
}

// A name that is not a plain label is never printed into a command.
func TestContainerNameIsALabel(t *testing.T) {
	s := newSim(t)
	s.lxc = true
	s.AddFile("/run/cloud-init/instance-data.json", `{"v1":{"local_hostname":"x; rm -rf /"}}`, 0o644)
	s.AddFile("/etc/hostname", "box\n", 0o644)
	e := simEnv(t, s, testConfig(t, ""))
	if n := containerName(e); n != "box" {
		t.Errorf("name %q", n)
	}
	s.Files["/etc/hostname"] = &system.FakeFile{Data: []byte("$(id)\n"), Mode: 0o644, Owner: "root", Group: "root"}
	if n := containerName(e); n != "NAME" {
		t.Errorf("name %q", n)
	}
}
