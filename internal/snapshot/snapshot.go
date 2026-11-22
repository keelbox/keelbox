// Package snapshot is the box's status snapshot: one small JSON file the
// watch run rewrites every two minutes (ADR 0030), which `keelbox status`
// reads for its day-two rows and a laptop reads over plain ssh for every
// box at once. It is world-readable and holds no secrets: versions, times,
// counts, unit states and session names, nothing from a credential file
// and no URL.
package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/plain"
	"github.com/keelbox/keelbox/internal/system"
)

// Path is where the snapshot lives on the box.
const Path = "/var/lib/keelbox/status.json"

// Schema is the version this keelbox writes and reads. A reader refuses a
// newer one: its fields may mean something else.
const Schema = 1

// StaleAfter is the age at which a snapshot no longer describes the box:
// the watch run writes it every two minutes.
const StaleAfter = 10 * time.Minute

// Status is the snapshot. Sections a box does not have, or that nothing has
// filled yet, are left out.
type Status struct {
	Schema  int       `json:"status"`
	Written time.Time `json:"written"`
	Box     string    `json:"box"`
	Keelbox string    `json:"keelbox"`
	Release string    `json:"release"`
	// Agent is the agent account's name: keelbox run by that account on
	// the box reads it to know it runs as the agent.
	Agent string `json:"agent,omitempty"`
	// Host is the machine's size.
	Host *Host `json:"host,omitempty"`
	// ReportsOff is set with reports.enabled: false, so the agent's
	// keelbox report says so instead of filing what the box drops.
	ReportsOff bool `json:"reports_off,omitempty"`

	LastApply *Apply    `json:"last_apply,omitempty"`
	Doctor    *Doctor   `json:"doctor,omitempty"`
	Sessions  *Sessions `json:"sessions,omitempty"`
	Updates   *Updates  `json:"updates,omitempty"`
	Disk      []Disk    `json:"disk,omitempty"`
	Backup    *Backup   `json:"backup,omitempty"`
	Power     *Power    `json:"power,omitempty"`
	// AgentUsage is what the agent account uses, against its limits.
	AgentUsage *AgentUsage `json:"agent_usage,omitempty"`
	// Projects is the agent's projects and whether their work exists
	// anywhere but on the box.
	Projects *Projects `json:"projects,omitempty"`
	// RequestsWaiting is how many package requests of the agent's wait
	// for the owner's keelbox approve.
	RequestsWaiting int `json:"requests_waiting,omitempty"`
}

// Host is the machine's size: CPUs and memory in bytes.
type Host struct {
	CPUs            int   `json:"cpus"`
	Memory          int64 `json:"memory"`
	MemoryAvailable int64 `json:"memory_available"`
}

// Apply is the last apply the logbook records.
type Apply struct {
	Time    time.Time `json:"time"`
	Result  string    `json:"result"`
	Keelbox string    `json:"keelbox"`
}

// Doctor is the last scheduled doctor run.
type Doctor struct {
	Time     time.Time `json:"time"`
	OK       int       `json:"ok"`
	Warnings int       `json:"warnings"`
	Failures int       `json:"failures"`
	// Failing names the checks that fail, as module/check.
	Failing []string `json:"failing,omitempty"`
}

// Sessions is the agent's tmux and Remote Control.
type Sessions struct {
	Tmux          bool     `json:"tmux"`
	Names         []string `json:"names,omitempty"`
	RemoteControl string   `json:"remote_control,omitempty"`
}

// Updates is what unattended-upgrades has left.
type Updates struct {
	Security       int        `json:"security"`
	RebootRequired *time.Time `json:"reboot_required_since,omitempty"`
}

// Disk is one mount's use.
type Disk struct {
	Mount   string `json:"mount"`
	Percent int    `json:"percent"`
}

// Backup is the last backup outcome.
type Backup struct {
	LastSuccess *time.Time `json:"last_success,omitempty"`
	LastFailed  bool       `json:"last_failed"`
}

// Power is the box's boot and how the last one ended.
type Power struct {
	LastBoot time.Time `json:"last_boot"`
	Unclean  bool      `json:"unclean,omitempty"`
}

