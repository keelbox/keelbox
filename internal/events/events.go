// Package events is how anything on a box tells its owner something: the
// fixed set of event kinds, the queue on the box that holds events until
// they are sent, the Sender interface channels implement, and the agent's
// own events file, which a root service reads as the agent and validates
// (docs/adr/0032-agent-events.md).
//
// Root sources (the watch run, doctor, agent CLI upgrades, backups) call
// Emit. The agent cannot: it appends lines to a file in its home, and only
// lines that pass AgentLine's checks become events, always of an agent
// kind, with the level the kind fixes and labelled as the agent's.
package events

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"time"
	"unicode"
)

// Level grades an event: how loudly a channel delivers it.
type Level int

// Levels, lowest first.
const (
	Info Level = iota
	Warn
	Urgent
)

var levelNames = []string{"info", "warn", "urgent"}

func (l Level) String() string {
	if l < Info || l > Urgent {
		return fmt.Sprintf("level(%d)", int(l))
	}
	return levelNames[l]
}

// ParseLevel reads info, warn or urgent.
func ParseLevel(s string) (Level, error) {
	for i, n := range levelNames {
		if s == n {
			return Level(i), nil
		}
	}
	return Info, fmt.Errorf("level %q: use info, warn or urgent", s)
}

func (l Level) MarshalJSON() ([]byte, error) {
	if l < Info || l > Urgent {
		return nil, fmt.Errorf("invalid level %d", int(l))
	}
	return json.Marshal(l.String())
}

func (l *Level) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := ParseLevel(s)
	*l = v
	return err
}

// Sources. SourceAgent is the agent's own events file; the rest are root.
const (
	SourceAgent   = "agent"
	SourceWatch   = "watch"
	SourceDoctor  = "doctor"
	SourceBackup  = "backup"
	SourcePower   = "power"
	SourceDisk    = "disk"
	SourceMemory  = "memory"
	SourceUpdates = "updates"
	SourceKeelbox = "keelbox" // keelbox notify test
	SourceApply   = "apply"   // an apply that ran detached from the laptop
	SourceInstall = "install" // the service that takes the agent's package requests
)

// Event is one thing to tell the owner.
type Event struct {
	Time   time.Time `json:"time"`
	Source string    `json:"source"`
	Kind   string    `json:"kind"`
	Level  Level     `json:"level"`
	Box    string    `json:"box"`
	// Key identifies what the event is about, for deduplication: for
	// example doctor:host/sshd or agent:<session>:agent-needs-input.
	Key string `json:"key"`
	// Fields hold only keys the kind's schema names, with checked values.
	Fields map[string]string `json:"fields,omitempty"`
}

// Kind is one kind of event: who may emit it, its level, the fields it may
// carry and whether it is sent unless muted.
type Kind struct {
	Name    string
	Sources []string
	Level   Level
	// Fields maps each allowed field to its check.
	Fields map[string]func(string) bool
	// Default is false for kinds sent only when the owner asks for them.
	Default bool
}

var (
	cliRe     = regexp.MustCompile(`^(claude-code|codex)$`)
	sessionRe = regexp.MustCompile(`^[0-9a-f]{8}$`)
	projectRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)
	reasonRe  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	keyRe     = regexp.MustCompile(`^[A-Za-z0-9._:/@-]{1,200}$`)
	fieldRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	boxRe     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

func matches(re *regexp.Regexp) func(string) bool { return re.MatchString }

// AgentTextMax is the most characters of the agent's own notification text
// an event carries (only with notify.agent_text).
const AgentTextMax = 200

// plainText accepts printable text without control characters, up to max
// runes.
func plainText(max int) func(string) bool {
	return func(s string) bool {
		if s == "" || len([]rune(s)) > max {
			return false
		}
		for _, r := range s {
			if !unicode.IsPrint(r) {
				return false
			}
		}
		return true
	}
}

// agentFields are the only fields an agent event can carry.
var agentFields = map[string]func(string) bool{
	"cli":     matches(cliRe),
	"session": matches(sessionRe),
	"project": matches(projectRe),
	"reason":  matches(reasonRe),
	"text":    plainText(AgentTextMax),
}

// rootFields accepts any short field from a root source; root is trusted
// with its own messages, so the check is only that it renders as one line.
func rootFields(names ...string) map[string]func(string) bool {
	m := map[string]func(string) bool{}
	for _, n := range names {
		m[n] = plainText(1000)
	}
	return m
}

