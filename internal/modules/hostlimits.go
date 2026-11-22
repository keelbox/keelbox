package modules

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/agentlimits"
	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	statussnap "github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/system"
)

// users.agent.limits (docs/adr/0048-agent-limits.md, 0052): a drop-in for
// the agent's user-UID.slice, which holds every process of the account
// (its SSH sessions, and its user manager with tmux, Remote Control and
// rootless Docker), caps its CPU and memory together. The memory slow-down
// mark (MemoryHigh) is on the slices the agent's user manager runs its
// services in, not on the whole account: past it the kernel holds back
// every allocation below it, and on the account it held back the user
// manager itself, so systemctl --user hung. A drop-in for
// user.slice lowers the weight of logins against system services, so a
// media server transcoding keeps most of the CPU while an agent builds.
// The agent's tmux and Remote Control services keep running when the
// kernel stops one of their processes at the memory ceiling. Without
// limits, keelbox removes its drop-ins and leaves the slices as systemd
// has them.

const (
	agentSliceDropIn = "/etc/systemd/system/user-%d.slice.d/50-keelbox-limits.conf"
	// managerDropIn points the agent's user manager, and no one else's, at
	// AgentUserConfig for unit files and drop-ins.
	managerDropIn = "/etc/systemd/system/user@%d.service.d/50-keelbox-limits.conf"
	// AgentUserConfig is a root-owned XDG config directory only the agent's
	// user manager reads: its systemd/user holds the slow-down drop-ins.
	AgentUserConfig = "/etc/keelbox/agent-user"
	loginsDropIn    = "/etc/systemd/system/user.slice.d/50-keelbox-limits.conf"
	oomDropInName   = "50-keelbox-oom.conf"
)

// agentOOMDropIns are the agent's user units that keep running when the
// kernel's OOM killer stops one of their processes: systemd's default for a
// service is to stop it, which for tmux would end every session.
var agentOOMDropIns = []string{"/etc/systemd/user/" + tmuxService + ".d/" + oomDropInName, "/etc/systemd/user/" + RemoteControlUnit + ".d/" + oomDropInName}

const oomConfig = `# Managed by keelbox (users.agent.limits): when the kernel stops one of
# this service's processes at the agent's memory ceiling, the others (every
# other tmux session) keep running.
[Service]
OOMPolicy=continue
`

// agentWorkSlices are the user manager's slices the agent's work runs in:
// services (tmux, Remote Control, rootless Docker) in app.slice, rootless
// Docker's containers in user.slice, and background.slice. session.slice
// (the user's D-Bus) and init.scope (the manager itself) are left out.
var agentWorkSlices = []string{"app.slice", "background.slice", "user.slice"}

func workSliceDropIn(slice string) string {
	return AgentUserConfig + "/systemd/user/" + slice + ".d/50-keelbox-limits.conf"
}

const managerConfig = `# Managed by keelbox (users.agent.limits): this user manager, the agent's,
# also reads unit drop-ins from /etc/keelbox/agent-user/systemd/user, where
# keelbox puts the memory slow-down mark on the slices its services run in.
[Service]
Environment=XDG_CONFIG_DIRS=/etc/keelbox/agent-user:/etc/xdg
`

// memoryHigh is the slow-down mark as systemd's MemoryHigh: bytes of the
// RAM there is, or a percentage when it is not known.
func memoryHigh(l config.Limits, mem memInfo) string {
	switch {
	case l.MemoryPercent > 0 && mem.RAM > 0:
		return strconv.FormatInt(mem.RAM/100*int64(l.MemoryHighPercent())&^4095, 10)
	case l.MemoryPercent > 0:
		return strconv.Itoa(l.MemoryHighPercent()) + "%"
	case l.MemoryBytes > 0:
		return strconv.FormatInt(l.MemoryHighBytes()&^4095, 10)
	}
	return ""
}

func workSliceConfig(high string) string {
	return "# Managed by keelbox (users.agent.limits): past this the kernel reclaims the\n" +
		"# memory of the agent's services and holds back their allocations, so they\n" +
		"# slow down before the ceiling on user-UID.slice stops one.\n[Slice]\nMemoryHigh=" + high + "\n"
}