// AgentUsage is the agent's slice as the watch run saw it
// (internal/agentlimits). Limits of -1 are none.
type AgentUsage struct {
	Time time.Time `json:"time"`
	// CPUPercent is the agent's share of all CPUs since the run before.
	CPUPercent float64 `json:"cpu_percent"`
	CPUUsec    uint64  `json:"cpu_usec"`
	CPUQuota   int     `json:"cpu_quota"` // systemd's CPUQuota percent (100 a CPU); 0 is none
	CPUs       int     `json:"cpus"`
	Memory     int64   `json:"memory"`
	MemoryHigh int64   `json:"memory_high"`
	MemoryMax  int64   `json:"memory_max"`
	// Counters since the slice was made, and when each last went up.
	Throttled     uint64     `json:"cpu_throttled"`
	HighEvents    uint64     `json:"memory_high_events"`
	OOMKills      uint64     `json:"oom_kills"`
	LastThrottled *time.Time `json:"last_cpu_throttled,omitempty"`
	LastHigh      *time.Time `json:"last_memory_high,omitempty"`
	LastOOMKill   *time.Time `json:"last_oom_kill,omitempty"`
}

// Projects is each directory directly under the agent's projects directory
// (internal/projects reads it, as the agent).
type Projects struct {
	// Dir is the directory, as the agent names it: ~/projects.
	Dir string `json:"dir"`
	// Total counts every directory; List has at most the first
	// projects.Scanned of them, work that is only on the box first.
	Total int       `json:"total"`
	List  []Project `json:"list,omitempty"`
}

// Project is one directory. Its name is the agent's to choose and is made
// one plain line before it gets here.
type Project struct {
	Name string `json:"name"`
	// Repo is false for a directory that is not a git repository.
	Repo bool `json:"repo"`
	// Uncommitted counts changed and untracked paths, as git status
	// lists them.
	Uncommitted int `json:"uncommitted,omitempty"`
	// Unpushed counts commits on local branches that no remote-tracking
	// ref has. Nothing is fetched: it is as true as the last fetch or push.
	Unpushed int `json:"unpushed,omitempty"`
	// NoRemote is a repository with no remote at all.
	NoRemote bool `json:"no_remote,omitempty"`
	// Unchecked is a repository git did not answer for in time, or at all.
	Unchecked bool `json:"unchecked,omitempty"`
}

// OnlyHere reports whether the directory holds work that exists nowhere
// but on the box, as far as git on the box can tell. One that could not be
// checked counts: nothing says its work is anywhere else.
func (p Project) OnlyHere() bool {
	return !p.Repo || p.Unchecked || p.NoRemote || p.Uncommitted > 0 || p.Unpushed > 0
}

