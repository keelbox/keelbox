package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/agentlimits"
	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/doctorrun"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/installs"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/manifest"
	"github.com/keelbox/keelbox/internal/modules"
	"github.com/keelbox/keelbox/internal/notify"
	"github.com/keelbox/keelbox/internal/plain"
	"github.com/keelbox/keelbox/internal/projects"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/runlock"
	"github.com/keelbox/keelbox/internal/runs"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/system"
	"github.com/keelbox/keelbox/internal/version"
	"github.com/keelbox/keelbox/internal/watch"
)

const lockPath = runlock.Path

// exitInterrupted is the exit code of an apply stopped by Ctrl-C (128 +
// SIGINT, as a shell reports it).
const exitInterrupted = 130

// NewEnv builds the run environment for this machine: facts, platform check
// and the channel's manifest.
func NewEnv(ctx context.Context, x system.Executor, cfg *config.Config, log io.Writer) (*engine.Env, error) {
	m, err := manifest.ForChannel(cfg.Channel)
	if err != nil {
		return nil, err
	}
	facts, err := engine.GatherFacts(ctx, x)
	if err != nil {
		return nil, err
	}
	if err := engine.CheckPlatform(facts, m); err != nil {
		return nil, err
	}
	return &engine.Env{X: x, Cfg: cfg, M: m, Facts: facts, Log: log, AptStale: true}, nil
}

func runLocal(ctx context.Context, cmd string, cfg *config.Config, o options, stdout, stderr io.Writer) int {
	if id := os.Getenv(envRun); id != "" && cmd == "apply" && os.Geteuid() == 0 {
		return detachedRun(ctx, id, cfg, o, stdout, stderr)
	}
	if os.Geteuid() != 0 {
		failf(stderr, "%s changes or inspects system state; run it with sudo: sudo \"$(command -v keelbox)\" %s\n", cmd, cmd)
		return 1
	}
	if cmd == "rollback" {
		return rollback(ctx, stdout, stderr)
	}
	resolved, err := cfg.Resolve()
	if err != nil {
		fail(stderr, err)
		return 2
	}
	resolved.Target = config.Target{Provider: config.ProviderLocal}
	resolved.ApplyDefaults()
	x := &system.Real{}
	if o.verbose {
		x.Log = stdout
	}
	env, err := NewEnv(ctx, x, resolved, nil)
	if err != nil {
		fail(stderr, err)
		return 1
	}
	// /proc/self/exe opens the binary that is running, whatever happens to
	// the path it was started from, which may be in a user's home.
	env.Self = "/proc/self/exe"
	env.Facts.InvokingUser = os.Getenv("SUDO_USER")
	// Set by the remote script from the SSH session's own $SSH_CONNECTION.
	env.SSHConnection = os.Getenv("KEELBOX_SSH_CONNECTION")
	// Set by the remote script: where the laptop reaches this box's sshd.
	env.SSHAddrs = os.Getenv(provider.EnvSSHAddrs)
	env.Adopt = o.adopt
	switch cmd {
	case "reboot":
		return rebootHere(ctx, env, stdout, stderr)
	case "plan":
		if o.impact {
			return PlanImpact(ctx, env, modules.All(), o.json, stdout)
		}
		return Plan(ctx, env, modules.All(), stdout)
	case "apply":
		// Before anything changes, here or in a detached run: a container
		// setting only its host can change stops part of the box. It stays
		// ahead of lock() and startRun, so the refusal reaches the laptop
		// at once and nothing, not even the lock file, is written.
		if err := modules.CheckContainer(ctx, env); err != nil {
			fail(stderr, err)
			return 1
		}
		if !keysGate(env, o.removeKeys, stderr) {
			return 1
		}
		if os.Getenv(envDetach) == "1" {
			return startRun(ctx, env, o, stdout, stderr)
		}
		unlock, err := lock()
		if err != nil {
			fail(stderr, err)
			return 1
		}
		defer unlock()
		from, err := releaseGuard(env, o.allowDowngrade)
		if err != nil {
			fail(stderr, err)
			return 1
		}
		if code := githubGate(ctx, env, stderr); code != 0 {
			return code
		}
		if !firstApplyGate(ctx, env, modules.All(), stderr) {
			return 1
		}
		out, errOut, done := applyView(resolved, o, stdout, stderr)
		defer done()
		env.Log = out
		return Apply(ctx, env, modules.All(), from, out, errOut)
	case "doctor":
		return Doctor(ctx, env, modules.All(), stdout)
	case "status":
		if o.json {
			return StatusJSON(ctx, env, stdout)
		}
		return Status(ctx, env, stdout)
	case "notify":
		return notifyTest(ctx, env, o, stdout, stderr)
	case "previews-sync":
		return previewsSync(ctx, env, stderr)
	case "reports":
		return Reports(env, o, stdout, stderr)
	case watch.Command, modules.EventsCommand:
		return dayTwo(ctx, cmd, env, stdout, stderr)
	case watch.NoteCommand:
		return noteConversations(ctx, env, stdout)
	case doctorrun.Command:
		return doctorRun(ctx, env, modules.All(), stdout, stderr)
	case "backup", "restore":
		return runBackup(ctx, cmd, env, o, stdout, stderr)
	case installs.Command:
		return installRun(ctx, env, stdout, stderr)
	case "requests":
		return Requests(env, o, stdout, stderr)
	case "approve":
		return approveRequests(ctx, env, o, installs.Options{Lock: installsLock}, stdout, stderr)
	case "deny":
		return denyRequest(env, o, installs.Options{Lock: installsLock}, stdout, stderr)
	}
	return 2
}

