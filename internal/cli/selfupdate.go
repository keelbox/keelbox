package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/keelbox/keelbox/internal/release"
	"github.com/keelbox/keelbox/internal/selfupdate"
	"github.com/keelbox/keelbox/internal/version"
)

func cmdSelfUpdate(ctx context.Context, o options, stdout, stderr io.Writer) int {
	if !release.IsRelease(version.Version) {
		failf(stderr, "this is a development build; install a release with: curl -fsSL https://get.keelbox.dev | sh")
		return 1
	}
	f, err := release.NewFetcher()
	if err != nil {
		fail(stderr, err)
		return 1
	}
	exe, err := os.Executable()
	if err != nil {
		fail(stderr, err)
		return 1
	}
	if by, cmd := packageManager(exe); cmd != "" {
		failf(stderr, "%s belongs to %s, which updates it: %s\n", exe, by, cmd)
		return 1
	}
	to, err := selfupdate.Update(ctx, f, exe, version.Version, release.Platform(runtime.GOOS, runtime.GOARCH), o.allowDowngrade)
	switch {
	case errors.Is(err, selfupdate.ErrUpToDate):
		fmt.Fprintf(stdout, "keelbox %s is the current stable release\n", to)
		printBoxesBehind(stdout, to)
		return 0
	case errors.Is(err, os.ErrPermission):
		failf(stderr, "cannot replace %s: %v; rerun it with sudo: sudo keelbox self-update\n", exe, err)
		return 1
	case err != nil:
		failf(stderr, "self-update: %v", err)
		return 1
	}
	fmt.Fprintf(stdout, "keelbox %s -> %s\n", version.Version, to)
	if note := boxCopyNote(exe, boxBinary); note != "" {
		fmt.Fprintln(stdout, note)
	}
	printBoxesBehind(stdout, to)
	return 0
}

// boxBinary is the box's own keelbox, which apply installs; tests move it.
var boxBinary = "/usr/local/bin/keelbox"

// boxCopyNote: on a box keelbox set up from itself, the box's own copy is
// a second binary, replaced by the next apply rather than by self-update.
func boxCopyNote(exe, box string) string {
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	if exe == box {
		return ""
	}
	if _, err := os.Stat(box); err != nil {
		return ""
	}
	return fmt.Sprintf("This machine is a box too: its own keelbox, %s, is replaced by the next apply: keelbox apply", box)
}

// packageManager says who installed keelbox and the command that updates
// it, when a package manager did, so self-update does not swap the file
// under it. Homebrew keeps it in its Cellar. A distribution package puts it
// in /usr/bin, where neither keelbox's installer (~/.local/bin) nor a box's
// apply (/usr/local/bin) does; on Arch that is an AUR package, which an AUR
// helper updates.
func packageManager(exe string) (by, cmd string) {
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	switch dir := filepath.Dir(filepath.ToSlash(exe)); {
	case strings.Contains(filepath.ToSlash(exe), "/Cellar/keelbox/"):
		return "Homebrew", "brew upgrade keelbox"
	case dir == "/usr/bin" || dir == "/bin" || dir == "/usr/sbin" || dir == "/sbin":
		if onArch() {
			return "an Arch package", "yay -Syu (or your AUR helper's update)"
		}
		return "your system's package manager", "your package manager's upgrade"
	}
	return "", ""
}

// onArch: pacman is the package manager. Tests replace it.
var onArch = func() bool {
	_, err := os.Stat("/usr/bin/pacman")
	return err == nil
}
