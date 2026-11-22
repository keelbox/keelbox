package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/runlock"
	"github.com/keelbox/keelbox/internal/watch"
)

// keelbox reboot restarts a box and waits until it is back: the box saves
// the agent's tmux sessions and has systemd reboot it a few seconds later,
// and this computer watches the box's boot ID change. The box reboots
// itself for an update with host.auto_reboot nightly or weekly
// (internal/watch).

const bootIDCommand = "cat /proc/sys/kernel/random/boot_id"

// rebootWait is how long keelbox reboot waits for the box to come back.
var rebootWait = 6 * time.Minute

// rebootPoll is how often it looks; tests shorten it.
var rebootPoll = 5 * time.Second

func cmdReboot(ctx context.Context, cfg *config.Config, o options, path, provider string, p *prompt.Prompter, stdout, stderr io.Writer) int {
	ref := boxRef(cfg, o.configFile)
	if forLaptop(o) {
		// The box's half: save the sessions and reboot.
		code, _ := runProvider(ctx, "reboot", cfg, o, path, provider, stdout, stderr)
		return code
	}
	if provider == config.ProviderLocal && os.Geteuid() != 0 {
		// This machine is the box: run as root, which asks there.
		return asRoot("reboot", path, o, stdout, stderr)
	}
	if isCloud(provider) && parkedLocally(cfg, "") != nil {
		failf(stderr, "%s is parked, so there is no server to reboot; bring it back: keelbox resume %s", cfg.Box, ref)
		return 1
	}
	if !o.yes {
		if !p.Interactive() {
			failf(stderr, "reboot stops what runs on %s, so it asks first, and there is no terminal to ask on; %s", cfg.Box, orOwner("to reboot anyway: keelbox reboot "+ref+" --yes", "keelbox reboot "+ref))
			return 2
		}
		what := cfg.Box
		if provider == config.ProviderLocal {
			what = "this machine (" + cfg.Box + ")"
		}
		note := "What runs in the agent's sessions stops."
		if back := watch.AfterReboot(cfg); back != "" {
			note += " " + back + "."
		}
		fmt.Fprintln(stdout, note)
		ok, err := p.Confirm(fmt.Sprintf("Reboot %s now?", what), false)
		if errors.Is(err, prompt.ErrInterrupted) {
			return exitInterrupted
		}
		if err != nil || !ok {
			fmt.Fprintln(stdout, "Left it running.")
			return 0
		}
	}
	if provider == config.ProviderLocal {
		code, _ := runProvider(ctx, "reboot", cfg, o, path, provider, stdout, stderr)
		return code
	}
	sh, err := boxShell(ctx, cfg, o, cfg.Users.Admin.Name, stdout, stderr)
	if err != nil {
		fail(stderr, err)
		return 1
	}
	before, err := sh.output(ctx, bootIDCommand)
	if err != nil {
		fail(stderr, sshFailure(ctx, err, cfg, sh.ssh, o, ""))
		return 1
	}
	if code, _ := runProvider(ctx, "reboot", cfg, o, path, provider, stdout, stderr); code != 0 {
		return code
	}
	fmt.Fprintf(stdout, "Waiting for %s to come back...\n", cfg.Box)
	start := time.Now()
	if err := backAfterReboot(ctx, sh, strings.TrimSpace(before)); err != nil {
		if ctx.Err() != nil {
			return exitInterrupted
		}
		failf(stderr, "%s has not come back after %s. It may still be starting: keelbox status %s; if it does not answer, look at its console in the cloud's dashboard (%s/guides/troubleshooting)", cfg.Box, rebootWait, ref, docsURL)
		return 1
	}
	if cfg.TmuxRecovery() {
		fmt.Fprintf(stdout, "%s is back after %s. Its tmux sessions, conversations and Remote Control come back within two minutes: keelbox sessions %s\n",
			cfg.Box, time.Since(start).Round(time.Second), ref)
	} else {
		fmt.Fprintf(stdout, "%s is back after %s: keelbox status %s\n", cfg.Box, time.Since(start).Round(time.Second), ref)
	}
	return 0
}

// backAfterReboot waits until the box answers with a boot ID other than
// before.
var backAfterReboot = func(ctx context.Context, sh *shell, before string) error {
	deadline := time.Now().Add(rebootWait)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(rebootPoll):
		}
		try, cancel := context.WithTimeout(ctx, 20*time.Second)
		id, err := sh.output(try, bootIDCommand)
		cancel()
		if id = strings.TrimSpace(id); err == nil && id != "" && id != before {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("not back")
		}
	}
}

// rebootHere is the box's half of keelbox reboot, as root: it waits for
// day-two runs, refuses while an apply runs, and reboots.
func rebootHere(ctx context.Context, e *engine.Env, stdout, stderr io.Writer) int {
	shared, own, err := runlock.DayTwo(runlock.Path, runlock.DayTwoPath, 30*time.Second)
	if errors.Is(err, runlock.ErrBusy) {
		failf(stderr, "an apply is running on %s; reboot once it has finished: keelbox reboot %s", e.Cfg.Box, e.Cfg.Box)
		return 1
	}
	if err != nil {
		fail(stderr, err)
		return 1
	}
	defer shared.Unlock()
	defer own.Unlock()
	if err := watch.Restart(ctx, e, "asked with keelbox reboot", time.Now().UTC()); err != nil {
		fail(stderr, err)
		return 1
	}
	saved := ""
	if e.Cfg.TmuxRecovery() {
		saved = "Saved the agent's tmux sessions. "
	}
	fmt.Fprintf(stdout, "%s%s reboots in %d seconds.\n", saved, e.Cfg.Box, int(watch.RebootDelay.Seconds()))
	return 0
}

// rebootNote is keelbox status's word on a needed reboot.
func rebootNote(cfg *config.Config) string {
	switch cfg.Host.AutoReboot {
	case config.AutoRebootNightly:
		return "the box reboots itself at night once idle; now: keelbox reboot " + cfg.Box
	case config.AutoRebootWeekly:
		return "the box reboots itself on Sunday night once idle; now: keelbox reboot " + cfg.Box
	}
	return "keelbox reboot " + cfg.Box
}
