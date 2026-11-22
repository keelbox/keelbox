// Package runs is the box's record of applies that run detached from the
// laptop that started them (docs/adr/0063-detached-apply.md). Each run is a
// transient root systemd unit, keelbox-apply-<id>.service, and writes one
// JSON line per event to /var/lib/keelbox/runs/<id>.jsonl: the run's start,
// each module and step as apply logs it, each module's tally, every other
// line of output, and the result. The file is root's, readable by the sudo
// group (accounts that can become root anyway), so a laptop can follow it
// over SSH as the account it logs in as. current names the newest run.
//
// The log carries apply's own output, which already holds no secrets
// (AGENTS.md), escaped with plain.Text and bounded in size.
package runs

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/keelbox/keelbox/internal/plain"
	"github.com/keelbox/keelbox/internal/system"
)

// Paths, ownership and limits of the run logs.
const (
	Dir         = "/var/lib/keelbox/runs"
	CurrentPath = Dir + "/current"
	// Group may read the logs: on Ubuntu the accounts with sudo, which
	// includes keelbox's admin and never the agent.
	Group = "sudo"
	// Keep is how many run logs stay; older ones are removed when a run
	// starts.
	Keep = 10
	// MaxBytes bounds a log. Past it, output lines are dropped (one record
	// says so); the run's tallies and its result are always written.
	MaxBytes = 4 << 20
	// MaxText bounds one line of output, in runes.
	MaxText = 2000
	// UnitPrefix starts the name of a run's transient unit.
	UnitPrefix = "keelbox-apply-"
)

// Kinds of record.
const (
	KindStart      = "start"       // ID, Box, Keelbox, By, Modules
	KindModule     = "module"      // a module started: Module
	KindStep       = "step"        // Module, Step, Word (ok, change, defer, FAILED, skip), Detail
	KindModuleDone = "module-done" // a module's tally: Module, Steps, Changed, Failed
	KindOut        = "out"         // a line of output: Text
	KindErr        = "err"         // a line of error output: Text
	KindEnd        = "end"         // the result: Result, Code, Steps, Changed, Failed, Seconds, Error
)

// Results of a run, as the logbook records an apply.
const (
	ResultOK          = "ok"
	ResultPartial     = "partial"
	ResultFailed      = "failed"
	ResultInterrupted = "interrupted"
)

// Record is one line of a run log.
type Record struct {
	Time    time.Time `json:"time"`
	Kind    string    `json:"kind"`
	ID      string    `json:"id,omitempty"`
	Box     string    `json:"box,omitempty"`
	Keelbox string    `json:"keelbox,omitempty"`
	By      string    `json:"by,omitempty"` // the account that started the run
	Modules []string  `json:"modules,omitempty"`
	Module  string    `json:"module,omitempty"`
	Step    string    `json:"step,omitempty"`
	Word    string    `json:"word,omitempty"`
	Detail  string    `json:"detail,omitempty"`
	Text    string    `json:"text,omitempty"`
	Steps   int       `json:"steps,omitempty"`
	Changed int       `json:"changed,omitempty"`
	Failed  int       `json:"failed,omitempty"`
	Result  string    `json:"result,omitempty"`
	Code    int       `json:"code,omitempty"`
	Seconds float64   `json:"seconds,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// Line is the record as apply's own log writes it, and whether it belongs
// on the error stream; ok is false for records that are not a line.
// Rendering a run's records gives the text an attached apply prints, so the
// progress view and plain output read a followed run as they read an
// apply's own output.
func (r Record) Line() (line string, stderr, ok bool) {
	switch r.Kind {
	case KindModule:
		return r.Module, false, true
	case KindStep:
		l := fmt.Sprintf("  %-7s %s", r.Word, r.Step)
		if r.Detail != "" {
			l += ": " + r.Detail
		}
		return l, false, true
	case KindOut:
		return r.Text, false, true
	case KindErr:
		return r.Text, true, true
	}
	return "", false, false
}

var idRe = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9a-f]{4}$`)

// ValidID reports whether id is a run id: it is used in paths, unit names
// and remote commands, so nothing else is accepted.
func ValidID(id string) bool { return idRe.MatchString(id) }

// NewID is a run id: the UTC start time, sortable, and a random suffix.
func NewID(now time.Time) (string, error) {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:]), nil
}

// Path is a run's log.
func Path(id string) string { return Dir + "/" + id + ".jsonl" }

// Unit is a run's transient systemd unit.
func Unit(id string) string { return UnitPrefix + id + ".service" }

