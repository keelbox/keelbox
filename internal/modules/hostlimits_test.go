package modules

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/agentlimits"
	"github.com/keelbox/keelbox/internal/engine"
	statussnap "github.com/keelbox/keelbox/internal/snapshot"
)

const limitsYAML = "  agent:\n    limits: {cpu: 75%, memory: 70%}\n"

// Limits write the agent slice's drop-in (CPUQuota for all four CPUs),
// the logins' weight and the OOM policy for the agent's tmux and Remote
// Control; removing them removes keelbox's drop-ins and nothing else.
func TestAgentLimitsConvergeAndGo(t *testing.T) {
	s := newSim(t)
	ctx := context.Background()
	e := simEnv(t, s, testConfig(t, limitsYAML))
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	u, err := s.LookupUser("agent")
	if err != nil {
		t.Fatal(err)
	}
	slice := fmt.Sprintf(agentSliceDropIn, u.UID)
	conf := s.content(slice)
	for _, want := range []string{"[Slice]", "CPUQuota=300%", "CPUWeight=50", "IOWeight=50", "MemoryMax=70%", "Managed by keelbox"} {
		if !strings.Contains(conf, want) {
			t.Errorf("%s lacks %q:\n%s", slice, want, conf)
		}
	}
	// The slow-down mark is on the user manager's work slices, not on the
	// whole account, whose user manager it would slow down too (ADR 0052).
	if strings.Contains(conf, "MemoryHigh") {
		t.Errorf("MemoryHigh on the account's slice:\n%s", conf)
	}
	if c := s.content(fmt.Sprintf(managerDropIn, u.UID)); !strings.Contains(c, "Environment=XDG_CONFIG_DIRS=/etc/keelbox/agent-user:/etc/xdg") {
		t.Errorf("manager drop-in: %q", c)
	}
	for _, sl := range agentWorkSlices {
		if c := s.content(workSliceDropIn(sl)); !strings.Contains(c, "MemoryHigh=63%") || !strings.Contains(c, "Managed by keelbox") {
			t.Errorf("%s: %q", sl, c)
		}
		// The manager ran before the drop-in: the mark is set at runtime.
		if !s.Ran("systemctl --user -M agent@ set-property --runtime " + sl + " MemoryHigh=63%") {
			t.Errorf("%s: no runtime mark", sl)
		}
	}
	if s.sliceHigh["session.slice"] != "" {
		t.Error("marked session.slice")
	}
	if strings.Contains(conf, "MemorySwapMax") {
		t.Errorf("swap limit without swap:\n%s", conf)
	}
	if c := s.content(loginsDropIn); !strings.Contains(c, "CPUWeight=50") || !strings.Contains(c, "IOWeight=50") {
		t.Errorf("logins:\n%s", c)
	}
	for _, p := range agentOOMDropIns {
		if !strings.Contains(s.content(p), "OOMPolicy=continue") {
			t.Errorf("%s: %q", p, s.content(p))
		}
	}
	if !s.Ran("systemctl daemon-reload") {
		t.Error("no daemon-reload")
	}
	for _, c := range s.Calls {
		if strings.Contains(c.String(), "restart") && strings.Contains(c.String(), tmuxService) {
			t.Errorf("restarted tmux: %s", c)
		}
	}
	if res, err := engine.Apply(ctx, e, All()); err != nil || len(res.Changes) != 0 {
		t.Errorf("second apply changed %v (%v)", res.Changes, err)
	}

	// With 4 GiB of swap, the agent may use 70% of it.
	s.AddFile("/proc/meminfo", "MemTotal:        8000000 kB\nSwapTotal:       4194304 kB\n", 0o444)
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if c := s.content(slice); !strings.Contains(c, fmt.Sprintf("MemorySwapMax=%d\n", int64(4<<30)/100*70)) || !strings.Contains(c, "4.0 GB of swap") {
		t.Errorf("swap:\n%s", c)
	}
	// With the RAM known, the mark is in bytes, as systemctl show says it.
	high := fmt.Sprint(int64(8000000<<10) / 100 * 63 &^ 4095)
	if c := s.content(workSliceDropIn("app.slice")); !strings.Contains(c, "MemoryHigh="+high+"\n") || s.sliceHigh["app.slice"] != high {
		t.Errorf("bytes: %q %v", c, s.sliceHigh)
	}

	// A size, and other weights.
	e.Cfg = testConfig(t, "  agent:\n    limits: {memory: 8G, cpu_weight: 20}\n")
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	// 8 GiB of 8 000 000 kB RAM is all of it: all the swap.
	if c := s.content(slice); strings.Contains(c, "CPUQuota") || !strings.Contains(c, fmt.Sprintf("MemoryMax=%d", 8<<30)) || !strings.Contains(c, fmt.Sprintf("MemorySwapMax=%d\n", int64(4<<30))) || !strings.Contains(c, "CPUWeight=20") {
		t.Errorf("size:\n%s", c)
	}

	// No limits, which the config has to say: keelbox's drop-ins go; a
	// drop-in of the owner's stays.
	s.AddFile("/etc/systemd/system/user.slice.d/10-mine.conf", "[Slice]\nTasksMax=4000\n", 0o644)
	e.Cfg = testConfig(t, "  agent:\n    limits: {memory: none}\n")
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	for _, p := range append([]string{slice, loginsDropIn, fmt.Sprintf(managerDropIn, u.UID), workSliceDropIn("app.slice")}, agentOOMDropIns...) {
		if s.has(p) {
			t.Errorf("%s left behind", p)
		}
	}
	if s.sliceHigh["app.slice"] != "infinity" {
		t.Errorf("runtime mark not lifted: %v", s.sliceHigh)
	}
	if !s.has("/etc/systemd/system/user.slice.d/10-mine.conf") {
		t.Error("removed the owner's drop-in")
	}
	if res, err := engine.Apply(ctx, e, All()); err != nil || len(res.Changes) != 0 {
		t.Errorf("after removing: %v (%v)", res.Changes, err)
	}
}