// agentSliceConfig is the agent slice's drop-in for cpus CPUs.
// The swap the agent may use is its share of RAM applied to the swap
// there is, so it cannot fill the swap other services fall back on.
func agentSliceConfig(l config.Limits, cpus int, mem memInfo) string {
	var b strings.Builder
	b.WriteString(`# Managed by keelbox (users.agent.limits): what the agent's processes use
# together, its SSH sessions, tmux, Remote Control and rootless Docker.
[Slice]
`)
	if l.CPUPercent > 0 && l.CPUPercent < 100 {
		fmt.Fprintf(&b, "# %d%% of %d CPUs; systemd counts one CPU as 100%%.\nCPUQuota=%d%%\n", l.CPUPercent, cpus, l.CPUPercent*cpus)
	}
	fmt.Fprintf(&b, "CPUWeight=%d\nIOWeight=%d\n", l.CPUWeight, l.IOWeight)
	// The slow-down mark is on the user manager's slices (workSliceConfig).
	switch {
	case l.MemoryPercent > 0:
		fmt.Fprintf(&b, "# The kernel stops the largest process at the ceiling.\nMemoryMax=%d%%\n", l.MemoryPercent)
	case l.MemoryBytes > 0:
		fmt.Fprintf(&b, "# The kernel stops the largest process at the ceiling.\nMemoryMax=%d\n", l.MemoryBytes)
	}
	if sw := agentSwap(l, mem); sw >= 0 {
		fmt.Fprintf(&b, "# The same share of the %s of swap.\nMemorySwapMax=%d\n", agentlimits.Size(mem.Swap), sw)
	}
	return b.String()
}

// memInfo is RAM and swap, from /proc/meminfo.
type memInfo struct{ RAM, Swap int64 }

func readMemInfo(e *engine.Env) memInfo {
	data, _ := e.X.ReadFile("/proc/meminfo")
	var m memInfo
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		n, _ := strconv.ParseInt(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			m.RAM = n << 10
		case "SwapTotal:":
			m.Swap = n << 10
		}
	}
	return m
}

// agentSwap is MemorySwapMax, or -1 for none: no memory limit, or no swap.
func agentSwap(l config.Limits, m memInfo) int64 {
	switch {
	case m.Swap <= 0:
		return -1
	case l.MemoryPercent > 0:
		return m.Swap / 100 * int64(l.MemoryPercent)
	case l.MemoryBytes > 0 && m.RAM > 0:
		if l.MemoryBytes >= m.RAM {
			return m.Swap
		}
		return int64(float64(m.Swap) * float64(l.MemoryBytes) / float64(m.RAM))
	}
	return -1
}

func loginsConfig(l config.Limits) string {
	return fmt.Sprintf(`# Managed by keelbox (users.agent.limits): logins, the agent's included,
# yield to system services (weight 100) when both want the CPU or disk.
[Slice]
CPUWeight=%d
IOWeight=%d
`, l.CPUWeight, l.IOWeight)
}

func daemonReload(ctx context.Context, e *engine.Env) error {
	_, err := e.X.Run(ctx, system.C("systemctl", "daemon-reload"))
	return err
}

// agentDaemonReload makes the agent's user manager read the OOM drop-ins;
// no unit restarts.
func agentDaemonReload(ctx context.Context, e *engine.Env) error {
	u, err := e.X.LookupUser(e.Cfg.Users.Agent.Name)
	if err != nil || !system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", fmt.Sprintf("user@%d.service", u.UID))) {
		return nil // read when the user manager starts
	}
	_, err = e.X.Run(ctx, userctl(u.Name, "daemon-reload"))
	return err
}

