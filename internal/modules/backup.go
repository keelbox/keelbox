package modules

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/backup"
	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// Backup installs restic, the repository credentials and a systemd timer
// that runs `keelbox backup` (docs/adr/0020-backups.md). Apply never
// contacts the repository; the first backup initialises it.
type Backup struct{}

func (Backup) Name() string                  { return "backup" }
func (Backup) Enabled(c *config.Config) bool { return c.BackupEnabled() }

// backupService and backupTimer predate the day-two units and keep their
// text: changing it would show as a change on every box.
var backupService = rootService{
	Unit: backup.Unit, Header: ". Backs up the box with restic; see keelbox backup.", Description: "keelbox backup",
	Command: "backup", After: []string{"network-online.target"}, Service: []string{"Nice=10", "IOSchedulingClass=idle"},
}

func backupTimer(schedule string) rootTimer {
	return rootTimer{
		Unit: backup.Timer, Header: " (backup.schedule).", Description: "keelbox backup schedule", Service: backup.Unit,
		Calendar: schedule, Key: "backup.schedule", Timer: []string{"Persistent=true", "RandomizedDelaySec=15min"},
	}
}

func (Backup) Steps(e *engine.Env) []engine.Step {
	b := e.Cfg.Backup
	steps := []engine.Step{
		engine.PackagesStep("backup-packages", func(*engine.Env) []string { return []string{"bzip2"} }),
		resticBinaryStep(),
		engine.DirStep("backup-secret-dir", backup.SecretDir, 0o700, "root", "root"),
		secretFileStep("backup-credentials", "backup.credentials_file", b.CredentialsFile, backup.CredentialsPath),
		secretFileStep("backup-password", "backup.password_file", b.PasswordFile, backup.PasswordPath),
		engine.DirStep("backup-state-dir", backup.StateDir, 0o700, "root", "root"),
		engine.DirStep("backup-cache-dir", backup.CacheDir, 0o700, "root", "root"),
		serviceStep("backup-service-unit", backupService),
	}
	return append(steps, timerSteps("backup-timer", backupTimer(b.Schedule))...)
}

