package watch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/snapshot"
)

const kernelLog = `Out of memory: Killed process 27802 (node) total-vm:5447660kB, anon-rss:3614976kB, file-rss:2176kB, shmem-rss:0kB, UID:1002 pgtables:10684kB oom_score_adj:200
Memory cgroup out of memory: Killed process 311 (python3) total-vm:900000kB, anon-rss:524288kB, file-rss:10kB, shmem-rss:0kB, UID:1002 pgtables:100kB oom_score_adj:200
Out of memory: Killed process 99 (mysqld) total-vm:900000kB, anon-rss:800000kB, file-rss:10kB, shmem-rss:0kB, UID:114 pgtables:100kB oom_score_adj:0
Out of memory: Killed process 5 (a b;$(x)) total-vm:9kB, anon-rss:1048576kB, file-rss:1kB, shmem-rss:0kB, UID:1002 pgtables:1kB oom_score_adj:200
`

// The names are the agent's processes only, with what each held, and a
// name is made plain: the process chose it.
func TestKilledNames(t *testing.T) {
	got := strings.Join(killedNames(kernelLog, 1002), ", ")
	if want := "node (3.4 GB), python3 (512 MB), a?b???x (1.0 GB)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := killedNames(kernelLog, 7); got != nil {
		t.Errorf("another account's: %q", got)
	}
	many := strings.Repeat(strings.Split(kernelLog, "\n")[0]+"\n", 6)
	if got := killedNames(many, 1002); len(got) != killedNamed+1 || got[killedNamed] != "2 more" {
		t.Errorf("six kills: %q", got)
	}
}

// An event when the count of the agent's slice went up since the run
// before, with what is new; none on the first run or when nothing was
// stopped. After a reboot the count starts again: all of it is new.
func TestMemoryAlert(t *testing.T) {
	b := newBox(t, "")
	ctx := context.Background()
	usage := func(kills, cpu uint64) *snapshot.AgentUsage {
		return &snapshot.AgentUsage{Time: t0, OOMKills: kills, CPUUsec: cpu, MemoryMax: 14 << 30}
	}
	memoryAlert(ctx, b.e, nil, usage(3, 10), t0)
	memoryAlert(ctx, b.e, usage(3, 10), usage(3, 20), t0)
	if q := queued(t, b); len(q) != 0 {
		t.Fatalf("nothing new was stopped: %+v", q)
	}
	memoryAlert(ctx, b.e, usage(3, 20), usage(5, 30), t0.Add(2*time.Minute))
	memoryAlert(ctx, b.e, usage(5, 30), usage(1, 4), t0.Add(4*time.Minute))
	q := queued(t, b)
	if len(q) != 2 || q[0].Fields["count"] != "2" || q[1].Fields["count"] != "1" {
		t.Fatalf("events: %+v", q)
	}
	ev := q[0]
	want := "memory ran out and the kernel stopped 2 of the agent's processes. The agent has 14.0 GB of the box's memory (users.agent.limits.memory). What it uses now: keelbox status acme"
	if d := q[1].Fields["detail"]; !strings.HasPrefix(d, "memory ran out and the kernel stopped one of the agent's processes. ") {
		t.Errorf("one process: %q", d)
	}
	if ev.Kind != "out-of-memory" || ev.Level != events.Warn || ev.Fields["detail"] != want {
		t.Errorf("event: %+v\nwant detail %q", ev, want)
	}
}
