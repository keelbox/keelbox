package provider

import "syscall"

// followAttr puts a follow's ssh in its own process group, out of reach of
// the terminal's Ctrl-C, and ends it with keelbox if keelbox is killed.
func followAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}
