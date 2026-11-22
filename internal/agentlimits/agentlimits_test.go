package agentlimits

import (
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/system"
)

func TestReadAndUsage(t *testing.T) {
	x := system.NewFake()
	dir := Dir(1001)
	x.AddFile(dir+"/cpu.max", "300000 100000\n", 0o644)
	x.AddFile(dir+"/cpu.weight", "50\n", 0o644)
	x.AddFile(dir+"/io.weight", "default 50\n", 0o644)
	x.AddFile(dir+"/memory.high", "max\n", 0o644)
	x.AddFile(dir+"/memory.max", "4294967296\n", 0o644)
	x.AddFile(dir+"/memory.current", "1073741824\n", 0o644)
	x.AddFile(dir+"/cpu.stat", "usage_usec 240000000\nnr_periods 10\nnr_throttled 4\nthrottled_usec 5\n", 0o644)
	x.AddFile(dir+"/memory.events", "high 0\nmax 0\noom_kill 1\n", 0o644)
	c, found := Read(x, 1001)
	if !found || c.QuotaPercent != 300 || c.CPUWeight != 50 || c.IOWeight != 50 || c.MemoryHigh != -1 || c.MemoryMax != 4<<30 || c.Throttled != 4 || c.OOMKills != 1 {
		t.Fatalf("%+v %v", c, found)
	}
	if _, found := Read(x, 1002); found {
		t.Error("found a slice that is not there")
	}
	t0 := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	first := Usage(c, nil, t0, 4)
	if first.CPUPercent != 0 || first.LastOOMKill != nil {
		t.Errorf("first: %+v", first)
	}
	// Two minutes later: 240 s more CPU over 4 CPUs is half of all of it.
	c.UsageUsec += 240_000_000
	c.Throttled += 3
	next := Usage(c, first, t0.Add(2*time.Minute), 4)
	if next.CPUPercent != 50 || next.LastThrottled == nil || !next.LastThrottled.Equal(t0.Add(2*time.Minute)) || next.LastOOMKill != nil {
		t.Errorf("next: %+v", next)
	}
	// After a reboot the counters start again: no share, last times kept.
	c.UsageUsec, c.Throttled = 10, 0
	after := Usage(c, next, t0.Add(4*time.Minute), 4)
	if after.CPUPercent != 0 || after.LastThrottled == nil {
		t.Errorf("after reboot: %+v", after)
	}
	if Size(2900000000) != "2.7 GB" || Size(512<<20) != "512 MB" || Size(-1) != "no limit" {
		t.Error("Size")
	}
}
