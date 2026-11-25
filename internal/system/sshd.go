package system

import (
	"context"
	"fmt"
	"strings"
)

// SSHDConfig prints sshd's effective config: sshd -T with args. sshd
// refuses to run without /run/sshd, which ssh.service makes when it
// starts. With socket activation, as on Ubuntu 24.04, that waits for the
// first SSH connection, so a check made after a boot by a timer, or from
// the console before anyone connected, found no directory and no config.
// Then sshd runs again in a mount namespace of its own, with an empty /run
// that holds the directory: the machine's own /run is left as it is.
func SSHDConfig(ctx context.Context, x Executor, args ...string) (string, error) {
	out, err := Output(ctx, x, C("sshd", append([]string{"-T"}, args...)...))
	if err == nil || !strings.Contains(err.Error(), "privilege separation directory") {
		return out, err
	}
	out, ferr := Output(ctx, x, C("unshare", append([]string{"--mount", "--propagation", "private", "--", "/bin/sh", "-c",
		`mount -t tmpfs -o mode=0755 keelbox-check /run && mkdir -m 0755 /run/sshd && exec sshd -T "$@"`, "sh"}, args...)...))
	if ferr != nil {
		why, _, _ := strings.Cut(strings.TrimSpace(ferr.Error()), "\n")
		return "", fmt.Errorf("%w; reading it in a mount namespace of its own failed too (%s). Start sshd, which makes the directory: sudo systemctl start ssh.service", err, why)
	}
	return out, nil
}
