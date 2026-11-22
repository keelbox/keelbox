package modules

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/runs"
	"github.com/keelbox/keelbox/internal/version"
)

// ConfigPath is where the applied config is kept on the box, so doctor and
// status run without arguments.
const ConfigPath = "/etc/keelbox/keelbox.yaml"

// BinaryPath is where apply installs the keelbox that applied the box.
// PreviousBinary keeps the one it replaced, for keelbox rollback.
const (
	BinaryPath     = "/usr/local/bin/keelbox"
	PreviousBinary = logbook.Dir + "/keelbox.previous"
)

// Logbook keeps the box's record: the logbook directory, a copy of the
// applied config and the keelbox binary that applied it. The CLI appends a
// logbook entry after every apply.
type Logbook struct{}

func (Logbook) Name() string                { return "logbook" }
func (Logbook) Enabled(*config.Config) bool { return true }

func (Logbook) Steps(e *engine.Env) []engine.Step {
	data, _ := e.Cfg.Marshal()
	return []engine.Step{
		engine.DirStep("logbook-dir", logbook.Dir, 0o755, "root", "root"),
		engine.DirStep("config-dir", "/etc/keelbox", 0o755, "root", "root"),
		engine.FileStep("config-copy", engine.File{Path: ConfigPath, Content: append([]byte("# Applied by keelbox; edit the source config and re-apply.\n"), data...), Mode: 0o600}),
		binaryStep(),
	}
}

// binaryStep installs the running keelbox as /usr/local/bin/keelbox, so the
// box's own keelbox (doctor, status, the agent's commands) is the version
// that last applied it. The binary it replaces is kept for rollback.
func binaryStep() engine.Step {
	differs := func(e *engine.Env) (bool, error) {
		if e.Self == "" || e.Self == BinaryPath {
			return false, nil
		}
		self, err := e.X.ReadFile(e.Self)
		if err != nil {
			return false, fmt.Errorf("read the running keelbox: %w", err)
		}
		cur, err := e.X.ReadFile(BinaryPath)
		if err != nil {
			return true, nil
		}
		return !bytes.Equal(self, cur), nil
	}
	return engine.Step{
		Name: "keelbox-binary",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			d, err := differs(e)
			if err != nil || !d {
				return engine.Converged(), err
			}
			return engine.Pending("install this keelbox (%s) as %s", version.Version, BinaryPath), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			self, err := e.X.ReadFile(e.Self)
			if err != nil {
				return err
			}
			if cur, err := e.X.ReadFile(BinaryPath); err == nil {
				if err := e.X.WriteFile(PreviousBinary, cur, 0o755, "root", "root"); err != nil {
					return fmt.Errorf("keep the previous keelbox: %w", err)
				}
			}
			return e.X.WriteFile(BinaryPath, self, 0o755, "root", "root")
		},
	}
}

func (Logbook) Verify(ctx context.Context, e *engine.Env) []engine.Finding {
	recs, err := logbook.Read(e.X)
	last := logbook.Last(recs, logbook.KindApply)
	var run []engine.Finding
	var lastTime time.Time
	if last != nil {
		lastTime = last.Time
	}
	// An apply the laptop left running, or one that died with the box.
	if s, unfinished := runs.Unfinished(ctx, e.X, lastTime); unfinished && s.Running {
		run = append(run, ok("logbook", "setup-run", "an apply is "+s.Describe(time.Now())))
	} else if unfinished {
		run = append(run, fail("logbook", "setup-run", s.Describe(time.Now())+"; every step before it is in place, carry on: keelbox apply "+e.Cfg.Box))
	}
	if err != nil || last == nil {
		return append(run, warn("logbook", "last-apply", "no apply recorded yet"))
	}
	detail := fmt.Sprintf("%s, %d change(s), %s ago", last.Result, len(last.Changes), time.Since(last.Time).Round(time.Second))
	if last.Result == "partial" {
		return append(run, fail("logbook", "last-apply", fmt.Sprintf("%s: %d step(s) failed, first %s/%s: %s", detail, len(last.Failed), last.Failed[0].Module, last.Failed[0].Step, last.Failed[0].Detail)))
	}
	if last.Result != "ok" {
		return append(run, fail("logbook", "last-apply", detail+": "+last.Error))
	}
	return append(run, ok("logbook", "last-apply", detail))
}