// The first plan, before the agent exists, says the limit waits for it.
func TestAgentLimitsBeforeTheAccount(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, limitsYAML))
	st, err := agentSliceStep().Check(context.Background(), e)
	if err != nil || st.OK || !strings.Contains(st.Detail, "once the account exists") {
		t.Errorf("%+v %v", st, err)
	}
}

func seedSlice(s *sim, uid int, cpuMax, memMax, oomKills string) {
	dir := agentlimits.Dir(uid)
	s.AddFile(dir+"/cpu.max", cpuMax+" 100000\n", 0o644)
	s.AddFile(dir+"/cpu.weight", "50\n", 0o644)
	s.AddFile(dir+"/io.weight", "default 50\n", 0o644)
	s.AddFile(dir+"/memory.high", "2702000128\n", 0o644)
	s.AddFile(dir+"/memory.max", memMax+"\n", 0o644)
	s.AddFile(dir+"/memory.current", "1073741824\n", 0o644)
	s.AddFile(dir+"/cpu.stat", "usage_usec 9000000\nuser_usec 8000000\nsystem_usec 1000000\nnr_periods 1000\nnr_throttled 250\nthrottled_usec 61000000\n", 0o644)
	s.AddFile(dir+"/memory.events", "low 0\nhigh 12\nmax 3\noom 1\noom_kill "+oomKills+"\n", 0o644)
}