// Log writes one run's records.
type Log struct {
	mu      sync.Mutex
	x       system.Executor
	id      string
	size    int64
	full    bool
	modules map[string]bool
	cur     *Record // the running module's tally (KindModuleDone)
	out     []byte  // partial lines
	err     []byte
	ended   bool
	Now     func() time.Time
}

// Begin starts a run's log with its start record, points current at it and
// removes all but the newest Keep logs.
func Begin(x system.Executor, start Record) (*Log, error) {
	if !ValidID(start.ID) {
		return nil, fmt.Errorf("run id %q is not valid", start.ID)
	}
	// Before a box's first apply, /var/lib/keelbox may be 0700 (it was
	// made for the stage); the account that follows needs to pass through
	// it. The logbook module makes it 0755 too.
	if err := x.MkdirAll(filepath.Dir(Dir), 0o755, "root", "root"); err != nil {
		return nil, err
	}
	if err := x.MkdirAll(Dir, 0o750, "root", Group); err != nil {
		return nil, err
	}
	prune(x, start.ID)
	start.Kind = KindStart
	l := newLog(x, start.ID, start.Modules)
	if start.Time.IsZero() {
		start.Time = l.Now().UTC()
	}
	if err := l.write(start); err != nil {
		return nil, err
	}
	if err := x.WriteFile(CurrentPath, []byte(start.ID+"\n"), 0o640, "root", Group); err != nil {
		return nil, err
	}
	return l, nil
}

// Resume opens a run's log, which Begin started, to carry on writing it.
func Resume(x system.Executor, id string) (*Log, error) {
	recs, size, err := read(x, id)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 || recs[0].Kind != KindStart {
		return nil, fmt.Errorf("%s has no start record", Path(id))
	}
	l := newLog(x, id, recs[0].Modules)
	l.size = size
	return l, nil
}

func newLog(x system.Executor, id string, modules []string) *Log {
	l := &Log{x: x, id: id, modules: map[string]bool{}, Now: time.Now}
	for _, m := range modules {
		l.modules[m] = true
	}
	return l
}

// ID is the run's id.
func (l *Log) ID() string { return l.id }

// Stdout is a writer for apply's output: module and step lines become
// their records, other lines out records.
func (l *Log) Stdout() io.Writer { return lineWriter{l, false} }

// Stderr is a writer for apply's error output: err records.
func (l *Log) Stderr() io.Writer { return lineWriter{l, true} }

type lineWriter struct {
	l      *Log
	stderr bool
}

func (w lineWriter) Write(b []byte) (int, error) {
	l := w.l
	l.mu.Lock()
	defer l.mu.Unlock()
	buf := &l.out
	if w.stderr {
		buf = &l.err
	}
	*buf = append(*buf, b...)
	for {
		i := bytes.IndexByte(*buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string((*buf)[:i]), "\r")
		*buf = (*buf)[i+1:]
		if err := l.line(line, w.stderr); err != nil {
			return len(b), err
		}
	}
	return len(b), nil
}

var stepRe = regexp.MustCompile(`^  (ok|change|defer|FAILED|skip) +(\S+)(?:: (.*))?$`)

// line records one line of output.
func (l *Log) line(s string, stderr bool) error {
	now := l.Now().UTC()
	if stderr {
		return l.write(Record{Time: now, Kind: KindErr, Text: s})
	}
	if l.modules[s] {
		if err := l.closeModule(now); err != nil {
			return err
		}
		l.cur = &Record{Kind: KindModuleDone, Module: s}
		return l.write(Record{Time: now, Kind: KindModule, Module: s})
	}
	if m := stepRe.FindStringSubmatch(s); m != nil && l.cur != nil {
		l.cur.Steps++
		switch m[1] {
		case "change":
			l.cur.Changed++
		case "FAILED", "skip":
			l.cur.Failed++
		}
		return l.write(Record{Time: now, Kind: KindStep, Module: l.cur.Module, Word: m[1], Step: m[2], Detail: m[3]})
	}
	return l.write(Record{Time: now, Kind: KindOut, Text: s})
}

func (l *Log) closeModule(now time.Time) error {
	if l.cur == nil {
		return nil
	}
	r := *l.cur
	l.cur = nil
	r.Time = now
	return l.write(r)
}

// End writes what is left of a partial line, the last module's tally and
// the end record. A second End does nothing.
func (l *Log) End(end Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ended {
		return nil
	}
	l.ended = true
	now := l.Now().UTC()
	for _, p := range []struct {
		b      *[]byte
		stderr bool
	}{{&l.out, false}, {&l.err, true}} {
		if len(*p.b) > 0 {
			s := string(*p.b)
			*p.b = nil
			if err := l.line(s, p.stderr); err != nil {
				return err
			}
		}
	}
	if err := l.closeModule(now); err != nil {
		return err
	}
	end.Kind = KindEnd
	if end.Time.IsZero() {
		end.Time = now
	}
	return l.write(end)
}