// State says it in a few words: "not a git repository", "3 uncommitted
// changes, 2 commits not pushed", "no remote", "clean and pushed".
func (p Project) State() string {
	switch {
	case !p.Repo:
		return "not a git repository"
	case p.Unchecked:
		return "could not be checked (git did not answer)"
	}
	var s []string
	if p.Uncommitted > 0 {
		s = append(s, count(p.Uncommitted, "uncommitted change", "uncommitted changes"))
	}
	switch {
	case p.NoRemote:
		s = append(s, "no remote")
	case p.Unpushed > 0:
		s = append(s, count(p.Unpushed, "commit not pushed", "commits not pushed"))
	}
	if len(s) == 0 {
		return "clean and pushed"
	}
	return strings.Join(s, ", ")
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// OnlyHere lists the projects with work that is only on the box.
func (p *Projects) OnlyHere() []Project {
	if p == nil {
		return nil
	}
	var out []Project
	for _, pr := range p.List {
		if pr.OnlyHere() {
			out = append(out, pr)
		}
	}
	return out
}

// Unlisted counts the directories past the ones read.
func (p *Projects) Unlisted() int {
	if p == nil {
		return 0
	}
	return max(p.Total-len(p.List), 0)
}

// Shown is how many projects keelbox status and keelbox destroy list
// before "and N more".
const Shown = 10

// Rows are the lines keelbox status prints under its projects row: one
// per project, at most Shown, then how many more there are.
func (p *Projects) Rows() []string {
	if p == nil {
		return nil
	}
	var out []string
	for i, pr := range p.List {
		if i == Shown {
			break
		}
		out = append(out, pr.Name+": "+pr.State())
	}
	if more := p.Total - len(out); more > 0 && len(out) > 0 {
		out = append(out, "and "+strconv.Itoa(more)+" more")
	}
	return out
}

// Summary is the projects row itself: "4 in ~/projects, 3 with work that
// is only on this box".
func (p *Projects) Summary() string {
	if p == nil {
		return ""
	}
	if p.Total == 0 {
		return "none in " + p.Dir
	}
	s := strconv.Itoa(p.Total) + " in " + p.Dir
	n := len(p.OnlyHere())
	switch {
	case n > 0 && p.Unlisted() > 0:
		s += ", at least " + strconv.Itoa(n) + " with work that is only on this box"
	case n > 0:
		s += ", " + strconv.Itoa(n) + " with work that is only on this box"
	case p.Unlisted() > 0:
		s += ", the first " + strconv.Itoa(len(p.List)) + " clean and pushed"
	default:
		s += ", all clean and pushed"
	}
	return s
}

// Write replaces the snapshot, 0644 root. Every string in it is made plain
// first: some come from the agent (its tmux session names), and the
// snapshot is printed on the owner's terminal.
func Write(x system.Executor, s Status) error {
	clean(reflect.ValueOf(&s).Elem())
	s.Schema = Schema
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return x.WriteFile(Path, append(b, '\n'), 0o644, "root", "root")
}

// Parse reads a snapshot, from the box or from ssh's output.
func Parse(data []byte) (Status, error) {
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("the status snapshot is not valid JSON: %w", err)
	}
	switch {
	case s.Schema == 0:
		return s, fmt.Errorf("the status snapshot has no schema version")
	case s.Schema > Schema:
		return s, fmt.Errorf("the box writes status schema %d, newer than this keelbox reads (%d): upgrade keelbox (keelbox self-update)", s.Schema, Schema)
	}
	clean(reflect.ValueOf(&s).Elem())
	return s, nil
}

// clean makes every string reachable from v one plain line (plain.Line):
// a snapshot from a box written by an older keelbox, or by hand, cannot put
// terminal sequences on the laptop.
func clean(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() {
			v.SetString(plain.Line(v.String()))
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			clean(v.Elem())
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				clean(v.Field(i))
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			clean(v.Index(i))
		}
	case reflect.Map:
		for _, k := range v.MapKeys() {
			e := reflect.New(v.Type().Elem()).Elem()
			e.Set(v.MapIndex(k))
			clean(e)
			v.SetMapIndex(k, e)
		}
	}
}

// Read reads the snapshot on the box; found is false when there is none.
func Read(x system.Executor) (s Status, found bool, err error) {
	data, err := x.ReadFile(Path)
	if err != nil || len(data) == 0 {
		return Status{}, false, nil
	}
	s, err = Parse(data)
	return s, err == nil, err
}

// Stale reports whether the snapshot is too old to describe the box.
func (s Status) Stale(now time.Time) bool { return now.Sub(s.Written) > StaleAfter }

// ReadHost reads the machine's size: CPUs from nproc, memory from
// /proc/meminfo. Nil when neither can be read.
func ReadHost(ctx context.Context, x system.Executor) *Host {
	var h Host
	if out, err := system.Output(ctx, x, system.C("nproc")); err == nil {
		h.CPUs, _ = strconv.Atoi(strings.TrimSpace(out))
	}
	data, _ := x.ReadFile("/proc/meminfo")
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		n, _ := strconv.ParseInt(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			h.Memory = n << 10
		case "MemAvailable:":
			h.MemoryAvailable = n << 10
		}
	}
	if h.CPUs == 0 && h.Memory == 0 {
		return nil
	}
	return &h
}
