//go:build !linux

package provider

import "syscall"

// followAttr puts a follow's ssh in its own process group, out of reach of
// the terminal's Ctrl-C. Killed with keelbox, it ends at its next write.
func followAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