// resticBinaryStep installs the manifest's restic release, checked against
// the manifest's checksum, root-owned in /usr/local/bin.
func resticBinaryStep() engine.Step {
	return engine.Step{
		Name: "restic",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			out, err := system.Output(ctx, e.X, system.C(backup.ResticBin, "version"))
			if err != nil {
				return engine.Pending("install restic %s", e.M.Restic.Version), nil
			}
			if !strings.HasPrefix(out, "restic "+e.M.Restic.Version+" ") {
				return engine.Pending("move restic to %s (have %s)", e.M.Restic.Version, firstField(strings.TrimPrefix(out, "restic "))), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			want := e.M.Restic.SHA256[e.Facts.Arch]
			if want == "" {
				return fmt.Errorf("release %s has no restic checksum for %s", e.M.Release, e.Facts.Arch)
			}
			dir, cleanup, err := tempDir(ctx, e, "")
			if err != nil {
				return err
			}
			defer cleanup()
			bz := dir + "/restic.bz2"
			if _, err := e.X.Run(ctx, curl(e.M.ResticURL(e.Facts.Arch), bz)); err != nil {
				return err
			}
			sum, err := system.Output(ctx, e.X, system.C("sha256sum", bz))
			if err != nil {
				return err
			}
			if got := firstField(sum); got != want {
				return fmt.Errorf("restic checksum %s does not match the release manifest (%s); refusing to install", got, want)
			}
			for _, c := range []system.Cmd{
				system.C("bunzip2", bz),
				system.C("install", "-m", "0755", "-o", "root", "-g", "root", dir+"/restic", backup.ResticBin),
			} {
				if _, err := e.X.Run(ctx, c); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// secretFileStep keeps a root-only copy of a secret file at dst. src is the
// config's path on the box: the staged copy a laptop uploaded, or a file the
// admin put there. When src is gone (doctor from the applied config, after
// the stage is cleared) the installed copy stands. Contents never appear in
// a plan, an error or the logbook.
func secretFileStep(step, key, src, dst string) engine.Step {
	src = config.ExpandHome(src)
	check := func(e *engine.Env) (bool, []byte, error) {
		dstSt, err := e.X.Stat(dst)
		if err != nil {
			return false, nil, err
		}
		dstOK := dstSt.Exists && !dstSt.IsDir && !dstSt.IsSymlink && dstSt.Mode == 0o600 && dstSt.Owner == "root"
		if src == dst {
			if !dstSt.Exists {
				return false, nil, fmt.Errorf("%s %s does not exist on the box", key, src)
			}
			return dstOK, nil, nil
		}
		srcSt, err := e.X.Stat(src)
		if err != nil {
			return false, nil, fmt.Errorf("%s %s: %w (keep it in a directory only root can change, such as %s)", key, src, err, backup.SecretDir)
		}
		if !srcSt.Exists {
			if dstSt.Exists {
				return dstOK, nil, nil
			}
			return false, nil, fmt.Errorf("%s %s does not exist on the box", key, src)
		}
		if srcSt.Mode&0o077 != 0 {
			return false, nil, fmt.Errorf("%s %s is readable by others (mode %o); chmod 600 it", key, src, srcSt.Mode)
		}
		data, err := e.X.ReadFile(src)
		if err != nil {
			return false, nil, err
		}
		cur, _ := e.X.ReadFile(dst)
		return dstOK && bytes.Equal(cur, data), data, nil
	}
	return engine.Step{
		Name: step,
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			ok, _, err := check(e)
			if err != nil || ok {
				return engine.Converged(), err
			}
			return engine.Pending("install %s as %s (mode 600)", key, dst), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			_, data, err := check(e)
			if err != nil {
				return err
			}
			if data == nil {
				if data, err = e.X.ReadFile(dst); err != nil {
					return err
				}
			}
			return e.X.WriteFile(dst, data, 0o600, "root", "root")
		},
	}
}

func (Backup) Verify(ctx context.Context, e *engine.Env) []engine.Finding {
	const mod = "backup"
	var out []engine.Finding
	if system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", backup.Timer)) {
		out = append(out, ok(mod, "timer", "scheduled: "+e.Cfg.Backup.Schedule))
	} else {
		out = append(out, fail(mod, "timer", backup.Timer+" not active"))
	}
	for _, p := range []string{backup.CredentialsPath, backup.PasswordPath} {
		st, err := e.X.Stat(p)
		if err != nil || !st.Exists || st.Mode != 0o600 || st.Owner != "root" {
			out = append(out, fail(mod, "credentials", p+" missing or not mode 600 root"))
		}
	}
	st, found := backup.ReadStatus(e)
	limit := e.Cfg.BackupStaleAfter()
	switch {
	case !found || st.LastSuccess.IsZero():
		detail := "no successful backup yet; run keelbox backup"
		if found && st.Error != "" {
			detail += "; last attempt failed: " + st.Error
		}
		out = append(out, warn(mod, "last-backup", detail))
	case time.Since(st.LastSuccess) > limit:
		detail := fmt.Sprintf("last successful backup %s ago, over backup.stale_after (%s)", time.Since(st.LastSuccess).Round(time.Minute), limit)
		if st.Error != "" {
			detail += "; last attempt failed: " + st.Error
		}
		out = append(out, flaky(fail(mod, "last-backup", detail)))
	case st.Error != "":
		out = append(out, warn(mod, "last-backup", fmt.Sprintf("last success %s ago, but the latest attempt failed: %s", time.Since(st.LastSuccess).Round(time.Minute), st.Error)))
	default:
		out = append(out, ok(mod, "last-backup", fmt.Sprintf("last successful backup %s ago", time.Since(st.LastSuccess).Round(time.Minute))))
	}
	return out
}

// BackupSummary is one line for keelbox status.
func BackupSummary(e *engine.Env) string {
	st, found := backup.ReadStatus(e)
	switch {
	case !found || st.LastSuccess.IsZero():
		return "no successful backup yet"
	case st.Error != "":
		return fmt.Sprintf("last success %s ago; latest attempt failed", time.Since(st.LastSuccess).Round(time.Minute))
	}
	return fmt.Sprintf("last success %s ago", time.Since(st.LastSuccess).Round(time.Minute))
}