func TestAgentLimitsDoctor(t *testing.T) {
	s := newSim(t)
	ctx := context.Background()
	e := simEnv(t, s, testConfig(t, limitsYAML))
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	u, _ := s.LookupUser("agent")
	find := func(check string) engine.Finding {
		for _, f := range (Host{}).Verify(ctx, e) {
			if f.Check == check {
				return f
			}
		}
		return engine.Finding{}
	}
	if f := find("agent-limits"); f.Level != engine.LevelWarn || !strings.Contains(f.Detail, "nothing runs as agent") {
		t.Errorf("no slice: %+v", f)
	}
	seedSlice(s, u.UID, "300000", "3002000000", "0")
	if got := AgentUsageSummary(ctx, e); got != "memory 1.0 GB of 2.8 GB (slows at 2.5 GB)" {
		t.Errorf("status: %q", got)
	}
	f := find("agent-limits")
	if f.Level != engine.LevelOK || f.Detail != "CPU 300% of 4 (75% of all); memory slows at 2.5 GB, ceiling 2.8 GB; weight 50 against system services' 100" {
		t.Errorf("limits: %+v", f)
	}
	if f := find("agent-throttling"); f.Level != engine.LevelOK || !strings.Contains(f.Detail, "CPU held back in 25.0% of periods (1m1s); memory over its slow-down mark 12 times; no process stopped") {
		t.Errorf("throttling: %+v", f)
	}
	seedSlice(s, u.UID, "300000", "3002000000", "2")
	if f := find("agent-throttling"); f.Level != engine.LevelWarn || !strings.Contains(f.Detail, "the kernel stopped 2 of the agent's processes because memory ran out") || !strings.Contains(f.Detail, "users.agent.limits.memory") {
		t.Errorf("oom: %+v", f)
	}
	// A day after the last one the warning has cleared; the count stays.
	old := time.Now().Add(-25 * time.Hour)
	if err := statussnap.Write(s, statussnap.Status{AgentUsage: &statussnap.AgentUsage{LastOOMKill: &old}}); err != nil {
		t.Fatal(err)
	}
	if f := find("agent-throttling"); f.Level != engine.LevelOK || !strings.Contains(f.Detail, "the kernel stopped 2 of its processes because memory ran out, last ") {
		t.Errorf("a day after: %+v", f)
	}
	// The drop-in is there but systemd has not read it.
	seedSlice(s, u.UID, "max", "max", "0")
	if f := find("agent-limits"); f.Level != engine.LevelFail || !strings.Contains(f.Detail, "CPU quota 0%, want 300%, no memory ceiling") || !strings.Contains(f.Detail, "systemctl daemon-reload") {
		t.Errorf("not in force: %+v", f)
	}
	if got := AgentUsageSummary(ctx, e); got != "memory 1.0 GB" {
		t.Errorf("status: %q", got)
	}
}

func TestAgentLimitsImpact(t *testing.T) {
	s := homeServer(t)
	items := impactOf(t, s, limitsYAML)
	for _, it := range items {
		if it.Subject == "limits" {
			if it.Gate || !strings.Contains(it.Detail, "the agent's processes together get at most 75% of the CPUs and 70% of memory (slowing down from 63%)") ||
				!strings.Contains(it.Detail, "CPU weight 50, IO weight 50, against 100") {
				t.Errorf("%+v", it)
			}
			return
		}
	}
	t.Errorf("no limits item:\n%s", strings.Join(describe(items), "\n"))
}

// A user manager that started with keelbox's drop-in (after a reboot)
// reads the mark itself on a daemon-reload: nothing is set at runtime.
func TestAgentMemoryHighReadByTheManager(t *testing.T) {
	s := newSim(t)
	s.managerXDG = true
	ctx := context.Background()
	e := simEnv(t, s, testConfig(t, limitsYAML))
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if s.Ran("systemctl --user -M agent@ set-property") || s.sliceHigh["app.slice"] != "63%" {
		t.Errorf("runtime mark with the drop-in read: %v", s.sliceHigh)
	}
	if res, err := engine.Apply(ctx, e, All()); err != nil || len(res.Changes) != 0 {
		t.Errorf("second apply changed %v (%v)", res.Changes, err)
	}
}