func cpuCount(ctx context.Context, e *engine.Env) (int, error) {
	out, err := system.Output(ctx, e.X, system.C("nproc"))
	if err != nil {
		return 0, fmt.Errorf("count the CPUs (nproc): %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("nproc printed %q", out)
	}
	return n, nil
}

func agentLimitsSteps(c *config.Config) []engine.Step {
	l, on := c.AgentLimits()
	steps := []engine.Step{agentSliceStep(), agentManagerStep()}
	if !on {
		steps = append(steps, keelboxFileAbsentStep("logins-weight", loginsDropIn, daemonReload))
		for i, p := range agentOOMDropIns {
			steps = append(steps, keelboxFileAbsentStep(fmt.Sprintf("agent-oom-policy-%d", i+1), p, agentDaemonReload))
		}
	} else {
		steps = append(steps, engine.FileStep("logins-weight", engine.File{Path: loginsDropIn, Content: []byte(loginsConfig(l)), Mode: 0o644, OnChange: daemonReload}))
		for i, p := range agentOOMDropIns {
			steps = append(steps, engine.FileStep(fmt.Sprintf("agent-oom-policy-%d", i+1), engine.File{Path: p, Content: []byte(oomConfig), Mode: 0o644, OnChange: agentDaemonReload}))
		}
	}
	return append(steps, workSliceSteps()...)
}

// limitsMemory reports whether the config caps the agent's memory.
func limitsMemory(c *config.Config) (config.Limits, bool) {
	l, on := c.AgentLimits()
	return l, on && (l.MemoryPercent > 0 || l.MemoryBytes > 0)
}

// limitsSlowDown reports whether the agent's services have a slow-down
// mark below the ceiling: with a ceiling the config sets, not the default.
func limitsSlowDown(c *config.Config) (config.Limits, bool) {
	l, on := limitsMemory(c)
	return l, on && l.SlowDown
}

// agentManagerStep writes or removes the agent's user manager drop-in,
// whose path holds the agent's UID. The manager reads it when it starts
// (at boot, with lingering); until then agentMemoryHighStep sets the mark
// on the running one.
func agentManagerStep() engine.Step {
	file := func(e *engine.Env) (engine.Step, bool) {
		u, err := e.X.LookupUser(e.Cfg.Users.Agent.Name)
		if err != nil {
			return engine.Step{}, false
		}
		path := fmt.Sprintf(managerDropIn, u.UID)
		if _, on := limitsSlowDown(e.Cfg); !on {
			return keelboxFileAbsentStep("agent-manager", path, daemonReload), true
		}
		return engine.FileStep("agent-manager", engine.File{Path: path, Content: []byte(managerConfig), Mode: 0o644, OnChange: daemonReload}), true
	}
	return engine.Step{
		Name: "agent-manager",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			s, ok := file(e)
			if !ok {
				if _, on := limitsSlowDown(e.Cfg); on {
					return engine.Pending("point %s's user manager at %s once the account exists", e.Cfg.Users.Agent.Name, AgentUserConfig), nil
				}
				return engine.Converged(), nil
			}
			return s.Check(ctx, e)
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			if s, ok := file(e); ok {
				return s.Apply(ctx, e)
			}
			return nil
		},
	}
}

// workSliceSteps write the slow-down mark for the user manager's work
// slices, or remove it, then make the running manager use it.
func workSliceSteps() []engine.Step {
	var steps []engine.Step
	for _, slice := range agentWorkSlices {
		path := workSliceDropIn(slice)
		steps = append(steps, engine.Step{
			Name: "agent-memory-high-" + strings.TrimSuffix(slice, ".slice"),
			Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
				return workSliceFile(e, path).Check(ctx, e)
			},
			Apply: func(ctx context.Context, e *engine.Env) error {
				return workSliceFile(e, path).Apply(ctx, e)
			},
		})
	}
	return append(steps, agentMemoryHighStep())
}

func workSliceFile(e *engine.Env, path string) engine.Step {
	l, on := limitsSlowDown(e.Cfg)
	if !on {
		return keelboxFileAbsentStep("", path, agentDaemonReload)
	}
	return engine.FileStep("", engine.File{Path: path, Content: []byte(workSliceConfig(memoryHigh(l, readMemInfo(e)))), Mode: 0o644, OnChange: agentDaemonReload})
}

