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
	if cmd := packageManager(exe); cmd != "" {
		failf(stderr, "%s belongs to Homebrew, which updates it: %s\n", exe, cmd)
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

// packageManager returns the command that updates keelbox when a package
// manager installed it, so self-update does not swap the file under it.
func packageManager(exe string) string {
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	if strings.Contains(filepath.ToSlash(exe), "/Cellar/keelbox/") {
		return "brew upgrade keelbox"
	}
	return ""
}
