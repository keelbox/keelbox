package events

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/system"
)

// The agent's events file, relative to its home. The agent writes it
// (keelbox event, run by its CLIs' hooks); keelbox-events.path watches it.
const (
	AgentDir  = ".local/state/keelbox"
	AgentFile = AgentDir + "/events.jsonl"
)

// Limits on the agent's file.
const (
	// agentLineMax is the longest line read; a longer one is dropped.
	agentLineMax = 4 << 10
	// agentFileMax is the size at which the writer keeps only its newest
	// agentFileKeep bytes, so the file never grows without end.
	agentFileMax  = 1 << 20
	agentFileKeep = 256 << 10
	// agentReadMax is the largest file root reads: the writer keeps it
	// near agentFileMax, so a larger one is the agent's doing and is not
	// read at all.
	agentReadMax = 2 * agentFileMax
	// syncMaxEvents is the most events one read turns into events, and
	// queueMaxAgent the most agent events the queue holds; the rest of a
	// flood is skipped, so the agent cannot fill root's disk through the
	// queue. Per-session and hourly budgets are the notification layer's.
	syncMaxEvents = 100
	queueMaxAgent = 200
	// agentMaxAge: older lines are skipped, so a file restored from a
	// backup, or rewritten, does not replay old events.
	agentMaxAge = time.Hour
	// agentMaxAhead allows a little clock difference the other way.
	agentMaxAhead = 5 * time.Minute
)

