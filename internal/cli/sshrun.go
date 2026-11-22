package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider"
)

// exitNoConnection is keelbox ssh BOX -- CMD's exit code when ssh did not
// connect and log in, so the command never ran. It is ssh's own code; a
// command that itself exits 255 cannot be told from it.
const exitNoConnection = 255

// plainWord is an argument the shell on the box reads as it is.
var plainWord = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`)

// shellWords quotes each argument for the shell on the box, so that it
// gets them as they were given: keelbox ssh acme -- ls "my dir" lists one
// folder. Shell syntax goes in one argument to sh -c.
func shellWords(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if plainWord.MatchString(a) {
			out[i] = a
			continue
		}
		out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(out, " ")
}

// runOnBox is keelbox ssh BOX [--admin] -- CMD: the command runs on the box
// without a terminal, with this process's input and output passed through,
// and keelbox exits with its exit code. It asks nothing: a host key not
// pinned is refused, and a cloud box known on the tailnet is reached there
// without asking its cloud for an address.
func runOnBox(ctx context.Context, cfg *config.Config, o options, user string, stdout, stderr io.Writer) int {
	ref := boxRef(cfg, o.configFile)
	if len(o.remote) == 0 {
		failf(stderr, "ssh -- takes the command to run on %s: keelbox ssh %s -- uptime", cfg.Box, ref)
		return 2
	}
	remote := shellWords(o.remote)
	if cfg.Target.Provider == config.ProviderLocal {
		c := (&shell{user: user}).local(ctx, remote)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, stdout, stderr
		return commandExit(ctx, c.Run())
	}
	var s *provider.SSH
	if t, ok := tailnetAddr(cfg); ok && cfg.Tailscale() && isCloud(cfg.Target.Provider) {
		s = sshTarget(cfg, t, o, io.Discard, stderr).As(user)
	} else {
		// A question about the cloud goes to stderr, where it is seen.
		sh, err := boxShell(ctx, cfg, o, user, stderr, stderr)
		if err != nil {
			fail(stderr, err)
			return exitNoConnection
		}
		s = sh.ssh
	}
	s.ConfirmKeys = nil
	opened, err := s.Exec(ctx, remote, os.Stdin, stdout, stderr)
	if !opened && err != nil && ctx.Err() == nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() == 255 {
			fixCmd := "keelbox ssh " + ref
			if o.admin {
				fixCmd += " --admin"
			}
			why := fmt.Sprintf("could not log in to %s as %s at %s", cfg.Box, user, s.Host)
			if !s.IsKnown() {
				why = fmt.Sprintf("keelbox has no pinned host key for %s at %s, and does not ask without a terminal", cfg.Box, s.Host)
			}
			failf(stderr, "%s; the command did not run. Check on a terminal: %s", why, fixCmd)
			return exitNoConnection
		}
	}
	return commandExit(ctx, err)
}

// commandExit is the exit code of the command on the box.
func commandExit(ctx context.Context, err error) int {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case ctx.Err() != nil:
		return exitInterrupted
	case errors.As(err, &ee) && ee.ExitCode() >= 0:
		return ee.ExitCode()
	}
	return 1
}
