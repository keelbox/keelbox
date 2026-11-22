package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/system"
)

var t0 = time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)

func rootEvent(kind string) Event {
	k, _ := Lookup(kind)
	return Event{Time: t0, Source: k.Sources[0], Kind: kind, Level: k.Level, Box: "acme", Key: "test:" + kind}
}

func TestLevels(t *testing.T) {
	for _, l := range []Level{Info, Warn, Urgent} {
		b, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		var back Level
		if err := json.Unmarshal(b, &back); err != nil || back != l {
			t.Errorf("%s round trip: %s %v", l, b, err)
		}
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Error("ParseLevel accepted loud")
	}
	if _, err := json.Marshal(Level(7)); err == nil {
		t.Error("marshalled an invalid level")
	}
}

func TestValidate(t *testing.T) {
	ok := rootEvent("doctor-failed")
	ok.Fields = map[string]string{"check": "host/sshd", "fix": "keelbox apply acme"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := map[string]func(*Event){
		"unknown kind":          func(e *Event) { e.Kind = "doctor-exploded" },
		"agent as root source":  func(e *Event) { e.Source = SourceAgent },
		"level not the kind's":  func(e *Event) { e.Level = Info },
		"field not in schema":   func(e *Event) { e.Fields = map[string]string{"url": "https://x"} },
		"control characters":    func(e *Event) { e.Fields = map[string]string{"detail": "a\x1b[2Jb"} },
		"bad box":               func(e *Event) { e.Box = "Acme Box" },
		"bad key":               func(e *Event) { e.Key = "a b" },
		"no time":               func(e *Event) { e.Time = time.Time{} },
		"multi-line root field": func(e *Event) { e.Fields = map[string]string{"detail": "a\nb"} },
	}
	for name, mutate := range bad {
		e := rootEvent("doctor-failed")
		mutate(&e)
		if err := e.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for _, name := range Kinds() {
		k, _ := Lookup(name)
		if strings.HasPrefix(name, "agent-") != k.FromAgent() {
			t.Errorf("%s: an agent kind must start agent- and only agent kinds may come from the agent", name)
		}
	}
}

func TestQueue(t *testing.T) {
	ctx := context.Background()
	x := system.NewFake()
	if _, err := Emit(x, Event{Kind: "nonsense"}); err == nil {
		t.Error("queued an invalid event")
	}
	for i, k := range []string{"watch-recovered", "doctor-failed", "disk-warn"} {
		e := rootEvent(k)
		e.Time = t0.Add(time.Duration(i) * time.Second)
		if _, err := Emit(x, e); err != nil {
			t.Fatal(err)
		}
	}
	if d := x.Files[QueueDir]; d == nil || d.Mode != 0o700 || x.Files[Dir].Mode != 0o700 {
		t.Error("queue directories are not 0700")
	}
	q, err := Pending(x)
	if err != nil || len(q) != 3 || q[0].Event.Kind != "watch-recovered" || q[2].Event.Kind != "disk-warn" {
		t.Fatalf("pending: %+v %v", q, err)
	}
	if f := x.Files[QueueDir+"/"+q[0].Name]; f.Mode != 0o600 || f.Owner != "root" {
		t.Errorf("queue file %o %s", f.Mode, f.Owner)
	}

	// No channel: everything waits.
	if d, err := Dispatch(ctx, x, nil, t0.Add(time.Minute)); err != nil || d.Waiting != 3 {
		t.Errorf("no senders: %+v %v", d, err)
	}
	// One channel works, one fails: retried later, only on the failing one.
	good, bad := &recorder{name: "good"}, &recorder{name: "bad", err: errors.New("503")}
	d, err := Dispatch(ctx, x, []Sender{good, bad}, t0.Add(time.Minute))
	if err != nil || d.Waiting != 3 || len(good.got) != 3 || len(bad.got) != 3 {
		t.Fatalf("first dispatch: %+v %v good=%d bad=%d", d, err, len(good.got), len(bad.got))
	}
	if d, _ := Dispatch(ctx, x, []Sender{good, bad}, t0.Add(2*time.Minute)); d.Waiting != 3 || len(bad.got) != 3 {
		t.Errorf("retried before the backoff: %+v", d)
	}
	bad.err = nil
	d, err = Dispatch(ctx, x, []Sender{good, bad}, t0.Add(4*time.Minute))
	if err != nil || d.Sent != 3 || len(good.got) != 3 || len(bad.got) != 6 {
		t.Errorf("retry: %+v %v good=%d bad=%d", d, err, len(good.got), len(bad.got))
	}
	if q, _ := Pending(x); len(q) != 0 {
		t.Errorf("sent events still queued: %d", len(q))
	}

	// Expired events are dropped, broken entries removed.
	if _, err := Emit(x, rootEvent("backup-failed")); err != nil {
		t.Fatal(err)
	}
	x.AddFile(QueueDir+"/broken.json", "{", 0o600)
	if d, _ := Dispatch(ctx, x, []Sender{Nop{}}, t0.Add(MaxAge+time.Minute)); d.Dropped != 1 {
		t.Errorf("expiry: %+v", d)
	}
	if names, _ := x.ReadDir(QueueDir); len(names) != 0 {
		t.Errorf("queue left: %v", names)
	}
}

type recorder struct {
	name string
	err  error
	got  []Event
}

func (r *recorder) Name() string { return r.name }
func (r *recorder) Send(_ context.Context, e Event) error {
	r.got = append(r.got, e)
	return r.err
}

func TestBackoff(t *testing.T) {
	for n, want := range map[int]time.Duration{1: 2 * time.Minute, 2: 4 * time.Minute, 3: 8 * time.Minute, 10: time.Hour} {
		if got := backoff(n); got != want {
			t.Errorf("backoff(%d) = %s, want %s", n, got, want)
		}
	}
}

func agentLine(kind string, at time.Time, extra ...string) string {
	l := fmt.Sprintf(`{"v":1,"time":%q,"cli":"claude-code","kind":%q,"session":"0a1b2c3d","project":"shop-api"`, at.Format(time.RFC3339Nano), kind)
	for _, e := range extra {
		l += "," + e
	}
	return l + "}\n"
}

func agentBox(t *testing.T) (*system.Fake, system.User, string) {
	t.Helper()
	x := system.NewFake()
	x.AddUser("agent", 1001)
	u, _ := x.LookupUser("agent")
	return x, u, u.Home + "/" + AgentFile
}

func writeAgent(t *testing.T, x *system.Fake, path, data string) {
	t.Helper()
	if err := x.UserFS("agent").WriteFile(path, []byte(data), 0o600, "agent", ""); err != nil {
		t.Fatal(err)
	}
}

func TestReadAgent(t *testing.T) {
	x, u, path := agentBox(t)
	now := t0.Add(time.Minute)
	if r, err := ReadAgent(x, u, "acme", now); err != nil || len(r.Events) != 0 {
		t.Fatalf("no file: %+v %v", r, err)
	}
	data := agentLine("agent-done", t0) +
		agentLine("doctor-failed", t0.Add(time.Second)) + // a root kind: forged
		agentLine("agent-needs-input", t0.Add(2*time.Second), `"reason":"permission"`) +
		agentLine("agent-done", t0.Add(3*time.Second), `"level":"urgent"`) + // unknown field
		agentLine("agent-done", t0.Add(-2*time.Hour)) + // too old
		agentLine("agent-limit", t0.Add(4*time.Second), `"text":"line\u0007bell"`) + // control character
		"not json\n" +
		agentLine("agent-limit", t0.Add(5*time.Second), `"reason":"rate_limit"`) +
		`{"v":1,"time":"` // incomplete: still being written
	writeAgent(t, x, path, data)
	r, err := ReadAgent(x, u, "acme", now)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range r.Events {
		kinds = append(kinds, e.Kind)
		if e.Source != SourceAgent || !e.Time.Equal(now) || e.Box != "acme" || e.Fields["cli"] != "claude-code" {
			t.Errorf("event %+v", e)
		}
		if k, _ := Lookup(e.Kind); e.Level != k.Level {
			t.Errorf("%s has level %s", e.Kind, e.Level)
		}
	}
	if strings.Join(kinds, ",") != "agent-done,agent-needs-input,agent-limit" || r.Skipped != 5 {
		t.Errorf("events %v, skipped %d, problems %v", kinds, r.Skipped, r.Problems)
	}
	if r.Events[1].Key != "agent:0a1b2c3d:agent-needs-input" || r.Events[1].Fields["reason"] != "permission" {
		t.Errorf("needs-input: %+v", r.Events[1])
	}

	// The rest of the incomplete line arrives: only it is read.
	writeAgent(t, x, path, data+`2026-11-07T09:00:06Z","cli":"codex","kind":"agent-done","session":"0a1b2c3d"}`+"\n")
	r, _ = ReadAgent(x, u, "acme", now)
	if len(r.Events) != 1 || r.Events[0].Fields["cli"] != "codex" {
		t.Errorf("after the line completed: %+v", r)
	}
	if r, _ = ReadAgent(x, u, "acme", now); len(r.Events) != 0 {
		t.Errorf("read the same lines twice: %+v", r.Events)
	}

	// Truncated, with old lines kept: nothing repeats, the new line is read.
	writeAgent(t, x, path, agentLine("agent-limit", t0.Add(5*time.Second), `"reason":"rate_limit"`)+agentLine("agent-error", t0.Add(7*time.Second)))
	r, _ = ReadAgent(x, u, "acme", now)
	if len(r.Events) != 1 || r.Events[0].Kind != "agent-error" {
		t.Errorf("after truncation: %+v", r.Events)
	}
}

func TestReadAgentRefusesALinkToARootFile(t *testing.T) {
	x, u, path := agentBox(t)
	x.AddFile("/etc/shadow", agentLine("agent-done", t0), 0o640)
	x.Files[u.Home+"/"+AgentDir] = &system.FakeFile{Dir: true, Mode: 0o700, Owner: "agent", Group: "agent"}
	x.Files[path] = &system.FakeFile{Target: "/etc/shadow", Owner: "agent", Group: "agent"}
	r, err := ReadAgent(x, u, "acme", t0)
	if err != nil || len(r.Events) != 0 || len(r.Problems) != 1 || !strings.Contains(r.Problems[0], "symbolic link") {
		t.Errorf("link to /etc/shadow: %+v %v", r, err)
	}
}

// What is not a plain regular file of the agent's is not read at all: a
// link even to a file the agent can read, a FIFO swapped in for the file
// (which would block a read), or a file far larger
// than the writer ever leaves.
func TestReadAgentReadsOnlyARegularFile(t *testing.T) {
	x, u, path := agentBox(t)
	x.Files[u.Home+"/"+AgentDir] = &system.FakeFile{Dir: true, Mode: 0o700, Owner: "agent", Group: "agent"}
	x.Files[u.Home+"/own"] = &system.FakeFile{Data: []byte(agentLine("agent-done", t0)), Mode: 0o600, Owner: "agent", Group: "agent"}
	for name, f := range map[string]*system.FakeFile{
		"link to its own file": {Target: u.Home + "/own", Owner: "agent", Group: "agent"},
		"FIFO":                 {Special: true, Mode: 0o600, Owner: "agent", Group: "agent"},
		"oversized":            {Data: []byte(strings.Repeat(agentLine("agent-done", t0), agentReadMax/len(agentLine("agent-done", t0))+1)), Mode: 0o600, Owner: "agent", Group: "agent"},
	} {
		x.Files[path] = f
		r, err := ReadAgent(x, u, "acme", t0)
		if err != nil || len(r.Events) != 0 || len(r.Problems) != 1 {
			t.Errorf("%s: %+v %v", name, r, err)
		}
	}
}

func sessionLine(kind, session string, at time.Time) string {
	return strings.Replace(agentLine(kind, at), `"0a1b2c3d"`, `"`+session+`"`, 1)
}

func TestSyncAgentCapsAFlood(t *testing.T) {
	x, u, path := agentBox(t)
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		b.WriteString(sessionLine("agent-done", fmt.Sprintf("%08x", i), t0.Add(time.Duration(i)*time.Millisecond)))
	}
	writeAgent(t, x, path, b.String())
	now := func() time.Time { return t0.Add(time.Minute) }
	if r, err := ReadAgent(x, u, "acme", now()); err != nil || len(r.Events) != syncMaxEvents {
		t.Errorf("one read took %d events, cap %d (%v)", len(r.Events), syncMaxEvents, err)
	}
	x.Remove(CursorPath)
	r, d, err := SyncAgent(context.Background(), x, u, "acme", AgentOptions{}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Events) != agentPerHour || d.Waiting != agentPerHour+1 {
		t.Errorf("a flood queued %d agent events (%+v), want the hourly budget %d plus one notice", len(r.Events), d, agentPerHour)
	}
	q, _ := Pending(x)
	notices := 0
	for _, e := range q {
		if e.Event.Kind == "notify-throttled" {
			notices++
		}
	}
	if notices != 1 {
		t.Errorf("%d throttle notices, want 1", notices)
	}
	// A second flood in the same hour: nothing more, and no second notice.
	b.Reset()
	for i := 1000; i < 1100; i++ {
		b.WriteString(sessionLine("agent-done", fmt.Sprintf("%08x", i), t0.Add(time.Duration(i)*time.Millisecond)))
	}
	writeAgent(t, x, path, b.String())
	if r, _, _ := SyncAgent(context.Background(), x, u, "acme", AgentOptions{}, nil, now); len(r.Events) != 0 {
		t.Errorf("over budget, still queued %d", len(r.Events))
	}
	if q2, _ := Pending(x); len(q2) != len(q) {
		t.Errorf("queue grew from %d to %d", len(q), len(q2))
	}
	if _, err := Emit(x, rootEvent("watch-gave-up")); err != nil {
		t.Errorf("a full agent budget stopped a root event: %v", err)
	}
}

func TestSyncAgentQueueCap(t *testing.T) {
	x, u, path := agentBox(t)
	for i := 0; i < queueMaxAgent; i++ {
		e := Event{Time: t0, Source: SourceAgent, Kind: "agent-done", Level: Info, Box: "acme", Key: fmt.Sprintf("agent:%08x:agent-done", i)}
		if _, err := Emit(x, e); err != nil {
			t.Fatal(err)
		}
	}
	writeAgent(t, x, path, agentLine("agent-done", t0))
	r, _, err := SyncAgent(context.Background(), x, u, "acme", AgentOptions{}, nil, func() time.Time { return t0.Add(time.Minute) })
	if err != nil || len(r.Events) != 0 || r.Skipped != 1 {
		t.Errorf("a full queue took more: %+v %v", r, err)
	}
}

func TestAgentBudgets(t *testing.T) {
	l := limits{Last: map[string]time.Time{}}
	ev := func(kind, session string) Event {
		return Event{Kind: kind, Key: "agent:" + session + ":" + kind, Fields: map[string]string{"session": session}}
	}
	steps := []struct {
		at   time.Duration
		e    Event
		want limitReason
	}{
		{0, ev("agent-needs-input", "aaaaaaaa"), limitPass},
		{10 * time.Second, ev("agent-needs-input", "aaaaaaaa"), limitRepeat},
		{20 * time.Second, ev("agent-done", "aaaaaaaa"), limitAnswered},
		{20 * time.Second, ev("agent-done", "bbbbbbbb"), limitPass},
		{50 * time.Second, ev("agent-done", "aaaaaaaa"), limitPass},
		{90 * time.Second, ev("agent-done", "bbbbbbbb"), limitRepeat},
		{3 * time.Minute, ev("agent-done", "bbbbbbbb"), limitPass},
	}
	for i, s := range steps {
		if got := l.admit(s.e, t0.Add(s.at)); got != s.want {
			t.Errorf("step %d (%s %s at %s): %q, want %q", i, s.e.Kind, s.e.Fields["session"], s.at, got, s.want)
		}
	}
	// The daily budget: 100 spread over the day, then nothing until the
	// oldest is a day old.
	l = limits{Last: map[string]time.Time{}}
	for i := 0; i < agentPerDay; i++ {
		if got := l.admit(ev("agent-done", fmt.Sprintf("%08x", i)), t0.Add(time.Duration(i)*10*time.Minute)); got != limitPass {
			t.Fatalf("event %d: %q", i, got)
		}
	}
	day := t0.Add(time.Duration(agentPerDay-1) * 10 * time.Minute)
	if got := l.admit(ev("agent-done", "ffffffff"), day.Add(time.Minute)); got != limitBudget {
		t.Errorf("over the daily budget: %q", got)
	}
	if got := l.admit(ev("agent-done", "fffffffe"), t0.Add(24*time.Hour+time.Second)); got != limitPass {
		t.Errorf("a day later: %q", got)
	}
}

func TestSyncAgentDropsTextUnlessAsked(t *testing.T) {
	x, u, path := agentBox(t)
	writeAgent(t, x, path, agentLine("agent-needs-input", t0, `"text":"Allow rm -rf?"`))
	now := func() time.Time { return t0.Add(time.Minute) }
	r, _, err := SyncAgent(context.Background(), x, u, "acme", AgentOptions{}, nil, now)
	if err != nil || len(r.Events) != 1 || r.Events[0].Fields["text"] != "" {
		t.Errorf("text kept without notify.agent_text: %+v %v", r.Events, err)
	}
	writeAgent(t, x, path, agentLine("agent-needs-input", t0, `"text":"Allow rm -rf?"`)+
		strings.Replace(agentLine("agent-needs-input", t0.Add(time.Second), `"text":"Allow rm -rf?"`), "0a1b2c3d", "11111111", 1))
	r, _, _ = SyncAgent(context.Background(), x, u, "acme", AgentOptions{Text: true}, nil, now)
	if len(r.Events) != 1 || r.Events[0].Fields["text"] != "Allow rm -rf?" {
		t.Errorf("text dropped with notify.agent_text: %+v", r.Events)
	}
}

func TestDispatchHoldsAndDigests(t *testing.T) {
	ctx := context.Background()
	x := system.NewFake()
	for i, k := range []string{"agent-done", "disk-warn"} {
		e := rootEvent(k)
		if k == "agent-done" {
			e.Source, e.Key = SourceAgent, "agent:0a1b2c3d:agent-done"
		}
		e.Time = t0.Add(time.Duration(i) * time.Second)
		if _, err := Emit(x, e); err != nil {
			t.Fatal(err)
		}
	}
	until := t0.Add(8 * time.Hour)
	quiet := &holder{recorder: recorder{name: "phone"}, until: until}
	d, err := Dispatch(ctx, x, []Sender{quiet}, t0.Add(time.Minute))
	if err != nil || d.Waiting != 2 || len(quiet.got) != 2 {
		t.Fatalf("quiet hours: %+v %v", d, err)
	}
	q, _ := Pending(x)
	if !q[0].Held || !q[0].Next.Equal(until) || q[0].Attempts != 0 {
		t.Errorf("held event: %+v", q[0])
	}
	if d, _ := Dispatch(ctx, x, []Sender{quiet}, until.Add(-time.Minute)); d.Waiting != 2 {
		t.Errorf("sent before quiet hours ended: %+v", d)
	}
	quiet.until = time.Time{}
	d, err = Dispatch(ctx, x, []Sender{quiet}, until)
	if err != nil || d.Sent != 2 || len(quiet.digests) != 1 || len(quiet.digests[0]) != 2 || len(quiet.got) != 2 {
		t.Errorf("after quiet hours: %+v %v digests=%v sends=%d", d, err, quiet.digests, len(quiet.got))
	}
}

// holder holds every event until until (when set) and takes digests.
type holder struct {
	recorder
	until   time.Time
	digests [][]Event
}

func (h *holder) Send(ctx context.Context, e Event) error {
	h.got = append(h.got, e)
	if !h.until.IsZero() {
		return &HoldError{Until: h.until}
	}
	return nil
}

func (h *holder) SendDigest(_ context.Context, es []Event) error {
	h.digests = append(h.digests, es)
	return nil
}

func TestAppendAgent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "keelbox", "events.jsonl")
	l := AgentLine{V: 1, Time: t0, CLI: "claude-code", Kind: "agent-done", Session: "0a1b2c3d"}
	if err := AppendAgent(path, l); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("events file: %v %v", st, err)
	}
	// Past the cap the file keeps only its newest lines, from a line start.
	big := strings.Repeat(agentLine("agent-done", t0), agentFileMax/len(agentLine("agent-done", t0))+10)
	if err := os.WriteFile(path, []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	l.Kind = "agent-limit"
	if err := AppendAgent(path, l); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if len(data) > agentFileKeep+agentLineMax || !strings.HasPrefix(string(data), `{"v":1`) || !strings.Contains(string(data), "agent-limit") {
		t.Errorf("after trimming: %d bytes, starts %q", len(data), data[:20])
	}
	l.Text = strings.Repeat("x", agentLineMax)
	if err := AppendAgent(path, l); err == nil {
		t.Error("wrote a line longer than the reader takes")
	}
}

