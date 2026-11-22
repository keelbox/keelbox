package cli

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sort"

	"github.com/keelbox/keelbox/internal/prompt"
)

// A box's own state is root's: /etc/keelbox/keelbox.yaml, the logbook,
// sshd's and UFW's settings. On the box, the user who set it up (keelbox up
// on this machine) or the admin runs status, doctor, plan and apply as
// themselves; keelbox runs the same command again with sudo instead of
// failing on a file it cannot read.

// sudoRun runs sudo with args on this terminal; tests replace it.
var sudoRun = func(args []string, stdout, stderr io.Writer) int {
	cmd := exec.Command("sudo", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, stdout, stderr
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		return ee.ExitCode()
	case err != nil:
		failf(stderr, "sudo: %v", err)
		return 1
	}
	return 0
}

// sudoNeedsPassword reports whether sudo would ask; tests replace it.
var sudoNeedsPassword = func() bool { return exec.Command("sudo", "-n", "true").Run() != nil }

// asRoot runs keelbox cmd on the config at path again, as root.
func asRoot(cmd, path string, o options, stdout, stderr io.Writer) int {
	self, err := os.Executable()
	if err != nil {
		fail(stderr, err)
		return 1
	}
	var args []string
	switch {
	case !prompt.IsTerminal(os.Stdin):
		args = append(args, "-n")
	case sudoNeedsPassword():
		notef(stderr, "%s reads the box's root-only files; sudo may ask for your password.", cmd)
	}
	// The child gets Ctrl-C from the terminal itself; envChild keeps it
	// from repeating this process's note about it.
	args = append(args, "env", envChild+"=1", self, cmd, path)
	args = append(args, flagArgs(o)...)
	if o.snapshot != "" {
		args = append(args, o.snapshot)
	}
	if o.request != "" {
		args = append(args, o.request)
	}
	if o.add != "" {
		args = append(args, o.add)
	}
	code := sudoRun(args, stdout, stderr)
	if code == 1 && !prompt.IsTerminal(os.Stdin) && sudoNeedsPassword() {
		failf(stderr, "%s needs root, and sudo asks for a password; in a terminal run: keelbox %s\n", cmd, cmd)
	}
	return code
}

// flagArgs are the flags given on the command line, as arguments again.
func flagArgs(o options) []string {
	c := o
	fs := flagSet(&c, io.Discard) // defining the flags resets their fields
	c = o
	var names []string
	for n := range o.set {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		if f := fs.Lookup(n); f != nil {
			out = append(out, "--"+n+"="+f.Value.String())
		}
	}
	return out
}