// The slow-down mark is read from app.slice when the account's slice has
// none, and doctor fails when the agent's services run without it.
func TestAgentLimitsDoctorReadsTheMarkFromAppSlice(t *testing.T) {
	s := newSim(t)
	ctx := context.Background()
	e := simEnv(t, s, testConfig(t, limitsYAML))
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	u, _ := s.LookupUser("agent")
	seedSlice(s, u.UID, "300000", "3002000000", "0")
	s.AddFile(agentlimits.Dir(u.UID)+"/memory.high", "max\n", 0o644)
	s.AddFile(agentlimits.AppDir(u.UID)+"/memory.high", "2702000128\n", 0o644)
	find := func() engine.Finding {
		for _, f := range (Host{}).Verify(ctx, e) {
			if f.Check == "agent-limits" {
				return f
			}
		}
		return engine.Finding{}
	}
	if f := find(); f.Level != engine.LevelOK || !strings.Contains(f.Detail, "memory slows at 2.5 GB, ceiling 2.8 GB") {
		t.Errorf("mark on app.slice: %+v", f)
	}
	s.AddFile(agentlimits.AppDir(u.UID)+"/memory.high", "max\n", 0o644)
	if f := find(); f.Level != engine.LevelFail || !strings.Contains(f.Detail, "no memory slow-down mark on the agent's services") {
		t.Errorf("no mark: %+v", f)
	}
}

// Without users.agent.limits the agent has a memory ceiling and nothing
// else: 90% of RAM, slowing down from 81% (ADR 0082).
func TestAgentHasAMemoryCeilingByDefault(t *testing.T) {
	s := newSim(t)
	ctx := context.Background()
	e := simEnv(t, s, testConfig(t, ""))
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	u, err := s.LookupUser("agent")
	if err != nil {
		t.Fatal(err)
	}
	c := s.content(fmt.Sprintf(agentSliceDropIn, u.UID))
	if !strings.Contains(c, "MemoryMax=90%") || strings.Contains(c, "CPUQuota") {
		t.Errorf("the agent's slice:\n%s", c)
	}
	// A ceiling alone: at a slow-down mark one runaway process holds back
	// all the agent's work and is not stopped.
	if s.has(workSliceDropIn("app.slice")) || s.has(fmt.Sprintf(managerDropIn, u.UID)) {
		t.Errorf("a slow-down mark with the default ceiling: %q", s.content(workSliceDropIn("app.slice")))
	}
	if res, err := engine.Apply(ctx, e, All()); err != nil || len(res.Changes) != 0 {
		t.Errorf("second apply changed %v (%v)", res.Changes, err)
	}
}

// The primer tells the agent what its work shares and what happens at the
// ceiling, in sizes; without a ceiling it says what running out does.
func TestPrimerSaysWhatMemoryTheAgentHas(t *testing.T) {
	s := newSim(t)
	// RAM that cannot be read: the share as the config has it.
	if p := RenderPrimer(simEnv(t, s, testConfig(t, ""))); !strings.Contains(p, "Everything you run shares 90% of memory") {
		t.Errorf("without /proc/meminfo:\n%s", p[:900])
	}
	s.AddFile("/proc/meminfo", "MemTotal:        8000000 kB\nSwapTotal:       4194304 kB\n", 0o444)
	p := RenderPrimer(simEnv(t, s, testConfig(t, "")))
	if strings.Contains(p, "slow\ndown first") {
		t.Errorf("a slow-down with the default ceiling:\n%s", p[:900])
	}
	for _, want := range []string{"Everything you run shares 6.9 GB of memory", "At 6.9 GB the kernel stops the\nlargest one; the rest keep running.", "`keelbox status` shows what you use"} {
		if !strings.Contains(p, want) {
			t.Errorf("primer lacks %q:\n%s", want, p[:900])
		}
	}
	p = RenderPrimer(simEnv(t, s, testConfig(t, "  agent:\n    limits: {memory: 8G}\n")))
	if !strings.Contains(p, "Everything you run shares 8.0 GB of memory") || !strings.Contains(p, "Past 7.2 GB all of them slow\ndown first.") {
		t.Errorf("a size:\n%s", p[:900])
	}
	p = RenderPrimer(simEnv(t, s, testConfig(t, "  agent:\n    limits: {memory: none}\n")))
	if strings.Contains(p, "Everything you run shares") || !strings.Contains(p, "Nothing holds your processes back short of all the memory") {
		t.Errorf("no ceiling:\n%s", p[:900])
	}
}
