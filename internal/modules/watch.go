package modules

import (
	"context"
	"fmt"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/reports"
	"github.com/keelbox/keelbox/internal/system"
	"github.com/keelbox/keelbox/internal/watch"
)

// Watch installs keelbox's day-two units (ADR 0030): the watch run, every
// two minutes, the events service, which reads the agent's events file
// as the agent whenever it changes (ADR 0032), and the boot marker that
// tells a power loss or crash from a reboot (ADR 0040), and the unit that
// notes the agent's conversations at shutdown (ADR 0096). None changes the
// box's configuration.
type Watch struct{}

func (Watch) Name() string                { return "watch" }
func (Watch) Enabled(*config.Config) bool { return true }

// The events units.
const (
	EventsCommand = "events-sync"
	eventsService = "keelbox-events.service"
	eventsPath    = "keelbox-events.path"
)

var watchService = rootService{
	Unit: watch.Service, Header: ". The watch run: heartbeat, status snapshot, queued events.",
	Description: "keelbox watch run", Command: watch.Command, DayTwo: true, Timeout: "1min",
}

var watchTimer = rootTimer{
	Unit: watch.Timer, Header: ". Starts the watch run every two minutes.", Description: "keelbox watch schedule",
	Service: watch.Service, Timer: []string{"OnBootSec=90s", "OnUnitActiveSec=2min", "AccuracySec=15s"},
}

var eventsSyncService = rootService{
	Unit: eventsService, Header: ". Reads the agent's events file, as the agent, into the event queue.",
	Description: "keelbox events: read the agent's events", Command: EventsCommand, DayTwo: true, Timeout: "1min",
}

func eventsPathUnit(agentHome string) rootPath {
	return rootPath{
		Unit: eventsPath, Header: ". Starts the events service when the agent's events file changes.",
		Description: "keelbox events: watch the agent's events file", Service: eventsService,
		Modified: agentHome + "/" + events.AgentFile,
	}
}

// bootUnit marks clean shutdowns. With the default dependencies systemd
// stops it at every orderly shutdown or reboot, before /var/lib is
// unmounted; its stop touches the marker its next start looks for.
var bootUnit = fmt.Sprintf(`# Managed by keelbox. Marks each clean shutdown, so the next boot can tell a
# power loss, crash or hard reset from a reboot.
[Unit]
Description=keelbox boot and clean-shutdown marker
RequiresMountsFor=%[1]s

[Service]
Type=oneshot
RemainAfterExit=yes
ExecCondition=%[2]s %[3]s %[4]s
ExecStart=%[2]s %[4]s
ExecStop=/usr/bin/touch %[5]s
TimeoutSec=30

[Install]
WantedBy=multi-user.target
`, watch.PowerDir, BinaryPath, HasCommand, watch.BootCommand, watch.CleanMarker)

// The conversations unit (ADR 0096). It does nothing at start; its stop,
// at every orderly shutdown or reboot, notes the tmux panes where Claude
// Code or Codex runs, so the first watch runs after the boot open their
// conversations again. The drop-in orders every user manager before it, so
// systemd stops it, and the note is taken, while the agent's tmux server
// still runs. A keelbox too old to know the command fails the stop, which
// is ignored.
const (
	ConversationsUnit = "keelbox-conversations.service"
	conversationsDrop = "user@.service.d/50-keelbox-conversations.conf"
)

var conversationsUnit = fmt.Sprintf(`# Managed by keelbox. At every orderly shutdown or reboot, notes the tmux
# panes where Claude Code or Codex runs, so the next boot opens their
# conversations again.
[Unit]
Description=keelbox: note the agent's conversations at shutdown
RequiresMountsFor=%[1]s
# A watch run still going stops first, so its save cannot land on this one.
Before=%[6]s

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/rm -f %[5]s
ExecStop=-%[2]s %[3]s --local %[4]s
TimeoutStopSec=30

[Install]
WantedBy=multi-user.target
`, watch.Dir, BinaryPath, watch.NoteCommand, ConfigPath, watch.ShutdownSavedPath, watch.Service)

var conversationsOrder = `# Managed by keelbox. Stops each user manager after
# ` + ConversationsUnit + `, so at shutdown it notes the agent's
# conversations while their tmux server still runs.
[Unit]
Before=` + ConversationsUnit + `
`

func (Watch) Steps(e *engine.Env) []engine.Step {
	agent := e.AgentUser()
	steps := []engine.Step{
		engine.DirStep("watch-dir", watch.Dir, 0o755, "root", "root"),
		engine.DirStep("power-dir", watch.PowerDir, 0o755, "root", "root"),
		unitFileStep("boot-unit-file", watch.BootUnit, bootUnit, false),
		engine.ServiceStep("boot-unit", watch.BootUnit),
		unitFileStep("conversations-order", conversationsDrop, conversationsOrder, false),
		unitFileStep("conversations-unit-file", ConversationsUnit, conversationsUnit, false),
		engine.ServiceStep("conversations-unit", ConversationsUnit),
		engine.DirStep("events-dir", events.Dir, 0o700, "root", "root"),
		engine.DirStep("events-queue-dir", events.QueueDir, 0o700, "root", "root"),
		engine.DirStep("reports-dir", reports.Dir, 0o700, "root", "root"),
		engine.DirStep("agent-events-dir", agent.Home+"/"+events.AgentDir, 0o700, agent.Name, agent.Name),
		serviceStep("watch-service-unit", watchService),
		serviceStep("events-service-unit", eventsSyncService),
	}
	steps = append(steps, timerSteps("watch-timer", watchTimer)...)
	return append(steps, pathSteps("events-path", eventsPathUnit(agent.Home))...)
}

func (Watch) Verify(ctx context.Context, e *engine.Env) []engine.Finding {
	const mod = "watch"
	var out []engine.Finding
	for _, u := range [][2]string{{"timer", watch.Timer}, {"events-path", eventsPath}, {"boot-marker", watch.BootUnit}, {"conversations", ConversationsUnit}} {
		if system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", u[1])) {
			out = append(out, ok(mod, u[0], u[1]+" active"))
		} else {
			out = append(out, fail(mod, u[0], u[1]+" is not active (systemctl status "+u[1]+"); keelbox apply "+e.Cfg.Box+" starts it again"))
		}
	}
	st, found := watch.ReadState(e.X)
	age := time.Since(st.Heartbeat).Round(time.Second)
	switch {
	case !found:
		out = append(out, warn(mod, "last-run", "no watch run yet; it starts within two minutes of apply"))
	case age > watch.StaleAfter:
		out = append(out, fail(mod, "last-run", fmt.Sprintf("last watch run %s ago, over %s: journalctl -u %s", age, watch.StaleAfter, watch.Service)))
	default:
		out = append(out, ok(mod, "last-run", fmt.Sprintf("%s ago", age)))
	}
	if q, err := events.Pending(e.X); err == nil {
		out = append(out, ok(mod, "event-queue", fmt.Sprintf("%d queued", len(q))))
	}
	return out
}