// kinds is the fixed set of event kinds (ADR 0032). A feature adds
// fields to its own kinds here; a new kind is a new row.
var kinds = []Kind{
	{Name: "agent-done", Sources: []string{SourceAgent}, Level: Info, Fields: agentFields, Default: true},
	{Name: "agent-needs-input", Sources: []string{SourceAgent}, Level: Warn, Fields: agentFields, Default: true},
	{Name: "agent-limit", Sources: []string{SourceAgent}, Level: Warn, Fields: agentFields, Default: true},
	{Name: "agent-error", Sources: []string{SourceAgent}, Level: Info, Fields: agentFields, Default: true},
	{Name: "watch-recovered", Sources: []string{SourceWatch}, Level: Info, Fields: rootFields("unit", "detail"), Default: true},
	{Name: "watch-gave-up", Sources: []string{SourceWatch}, Level: Urgent, Fields: rootFields("unit", "detail", "fix"), Default: true},
	{Name: "doctor-failed", Sources: []string{SourceDoctor}, Level: Urgent, Fields: rootFields("check", "detail", "fix"), Default: true},
	{Name: "doctor-warning", Sources: []string{SourceDoctor}, Level: Warn, Fields: rootFields("check", "detail", "fix"), Default: true},
	{Name: "doctor-cleared", Sources: []string{SourceDoctor}, Level: Info, Fields: rootFields("check", "detail"), Default: true},
	{Name: "updates-pending", Sources: []string{SourceUpdates}, Level: Warn, Fields: rootFields("security", "reboot_since", "detail"), Default: true},
	{Name: "rebooting", Sources: []string{SourceUpdates}, Level: Info, Fields: rootFields("detail"), Default: true},
	{Name: "backup-failed", Sources: []string{SourceBackup}, Level: Warn, Fields: rootFields("detail", "fix"), Default: true},
	{Name: "backup-stale", Sources: []string{SourceBackup}, Level: Urgent, Fields: rootFields("last_success", "detail", "fix"), Default: true},
	{Name: "disk-warn", Sources: []string{SourceDisk}, Level: Warn, Fields: rootFields("mount", "percent", "detail"), Default: true},
	{Name: "disk-full", Sources: []string{SourceDisk}, Level: Urgent, Fields: rootFields("mount", "percent", "detail"), Default: true},
	{Name: "out-of-memory", Sources: []string{SourceMemory}, Level: Warn, Fields: rootFields("count", "processes", "detail"), Default: true},
	{Name: "power-back", Sources: []string{SourcePower}, Level: Warn, Fields: rootFields("between", "up_since", "detail"), Default: true},
	{Name: "apply-finished", Sources: []string{SourceApply}, Level: Info, Fields: rootFields("result", "detail"), Default: true},
	{Name: "apply-failed", Sources: []string{SourceApply}, Level: Urgent, Fields: rootFields("result", "detail", "fix"), Default: true},
	{Name: "install-request", Sources: []string{SourceInstall}, Level: Warn, Fields: rootFields("packages", "id", "fix"), Default: true},
	{Name: "test", Sources: []string{SourceKeelbox}, Level: Info, Fields: rootFields("channel", "detail"), Default: true},
	{Name: "notify-throttled", Sources: []string{SourceKeelbox}, Level: Warn, Fields: rootFields("count", "detail"), Default: true},
}

// Lookup returns the kind called name.
func Lookup(name string) (Kind, bool) {
	for _, k := range kinds {
		if k.Name == name {
			return k, true
		}
	}
	return Kind{}, false
}

// Kinds returns the names of every kind, sorted.
func Kinds() []string {
	out := make([]string, len(kinds))
	for i, k := range kinds {
		out[i] = k.Name
	}
	sort.Strings(out)
	return out
}

// FromAgent reports whether the agent's file may produce this kind.
func (k Kind) FromAgent() bool { return contains(k.Sources, SourceAgent) }

// Validate checks e against its kind: the source may emit it, the level is
// the kind's, and every field is in the schema with a value that passes.
func (e Event) Validate() error {
	k, ok := Lookup(e.Kind)
	switch {
	case !ok:
		return fmt.Errorf("event kind %q is unknown", e.Kind)
	case !contains(k.Sources, e.Source):
		return fmt.Errorf("event %s cannot come from %q", e.Kind, e.Source)
	case e.Level != k.Level:
		return fmt.Errorf("event %s is %s, not %s", e.Kind, k.Level, e.Level)
	case !boxRe.MatchString(e.Box):
		return fmt.Errorf("event %s: box %q is not a box name", e.Kind, e.Box)
	case !keyRe.MatchString(e.Key):
		return fmt.Errorf("event %s: key %q has characters outside [A-Za-z0-9._:/@-]", e.Kind, e.Key)
	case e.Time.IsZero():
		return fmt.Errorf("event %s has no time", e.Kind)
	}
	for name, v := range e.Fields {
		check, ok := k.Fields[name]
		if !fieldRe.MatchString(name) || !ok {
			return fmt.Errorf("event %s cannot carry field %q", e.Kind, name)
		}
		if !check(v) {
			return fmt.Errorf("event %s: field %s has a value it cannot carry", e.Kind, name)
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
