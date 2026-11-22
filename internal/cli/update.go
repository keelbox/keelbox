package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/release"
	"github.com/keelbox/keelbox/internal/selfupdate"
	"github.com/keelbox/keelbox/internal/version"
)

// How keelbox update finds and replaces itself; tests stand in for each.
var (
	// currentRelease is the stable channel's version, read and verified as
	// self-update reads it.
	currentRelease = func(ctx context.Context) (string, error) {
		f, err := release.NewFetcher()
		if err != nil {
			return "", err
		}
		idx, err := f.Index(ctx)
		if err != nil {
			return "", err
		}
		return idx.Version, nil
	}
	// replaceSelf is self-update: the verified download in place of exe.
	replaceSelf = func(ctx context.Context, exe string, allowDowngrade bool) (string, error) {
		f, err := release.NewFetcher()
		if err != nil {
			return "", err
		}
		return selfupdate.Update(ctx, f, exe, version.Version, release.Platform(runtime.GOOS, runtime.GOARCH), allowDowngrade)
	}
	// runNew replaces this process with the keelbox just installed.
	runNew = func(exe string, args []string) error {
		return syscall.Exec(exe, args, os.Environ()) //nolint:gosec // G204: this keelbox's own path, just replaced by the verified download
	}
	runningVersion = func() string { return version.Version }
	selfExe        = os.Executable
)

// cmdUpdate is keelbox update [box]: keelbox on this computer moved to the
// current release, then the box applied with it, which installs that
// keelbox there and what it brings. It is self-update and apply in one
// command, in that order, so that the release that is applied is the one
// just installed: after an update this process is the old keelbox, and it
// hands over to the new one for the apply. args are this run's arguments
// after "update"; apply runs keelbox apply for the box with this keelbox.
func cmdUpdate(ctx context.Context, cfg *config.Config, o options, path string, args []string, apply func() int, stdout, stderr io.Writer) int {
	ref := boxRef(cfg, path)
	have := runningVersion()
	if !release.IsRelease(have) {
		notef(stderr, "this keelbox is a development build, which nothing updates; applying it to %s as it is", cfg.Box)
		return apply()
	}
	latest, err := currentRelease(ctx)
	if err != nil {
		failf(stderr, "could not read the current release (%v), so keelbox here was not updated and %s was not touched. Apply what this keelbox has: keelbox apply %s\n", err, cfg.Box, ref)
		return 1
	}
	if latest == have || release.CompareVersions(latest, have) < 0 && !o.allowDowngrade {
		fmt.Fprintf(stdout, "keelbox %s here is the current release. Applying it to %s.\n", have, cfg.Box)
		return apply()
	}
	exe, err := selfExe()
	if err != nil {
		fail(stderr, err)
		return 1
	}
	if real, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = real
	}
	if cmd := packageManager(exe); cmd != "" {
		failf(stderr, "keelbox %s is out and this keelbox, %s, belongs to Homebrew, which updates it; %s was not touched. Update it, then run this again: %s\n", latest, have, cfg.Box, cmd)
		return 1
	}
	to, err := replaceSelf(ctx, exe, o.allowDowngrade)
	switch {
	case errors.Is(err, selfupdate.ErrUpToDate):
		return apply()
	case errors.Is(err, os.ErrPermission):
		failf(stderr, "cannot replace %s (%v); %s was not touched. Update keelbox with sudo, then apply: sudo keelbox self-update && keelbox apply %s\n", exe, err, cfg.Box, ref)
		return 1
	case err != nil:
		failf(stderr, "updating keelbox here failed (%v); %s was not touched. Apply what this keelbox has: keelbox apply %s\n", err, cfg.Box, ref)
		return 1
	}
	fmt.Fprintf(stdout, "keelbox %s -> %s on this computer. Applying it to %s.\n", have, to, cfg.Box)
	// The new keelbox applies: this one has the old release's steps.
	if err := runNew(exe, append([]string{"keelbox", "apply"}, args...)); err != nil {
		failf(stderr, "keelbox here is %s now, but starting it failed (%v); %s was not touched. Apply it: keelbox apply %s\n", to, err, cfg.Box, ref)
		return 1
	}
	return 0
}

// argsAfterCommand is a command line's arguments after the command word:
// the box and the flags, as the user wrote them.
func argsAfterCommand(args []string, cmd string) []string {
	for i, a := range args {
		if a == cmd {
			return append(append([]string{}, args[:i]...), args[i+1:]...)
		}
	}
	return args
}
