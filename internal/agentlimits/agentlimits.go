// Package agentlimits reads what the agent account uses and how its limits
// held it back, from the cgroup of its systemd slice (user-UID.slice),
// which holds every process of the account: its SSH sessions, its user
// manager's tmux and Remote Control services and its rootless Docker
// (docs/adr/0048-agent-limits.md). The slow-down mark is on the user
// manager's app.slice (ADR 0052). It only reads.
package agentlimits

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/snapshot"
)

// Slice is the agent's systemd slice.
func Slice(uid int) string { return fmt.Sprintf("user-%d.slice", uid) }

// Dir is the slice's cgroup.
func Dir(uid int) string { return fmt.Sprintf("/sys/fs/cgroup/user.slice/user-%d.slice", uid) }

// AppDir is the cgroup of the user manager's app.slice, where the agent's
// services run, inside the slice.
func AppDir(uid int) string { return fmt.Sprintf("%s/user@%d.service/app.slice", Dir(uid), uid) }

// Reader reads files as root.
type Reader interface {
	ReadFile(path string) ([]byte, error)
}

// Counters is the slice's cgroup now. A limit of -1 is none ("max").
type Counters struct {
	// QuotaPercent is cpu.max as systemd's CPUQuota: 100 is one CPU; 0
	// is no quota.
	QuotaPercent int
	CPUWeight    int
	IOWeight     int
	// MemoryHigh is the slow-down mark: the slice's, or else its app.slice's.
	MemoryHigh int64
	MemoryMax  int64
	// AppSlice reports whether the user manager's app.slice is there.
	AppSlice bool
	Memory   int64 // memory.current
	// From cpu.stat.
	UsageUsec, Periods, Throttled, ThrottledUsec uint64
	// From memory.events: times the slice went over memory.high (and was
	// held back), reached memory.max, and the kernel's OOM killer acted in
	// it.
	HighEvents, MaxEvents, OOMKills uint64
}

// Read reads the slice's cgroup; found is false when the slice is not
// there (the agent has no process running and no lingering).
func Read(x Reader, uid int) (c Counters, found bool) { return ReadAs(x, x, uid) }

// ReadAs is Read with the reader for the agent's own part of the tree:
// systemd hands the user manager's cgroups to the account, and root's file
// calls refuse a path another account can change (ADR 0015), so the mark on
// app.slice is read as the agent.
func ReadAs(x, agent Reader, uid int) (c Counters, found bool) {
	dir := Dir(uid)
	read := func(name string) (string, bool) {
		b, err := x.ReadFile(dir + "/" + name)
		return strings.TrimSpace(string(b)), err == nil
	}
	cpuMax, ok := read("cpu.max")
	stat, statOK := read("cpu.stat")
	if !ok && !statOK {
		return Counters{}, false
	}
	c.QuotaPercent = parseCPUMax(cpuMax)
	w, _ := read("cpu.weight")
	c.CPUWeight, _ = strconv.Atoi(w)
	io, _ := read("io.weight") // "default 100"
	if f := strings.Fields(io); len(f) == 2 && f[0] == "default" {
		c.IOWeight, _ = strconv.Atoi(f[1])
	}
	h, _ := read("memory.high")
	c.MemoryHigh = parseBytes(h)
	if b, err := agent.ReadFile(AppDir(uid) + "/memory.high"); err == nil {
		c.AppSlice = true
		if c.MemoryHigh < 0 {
			c.MemoryHigh = parseBytes(strings.TrimSpace(string(b)))
		}
	}
	m, _ := read("memory.max")
	c.MemoryMax = parseBytes(m)
	cur, _ := read("memory.current")
	c.Memory = parseBytes(cur)
	kv := keyValues(stat)
	c.UsageUsec, c.Periods, c.Throttled, c.ThrottledUsec = kv["usage_usec"], kv["nr_periods"], kv["nr_throttled"], kv["throttled_usec"]
	ev, _ := read("memory.events")
	kv = keyValues(ev)
	c.HighEvents, c.MaxEvents, c.OOMKills = kv["high"], kv["max"], kv["oom_kill"]
	return c, true
}

// parseCPUMax reads cpu.max ("300000 100000", or "max 100000").
func parseCPUMax(s string) int {
	f := strings.Fields(s)
	if len(f) != 2 || f[0] == "max" {
		return 0
	}
	q, err1 := strconv.ParseFloat(f[0], 64)
	p, err2 := strconv.ParseFloat(f[1], 64)
	if err1 != nil || err2 != nil || p == 0 {
		return 0
	}
	return int(q/p*100 + 0.5)
}

func parseBytes(s string) int64 {
	if s == "max" || s == "" {
		return -1
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

func keyValues(s string) map[string]uint64 {
	out := map[string]uint64{}
	for _, l := range strings.Split(s, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), " "); ok {
			n, _ := strconv.ParseUint(v, 10, 64)
			out[k] = n
		}
	}
	return out
}

// Usage is the snapshot's agent section from the counters now and the
// previous snapshot's: the agent's share of all CPUs since then, and when
// it was last held back.
func Usage(c Counters, prev *snapshot.AgentUsage, now time.Time, cpus int) *snapshot.AgentUsage {
	u := &snapshot.AgentUsage{
		Time: now, CPUUsec: c.UsageUsec, Memory: c.Memory, MemoryHigh: c.MemoryHigh, MemoryMax: c.MemoryMax,
		CPUQuota: c.QuotaPercent, CPUs: cpus, Throttled: c.Throttled, HighEvents: c.HighEvents, OOMKills: c.OOMKills,
	}
	if prev == nil {
		return u
	}
	u.LastThrottled, u.LastHigh, u.LastOOMKill = prev.LastThrottled, prev.LastHigh, prev.LastOOMKill
	// Counters go back to zero when the slice is made again (a reboot).
	if c.UsageUsec < prev.CPUUsec || c.Throttled < prev.Throttled {
		return u
	}
	if el := now.Sub(prev.Time); el > time.Second && cpus > 0 {
		pct := float64(c.UsageUsec-prev.CPUUsec) / float64(el.Microseconds()*int64(cpus)) * 100
		u.CPUPercent = float64(int(pct*10+0.5)) / 10
	}
	at := now
	if c.Throttled > prev.Throttled {
		u.LastThrottled = &at
	}
	if c.HighEvents > prev.HighEvents {
		u.LastHigh = &at
	}
	if c.OOMKills > prev.OOMKills {
		u.LastOOMKill = &at
	}
	return u
}

// Size is bytes for people: "2.7 GB", "512 MB".
func Size(n int64) string {
	switch {
	case n < 0:
		return "no limit"
	case n >= 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GB"
	}
	return strconv.FormatInt(n>>20, 10) + " MB"
}
