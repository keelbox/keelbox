// Package selfupdate moves the running keelbox to the release its channel
// index names. Downloads and verification are internal/release's Fetch, so
// the box binary and the laptop binary share one trust path
// (docs/adr/0016-release-signatures.md).
package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/keelbox/keelbox/internal/release"
)

// ErrUpToDate is returned when the channel already points at the running
// version.
var ErrUpToDate = errors.New("already up to date")

// ErrDowngrade is returned when the channel names an older release than the
// running one and the caller did not allow a downgrade.
var ErrDowngrade = errors.New("the channel names an older release")

// Update replaces the executable at exe (symlinks resolved) with the
// channel's current release for platform and returns the new version. An
// older release than current is refused unless allowDowngrade is set: a
// tampered index could otherwise roll every self-updating keelbox back to a
// release with known bugs, since older releases are genuinely signed. The
// download is verified by release.Fetch and must run `version` before it
// replaces the old binary.
func Update(ctx context.Context, f *release.Fetcher, exe, current, platform string, allowDowngrade bool) (string, error) {
	if !release.IsRelease(current) {
		return "", release.ErrDevBuild
	}
	idx, err := f.Index(ctx)
	if err != nil {
		return "", err
	}
	if idx.Version == current {
		return current, ErrUpToDate
	}
	if release.CompareVersions(idx.Version, current) < 0 && !allowDowngrade {
		return "", fmt.Errorf("%w: %s is older than this keelbox (%s); pass --allow-downgrade to install it", ErrDowngrade, idx.Version, current)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	staged := filepath.Join(filepath.Dir(exe), ".keelbox-"+idx.Version)
	defer os.Remove(staged)
	if err := f.Fetch(ctx, idx, idx.Version, platform, staged); err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, staged, "version").Output()
	if err != nil || !strings.Contains(string(out), idx.Version) {
		return "", fmt.Errorf("downloaded keelbox %s does not run here: %v %s", idx.Version, err, out)
	}
	if err := os.Rename(staged, exe); err != nil {
		return "", err
	}
	return idx.Version, nil
}
