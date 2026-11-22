package events

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/keelbox/keelbox/internal/system"
)

// RateLimitPath is root's record of the agent events it let through, so the
// budgets hold across runs.
const RateLimitPath = Dir + "/ratelimit.json"

// Budgets for agent events. Root's own events have none:
// an agent flood never keeps them out.
const (
	// agentRepeat: one event per session and kind in this window.
	agentRepeat = 2 * time.Minute
	// doneAfterInput: a done this soon after a needs-input for the same
	// session adds nothing; the owner is already looking.
	doneAfterInput = 30 * time.Second
	agentPerHour   = 30
	agentPerDay    = 100
	// throttledEvery is how often the owner hears that agent events are
	// being dropped.
	throttledEvery = time.Hour
)

// limits is the rate limit state, 0600 root.
type limits struct {
	// Last is when each key (agent:<session>:<kind>) last got through.
	Last map[string]time.Time `json:"last,omitempty"`
	// Admitted are the times of the agent events let through in the last
	// day, oldest first.
	Admitted []time.Time `json:"admitted,omitempty"`
	// Dropped counts the events dropped by the hourly and daily budgets
	// since the owner was last told.
	Dropped   int       `json:"dropped,omitempty"`
	Throttled time.Time `json:"throttled,omitempty"`
}

func readLimits(x system.Executor) limits {
	var l limits
	if data, err := x.ReadFile(RateLimitPath); err == nil {
		_ = json.Unmarshal(data, &l)
	}
	if l.Last == nil {
		l.Last = map[string]time.Time{}
	}
	return l
}

func (l *limits) save(x system.Executor, now time.Time) error {
	for k, t := range l.Last {
		if now.Sub(t) > time.Hour {
			delete(l.Last, k)
		}
	}
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if err := x.MkdirAll(Dir, 0o700, "root", "root"); err != nil {
		return err
	}
	return x.WriteFile(RateLimitPath, append(b, '\n'), 0o600, "root", "root")
}

// Why an agent event was not let through.
type limitReason string

const (
	limitPass     limitReason = ""
	limitRepeat   limitReason = "repeat"
	limitAnswered limitReason = "after needs-input"
	limitBudget   limitReason = "budget"
)

// admit decides whether the agent event e gets through at now, and counts
// it if so.
func (l *limits) admit(e Event, now time.Time) limitReason {
	session := e.Fields["session"]
	if t, ok := l.Last[e.Key]; ok && now.Sub(t) < agentRepeat {
		return limitRepeat
	}
	if e.Kind == "agent-done" {
		if t, ok := l.Last["agent:"+session+":agent-needs-input"]; ok && now.Sub(t) < doneAfterInput {
			return limitAnswered
		}
	}
	keep := l.Admitted[:0]
	hour := 0
	for _, t := range l.Admitted {
		if now.Sub(t) < 24*time.Hour {
			keep = append(keep, t)
			if now.Sub(t) < time.Hour {
				hour++
			}
		}
	}
	l.Admitted = keep
	if hour >= agentPerHour || len(l.Admitted) >= agentPerDay {
		l.Dropped++
		return limitBudget
	}
	l.Last[e.Key] = now
	l.Admitted = append(l.Admitted, now)
	sort.Slice(l.Admitted, func(i, j int) bool { return l.Admitted[i].Before(l.Admitted[j]) })
	return limitPass
}

// throttled returns the notice that agent events are being dropped, at
// most once an hour, or false.
func (l *limits) throttled(box string, now time.Time) (Event, bool) {
	if l.Dropped == 0 || now.Sub(l.Throttled) < throttledEvery {
		return Event{}, false
	}
	e := Event{Time: now, Source: SourceKeelbox, Kind: "notify-throttled", Level: Warn, Box: box, Key: "notify:throttled",
		Fields: map[string]string{"count": fmt.Sprint(l.Dropped),
			"detail": fmt.Sprintf("more than %d agent events in an hour or %d in a day; the rest are not sent", agentPerHour, agentPerDay)}}
	l.Dropped, l.Throttled = 0, now
	return e, true
}
