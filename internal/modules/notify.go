package modules

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/notify"
)

// Notify installs what notifications need (docs/adr/0043-notifications.md):
// root-only copies of each channel's secret files, and, through the agent
// policy builder, managed hooks in the agent CLIs that run keelbox event.
// Sending is the day-two runs' (the events service and the watch run).
type Notify struct{}

func (Notify) Name() string { return "notify" }

// Enabled always: with no channel left, the module removes the secret
// copies an earlier config installed.
func (Notify) Enabled(*config.Config) bool { return true }

// EventCommand is what the managed hooks run: the box's keelbox, as the
// agent, with the hook's JSON on stdin.
const EventCommand = "event"

func (Notify) Steps(e *engine.Env) []engine.Step {
	chans := e.Cfg.Notify.Channels
	if len(chans) == 0 {
		return []engine.Step{notifySecretsPrune(nil)}
	}
	steps := []engine.Step{engine.DirStep("notify-secret-dir", notify.SecretDir, 0o700, "root", "root")}
	want := map[string]bool{}
	for i, ch := range chans {
		for _, s := range notify.Secrets(ch) {
			dst := notify.SecretPath(ch.Name, s.Key)
			want[dst] = true
			steps = append(steps, secretFileStep("notify-"+ch.Name+"-"+strings.TrimSuffix(s.Key, "_file"),
				fmt.Sprintf("notify.channels[%d].%s", i, s.Key), s.Path, dst))
		}
	}
	return append(steps, notifySecretsPrune(want))
}

// notifySecretsPrune removes copies of secret files no channel names any
// more, and the directory once no channel is left.
func notifySecretsPrune(want map[string]bool) engine.Step {
	stale := func(e *engine.Env) ([]string, bool, error) {
		st, err := e.X.Stat(notify.SecretDir)
		if err != nil || !st.Exists {
			return nil, false, err
		}
		names, err := e.X.ReadDir(notify.SecretDir)
		if err != nil {
			return nil, true, err
		}
		var out []string
		for _, n := range names {
			if p := notify.SecretDir + "/" + n; !want[p] {
				out = append(out, p)
			}
		}
		return out, true, nil
	}
	return engine.Step{
		Name: "notify-secrets-unused",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			old, exists, err := stale(e)
			switch {
			case err != nil:
				return engine.Status{}, err
			case len(old) > 0:
				return engine.Pending("remove %d secret file(s) no channel uses from %s", len(old), notify.SecretDir), nil
			case want == nil && exists:
				return engine.Pending("remove %s (no notify.channels)", notify.SecretDir), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			old, _, err := stale(e)
			if err != nil {
				return err
			}
			for _, p := range old {
				if err := e.X.Remove(p); err != nil {
					return err
				}
			}
			if want == nil {
				return e.X.Remove(notify.SecretDir)
			}
			return nil
		},
	}
}

// notifyHooks is the notification policy source: managed hooks that report
// an agent finishing, waiting for input, hitting a limit or failing. They
// run async with a short timeout, so they never hold the CLI up; managed
// hooks run whatever the agent's own settings say about hooks.
func notifyHooks(e *engine.Env, p *AgentPolicy) {
	if len(e.Cfg.Notify.Channels) == 0 {
		return
	}
	hook := func(cli string) Hook {
		cmd := BinaryPath + " " + EventCommand + " --from " + cli
		if e.Cfg.NotifyAgentText() {
			cmd += " --text"
		}
		return Hook{Command: cmd, Timeout: 10, Async: true}
	}
	for _, ev := range []string{"Stop", "StopFailure", "Notification"} {
		p.AddHook(PolicyClaude, ev, "", hook(PolicyClaude))
	}
	for _, ev := range []string{"Stop", "PermissionRequest"} {
		p.AddHook(PolicyCodex, ev, "", hook(PolicyCodex))
	}
}

// queueWarnAfter is how old a queued event may be before doctor warns.
const queueWarnAfter = time.Hour

func (Notify) Verify(ctx context.Context, e *engine.Env) []engine.Finding {
	const mod = "notify"
	chans := e.Cfg.Notify.Channels
	if len(chans) == 0 {
		return nil
	}
	var out []engine.Finding
	for _, ch := range chans {
		bad := ""
		for _, s := range notify.Secrets(ch) {
			p := notify.SecretPath(ch.Name, s.Key)
			if st, err := e.X.Stat(p); err != nil || !st.Exists || st.IsSymlink || st.Mode != 0o600 || st.Owner != "root" {
				bad = p
				break
			}
		}
		if bad != "" {
			out = append(out, fail(mod, ch.Name, bad+" missing or not mode 600 root; keelbox apply "+e.Cfg.Box+" installs it"))
			continue
		}
		l, sent := notify.ReadLast(e.X)[ch.Name]
		switch {
		case !sent:
			out = append(out, ok(mod, ch.Name, ch.Type+", nothing sent yet (keelbox notify test "+e.Cfg.Box+")"))
		case l.Error != "":
			out = append(out, warn(mod, ch.Name, "last send failed: "+l.Error+"; keelbox notify test "+e.Cfg.Box+" --channel "+ch.Name))
		default:
			out = append(out, ok(mod, ch.Name, ch.Type+", last sent "+l.OK.Local().Format("2006-01-02 15:04")))
		}
	}
	// Events quiet hours hold are meant to wait.
	if q, err := events.Pending(e.X); err == nil {
		var oldest time.Time
		waiting := 0
		for _, x := range q {
			if x.Held {
				continue
			}
			waiting++
			if oldest.IsZero() || x.Event.Time.Before(oldest) {
				oldest = x.Event.Time
			}
		}
		if age := time.Since(oldest); waiting > 0 && age > queueWarnAfter {
			out = append(out, warn(mod, "queue", fmt.Sprintf("%d event(s) waiting to be sent, the oldest for %s: a channel keeps failing; keelbox notify test %s",
				waiting, age.Round(time.Minute), e.Cfg.Box)))
		}
	}
	return out
}
