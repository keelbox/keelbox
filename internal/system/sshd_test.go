package system

import (
	"context"
	"strings"
	"testing"
)

// After a boot, before the first SSH connection, socket activation has not
// started ssh.service and /run/sshd does not exist: sshd -T refuses to run.
// The check reads the config in a mount namespace of its own instead.
func TestSSHDConfigWithoutRunSSHD(t *testing.T) {
	x := NewFake()
	x.On("sshd -T", Result{ExitCode: 255, Stderr: "Missing privilege separation directory: /run/sshd\n"})
	x.OK("unshare --mount --propagation private -- /bin/sh -c", "permitrootlogin no\n")
	out, err := SSHDConfig(context.Background(), x, "-C", "user=agent")
	if err != nil || out != "permitrootlogin no" {
		t.Fatalf("got %q, %v", out, err)
	}
	last := x.Calls[len(x.Calls)-1]
	if got := last.Args[len(last.Args)-3:]; got[0] != "sh" || got[1] != "-C" || got[2] != "user=agent" {
		t.Errorf("args not passed on: %q", last.Args)
	}
	// Any other failure is the config's and stays one.
	x = NewFake()
	x.On("sshd -T", Result{ExitCode: 255, Stderr: "/etc/ssh/sshd_config line 3: Bad configuration option: Nope\n"})
	if _, err := SSHDConfig(context.Background(), x); err == nil || x.Ran("unshare") {
		t.Errorf("err %v, unshare %v", err, x.Ran("unshare"))
	}

	// The fallback fails too: sshd's own reason first, then the fix.
	x = NewFake()
	x.On("sshd -T", Result{ExitCode: 255, Stderr: "Missing privilege separation directory: /run/sshd\n"})
	x.On("unshare", Result{ExitCode: 1, Stderr: "unshare: unshare failed: Operation not permitted\n"})
	_, err = SSHDConfig(context.Background(), x)
	if err == nil || !strings.Contains(err.Error(), "Missing privilege separation directory") || !strings.HasSuffix(err.Error(), "sudo systemctl start ssh.service") {
		t.Errorf("fallback failed: %v", err)
	}
}