// lock takes the apply lock. Day-two runs hold it shared for a few
// seconds, so apply waits for them; another apply holds it for minutes.
func lock() (func(), error) {
	l, err := runlock.Apply(lockPath, 30*time.Second)
	if errors.Is(err, runlock.ErrBusy) {
		return nil, fmt.Errorf("another keelbox apply is running (%s is locked)", lockPath)
	}
	if err != nil {
		return nil, err
	}
	return l.Unlock, nil
}

// rollback re-applies the box with the keelbox it ran before the last
// upgrade. That binary installs itself again, keeping the newer one as the
// previous, so a second rollback undoes the first. The laptop's keelbox
// moves the box forward again on its next apply.
func rollback(ctx context.Context, stdout, stderr io.Writer) int {
	if _, err := os.Stat(modules.PreviousBinary); err != nil {
		failf(stderr, "nothing to roll back to: %s does not exist (it is kept when an apply replaces keelbox)\n", modules.PreviousBinary)
		return 1
	}
	// Run the previous keelbox from the stage, not from where it is kept:
	// apply then swaps the two, keeping the current binary as the previous
	// one, so a second rollback undoes the first.
	prev, err := os.ReadFile(modules.PreviousBinary)
	if err != nil {
		failf(stderr, "rollback: %v", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(provider.RemoteBinary), 0o700); err != nil {
		failf(stderr, "rollback: %v", err)
		return 1
	}
	if err := replaceExecutable(provider.RemoteBinary, prev); err != nil {
		failf(stderr, "rollback: %v", err)
		return 1
	}
	cmd := exec.CommandContext(ctx, provider.RemoteBinary, "apply", "--local", "--allow-downgrade", modules.ConfigPath)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		failf(stderr, "rollback: %v", err)
		return 1
	}
	return 0
}

