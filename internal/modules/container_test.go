package modules

import (
	"context"
	"fmt"
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

// lxcSim is an LXC container with nesting off and no TUN device.
func lxcSim(t *testing.T) *sim {
	s := newSim(t)
	s.lxc, s.nestingOff, s.noTun = true, true, true
	return s
}

// guestDir is a guest API socket's directory as LXD and Incus make it: the
// host's root, nobody inside.
func guestDir(s *sim, dir string) {
	s.Files[dir] = &system.FakeFile{Dir: true, Mode: 0o755, Owner: "nobody", Group: "nogroup"}
	s.Files[dir+"/sock"] = &system.FakeFile{Mode: 0o666, Owner: "nobody", Group: "nogroup"}
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

// The checks only read: every command they run is one of these, and no
// file changes.
func TestContainerChecksChangeNothing(t *testing.T) {
	s := lxcSim(t)
	guestDir(s, "/dev/lxd")
	s.guestName = "kb-1"
	e := simEnv(t, s, testConfig(t, "agents: [claude-code]\n"))
	snapshot := func() string {
		var b strings.Builder
		for _, p := range s.Paths() {
			fmt.Fprintf(&b, "%s %+v\n", p, *s.Files[p])
		}
		return b.String()
	}
	before := snapshot()
	s.Calls = nil
	_ = CheckContainer(context.Background(), e)
	_ = verifyContainer(context.Background(), e)
	readOnly := []string{"systemd-detect-virt --container", nestingProbe.String(), tunProbe.String(), "curl -fsS --max-time 5 --unix-socket /dev/lxd/sock "}
	for _, c := range s.Calls {
		known := false
		for _, r := range readOnly {
			known = known || strings.HasPrefix(c.String(), r)
		}
		if !known {
			t.Errorf("ran %q, which is not one of the read-only probes", c)
		}
	}
	if len(s.Calls) == 0 {
		t.Error("ran nothing")
	}
	if snapshot() != before {
		t.Error("the checks changed files")
	}
}

// A container with nesting on and the TUN device passes, and doctor says so.
func TestContainerChecksPassWithNestingAndTun(t *testing.T) {
	s := newSim(t)
	s.lxc = true
	e := simEnv(t, s, testConfig(t, "agents: [claude-code]\n"))
	if err := CheckContainer(context.Background(), e); err != nil {
		t.Errorf("nesting on, tun present: %v", err)
	}
	f := containerFindings(e)
	if f["container-nesting"].Level != engine.LevelOK || f["container-tun"].Level != engine.LevelOK {
		t.Errorf("doctor: %+v", f)
	}
}

// On LXD, apply refuses before any change and names the LXD commands with
// the container's name from cloud-init, which stays when apply renames the
// host; doctor fails with the same commands.
func TestContainerChecksOnLXD(t *testing.T) {
	s := lxcSim(t)
	guestDir(s, "/dev/lxd")
	s.AddFile("/run/cloud-init/instance-data.json", `{"v1":{"local_hostname":"kb-lxc-1","platform":"lxd"}}`, 0o644)
	s.AddFile("/etc/hostname", "testbox\n", 0o644)
	e := simEnv(t, s, testConfig(t, "agents: [claude-code]\n"))
	err := CheckContainer(context.Background(), e)
	if err == nil {
		t.Fatal("apply went ahead with nesting off and no tun")
	}
	want := `testbox is an LXC container, and nothing has changed:
  - nesting is off (a new user namespace cannot mount /proc), so Docker and the agents' sandboxes cannot start
  - /dev/net/tun cannot be opened, so Tailscale cannot start
Change them on the LXD host:
  lxc config set kb-lxc-1 security.nesting=true
  lxc config device add kb-lxc-1 tun unix-char path=/dev/net/tun
  lxc restart kb-lxc-1
Then: keelbox apply testbox`
	if err.Error() != want {
		t.Errorf("error:\n%s\nwant:\n%s", err, want)
	}
	f := containerFindings(e)
	if n := f["container-nesting"]; n.Level != engine.LevelFail || !strings.HasSuffix(n.Detail, "on the container's host: lxc config set kb-lxc-1 security.nesting=true; then lxc restart kb-lxc-1") {
		t.Errorf("doctor nesting: %+v", n)
	}
	if n := f["container-tun"]; n.Level != engine.LevelFail || !strings.Contains(n.Detail, "lxc config device add kb-lxc-1 tun") {
		t.Errorf("doctor tun: %+v", n)
	}
}

// Without cloud-init, the guest API's meta-data names the container after
// keelbox has renamed the host; without that too, the name is NAME and the
// message says what it stands for, never the box's own hostname.
func TestContainerNameAfterRename(t *testing.T) {
	s := lxcSim(t)
	guestDir(s, "/dev/incus")
	s.AddFile("/etc/hostname", "testbox\n", 0o644) // renamed by apply
	s.guestName = "web-1"
	e := simEnv(t, s, testConfig(t, ""))
	err := CheckContainer(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "Change them on the Incus host:\n  incus config set web-1 security.nesting=true\n  incus config device add web-1 tun unix-char path=/dev/net/tun\n  incus restart web-1\n") {
		t.Errorf("guest API name: %v", err)
	}
	s.guestName = ""
	err = CheckContainer(context.Background(), e)
	if err == nil || !strings.Contains(err.Error(), "Change them on the Incus host, where NAME is the container's name:\n  incus config set NAME security.nesting=true\n  incus config device add NAME tun") || strings.Contains(err.Error(), "add testbox") {
		t.Errorf("no name: %v", err)
	}
	if n := containerFindings(e)["container-tun"]; !strings.HasSuffix(n.Detail, "keelbox apply testbox prints the commands for the container's host") {
		t.Errorf("doctor without a name: %+v", n)
	}
}

// With no guest API socket the host may be Proxmox, or LXD or Incus with
// that API off: both sets of commands, Proxmox's read before they add.
// Without Docker, agents or Tailscale nothing needs either setting.
func TestContainerChecksWithoutAGuestSocket(t *testing.T) {
	s := lxcSim(t)
	e := simEnv(t, s, testConfig(t, "agents: [codex]\nruntime:\n  docker: false\naccess:\n  tailscale:\n    enabled: false\n"))
	err := CheckContainer(context.Background(), e)
	if err == nil {
		t.Fatal("apply went ahead with nesting off")
	}
	msg := err.Error()
	for _, w := range []string{
		"  - nesting is off (a new user namespace cannot mount /proc), so the agents' sandboxes cannot start\n",
		"On Proxmox, where CTID is the container's number:\n  pct config CTID | grep -E '^(features|dev[0-9])'\n  pct set CTID --features nesting=1,keyctl=1    # keep the features listed above, comma-separated\n  pct reboot CTID\n",
		"On LXD, or with incus for Incus:\n  lxc config set ubuntu security.nesting=true\n  lxc restart ubuntu\n",
	} {
		if !strings.Contains(msg, w) {
			t.Errorf("error lacks %q:\n%s", w, msg)
		}
	}
	if strings.Contains(msg, "tun") || s.Ran("sh -c") {
		t.Errorf("Tailscale is off, yet the TUN device was probed or named:\n%s", msg)
	}
	if !strings.HasSuffix(msg, "Then: keelbox apply testbox") {
		t.Errorf("the error does not end with the command:\n%s", msg)
	}
	if _, ok := containerFindings(e)["container-tun"]; ok {
		t.Error("doctor checks the TUN device with Tailscale off")
	}

	bare := simEnv(t, s, testConfig(t, "agents: []\nruntime:\n  docker: false\naccess:\n  tailscale:\n    enabled: false\n"))
	s.Calls = nil
	if err := CheckContainer(context.Background(), bare); err != nil {
		t.Errorf("nothing needs nesting: %v", err)
	}
	if s.Ran("unshare") || len(containerFindings(bare)) != 0 {
		t.Error("probed nesting with nothing that needs it")
	}
}

// Both sockets: LXD or Incus, not Proxmox.
func TestContainerChecksWithBothSockets(t *testing.T) {
	s := lxcSim(t)
	guestDir(s, "/dev/lxd")
	guestDir(s, "/dev/incus")
	e := simEnv(t, s, testConfig(t, ""))
	msg := CheckContainer(context.Background(), e).Error()
	if strings.Contains(msg, "pct") || !strings.Contains(msg, "Change them on the LXD host, or with incus for Incus:\n  lxc config set ubuntu security.nesting=true\n  lxc config device add ubuntu tun") {
		t.Errorf("both sockets:\n%s", msg)
	}
}

// A name that is not a plain label is never printed into a command.
func TestContainerNameIsALabel(t *testing.T) {
	s := newSim(t)
	s.lxc = true
	s.AddFile("/run/cloud-init/instance-data.json", `{"v1":{"local_hostname":"x; rm -rf /"}}`, 0o644)
	s.AddFile("/etc/hostname", "box\n", 0o644)
	e := simEnv(t, s, testConfig(t, ""))
	if n := containerName(context.Background(), e, ""); n != "box" {
		t.Errorf("name %q", n)
	}
	s.Files["/etc/hostname"] = &system.FakeFile{Data: []byte("$(id)\n"), Mode: 0o644, Owner: "root", Group: "root"}
	if n := containerName(context.Background(), e, ""); n != "NAME" {
		t.Errorf("name %q", n)
	}
}

// With Tailscale on and no guest socket, the Proxmox commands add the TUN
// device on a free devN after reading the existing ones.
func TestContainerTunOnProxmox(t *testing.T) {
	s := lxcSim(t)
	s.nestingOff = false
	e := simEnv(t, s, testConfig(t, ""))
	msg := CheckContainer(context.Background(), e).Error()
	for _, w := range []string{
		"  pct config CTID | grep -E '^(features|dev[0-9])'\n  pct set CTID --dev1 /dev/net/tun    # a devN not listed above; Proxmox 8.1 or later\n  pct reboot CTID\n",
		"  lxc config device add ubuntu tun unix-char path=/dev/net/tun\n",
	} {
		if !strings.Contains(msg, w) {
			t.Errorf("error lacks %q:\n%s", w, msg)
		}
	}
	if strings.Contains(msg, "nesting") {
		t.Errorf("nesting is on, yet named:\n%s", msg)
	}
}

// Both sockets: doctor cannot say which tool, so it points at apply.
func TestContainerDoctorWithBothSockets(t *testing.T) {
	s := lxcSim(t)
	guestDir(s, "/dev/lxd")
	guestDir(s, "/dev/incus")
	e := simEnv(t, s, testConfig(t, ""))
	for _, c := range []string{"container-nesting", "container-tun"} {
		if n := containerFindings(e)[c]; n.Level != engine.LevelFail || !strings.HasSuffix(n.Detail, "keelbox apply testbox prints the commands for the container's host") {
			t.Errorf("%s: %+v", c, n)
		}
	}
}

// cloud-init's local_hostname names the container only on LXD's or Incus's
// datasource; elsewhere it is the hostname cloud-init was given, and once
// it is the box's it says nothing about the container.
func TestContainerNameFromCloudInitPlatform(t *testing.T) {
	s := newSim(t)
	s.lxc = true
	s.AddFile("/etc/hostname", "testbox\n", 0o644)
	e := simEnv(t, s, testConfig(t, ""))
	for data, want := range map[string]string{
		`{"v1":{"local_hostname":"testbox","platform":"lxd"}}`:     "testbox",
		`{"v1":{"local_hostname":"testbox","platform":"nocloud"}}`: "NAME",
		`{"v1":{"local_hostname":"ct-7","platform":"nocloud"}}`:    "ct-7",
	} {
		s.AddFile("/run/cloud-init/instance-data.json", data, 0o644)
		if n := containerName(context.Background(), e, ""); n != want {
			t.Errorf("%s: name %q, want %q", data, n, want)
		}
	}
}