// An agent that rewrites its events file without pause starts one events
// run per RunPace at most, and a change that added no
// bytes past the cursor needs no run.
func TestPaceAndNothingNew(t *testing.T) {
	x, u, path := agentBox(t)
	if w := Pace(x, t0); w != 0 {
		t.Errorf("first run waits %s", w)
	}
	if w := Pace(x, t0.Add(time.Second)); w != RunPace-time.Second {
		t.Errorf("a run a second later waits %s", w)
	}
	// That run starts at t0+5s; one right after it waits a whole pace.
	if w := Pace(x, t0.Add(5*time.Second)); w != RunPace {
		t.Errorf("a run at the next start waits %s", w)
	}
	if w := Pace(x, t0.Add(time.Hour)); w != 0 {
		t.Errorf("a run an hour later waits %s", w)
	}
	if !NothingNew(x, u) {
		t.Error("no file and no cursor: something new")
	}
	writeAgent(t, x, path, agentLine("agent-done", t0))
	if NothingNew(x, u) {
		t.Error("a new line: nothing new")
	}
	if _, err := ReadAgent(x, u, "acme", t0); err != nil {
		t.Fatal(err)
	}
	if !NothingNew(x, u) {
		t.Error("everything read: something new")
	}
}

// A keelbox report the agent filed goes to the caller for the inbox: it
// never becomes an event, so nothing is queued or sent (ADR 0062).
func TestSyncAgentKeepsReportsOutOfTheQueue(t *testing.T) {
	x, u, path := agentBox(t)
	report := fmt.Sprintf(`{"v":1,"time":%q,"cli":"keelbox","kind":%q,"session":"0a1b2c3d","title":"expose fails","area":"expose","detail":"exact error"}`+"\n",
		t0.Add(time.Second).Format(time.RFC3339Nano), ReportKind)
	writeAgent(t, x, path, agentLine("agent-done", t0)+report)
	rec := &recorder{name: "phone"}
	r, _, err := SyncAgent(context.Background(), x, u, "acme", AgentOptions{Text: true}, []Sender{rec}, func() time.Time { return t0.Add(time.Minute) })
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Reports) != 1 || r.Reports[0].Title != "expose fails" || r.Reports[0].Detail != "exact error" || len(r.Events) != 1 {
		t.Fatalf("read %+v", r)
	}
	for _, e := range rec.got {
		if e.Kind != "agent-done" {
			t.Errorf("sent %s for a report", e.Kind)
		}
	}
	if q, _ := Pending(x); len(q) > 1 {
		t.Errorf("queued %d events", len(q))
	}
	// Read once only.
	if r, _, _ := SyncAgent(context.Background(), x, u, "acme", AgentOptions{}, nil, func() time.Time { return t0.Add(time.Minute) }); len(r.Reports) != 0 {
		t.Errorf("report read twice: %+v", r.Reports)
	}
}
