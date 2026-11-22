package cli

import (
	"context"
	"io"

	"github.com/keelbox/keelbox/internal/backup"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/previews"
)

// previewsSync is keelbox-previews.service: publish the agent's previews.
func previewsSync(ctx context.Context, e *engine.Env, stderr io.Writer) int {
	if !e.Cfg.PreviewsEnabled() {
		failf(stderr, "previews are not enabled in the config")
		return 1
	}
	st, err := previews.Sync(ctx, e)
	if err != nil {
		fail(stderr, err)
		return 1
	}
	if st.Error != "" {
		notef(stderr, "previews not served: %s", st.Error)
	}
	return 0
}

// runBackup runs backup, backup --list and restore on the box, as root.
func runBackup(ctx context.Context, cmd string, e *engine.Env, o options, stdout, stderr io.Writer) int {
	if !e.Cfg.BackupEnabled() {
		failf(stderr, "backups are not configured: set backup.repository, backup.credentials_file and backup.password_file, then keelbox apply")
		return 1
	}
	var err error
	switch {
	case cmd == "restore":
		err = backup.Restore(ctx, e, backup.RestoreOptions{Snapshot: o.snapshot, Target: o.target, Overwrite: o.overwrite}, stdout)
	case o.list:
		err = backup.List(ctx, e, stdout)
	default:
		err = backup.Run(ctx, e, stdout)
	}
	if err != nil {
		fail(stderr, err)
		return 1
	}
	return 0
}