// agentMemoryHighStep makes the running user manager's work slices carry
// the mark. A manager that started before agentManagerStep's drop-in does
// not read AgentUserConfig until it starts again, which would end every
// session; set-property --runtime gives its slices the mark now, until it
// next starts and reads the drop-ins itself. Without a memory limit, a mark
// keelbox set that way is lifted.
func agentMemoryHighStep() engine.Step {
	want := func(e *engine.Env) string {
		if l, on := limitsSlowDown(e.Cfg); on {
			return memoryHigh(l, readMemInfo(e))
		}
		return "infinity"
	}
	// stale lists the slices whose mark is not want.
	stale := func(ctx context.Context, e *engine.Env, u system.User) []string {
		var out []string
		w := want(e)
		for _, slice := range agentWorkSlices {
			if w == "infinity" {
				// Lift only what keelbox set at runtime; a mark of the
				// agent's own stays.
				data, err := e.X.ReadFile(fmt.Sprintf("/run/user/%d/systemd/user.control/%s.d/50-MemoryHigh.conf", u.UID, slice))
				if err == nil && !strings.Contains(string(data), "MemoryHigh=infinity") {
					out = append(out, slice)
				}
				continue
			}
			got, err := system.Output(ctx, e.X, userctl(u.Name, "show", "--value", "-p", "MemoryHigh", slice))
			if err == nil && strings.TrimSpace(got) != w {
				out = append(out, slice)
			}
		}
		return out
	}
	running := func(ctx context.Context, e *engine.Env) (system.User, bool) {
		u, err := e.X.LookupUser(e.Cfg.Users.Agent.Name)
		if err != nil || !system.Succeeds(ctx, e.X, system.C("systemctl", "is-active", "--quiet", fmt.Sprintf("user@%d.service", u.UID))) {
			return system.User{}, false
		}
		return u, true
	}
	return engine.Step{
		Name: "agent-memory-high",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			u, ok := running(ctx, e)
			if !ok {
				return engine.Converged(), nil // read when the user manager starts
			}
			if s := stale(ctx, e, u); len(s) > 0 {
				if want(e) == "infinity" {
					return engine.Pending("lift the memory slow-down mark on %s's %s", u.Name, strings.Join(s, ", ")), nil
				}
				return engine.Pending("set the memory slow-down mark on %s's %s", u.Name, strings.Join(s, ", ")), nil
			}
			return engine.Converged(), nil
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			u, ok := running(ctx, e)
			if !ok {
				return nil
			}
			if _, err := e.X.Run(ctx, userctl(u.Name, "daemon-reload")); err != nil {
				return err
			}
			for _, slice := range stale(ctx, e, u) {
				if _, err := e.X.Run(ctx, userctl(u.Name, "set-property", "--runtime", slice, "MemoryHigh="+want(e))); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// agentSliceStep writes or removes the agent slice's drop-in. The path
// holds the agent's UID, which exists only once the account does, so the
// step works it out when it runs rather than when the plan is made.
func agentSliceStep() engine.Step {
	file := func(ctx context.Context, e *engine.Env) (engine.Step, bool, error) {
		u, err := e.X.LookupUser(e.Cfg.Users.Agent.Name)
		if err != nil {
			return engine.Step{}, false, nil
		}
		path := fmt.Sprintf(agentSliceDropIn, u.UID)
		l, on := e.Cfg.AgentLimits()
		if !on {
			return keelboxFileAbsentStep("agent-limits", path, daemonReload), true, nil
		}
		cpus, err := cpuCount(ctx, e)
		if err != nil {
			return engine.Step{}, false, err
		}
		return engine.FileStep("agent-limits", engine.File{Path: path, Content: []byte(agentSliceConfig(l, cpus, readMemInfo(e))), Mode: 0o644, OnChange: daemonReload}), true, nil
	}
	return engine.Step{
		Name: "agent-limits",
		Check: func(ctx context.Context, e *engine.Env) (engine.Status, error) {
			s, ok, err := file(ctx, e)
			switch {
			case err != nil:
				return engine.Status{}, err
			case !ok:
				if _, on := e.Cfg.AgentLimits(); on {
					return engine.Pending("limit %s's processes once the account exists", e.Cfg.Users.Agent.Name), nil
				}
				return engine.Converged(), nil
			}
			return s.Check(ctx, e)
		},
		Apply: func(ctx context.Context, e *engine.Env) error {
			s, ok, err := file(ctx, e)
			if err != nil || !ok {
				return err
			}
			return s.Apply(ctx, e)
		},
	}
}

// verifyAgentLimits is doctor's view: the limits in force on the slice now,
// and how often they held the agent back since it was made.
func verifyAgentLimits(ctx context.Context, e *engine.Env) []engine.Finding {
	l, on := e.Cfg.AgentLimits()
	u, err := e.X.LookupUser(e.Cfg.Users.Agent.Name)
	if !on || err != nil {
		return nil
	}
	c, found := agentlimits.ReadAs(e.X, e.FS(u.Name), u.UID)
	if !found {
		return []engine.Finding{warn("host", "agent-limits", "no "+agentlimits.Slice(u.UID)+" now (nothing runs as "+u.Name+"); its limits apply when something does")}
	}
	cpus, _ := cpuCount(ctx, e)
	var bad []string
	if want := l.CPUPercent * cpus; l.CPUPercent > 0 && l.CPUPercent < 100 && c.QuotaPercent != want {
		bad = append(bad, fmt.Sprintf("CPU quota %d%%, want %d%%", c.QuotaPercent, want))
	}
	if (l.MemoryPercent > 0 || l.MemoryBytes > 0) && c.MemoryMax < 0 {
		bad = append(bad, "no memory ceiling")
	}
	if l.SlowDown && c.AppSlice && c.MemoryHigh < 0 {
		bad = append(bad, "no memory slow-down mark on the agent's services (app.slice)")
	}
	if c.CPUWeight != l.CPUWeight {
		bad = append(bad, fmt.Sprintf("CPU weight %d, want %d", c.CPUWeight, l.CPUWeight))
	}
	if len(bad) > 0 {
		return []engine.Finding{fail("host", "agent-limits", agentlimits.Slice(u.UID)+" is not limited as configured ("+strings.Join(bad, ", ")+"): sudo systemctl daemon-reload, or keelbox apply "+e.Cfg.Box)}
	}
	out := []engine.Finding{ok("host", "agent-limits", describeLimits(c, cpus))}
	return append(out, throttling(e, u.UID, c))
}

// describeLimits is the limits in force, in words.
func describeLimits(c agentlimits.Counters, cpus int) string {
	cpu := "no CPU cap"
	if c.QuotaPercent > 0 && cpus > 0 {
		cpu = fmt.Sprintf("CPU %d%% of %d (%d%% of all)", c.QuotaPercent, cpus, c.QuotaPercent/cpus)
	}
	mem := "no memory ceiling"
	switch {
	case c.MemoryMax >= 0 && c.MemoryHigh >= 0:
		mem = "memory slows at " + agentlimits.Size(c.MemoryHigh) + ", ceiling " + agentlimits.Size(c.MemoryMax)
	case c.MemoryMax >= 0:
		mem = "memory ceiling " + agentlimits.Size(c.MemoryMax)
	}
	return fmt.Sprintf("%s; %s; weight %d against system services' 100", cpu, mem, c.CPUWeight)
}

// throttling says how the limits held the agent back since the slice was
// made (with lingering, the last boot), and when last, from the watch run.
func throttling(e *engine.Env, uid int, c agentlimits.Counters) engine.Finding {
	var last *statussnap.AgentUsage
	if s, found, _ := statussnap.Read(e.X); found {
		last = s.AgentUsage
	}
	when := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return ", last " + t.Local().Format("2006-01-02 15:04")
	}
	var parts []string
	if c.Periods > 0 {
		pct := float64(c.Throttled) / float64(c.Periods) * 100
		d := fmt.Sprintf("CPU held back in %.1f%% of periods (%s)", pct, time.Duration(float64(c.ThrottledUsec)*float64(time.Microsecond)).Round(time.Second))
		if last != nil {
			d += when(last.LastThrottled)
		}
		parts = append(parts, d)
	}
	if c.MemoryHigh >= 0 || c.HighEvents > 0 {
		mem := fmt.Sprintf("memory over its slow-down mark %d times", c.HighEvents)
		if last != nil {
			mem += when(last.LastHigh)
		}
		parts = append(parts, mem)
	}
	if len(parts) == 0 {
		parts = append(parts, "nothing held back")
	}
	if c.OOMKills > 0 {
		// A warning for a day after the last one, so that it clears by
		// itself; after that the count stays in the line.
		if at := lastOOM(last); at == nil || time.Since(*at) < oomWarnFor {
			return warn("host", "agent-throttling", fmt.Sprintf("since %s was made, the kernel stopped %d of the agent's processes because memory ran out%s; %s. Run less at once or raise users.agent.limits.memory; see what was stopped: journalctl -k -g 'Killed process'",
				agentlimits.Slice(uid), c.OOMKills, when(lastOOM(last)), strings.Join(parts, "; ")))
		}
		return ok("host", "agent-throttling", fmt.Sprintf("since %s was made: %s; the kernel stopped %d of its processes because memory ran out%s",
			agentlimits.Slice(uid), strings.Join(parts, "; "), c.OOMKills, when(lastOOM(last))))
	}
	return ok("host", "agent-throttling", "since "+agentlimits.Slice(uid)+" was made: "+strings.Join(parts, "; ")+"; no process stopped")
}

// oomWarnFor is how long doctor warns after the kernel last stopped a
// process of the agent's for memory.
const oomWarnFor = 24 * time.Hour

func lastOOM(u *statussnap.AgentUsage) *time.Time {
	if u == nil {
		return nil
	}
	return u.LastOOMKill
}

// verifyIOWeight names the disks' IO schedulers, for io_weight: only bfq
// honours it.
func verifyIOWeight(ctx context.Context, e *engine.Env) []engine.Finding {
	if _, on := e.Cfg.AgentLimits(); !on {
		return nil
	}
	out, err := system.Output(ctx, e.X, system.C("lsblk", "-dno", "NAME,SCHED"))
	if err != nil {
		return nil
	}
	var bfq, other []string
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) != 2 || strings.HasPrefix(f[0], "loop") {
			continue
		}
		if f[1] == "bfq" {
			bfq = append(bfq, f[0])
		} else {
			other = append(other, f[0]+" ("+f[1]+")")
		}
	}
	switch {
	case len(other) == 0 && len(bfq) > 0:
		return []engine.Finding{ok("host", "io-weight", "IO weight in effect: "+strings.Join(bfq, ", ")+" use bfq")}
	case len(other) > 0:
		return []engine.Finding{ok("host", "io-weight", "IO weight has no effect on "+strings.Join(other, ", ")+": only the bfq scheduler honours it; CPU and memory limits hold regardless")}
	}
	return nil
}

