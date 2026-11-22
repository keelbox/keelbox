// Package logbook is the box's change record: one JSON line per apply and
// per action a day-two run takes, appended to a root-owned file on the box.
// It records what keelbox changed, never command output or secrets
// (docs/adr/0031-logbook-kinds.md).
package logbook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

// Dir and Path locate the logbook on the box.
const (
	Dir  = "/var/lib/keelbox"
	Path = Dir + "/logbook.jsonl"
)

// Kinds of record. An apply record has no kind field, so a reader that
// knows no kinds reads it as an apply.
const (
	KindApply = "apply"
	KindWatch = "watch" // the watch run restarted something
	// KindAgents is an agent CLI upgrade or rollback by the agent store
	// (ADR 0088). Nothing writes it; it is kept so such records on a box
	// still read.
	KindAgents = "agents"
	KindDoctor = "doctor" // a scheduled doctor check changed state
	KindPower  = "power"  // the box came back after an unclean shutdown
	// KindInstall is a package request of the agent's and what became of
	// it (docs/adr/0074-agent-permissions.md).
	KindInstall = "install"
)

// Kinds lists every kind a record may carry.
var Kinds = []string{KindApply, KindWatch, KindAgents, KindDoctor, KindPower, KindInstall}

// Results of a day-two record. Apply records also use partial and
// interrupted.
const (
	ResultOK     = "ok"
	ResultFailed = "failed"
)

// Record is one apply, or one action of a day-two run.
type Record struct {
	Time time.Time `json:"time"`
	// Kind is empty for an apply (KindApply); see Kinds.
	Kind    string `json:"kind,omitempty"`
	Box     string `json:"box"`
	Keelbox string `json:"keelbox"`
	// Release is the release the box is on. A day-two record carries the
	// release of the box's last converged apply, so an older keelbox that
	// takes the newest record as the last apply still reads the box's
	// release from it.
	Release string `json:"release"`
	// FromRelease is the release the box was on before, when this apply
	// moved it: an upgrade, or a downgrade with --allow-downgrade.
	FromRelease string          `json:"from_release,omitempty"`
	ConfigHash  string          `json:"config_hash"`
	Result      string          `json:"result"` // ok | partial (a soft step failed) | failed | interrupted (Ctrl-C, between two steps)
	Error       string          `json:"error,omitempty"`
	Checked     int             `json:"checked"`
	Changes     []engine.Change `json:"changes"`
	Deferred    []engine.Change `json:"deferred,omitempty"`
	Failed      []engine.Change `json:"failed,omitempty"`
	Seconds     float64         `json:"seconds"`
}

// IsApply reports whether r records an apply.
func (r Record) IsApply() bool { return r.Kind == "" || r.Kind == KindApply }

// Append adds a record as one line written with O_APPEND, so it never
// rewrites what is there and day-two runs can record actions while an
// apply runs.
func Append(x system.Executor, r Record) error {
	if r.Kind == KindApply {
		r.Kind = ""
	}
	if err := r.check(); err != nil {
		return err
	}
	if r.Changes == nil {
		r.Changes = []engine.Change{}
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := x.MkdirAll(Dir, 0o755, "root", "root"); err != nil {
		return err
	}
	return x.AppendFile(Path, append(line, '\n'), 0o644, "root", "root")
}

// check keeps day-two records readable by a reader that knows no kinds:
// they carry the box's release, and a result an older doctor can report (it reads a
// partial record's first failed step, so a record without one would stop
// it).
func (r Record) check() error {
	if r.IsApply() {
		return nil
	}
	if !contains(Kinds, r.Kind) {
		return fmt.Errorf("logbook: unknown record kind %q", r.Kind)
	}
	if r.Release == "" {
		return fmt.Errorf("logbook: a %s record needs the box's release", r.Kind)
	}
	if r.Result != ResultOK && r.Result != ResultFailed {
		return fmt.Errorf("logbook: a %s record's result is ok or failed, not %q", r.Kind, r.Result)
	}
	return nil
}

// Action records what a day-two run did. It fills the time, the box and
// the box's release from the last converged apply, and refuses when the
// box has never been applied: then nothing installed the run.
func Action(x system.Executor, r Record) error {
	if r.IsApply() {
		return fmt.Errorf("logbook: Action records day-two runs; apply records use Append")
	}
	last, err := LastApplied(x)
	if err != nil {
		return err
	}
	if last == nil {
		return fmt.Errorf("logbook: no apply recorded, so no release to record the %s action against", r.Kind)
	}
	if r.Time.IsZero() {
		r.Time = time.Now().UTC()
	}
	if r.Box == "" {
		r.Box = last.Box
	}
	r.Release = last.Release
	if r.Result == "" {
		r.Result = ResultOK
	}
	return Append(x, r)
}

// LastApplied returns the newest apply record that converged the box
// (result ok or partial), or nil if there is none.
func LastApplied(x system.Executor) (*Record, error) {
	recs, err := Read(x)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	for i := len(recs) - 1; i >= 0; i-- {
		if r := recs[i]; r.IsApply() && r.Result != "failed" && r.Result != "interrupted" {
			return &recs[i], nil
		}
	}
	return nil, nil
}

// Last returns the newest record of kind in recs, or nil.
func Last(recs []Record, kind string) *Record {
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Kind == kind || (kind == KindApply && recs[i].IsApply()) {
			return &recs[i]
		}
	}
	return nil
}

// Read returns every record, oldest first.
func Read(x system.Executor) ([]Record, error) {
	data, err := x.ReadFile(Path)
	if err != nil {
		return nil, err
	}
	var out []Record
	for i, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			return out, fmt.Errorf("%s line %d: %w", Path, i+1, err)
		}
		out = append(out, r)
	}
	return out, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