// Ended reports whether End has run.
func (l *Log) Ended() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.ended
}

// write appends one record. Text is escaped and cut to MaxText; past
// MaxBytes only tallies and the result are written.
func (l *Log) write(r Record) error {
	r.Text = clip(plain.Text(r.Text))
	r.Detail = clip(plain.Text(r.Detail))
	r.Error = clip(plain.Text(r.Error))
	small := r.Kind == KindStart || r.Kind == KindModuleDone || r.Kind == KindEnd
	if l.full && !small {
		return nil
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if !small && l.size+int64(len(b))+1 > MaxBytes {
		l.full = true
		b, _ = json.Marshal(Record{Time: r.Time, Kind: KindErr, Text: fmt.Sprintf("keelbox: this run's log reached %d MiB; later output is in the journal: journalctl -u %s", MaxBytes>>20, Unit(l.id))})
	}
	b = append(b, '\n')
	if err := l.x.AppendFile(Path(l.id), b, 0o640, "root", Group); err != nil {
		return err
	}
	l.size += int64(len(b))
	return nil
}

func clip(s string) string {
	if r := []rune(s); len(r) > MaxText {
		return string(r[:MaxText-1]) + "…"
	}
	return s
}

// prune removes run logs beyond the newest Keep, counting the one about
// to start. Ids sort by start time.
func prune(x system.Executor, starting string) {
	names, err := x.ReadDir(Dir)
	if err != nil {
		return
	}
	var ids []string
	for _, n := range names {
		if id, ok := strings.CutSuffix(n, ".jsonl"); ok && ValidID(id) && id != starting {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for len(ids) > Keep-1 {
		_ = x.Remove(Path(ids[0]))
		ids = ids[1:]
	}
}

// Parse reads records from a log's text. A last line without its newline
// is left out (it is still being written); any other line that does not
// parse is an error.
func Parse(data []byte) ([]Record, error) {
	var out []Record
	lines := bytes.Split(data, []byte("\n"))
	for i, l := range lines {
		if i == len(lines)-1 || len(bytes.TrimSpace(l)) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(l, &r); err != nil {
			return out, fmt.Errorf("run log line %d: %w", i+1, err)
		}
		out = append(out, r)
	}
	return out, nil
}

func read(x system.Executor, id string) ([]Record, int64, error) {
	if !ValidID(id) {
		return nil, 0, fmt.Errorf("run id %q is not valid", id)
	}
	data, err := x.ReadFile(Path(id))
	if err != nil {
		return nil, 0, err
	}
	recs, err := Parse(data)
	return recs, int64(len(data)), err
}

// Read returns a run's records.
func Read(x system.Executor, id string) ([]Record, error) {
	recs, _, err := read(x, id)
	return recs, err
}

// Current is the newest run's id; "" when no run was ever started.
func Current(x system.Executor) (string, error) {
	b, err := x.ReadFile(CurrentPath)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if !ValidID(id) {
		return "", fmt.Errorf("%s does not name a run", CurrentPath)
	}
	return id, nil
}

// Active reports whether a run's unit is still running.
func Active(ctx context.Context, x system.Executor, id string) bool {
	return system.Succeeds(ctx, x, system.C("systemctl", "is-active", "--quiet", Unit(id)))
}

// Last summarises the newest run as the box sees it now; ok is false when
// no run was ever started.
func Last(ctx context.Context, x system.Executor) (s Summary, ok bool, err error) {
	id, err := Current(x)
	if err != nil || id == "" {
		return Summary{}, false, err
	}
	recs, err := Read(x, id)
	if err != nil {
		return Summary{}, false, err
	}
	s = Summarise(recs)
	s.Running = !s.Done && Active(ctx, x, id)
	return s, true, nil
}

// Unfinished is the newest run when it is still running, or when it died
// and no apply has been recorded since it started (lastApply, from the
// logbook, is the newest apply's time). ok is false otherwise.
func Unfinished(ctx context.Context, x system.Executor, lastApply time.Time) (Summary, bool) {
	s, ok, err := Last(ctx, x)
	if err != nil || !ok || s.Done {
		return Summary{}, false
	}
	if !s.Running && lastApply.After(s.Started) {
		return Summary{}, false
	}
	return s, true
}
