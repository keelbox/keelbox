package watch

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/agentlimits"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/system"
)

// killedRe reads the kernel's line for a process its OOM killer stopped:
//
//	Out of memory: Killed process 812 (node) total-vm:...kB, anon-rss:3614976kB, ... UID:1002 ...
//
// The line for a kill at a cgroup's ceiling starts "Memory cgroup out of
// memory" and is otherwise the same.
var killedRe = regexp.MustCompile(`Killed process \d+ \(([^)]*)\).*?anon-rss:(\d+)kB.*?UID:(\d+)`)

// killedNamed is how many stopped processes an event names.
const killedNamed = 4

// memoryAlert raises out-of-memory when the kernel stopped processes of
// the agent's since the run before: the count is the agent slice's own
// (memory.events), which counts every kill in it, at the agent's ceiling
// or because the whole box ran out. The names are the kernel log's, where
// root can read them; an event without names still says how many.
func memoryAlert(ctx context.Context, e *engine.Env, prev, cur *snapshot.AgentUsage, now time.Time) {
	if prev == nil || cur == nil {
		return // the first run has nothing to compare with
	}
	n := cur.OOMKills
	if cur.OOMKills >= prev.OOMKills && cur.CPUUsec >= prev.CPUUsec {
		n -= prev.OOMKills // the same slice as before: what is new
	}
	if n == 0 {
		return
	}
	detail := "memory ran out and the kernel stopped one of the agent's processes"
	if n > 1 {
		detail = fmt.Sprintf("memory ran out and the kernel stopped %d of the agent's processes", n)
	}
	names := killed(ctx, e, prev.Time)
	if len(names) > 0 {
		detail += ": " + strings.Join(names, ", ")
	}
	detail += "."
	if cur.MemoryMax >= 0 {
		detail += " The agent has " + agentlimits.Size(cur.MemoryMax) + " of the box's memory (users.agent.limits.memory)."
	}
	detail += " What it uses now: keelbox status " + e.Cfg.Box
	k, _ := events.Lookup("out-of-memory")
	ev := events.Event{Time: now, Source: events.SourceMemory, Kind: k.Name, Level: k.Level, Box: e.Cfg.Box, Key: "memory",
		Fields: map[string]string{"count": strconv.FormatUint(n, 10), "detail": detail}}
	if len(names) > 0 {
		ev.Fields["processes"] = strings.Join(names, ", ")
	}
	if _, err := events.Emit(e.X, ev); err != nil && e.Log != nil {
		fmt.Fprintf(e.Log, "keelbox watch-run: event %s: %v\n", k.Name, err)
	}
}

// killed names the agent's processes the kernel stopped since a time, with
// the memory each held, largest first as the kernel chose them: "node (3.4
// GB)". A process's name is its own to set, so anything but letters,
// digits and a few marks becomes "?".
func killed(ctx context.Context, e *engine.Env, since time.Time) []string {
	u := e.AgentUser()
	if u.UID < 0 {
		return nil
	}
	out, err := bounded(ctx, e.X, system.C("journalctl", "-k", "-o", "cat", "--no-pager", "--since", "@"+strconv.FormatInt(since.Unix(), 10), "-g", "Killed process"))
	if err != nil {
		return nil
	}
	return killedNames(out, u.UID)
}

func killedNames(log string, uid int) []string {
	var names []string
	more := 0
	for _, l := range strings.Split(log, "\n") {
		m := killedRe.FindStringSubmatch(l)
		if m == nil || m[3] != strconv.Itoa(uid) {
			continue
		}
		if len(names) == killedNamed {
			more++
			continue
		}
		kb, _ := strconv.ParseInt(m[2], 10, 64)
		names = append(names, plainName(m[1])+" ("+agentlimits.Size(kb<<10)+")")
	}
	if more > 0 {
		names = append(names, fmt.Sprintf("%d more", more))
	}
	return names
}

func plainName(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-', c == '+', c == ':':
		default:
			b[i] = '?'
		}
	}
	if len(b) == 0 {
		return "?"
	}
	return string(b)
}