// replaceExecutable puts data at path by writing a sibling file and renaming
// it over path. Writing path in place fails with "text file busy" while it
// is running, which it is when rollback is started from a laptop: the
// uploaded keelbox runs from the stage it then replaces.
func replaceExecutable(path string, data []byte) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil { //nolint:gosec // G306: an executable
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil { //nolint:gosec // G302: an executable
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// releaseGuard refuses to apply a release older than the box's last
// converged apply, so an out-of-date laptop never silently downgrades the
// pinned tools, unless the user asks for it. It returns the box's current
// release when this apply would move it, for the logbook.
func releaseGuard(e *engine.Env, allowDowngrade bool) (string, error) {
	last, err := logbook.LastApplied(e.X)
	if err != nil {
		return "", fmt.Errorf("read the logbook: %w", err)
	}
	if last == nil || last.Release == e.M.Release {
		return "", nil
	}
	if manifest.CompareReleases(e.M.Release, last.Release) < 0 && !allowDowngrade {
		return "", fmt.Errorf("the box is on release %s (applied %s by keelbox %s); this keelbox carries the older release %s. Upgrade keelbox, or pass --allow-downgrade to move the box back",
			last.Release, last.Time.Format("2006-01-02"), last.Keelbox, e.M.Release)
	}
	return last.Release, nil
}

func header(e *engine.Env, verb string) string {
	return fmt.Sprintf("keelbox %s: %s (release %s, Ubuntu %s %s)\n", verb, e.Cfg.Box, e.M.Release, e.Facts.OSVersion, e.Facts.Arch)
}

// Plan prints what apply would change. Exit 0 either way; 1 if a check errored.
func Plan(ctx context.Context, e *engine.Env, mods []engine.Module, out io.Writer) int {
	fmt.Fprint(out, header(e, "plan"))
	items := engine.Plan(ctx, e, mods)
	changes, errs, mod := 0, 0, ""
	for _, it := range items {
		if it.Module != mod {
			mod = it.Module
			fmt.Fprintf(out, "\n%s\n", mod)
		}
		switch {
		case it.Err != nil:
			errs++
			fmt.Fprintf(out, "  !  %-30s check failed: %s\n", it.Step, plain.Text(it.Err.Error()))
		case it.OK:
			fmt.Fprintf(out, "  =  %s\n", it.Step)
		default:
			changes++
			fmt.Fprintf(out, "  +  %-30s %s\n", it.Step, it.Detail)
		}
	}
	fmt.Fprintln(out)
	if changes == 0 && errs == 0 {
		fmt.Fprintf(out, "No changes. The box matches the config (%d checks).\n", len(items))
		return 0
	}
	fmt.Fprintf(out, "Plan: %d to change, %d unchanged", changes, len(items)-changes-errs)
	if errs > 0 {
		fmt.Fprintf(out, ", %d check errors", errs)
	}
	fmt.Fprintln(out, ".")
	if errs > 0 {
		return 1
	}
	return 0
}

// Apply converges the box and records the run in the logbook.
// fromRelease, when set, is the release the box is moving from.
func Apply(ctx context.Context, e *engine.Env, mods []engine.Module, fromRelease string, out, errOut io.Writer) int {
	fmt.Fprint(out, header(e, "apply"))
	if fromRelease != "" {
		fmt.Fprintf(out, "Moving the box from release %s to %s.\n", fromRelease, e.M.Release)
	}
	res, err := engine.Apply(ctx, e, mods)
	rec := logbook.Record{
		Time: time.Now().UTC(), Box: e.Cfg.Box, Keelbox: version.Version, Release: e.M.Release,
		FromRelease: fromRelease, ConfigHash: e.Cfg.Hash(), Result: "ok", Checked: res.Checked, Changes: res.Changes,
		Deferred: res.Deferred, Failed: res.Failed, Seconds: res.Duration.Seconds(),
	}
	interrupted := errors.Is(err, engine.ErrInterrupted)
	switch {
	case interrupted:
		rec.Result, rec.Error = "interrupted", err.Error()
	case err != nil:
		rec.Result, rec.Error = "failed", err.Error()
	case len(res.Failed) > 0:
		rec.Result = "partial"
	}
	if lerr := logbook.Append(e.X, rec); lerr != nil {
		fmt.Fprintln(errOut, "keelbox: warning: could not write the logbook:", lerr)
	}
	fmt.Fprintln(out)
	if interrupted {
		// The word "stopped" is what ui.Progress settles the module on.
		fmt.Fprintf(errOut, "keelbox: apply stopped %s, after %s. Every step before it is in place; the next apply carries on from there.\n",
			strings.TrimPrefix(err.Error(), engine.ErrInterrupted.Error()+" "), plural(len(res.Changes), "change", "changes"))
		return exitInterrupted
	}
	if err != nil {
		fmt.Fprintf(errOut, "keelbox: apply failed after %s: %s\n", plural(len(res.Changes), "change", "changes"), plain.Text(err.Error()))
		return 1
	}
	for _, d := range res.Deferred {
		fmt.Fprintf(out, "Deferred %s/%s: %s.\n", d.Module, d.Step, d.Detail)
	}
	for _, f := range res.Failed {
		fmt.Fprintf(errOut, "keelbox: %s/%s failed: %s\n", f.Module, f.Step, strings.TrimPrefix(f.Detail, f.Module+"/"+f.Step+": "))
	}
	took := res.Duration.Round(time.Second)
	switch {
	case len(res.Changes) == 0 && len(res.Failed) == 0 && len(res.Deferred) == 0:
		fmt.Fprintf(out, "No changes. The box matches the config (%d checks, %s).\n", res.Checked, took)
	case len(res.Changes) == 0:
		fmt.Fprintf(out, "No changes applied (%d checks, %s).\n", res.Checked, took)
	default:
		fmt.Fprintf(out, "Applied %s in %s. Recorded in %s.\n", plural(len(res.Changes), "change", "changes"), took, logbook.Path)
	}
	for _, c := range res.Changes {
		if c.Module == "host" && c.Step == modules.BootstrapSudoersStep {
			// Said in so many words: the owner's own sudo changes.
			fmt.Fprintf(out, "Removed %s; sudo asks for the password again.\n", strings.TrimPrefix(c.Detail, "remove "))
		}
	}
	applyPackagesNote(ctx, e, out)
	if len(res.Failed) > 0 {
		fmt.Fprintf(errOut, "keelbox: %s failed; the rest of the box is applied. Fix and re-run keelbox apply.\n", plural(len(res.Failed), "step", "steps"))
		return 1
	}
	return 0
}

// Doctor prints verify findings and drift. Exit 1 on any failure.
func Doctor(ctx context.Context, e *engine.Env, mods []engine.Module, out io.Writer) int {
	fmt.Fprint(out, header(e, "doctor"))
	fs := engine.Doctor(ctx, e, mods)
	counts := map[engine.Level]int{}
	mod := ""
	for _, f := range fs {
		counts[f.Level]++
		if f.Module != mod {
			mod = f.Module
			fmt.Fprintf(out, "\n%s\n", mod)
		}
		fmt.Fprintf(out, "  %-4s  %-24s %s\n", f.Level, f.Check, f.Detail)
	}
	fmt.Fprintf(out, "\n%d ok, %d warnings, %d failures.\n", counts[engine.LevelOK], counts[engine.LevelWarn], counts[engine.LevelFail])
	if engine.Failed(fs) {
		return 1
	}
	return 0
}

// statusTimeout bounds each command status runs as the agent.
const statusTimeout = 20 * time.Second

// Status prints a short summary.
func Status(ctx context.Context, e *engine.Env, out io.Writer) int {
	// Rows carry the agent's session names and its CLIs' output.
	row := func(k, v string) { fmt.Fprintf(out, "%-10s %s\n", k, plain.Line(v)) }
	row("box", fmt.Sprintf("%s (Ubuntu %s %s)", e.Cfg.Box, e.Facts.OSVersion, e.Facts.Arch))
	row("keelbox", fmt.Sprintf("%s, release %s", version.Version, e.M.Release))
	recs, _ := logbook.Read(e.X)
	var lastApply time.Time
	if l := logbook.Last(recs, logbook.KindApply); l != nil {
		lastApply = l.Time
		row("applied", fmt.Sprintf("%s, %s, %d change(s)", l.Time.Format("2006-01-02 15:04 MST"), l.Result, len(l.Changes)))
	} else {
		row("applied", "never")
	}
	if s, ok := runs.Unfinished(ctx, e.X, lastApply); ok {
		d := s.Describe(time.Now())
		if !s.Running {
			d += "; carry on: keelbox apply " + e.Cfg.Box
		}
		row("setup", d)
	}
	if up, err := system.Output(ctx, e.X, system.C("uptime", "-p")); err == nil {
		row("uptime", up)
	}
	if df, err := system.Output(ctx, e.X, system.C("df", "--output=pcent", "/")); err == nil {
		row("disk", strings.TrimSpace(lastLine(df))+" of / used")
	}
	if free, err := system.Output(ctx, e.X, system.C("free", "-m")); err == nil {
		row("memory", memUsed(free))
	}
	if u := modules.AgentUsageSummary(ctx, e); u != "" {
		row("agent", u)
	}
	if snap, found, _ := snapshot.Read(e.X); found {
		if h := snap.Host; h != nil {
			v := fmt.Sprintf("%d CPU(s), %s memory", h.CPUs, agentlimits.Size(h.Memory))
			if why := modules.SmallHost(h.CPUs, h.Memory); why != "" {
				v += "; small: " + why
			}
			row("size", v)
		}
		if snap.Updates != nil {
			v := updatesCell(snap.Updates)
			if snap.Updates.RebootRequired != nil {
				v += " (reboot needed since " + snap.Updates.RebootRequired.Local().Format("2006-01-02 15:04") + ": " + rebootNote(e.Cfg) + ")"
			}
			row("updates", v)
		}
	}
	if e.Cfg.Tailscale() {
		st := "not connected; " + modules.TailnetJoin(e)
		if modules.TailscaleRunning(ctx, e) {
			st = "connected"
		}
		row("tailnet", st)
		ssh := "open (LAN and internet)"
		switch {
		case modules.PublicSSHClosed(e) && e.Cfg.PublicSSHMode() == config.PublicSSHLAN:
			ssh = "closed to the internet: tailnet and LAN only"
		case modules.PublicSSHClosed(e):
			ssh = "closed: tailnet only"
		}
		row("ssh", ssh)
	}
	if e.Cfg.PreviewsEnabled() {
		row("previews", modules.PreviewsSummary(e))
	}
	if e.Cfg.BackupEnabled() {
		row("backup", modules.BackupSummary(e))
	}
	row("doctor", doctorrun.Summary(e.X, e.Cfg.DoctorSchedule(), time.Now()))
	if s := notify.Summary(e.Cfg, e.X); s != "" {
		row("notify", s)
	}
	if e.Cfg.Tmux() {
		agent := e.Cfg.Users.Agent.Name
		c := system.As(agent, "tmux", "list-sessions", "-F", "#S (#{session_windows} #{?#{==:#{session_windows},1},window,windows})")
		c.Timeout = statusTimeout
		s, err := system.Output(ctx, e.X, c)
		if err != nil || s == "" {
			s = "none"
		}
		row("sessions", strings.ReplaceAll(s, "\n", ", "))
	}
	if e.Cfg.RemoteControl() {
		row("remote", "Claude Code Remote Control "+modules.RemoteControlSummary(ctx, e))
	}
	var agents []string
	for _, a := range e.Cfg.Agents {
		bin := map[string]string{config.AgentClaudeCode: "claude", config.AgentCodex: "codex"}[a]
		c := system.As(e.Cfg.Users.Agent.Name, e.AgentTool(bin), "--version")
		c.Timeout = statusTimeout
		v, err := system.Output(ctx, e.X, c)
		if err != nil {
			v = "missing"
		}
		agents = append(agents, a+" "+firstLine(v))
	}
	if len(agents) > 0 {
		row("agents", strings.Join(agents, ", "))
	}
	projectRows(out, projects.Probe(ctx, e.X, e.Cfg))
	installRows(ctx, e, out)
	for _, r := range watch.StatusRows(e.X, e.Cfg.Watchdog()) {
		row(r[0], r[1])
	}
	return 0
}

// projectRows prints the agent's projects: a summary row, then a line for
// each under the value column. The names are the agent's; they were made
// plain where they were read and are again here.
func projectRows(out io.Writer, p *snapshot.Projects) {
	if p == nil {
		return
	}
	fmt.Fprintf(out, "%-10s %s\n", "projects", plain.Line(p.Summary()))
	for _, l := range p.Rows() {
		fmt.Fprintf(out, "%-10s %s\n", "", plain.Line(l))
	}
}

// StatusJSON prints the box's status snapshot, collected now, as one line
// of JSON: what keelbox destroy reads before it deletes a server.
func StatusJSON(ctx context.Context, e *engine.Env, out io.Writer) int {
	s := watch.Collect(ctx, e, time.Now().UTC())
	s.Schema = snapshot.Schema
	b, err := json.Marshal(s)
	if err != nil {
		fmt.Fprintln(out, "keelbox:", err)
		return 1
	}
	fmt.Fprintln(out, string(b))
	return 0
}

func lastLine(s string) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	return l[len(l)-1]
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return l
}

func memUsed(free string) string {
	for _, l := range strings.Split(free, "\n") {
		f := strings.Fields(l)
		if len(f) >= 7 && f[0] == "Mem:" {
			return fmt.Sprintf("%s MB used of %s MB, %s MB available", f[2], f[1], f[6])
		}
	}
	return "unknown"
}