// AgentLine is one line of the agent's events file. Every field is checked
// again by root before it becomes an event; nothing else in the line is
// kept.
type AgentLine struct {
	V       int       `json:"v"`
	Time    time.Time `json:"time"`
	CLI     string    `json:"cli"`
	Kind    string    `json:"kind"`
	Session string    `json:"session"`
	Project string    `json:"project,omitempty"`
	Reason  string    `json:"reason,omitempty"`
	Text    string    `json:"text,omitempty"`
	// A keelbox report's fields (Kind ReportKind, Session its ID).
	Title  string `json:"title,omitempty"`
	Area   string `json:"area,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// ReportKind marks a line that is a keelbox report the agent filed: it
// goes to the box's report inbox, never to the event queue or a channel
// (ADR 0062).
const ReportKind = "keelbox-report"

// Event turns a line into the event it stands for, or says why it cannot
// be one. The kind must be an agent kind; its level is the kind's, not the
// agent's choice; the source is always the agent.
func (l AgentLine) Event(box string, now time.Time) (Event, error) {
	if l.V != 1 {
		return Event{}, fmt.Errorf("version %d is not 1", l.V)
	}
	k, ok := Lookup(l.Kind)
	if !ok || !k.FromAgent() {
		return Event{}, fmt.Errorf("kind %q is not an agent event", l.Kind)
	}
	if l.Time.Before(now.Add(-agentMaxAge)) || l.Time.After(now.Add(agentMaxAhead)) {
		return Event{}, fmt.Errorf("time %s is not within the last hour", l.Time.Format(time.RFC3339))
	}
	fields := map[string]string{"cli": l.CLI, "session": l.Session}
	for name, v := range map[string]string{"project": l.Project, "reason": l.Reason, "text": l.Text} {
		if v != "" {
			fields[name] = v
		}
	}
	e := Event{Time: now, Source: SourceAgent, Kind: l.Kind, Level: k.Level, Box: box,
		Key: "agent:" + l.Session + ":" + l.Kind, Fields: fields}
	if err := e.Validate(); err != nil {
		return Event{}, err
	}
	return e, nil
}

// AppendAgent adds a line to the events file at path. It runs as the agent
// (keelbox event), so it uses the agent's own file access. Past
// agentFileMax it rewrites the file with only its newest lines.
func AppendAgent(path string, l AgentLine) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if len(b)+1 > agentLineMax {
		return fmt.Errorf("event line is longer than %d bytes", agentLineMax)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if st, err := os.Stat(path); err == nil && st.Size() > agentFileMax {
		if err := keepTail(path); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// keepTail replaces path with its last agentFileKeep bytes, from a line
// start.
func keepTail(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	tail := data[len(data)-min(len(data), agentFileKeep):]
	if i := bytes.IndexByte(tail, '\n'); i >= 0 {
		tail = tail[i+1:]
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, tail, 0o600); err != nil { //nolint:gosec // G703: the agent's own file, written as the agent
		return err
	}
	return os.Rename(tmp, path)
}

// Cursor is how far root has read the agent's file. Head is a hash of the
// file's first bytes: a file that was truncated or replaced no longer
// matches, and is read from the start. Last is the newest line time taken,
// so reading a replaced file again does not repeat events.
type Cursor struct {
	Offset int64     `json:"offset"`
	Head   string    `json:"head"`
	Last   time.Time `json:"last"`
}

const headLen = 64

func head(data []byte) string {
	s := sha256.Sum256(data[:min(len(data), headLen)])
	return hex.EncodeToString(s[:])
}

// ReadCursor reads root's cursor; a missing or broken one starts over.
func ReadCursor(x system.Executor) Cursor {
	var c Cursor
	if data, err := x.ReadFile(CursorPath); err == nil {
		_ = json.Unmarshal(data, &c)
	}
	return c
}

// RunPace is the least time between the starts of two events runs. The
// agent changes its file whenever it likes, and each change can start
// keelbox-events.service (its path unit has no start limit, ADR 0030), so
// without it a file rewritten without pause costs root a run after run.
const RunPace = 5 * time.Second

// runMark holds the start time of the last events run.
const runMark = Dir + "/events-run"

// Pace records an events run starting at now and returns how long it waits
// first: what is left of RunPace since the last start, at most RunPace.
func Pace(x system.Executor, now time.Time) time.Duration {
	var wait time.Duration
	if data, err := x.ReadFile(runMark); err == nil {
		if last, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data))); err == nil {
			if d := RunPace - now.Sub(last); d > 0 {
				wait = min(d, RunPace)
			}
		}
	}
	if err := x.MkdirAll(Dir, 0o700, "root", "root"); err == nil {
		_ = x.WriteFile(runMark, []byte(now.Add(wait).Format(time.RFC3339Nano)+"\n"), 0o600, "root", "root")
	}
	return wait
}

// NothingNew reports whether the agent's file holds nothing past root's
// cursor, from its size alone (one stat as the agent): such a run has
// nothing to read and need not wait for the day-two lock.
func NothingNew(x system.Executor, agent system.User) bool {
	st, err := x.UserFS(agent.Name).Stat(agent.Home + "/" + AgentFile)
	if err != nil {
		return false
	}
	cur := ReadCursor(x)
	return !st.Exists && cur.Offset == 0 || st.Exists && !st.IsSymlink && st.Size == cur.Offset
}

// AgentRead is what one read of the agent's file found.
type AgentRead struct {
	Events []Event
	// Problems describe skipped lines, a few at most, for the journal.
	Problems []string
	// Skipped counts lines that did not become events.
	Skipped int
	// Reports are the keelbox report lines read, still unchecked: the
	// caller checks each and files it in the inbox, or drops it.
	Reports []AgentLine
}

// ReadAgent reads the agent's events file as the agent, from the cursor,
// and returns the events its complete new lines stand for. The file is
// read with the agent's rights, only as a regular file that is not a link
// and opens without blocking (system.ReadData), at most agentReadMax, and
// never trusted: each line must pass AgentLine.Event. The cursor is saved
// after the read.
func ReadAgent(x system.Executor, agent system.User, box string, now time.Time) (AgentRead, error) {
	var r AgentRead
	path := agent.Home + "/" + AgentFile
	cur := ReadCursor(x)
	data, err := system.ReadData(x.UserFS(agent.Name), path, agentReadMax)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return r, nil
	case err != nil:
		r.Problems = append(r.Problems, "cannot read "+path+" as "+agent.Name+": "+err.Error())
		return r, nil
	}
	if int64(len(data)) < cur.Offset || (cur.Offset > 0 && head(data[:min(int64(len(data)), cur.Offset)]) != cur.Head) {
		cur.Offset = 0 // truncated or replaced: read it again from the start
	}
	rest := data[cur.Offset:]
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			break // an incomplete line: the agent is still writing it
		}
		line := rest[:i]
		rest = rest[i+1:]
		cur.Offset += int64(i + 1)
		if l, last, err := reportLine(line, now, cur.Last); err == nil {
			if len(r.Reports) < syncMaxEvents {
				cur.Last = last
				r.Reports = append(r.Reports, l)
			}
			continue
		}
		ev, last, err := agentEvent(line, box, now, cur.Last)
		if err == nil && len(r.Events) >= syncMaxEvents {
			err = fmt.Errorf("more than %d events in one read", syncMaxEvents)
		}
		if err != nil {
			r.Skipped++
			if len(r.Problems) < 5 {
				r.Problems = append(r.Problems, "skipped a line of "+path+": "+err.Error())
			}
			continue
		}
		cur.Last = last
		r.Events = append(r.Events, ev)
	}
	cur.Head = head(data[:cur.Offset])
	b, err := json.Marshal(cur)
	if err != nil {
		return r, err
	}
	if err := x.MkdirAll(Dir, 0o700, "root", "root"); err != nil {
		return r, err
	}
	return r, x.WriteFile(CursorPath, append(b, '\n'), 0o600, "root", "root")
}

// reportLine is a line that is a keelbox report, new and within the hour;
// an error for any other line.
func reportLine(line []byte, now, last time.Time) (AgentLine, time.Time, error) {
	var l AgentLine
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if len(line)+1 > agentLineMax || dec.Decode(&l) != nil || l.V != 1 || l.Kind != ReportKind {
		return l, last, errors.New("not a report line")
	}
	if !l.Time.After(last) || l.Time.Before(now.Add(-agentMaxAge)) || l.Time.After(now.Add(agentMaxAhead)) {
		return l, last, errors.New("not a new report line")
	}
	return l, l.Time, nil
}

func agentEvent(line []byte, box string, now, last time.Time) (Event, time.Time, error) {
	if len(line)+1 > agentLineMax {
		return Event{}, last, fmt.Errorf("line longer than %d bytes", agentLineMax)
	}
	var l AgentLine
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return Event{}, last, errors.New("not an event line")
	}
	if !l.Time.After(last) {
		return Event{}, last, errors.New("not newer than the last event read")
	}
	e, err := l.Event(box, now)
	if err != nil {
		return Event{}, last, err
	}
	return e, l.Time, nil
}

// AgentOptions are the owner's choices for agent events.
type AgentOptions struct {
	// Text keeps the agent's own notification text (notify.agent_text).
	// Without it a text field in a line is dropped, whatever the agent
	// wrote.
	Text bool
}

// SyncAgent queues the agent's new events and then sends what is due. It
// reads again while the last read found lines, so lines the agent wrote
// while it ran are not left for the next run, up to a few rounds. Each
// event must also pass the agent budgets: one per session and kind every
// two minutes, no done right after a needs-input, at most 30 an hour and
// 100 a day, with one notice an hour while the budget drops events.
func SyncAgent(ctx context.Context, x system.Executor, agent system.User, box string, opts AgentOptions, senders []Sender, now func() time.Time) (AgentRead, Delivery, error) {
	var total AgentRead
	queued, err := Pending(x)
	if err != nil {
		return total, Delivery{}, err
	}
	room := queueMaxAgent
	for _, q := range queued {
		if q.Event.Source == SourceAgent {
			room--
		}
	}
	lim := readLimits(x)
	for round := 0; round < 5; round++ {
		r, err := ReadAgent(x, agent, box, now())
		total.Problems = append(total.Problems, r.Problems...)
		total.Skipped += r.Skipped
		total.Reports = append(total.Reports, r.Reports...)
		if err != nil {
			return total, Delivery{}, err
		}
		for _, e := range r.Events {
			if !opts.Text {
				delete(e.Fields, "text")
			}
			if room <= 0 || lim.admit(e, e.Time) != limitPass {
				total.Skipped++
				continue
			}
			if _, err := Emit(x, e); err != nil {
				return total, Delivery{}, err
			}
			room--
			total.Events = append(total.Events, e)
		}
		if len(r.Events) == 0 && r.Skipped == 0 && len(r.Reports) == 0 {
			break
		}
	}
	if e, ok := lim.throttled(box, now()); ok {
		if _, err := Emit(x, e); err != nil {
			return total, Delivery{}, err
		}
	}
	if err := lim.save(x, now()); err != nil {
		return total, Delivery{}, err
	}
	d, err := Dispatch(ctx, x, senders, now())
	return total, d, err
}