// limitsImpact is what users.agent.limits changes on a machine in use.
// Nothing is cut off, so it never gates the first apply.
func limitsImpact(ctx context.Context, e *engine.Env, in engine.Inspection) []engine.ImpactItem {
	l, on := e.Cfg.AgentLimits()
	_, slice := in.Pending("host", "agent-limits")
	_, logins := in.Pending("host", "logins-weight")
	if !slice && !logins {
		return nil
	}
	if !on {
		return []engine.ImpactItem{{Group: engine.ImpactChanges, Subject: "limits", Detail: "keelbox removes its limits on the agent's processes and the lower weight of logins"}}
	}
	var caps []string
	if l.CPUPercent > 0 && l.CPUPercent < 100 {
		caps = append(caps, fmt.Sprintf("%d%% of the CPUs", l.CPUPercent))
	}
	switch {
	case l.MemoryPercent > 0 && !l.SlowDown:
		caps = append(caps, fmt.Sprintf("%d%% of memory", l.MemoryPercent))
	case l.MemoryPercent > 0:
		caps = append(caps, fmt.Sprintf("%d%% of memory (slowing down from %d%%)", l.MemoryPercent, l.MemoryHighPercent()))
	case l.MemoryBytes > 0:
		caps = append(caps, agentlimits.Size(l.MemoryBytes)+" of memory (slowing down from "+agentlimits.Size(l.MemoryHighBytes())+")")
	}
	d := "the agent's processes together"
	if len(caps) > 0 {
		d += " get at most " + strings.Join(caps, " and ") + ";"
	} else {
		d += " have no cap;"
	}
	d += fmt.Sprintf(" logins, the agent's included, yield to system services when both are busy (CPU weight %d, IO weight %d, against 100)", l.CPUWeight, l.IOWeight)
	return []engine.ImpactItem{{Group: engine.ImpactChanges, Subject: "limits", Detail: d, Hint: "users.agent.limits: {memory: none} leaves the agent unlimited"}}
}

