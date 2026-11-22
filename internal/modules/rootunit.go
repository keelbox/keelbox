package modules

import (
	"context"
	"fmt"
	"strings"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// rootService is a root oneshot service that runs one keelbox command
// against the applied config, started by a timer or a path unit. Day-two
// services (ADR 0030) get the shared properties below; backup predates
// them and sets its own.
type rootService struct {
	Unit        string // keelbox-watch.service
	Header      string // first line, after "# Managed by keelbox"
	Description string
	Command     string   // the keelbox command, such as watch-run
	After       []string // ordering, such as network-online.target (also wanted)
	Service     []string // extra [Service] lines
	// DayTwo marks a day-two run: an older keelbox installed by a
	// rollback skips it instead of failing (ExecCondition), it never hits
	// a start limit (a path unit the agent can trigger would otherwise
	// stop watching), and it runs with a larger CPU and I/O share than the
	// agent at a lower nice level, with a time limit.
	DayTwo  bool
	Timeout string // TimeoutStartSec for a day-two run, default 2min
	// Packages marks a day-two run that installs packages because the
	// agent asked (the install service). Its work is the agent's, done
	// outside the agent's slice, so it gets a smaller CPU and I/O share
	// than system services rather than a larger one; and it may gain
	// privileges on exec, as apt-get run by hand does, since packages'
	// maintainer scripts are written for that.
	Packages bool
}

// HasCommand is the command a day-two service's ExecCondition runs: it
// exits 0 when the installed keelbox knows the service's command. A
// rollback can install an older keelbox, and one that knows neither this
// command nor the service's exits 2. systemd reads that as "skip this run"
// rather than as a failure.
const HasCommand = "has-command"

func (s rootService) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by keelbox%s\n[Unit]\nDescription=%s\n", s.Header, s.Description)
	if len(s.After) > 0 {
		after := strings.Join(s.After, " ")
		fmt.Fprintf(&b, "After=%s\nWants=%s\n", after, after)
	}
	if s.DayTwo {
		b.WriteString("StartLimitIntervalSec=0\n")
	}
	b.WriteString("\n[Service]\nType=oneshot\n")
	if s.DayTwo {
		fmt.Fprintf(&b, "ExecCondition=%s %s %s\n", BinaryPath, HasCommand, s.Command)
	}
	fmt.Fprintf(&b, "ExecStart=%s %s --local %s\n", BinaryPath, s.Command, ConfigPath)
	if s.DayTwo {
		timeout := s.Timeout
		if timeout == "" {
			timeout = "2min"
		}
		if s.Packages {
			fmt.Fprintf(&b, "TimeoutStartSec=%s\nNice=10\nCPUWeight=50\nIOWeight=50\n", timeout)
		} else {
			fmt.Fprintf(&b, "TimeoutStartSec=%s\nNice=5\nCPUWeight=200\nIOWeight=200\nNoNewPrivileges=yes\n", timeout)
		}
	}
	for _, l := range s.Service {
		b.WriteString(l + "\n")
	}
	return b.String()
}

// rootTimer starts a rootService on a schedule.
type rootTimer struct {
	Unit        string // keelbox-watch.timer
	Header      string
	Description string
	Service     string   // the unit it starts
	Timer       []string // [Timer] lines before Unit=, such as OnBootSec=90s
	// Calendar, when set, is an OnCalendar expression from the config,
	// which systemd checks before the timer is changed; Key names the
	// config key in the error.
	Calendar, Key string
}

func (t rootTimer) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by keelbox%s\n[Unit]\nDescription=%s\n\n[Timer]\n", t.Header, t.Description)
	if t.Calendar != "" {
		fmt.Fprintf(&b, "OnCalendar=%s\n", t.Calendar)
	}
	for _, l := range t.Timer {
		b.WriteString(l + "\n")
	}
	fmt.Fprintf(&b, "Unit=%s\n\n[Install]\nWantedBy=timers.target\n", t.Service)
	return b.String()
}

// rootPath starts a rootService when a file changes. The watched path may
// be in an account's home: systemd watches it, and the service reads it as
// that account (ADR 0015).
type rootPath struct {
	Unit        string // keelbox-events.path
	Header      string
	Description string
	Service     string
	Modified    string // PathModified=: every write, appends included
}

func (p rootPath) text() string {
	return fmt.Sprintf("# Managed by keelbox%s\n[Unit]\nDescription=%s\n\n[Path]\nPathModified=%s\nUnit=%s\n\n[Install]\nWantedBy=paths.target\n",
		p.Header, p.Description, p.Modified, p.Service)
}

// serviceStep writes a root service unit; a change reloads systemd.
func serviceStep(step string, s rootService) engine.Step {
	return unitFileStep(step, s.Unit, s.text(), false)
}

// timerSteps write a timer and keep it enabled and running. A changed
// timer that is running is restarted, so a new schedule takes effect.
func timerSteps(step string, t rootTimer) []engine.Step {
	f := engine.File{Path: unitDir + t.Unit, Content: []byte(t.text()), Mode: 0o644, OnChange: reloadRestart(t.Unit)}
	if t.Calendar != "" {
		f.Validate = func(ctx context.Context, e *engine.Env) error {
			if _, err := e.X.Run(ctx, system.C("systemd-analyze", "calendar", "--", t.Calendar)); err != nil { //nolint:misspell // the command's name
				return fmt.Errorf("%s %q: %w", t.Key, t.Calendar, err)
			}
			return nil
		}
	}
	return []engine.Step{engine.FileStep(step+"-unit", f), engine.ServiceStep(step, t.Unit)}
}

// pathSteps write a path unit and keep it enabled and running.
func pathSteps(step string, p rootPath) []engine.Step {
	f := engine.File{Path: unitDir + p.Unit, Content: []byte(p.text()), Mode: 0o644, OnChange: reloadRestart(p.Unit)}
	return []engine.Step{engine.FileStep(step+"-unit", f), engine.ServiceStep(step, p.Unit)}
}

// reloadRestart reloads systemd and restarts unit if it is running.
func reloadRestart(unit string) func(context.Context, *engine.Env) error {
	return func(ctx context.Context, e *engine.Env) error {
		if _, err := e.X.Run(ctx, system.C("systemctl", "daemon-reload")); err != nil {
			return err
		}
		if system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", unit)) {
			_, err := e.X.Run(ctx, system.C("systemctl", "restart", unit))
			return err
		}
		return nil
	}
}