// AgentUsageSummary is keelbox status's line for the agent: what it uses
// now against its limits.
func AgentUsageSummary(ctx context.Context, e *engine.Env) string {
	u, err := e.X.LookupUser(e.Cfg.Users.Agent.Name)
	if err != nil {
		return ""
	}
	c, found := agentlimits.ReadAs(e.X, e.FS(u.Name), u.UID)
	if !found {
		return ""
	}
	cpu, lastKill := "", ""
	if s, found, _ := statussnap.Read(e.X); found && s.AgentUsage != nil && s.AgentUsage.LastOOMKill != nil {
		lastKill = ", last " + s.AgentUsage.LastOOMKill.Local().Format("2006-01-02 15:04")
	}
	if s, found, _ := statussnap.Read(e.X); found && s.AgentUsage != nil && !s.Stale(time.Now()) {
		cpu = fmt.Sprintf("CPU %.0f%% over the last two minutes", s.AgentUsage.CPUPercent)
		if q, n := s.AgentUsage.CPUQuota, s.AgentUsage.CPUs; q > 0 && n > 0 {
			cpu += fmt.Sprintf(" (cap %d%%)", q/n)
		}
		cpu += ", "
	}
	mem := "memory " + agentlimits.Size(c.Memory)
	switch {
	case c.MemoryMax >= 0 && c.MemoryHigh >= 0:
		mem += " of " + agentlimits.Size(c.MemoryMax) + " (slows at " + agentlimits.Size(c.MemoryHigh) + ")"
	case c.MemoryMax >= 0:
		mem += " of " + agentlimits.Size(c.MemoryMax)
	}
	s := cpu + mem
	if c.OOMKills > 0 {
		s += fmt.Sprintf("; the kernel stopped %d of its processes because memory ran out%s", c.OOMKills, lastKill)
	}
	return s
}
