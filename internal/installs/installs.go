// Package installs is how the agent account gets an Ubuntu package without
// sudo (docs/adr/0074-agent-permissions.md). The agent's keelbox install
// writes one request to a file in its own home. keelbox-install.path starts
// keelbox-install.service, root, which reads that file as the agent
// (Env.FS, so a link the agent plants is never followed with root's
// rights), checks the request against the box, and with
// users.agent.permissions auto or full installs it with apt-get; with
// manual it keeps it until the owner runs keelbox approve, and installs it
// then.
//
// Nothing the agent writes is trusted. The request must be a small regular
// file that decodes strictly; its package names must match nameRe, be on no
// deny-list family, and each have an install candidate in the box's own apt
// sources; and what apt would do for them is read from a simulated run
// first: a request that would remove any package, or install or upgrade a
// deny-listed one, is refused. The install itself runs with --no-remove.
// Every request and its outcome goes in the logbook, and the packages the
// agent has had installed in a record of root's. Only the service runs
// apt-get for a request, so an install is never cut off with the session of
// whoever approved it.
package installs

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/logbook"
	"github.com/keelbox/keelbox/internal/plain"
	"github.com/keelbox/keelbox/internal/system"
	"github.com/keelbox/keelbox/internal/version"
)

// Root's files. The directory is root's and world-readable: the agent's
// keelbox install reads the mode and what became of its requests there.
const (
	Dir = "/var/lib/keelbox/installs"
	// ModePath says which account may ask and what happens when it does;
	// apply writes it from the config.
	ModePath = Dir + "/mode.json"
	// RequestsPath is what root made of each request: the ones waiting
	// for approval and the last decided ones.
	RequestsPath = Dir + "/requests.json"
	// InstalledPath is the record of the packages installed for the agent.
	InstalledPath = Dir + "/installed.json"
	// toldPath is the set of packages apply last said host.packages lacks.
	toldPath   = Dir + "/told.json"
	runMark    = Dir + "/run"
	updateMark = Dir + "/apt-update"
)

// The agent's request file, relative to its home, and the units that read it.
const (
	AgentDir  = ".local/state/keelbox"
	AgentFile = AgentDir + "/install-request.json"
	Service   = "keelbox-install.service"
	PathUnit  = "keelbox-install.path"
	// Command is what the service runs.
	Command = "install-run"
)

// Limits on what the agent can make root do or keep.
const (
	// requestMax is the largest request file root reads: MaxNames names of
	// maxNameLen and a reason fit in a fraction of it.
	requestMax = 16 << 10
	// MaxPending is the most requests kept waiting for approval.
	MaxPending = 20
	// historyMax is the most decided requests kept.
	historyMax = 50
	// HourMax is the most requests taken in an hour; the rest are refused.
	HourMax = 30
	// requestMaxAge and requestMaxAhead bound a request's own time, so an
	// old file written again is not a new request.
	requestMaxAge   = 15 * time.Minute
	requestMaxAhead = 5 * time.Minute
	// RunPace is the least time between two runs of the service. The agent
	// changes its file whenever it likes and each change can start a run.
	RunPace = 2 * time.Second
	// updateEvery is how old the package lists may be before a request
	// refreshes them.
	updateEvery = time.Hour
)

// States of a request.
const (
	StatePending    = "pending"    // waiting for the owner's keelbox approve
	StateApproved   = "approved"   // the owner said yes; the service installs it at its next run
	StateInstalling = "installing" // apt-get is running for it
	StateInstalled  = "installed"  // done, or nothing was left to install
	StateRefused    = "refused"    // outside the bounds; nothing was run
	StateFailed     = "failed"     // apt-get did not install it
	StateDenied     = "denied"     // the owner said no
)

// Mode is ModePath: the agent account and the box's permissions mode.
type Mode struct {
	Box   string `json:"box"`
	Agent string `json:"agent"`
	Mode  string `json:"mode"`
}

// ModeFile is ModePath's content for a config.
func ModeFile(c *config.Config) []byte {
	b, _ := json.MarshalIndent(Mode{Box: c.Box, Agent: c.Users.Agent.Name, Mode: c.AgentPermissions()}, "", "  ")
	return append(b, '\n')
}

// ParseMode reads ModePath's content.
func ParseMode(data []byte) (Mode, error) {
	var m Mode
	if err := json.Unmarshal(data, &m); err != nil {
		return Mode{}, err
	}
	if m.Agent == "" || !config.ValidPermissions(m.Mode) {
		return Mode{}, errors.New("no agent or mode in it")
	}
	return m, nil
}

// Request is the agent's file: one request. Every field is checked by root
// before anything is done with it.
type Request struct {
	V        int       `json:"v"`
	ID       string    `json:"id"`
	Time     time.Time `json:"time"`
	Packages []string  `json:"packages"`
	// Why is the agent's stated reason, shown to the owner as the agent's
	// text.
	Why string `json:"why,omitempty"`
}

var idRe = regexp.MustCompile(`^[0-9a-f]{8}$`)

// ValidID reports whether s is a request ID: 8 hex digits.
func ValidID(s string) bool { return idRe.MatchString(s) }

// NewID is a random request ID.
func NewID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Entry is what root made of one request. Packages hold only names that
// passed CheckNames; Why is the agent's text, cleaned (CleanWhy).
type Entry struct {
	// ID is root's name for the request, random and made by root: it is
	// what the owner approves. Ref is the ID the agent's file carried,
	// kept only so the agent's keelbox install finds its answer. Were the
	// agent's own ID the name, a later request under an ID the owner once
	// approved could be passed off as that one.
	ID       string    `json:"id"`
	Ref      string    `json:"ref,omitempty"`
	Asked    time.Time `json:"asked"` // root's clock when it read the request
	By       string    `json:"by"`    // the agent account
	Packages []string  `json:"packages,omitempty"`
	Why      string    `json:"why,omitempty"`
	State    string    `json:"state"`
	// Detail says why a request was refused or failed, for the agent and
	// the owner.
	Detail string `json:"detail,omitempty"`
	// Installed are the requested packages this request put on the box.
	Installed []string `json:"installed,omitempty"`
	// Approved is set when the owner's keelbox approve let it through.
	Approved bool       `json:"approved,omitempty"`
	Decided  *time.Time `json:"decided,omitempty"`

	// code is the refusal in fixed words, for the logbook.
	code string
}

// Done reports whether nothing more will happen to the request.
func (en Entry) Done() bool {
	return en.State != StatePending && en.State != StateApproved && en.State != StateInstalling
}

// when is the time a decided request was decided.
func (en Entry) when() time.Time {
	if en.Decided != nil {
		return *en.Decided
	}
	return en.Asked
}

// State is RequestsPath.
type State struct {
	Requests []Entry `json:"requests"`
}

// ParseState reads RequestsPath's content.
func ParseState(data []byte) (State, error) {
	var s State
	err := json.Unmarshal(data, &s)
	return s, err
}

// ReadState reads root's record of the requests; none yet is empty.
func ReadState(x system.Executor) (State, error) {
	data, err := x.ReadFile(RequestsPath)
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	s, err := ParseState(data)
	if err != nil {
		return State{}, fmt.Errorf("%s: %w", RequestsPath, err)
	}
	return s, nil
}

// Find returns the request with id.
func (s *State) Find(id string) *Entry {
	for i := range s.Requests {
		if s.Requests[i].ID == id {
			return &s.Requests[i]
		}
	}
	return nil
}

// FindRef returns the request the agent filed under ref.
func (s *State) FindRef(ref string) *Entry {
	for i := range s.Requests {
		if ref != "" && s.Requests[i].Ref == ref {
			return &s.Requests[i]
		}
	}
	return nil
}

// mint is a new ID for a request, one no request on record has.
func (s *State) mint() string {
	for {
		if id := NewID(); s.Find(id) == nil {
			return id
		}
	}
}

// Pending are the requests waiting for approval, oldest first.
func (s State) Pending() []Entry { return s.in(StatePending) }

func (s State) in(states ...string) []Entry {
	var out []Entry
	for _, en := range s.Requests {
		if contains(states, en.State) {
			out = append(out, en)
		}
	}
	return out
}

// set adds en, or replaces the entry with its ID.
func (s *State) set(en Entry) {
	if cur := s.Find(en.ID); cur != nil {
		*cur = en
		return
	}
	s.Requests = append(s.Requests, en)
}

// write keeps every request that is not decided and, of those that are,
// the historyMax decided last: a request the owner just approved is not
// dropped for having been asked long ago.
func (s *State) write(x system.Executor) error {
	var decided []time.Time
	for _, r := range s.Requests {
		if r.Done() {
			decided = append(decided, r.when())
		}
	}
	if drop := len(decided) - historyMax; drop > 0 {
		sort.Slice(decided, func(i, j int) bool { return decided[i].Before(decided[j]) })
		cut := decided[drop-1] // the newest time that goes
		var keep []Entry
		for _, r := range s.Requests {
			if r.Done() && drop > 0 && !r.when().After(cut) {
				drop--
				continue
			}
			keep = append(keep, r)
		}
		s.Requests = keep
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := x.MkdirAll(Dir, 0o755, "root", "root"); err != nil {
		return err
	}
	return x.WriteFile(RequestsPath, append(b, '\n'), 0o644, "root", "root")
}

// Package is one package installed for the agent.
type Package struct {
	Name    string    `json:"name"`
	Time    time.Time `json:"time"`
	Request string    `json:"request"`
}

type installedFile struct {
	Packages []Package `json:"packages"`
}

// ReadInstalled is root's record of the packages installed for the agent,
// oldest first. keelbox apply never removes them, nor the record.
func ReadInstalled(x system.Executor) ([]Package, error) {
	data, err := x.ReadFile(InstalledPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f installedFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", InstalledPath, err)
	}
	return f.Packages, nil
}

func addInstalled(x system.Executor, names []string, id string, now time.Time) error {
	if len(names) == 0 {
		return nil
	}
	have, err := ReadInstalled(x)
	if err != nil {
		return err
	}
	for _, n := range names {
		known := false
		for _, p := range have {
			known = known || p.Name == n
		}
		if !known {
			have = append(have, Package{Name: n, Time: now, Request: id})
		}
	}
	b, err := json.MarshalIndent(installedFile{Packages: have}, "", "  ")
	if err != nil {
		return err
	}
	return x.WriteFile(InstalledPath, append(b, '\n'), 0o644, "root", "root")
}

// Pace records a run of the service starting at now and returns how long
// it waits first: what is left of RunPace since the last start.
func Pace(x system.Executor, now time.Time) time.Duration {
	var wait time.Duration
	if data, err := x.ReadFile(runMark); err == nil {
		if last, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data))); err == nil {
			if d := RunPace - now.Sub(last); d > 0 {
				wait = min(d, RunPace)
			}
		}
	}
	if err := x.MkdirAll(Dir, 0o755, "root", "root"); err == nil {
		_ = x.WriteFile(runMark, []byte(now.Add(wait).Format(time.RFC3339Nano)+"\n"), 0o600, "root", "root")
	}
	return wait
}

// readRequest reads the agent's request file as the agent: only a regular
// file that is not a link, opened without blocking, of at most requestMax
// bytes (system.ReadData), decoded with no unknown field. found is false
// when there is no file; problem says why a file that is there is not a
// request.
func readRequest(e *engine.Env, now time.Time) (req Request, found bool, problem string) {
	agent := e.AgentUser()
	path := agent.Home + "/" + AgentFile
	data, err := system.ReadData(e.FS(agent.Name), path, requestMax)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Request{}, false, ""
	case err != nil:
		return Request{}, false, "cannot read " + path + " as " + agent.Name + ": " + err.Error()
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || dec.More() || req.V != 1 || !ValidID(req.ID) {
		return Request{}, false, path + " is not a keelbox install request"
	}
	if req.Time.Before(now.Add(-requestMaxAge)) || req.Time.After(now.Add(requestMaxAhead)) {
		return Request{}, false, fmt.Sprintf("request %s in %s is dated %s, not within the last %s", req.ID, path, req.Time.UTC().Format(time.RFC3339), requestMaxAge)
	}
	return req, true, ""
}

// Waiting reports whether a run of the service has something to act on: a
// request in the agent's file that root has not seen, one the owner
// approved, or one a run left unfinished. problem says why a file that is
// there is not a request, for the journal. It takes no lock.
func Waiting(e *engine.Env, now time.Time) (waiting bool, problem string) {
	req, found, problem := readRequest(e, now)
	st, err := ReadState(e.X)
	if err != nil || len(st.in(StateApproved, StateInstalling)) > 0 {
		return true, problem
	}
	return found && st.FindRef(req.ID) == nil, problem
}

// Options are what a run needs from its caller.
type Options struct {
	// Now is the clock.
	Now func() time.Time
	// Lock takes the installs lock, which every change to root's record
	// of the requests is made under: the service's, and keelbox approve's
	// and deny's. It is not held while apt-get installs.
	Lock func() (unlock func(), err error)
	// Notify is called for a request that now waits for approval, so the
	// caller can raise the event under the event queue's lock.
	Notify func(Entry)
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

// errUnchanged ends a change that has nothing to write.
var errUnchanged = errors.New("unchanged")

// change reads root's record under the installs lock, lets fn change it,
// and writes it back unless fn returns errUnchanged.
func change(e *engine.Env, o Options, fn func(st *State) error) error {
	if o.Lock != nil {
		unlock, err := o.Lock()
		if err != nil {
			return err
		}
		defer unlock()
	}
	st, err := ReadState(e.X)
	if err != nil {
		return err
	}
	switch err := fn(&st); {
	case errors.Is(err, errUnchanged):
		return nil
	case err != nil:
		return err
	}
	return st.write(e.X)
}

// Outcome is what a run did.
type Outcome struct {
	Entries  []Entry
	Problems []string // for the journal
}

// Run is one run of keelbox-install.service. It closes a request an
// earlier run left unfinished, installs the requests the owner approved,
// then reads the agent's file and, for a request root has not seen, checks
// it and records it: with permissions manual as waiting for approval,
// otherwise by installing it. It reads the file again afterwards, a few
// times, for a request written while it worked.
func Run(ctx context.Context, e *engine.Env, o Options) (Outcome, error) {
	var out Outcome
	// A request still marked installing was cut off: only this service
	// installs, and systemd runs one of it at a time.
	err := change(e, o, func(st *State) error {
		cut := st.in(StateInstalling)
		for _, en := range cut {
			now := o.now()
			en.State, en.Decided = StateFailed, &now
			en.Detail = "the install was cut off before apt-get finished (a restart, or the service's time limit). Ask again; if apt then says dpkg was interrupted, " +
				"tell the user: keelbox ssh " + e.Cfg.Box + " --admin, then sudo dpkg --configure -a"
			st.set(en)
			record(e, en, now)
			out.Entries = append(out.Entries, en)
		}
		if len(cut) == 0 {
			return errUnchanged
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	// start is called under the lock; apt-get then runs without it.
	step := func(start func(st *State) (Entry, []string, bool)) (bool, error) {
		var en Entry
		var todo []string
		var found bool
		err := change(e, o, func(st *State) error {
			if en, todo, found = start(st); !found {
				return errUnchanged
			}
			return nil
		})
		if err != nil || !found {
			return false, err
		}
		if len(todo) > 0 {
			if en, err = finishInstall(ctx, e, en, todo, o); err != nil {
				return true, err
			}
		}
		if en.State == StatePending && o.Notify != nil {
			o.Notify(en)
		}
		out.Entries = append(out.Entries, en)
		return true, nil
	}
	for i := 0; i <= MaxPending; i++ {
		found, err := step(func(st *State) (Entry, []string, bool) {
			approved := st.in(StateApproved)
			if len(approved) == 0 {
				return Entry{}, nil, false
			}
			en := approved[0]
			todo := begin(ctx, e, st, &en, o)
			return en, todo, true
		})
		if err != nil || !found {
			if err != nil {
				return out, err
			}
			break
		}
	}
	for round := 0; round < 3; round++ {
		req, found, problem := readRequest(e, o.now())
		if problem != "" {
			out.Problems = append(out.Problems, problem)
		}
		if !found {
			break
		}
		taken, err := step(func(st *State) (Entry, []string, bool) {
			if st.FindRef(req.ID) != nil {
				return Entry{}, nil, false
			}
			en, todo := take(ctx, e, st, req, o)
			return en, todo, true
		})
		if err != nil || !taken {
			return out, err
		}
	}
	return out, nil
}

// take turns a request root has not seen into an entry of st. It returns
// the packages to install now, none when the request is refused, already
// met, or waits for approval.
func take(ctx context.Context, e *engine.Env, st *State, req Request, o Options) (Entry, []string) {
	now := o.now()
	en := Entry{ID: st.mint(), Ref: req.ID, Asked: now, By: e.Cfg.Users.Agent.Name, Why: CleanWhy(req.Why)}
	hour := 0
	for _, r := range st.Requests {
		if now.Sub(r.Asked) < time.Hour {
			hour++
		}
	}
	names, refusal := CheckNames(req.Packages)
	if hour >= HourMax {
		// Past the budget a request still gets its answer, but only the
		// first one a line in the logbook: a file rewritten without end
		// does not grow root's record without end.
		en.State, en.Decided = StateRefused, &now
		en.Detail = fmt.Sprintf("%d requests in the last hour already; the most is %d. Ask again later, for everything the work needs at once", hour, HourMax)
		en.code = "too many requests"
		st.set(en)
		if hour == HourMax {
			record(e, en, now)
		}
		return en, nil
	}
	if refusal != nil {
		finish(e, st, &en, refusal, now)
		return en, nil
	}
	en.Packages = names
	if e.Cfg.AgentInstalls() {
		return en, begin(ctx, e, st, &en, o)
	}
	// Manual: the box is asked now, so a request the owner could never
	// approve does not wait for them, and one already met is not asked.
	todo, refusal := plan(ctx, e, names, now)
	waiting := len(st.in(StatePending, StateApproved))
	switch {
	case refusal != nil || len(todo) == 0:
		finish(e, st, &en, refusal, now)
	case waiting >= MaxPending:
		finish(e, st, &en, refuse("too many requests waiting", "%d requests already wait for the owner; the most is %d. Tell the user: keelbox requests %s", waiting, MaxPending, e.Cfg.Box), now)
	default:
		en.State = StatePending
		st.set(en)
		record(e, en, now)
	}
	return en, nil
}

// begin checks en against the box, the same whether the service takes it
// straight from the agent or the owner approved it, and marks it as
// installing: it returns the packages apt-get is to install. A request
// that is refused, or has nothing left to install, ends here.
func begin(ctx context.Context, e *engine.Env, st *State, en *Entry, o Options) []string {
	now := o.now()
	todo, refusal := plan(ctx, e, en.Packages, now)
	if refusal != nil || len(todo) == 0 {
		finish(e, st, en, refusal, now)
		return nil
	}
	en.State = StateInstalling
	st.set(*en)
	return todo
}

// finish ends a request without an install: refused, or with nothing left
// to install.
func finish(e *engine.Env, st *State, en *Entry, refusal *Refusal, now time.Time) {
	en.State, en.Decided = StateInstalled, &now
	if refusal != nil {
		en.State, en.Detail, en.code = StateRefused, refusal.Msg, refusal.Code
	}
	st.set(*en)
	record(e, *en, now)
}

// finishInstall runs apt-get for a request marked installing and records
// what came of it.
func finishInstall(ctx context.Context, e *engine.Env, en Entry, todo []string, o Options) (Entry, error) {
	// Installs only: --no-remove makes apt-get fail rather than remove a
	// package, whatever changed since the simulated run. Services are not
	// restarted for a library the agent's request upgraded: needrestart
	// looks again at the next upgrade.
	c := engine.AptCmd(append([]string{"install", "-y", "--no-remove", "--"}, todo...)...)
	c.Env = []string{"LC_ALL=C.UTF-8", "NEEDRESTART_SUSPEND=1"}
	_, aptErr := e.X.Run(ctx, c)
	have, _ := engine.InstalledPackages(ctx, e, todo)
	var got, missing []string
	for _, n := range todo {
		if have[n] {
			got = append(got, n)
		} else {
			missing = append(missing, n)
		}
	}
	now := o.now()
	en.Installed, en.Decided = got, &now
	switch {
	case aptErr != nil:
		en.State, en.Detail = StateFailed, "apt-get did not install "+strings.Join(todo, ", ")+": "+aptSays(aptErr)
	case len(missing) > 0:
		en.State, en.Detail = StateFailed, "apt-get ended without error, but "+strings.Join(missing, ", ")+" is not installed"
	default:
		en.State = StateInstalled
	}
	err := change(e, o, func(st *State) error {
		st.set(en)
		return addInstalled(e.X, got, en.ID, now)
	})
	record(e, en, now)
	return en, err
}

// Refusals whose names apt does not know as packages.
const (
	codeNoCandidate = "no install candidate"
	codeNotExact    = "not exact package names"
)

// aptLocale keeps what apt prints in the words the parsers read, whatever
// locale the service inherited.
var aptLocale = []string{"LC_ALL=C.UTF-8"}

// plan checks names against the box and returns the ones to install: those
// not installed yet. Each name must be exactly a package apt knows, with a
// candidate from the box's configured sources, and a simulated install must
// remove nothing and touch no deny-listed package.
func plan(ctx context.Context, e *engine.Env, names []string, now time.Time) ([]string, *Refusal) {
	refreshLists(ctx, e, now)
	// apt-cache policy prints a stanza for each name it knows. A name it
	// does not know it tries as a pattern, and prints the packages that
	// match: so a stanza under any other name means the request was not
	// exact names, and apt-get install would read it the same way.
	policyCmd := system.C("apt-cache", append([]string{"policy", "--"}, names...)...)
	policyCmd.Env = aptLocale
	r, err := e.X.Run(ctx, policyCmd)
	pol := parsePolicy(r.Stdout)
	var todo []string
	for _, n := range names {
		p, known := pol[n]
		switch {
		case !known:
			return nil, refuse(codeNoCandidate, "apt has no package named %s in this box's sources. Only packages from the sources the box already has are installed: check the name (apt-cache search)", n)
		case p.installed != "" && p.installed != "(none)":
			continue
		case p.candidate == "" || p.candidate == "(none)":
			return nil, refuse(codeNoCandidate, "%s has no install candidate in this box's apt sources; if it is a name several packages provide, ask for one of them (apt-cache showpkg %s)", n, n)
		}
		todo = append(todo, n)
	}
	for n := range pol {
		if !contains(names, n) {
			return nil, refuse(codeNotExact, "apt read the request as a pattern (it matched %s): name each package exactly", plain.Line(n))
		}
	}
	if err != nil {
		return nil, refuse("apt-cache failed", "apt-cache policy failed on this box: %s", aptSays(err))
	}
	if len(todo) == 0 {
		return nil, nil
	}
	simCmd := engine.AptCmd(append([]string{"install", "-s", "-y", "--no-remove", "--"}, todo...)...)
	simCmd.Env = aptLocale
	sim, err := e.X.Run(ctx, simCmd)
	t := parseSimulation(sim.Stdout)
	what := strings.Join(todo, ", ")
	switch {
	case len(t.remove) > 0:
		return nil, refuse("would remove a package", "installing %s would remove %s; keelbox install never removes a package", what, strings.Join(t.remove, ", "))
	case err != nil:
		return nil, refuse("apt cannot install it", "apt cannot install %s as the box is: %s", what, aptSays(err))
	case len(t.install) == 0:
		return nil, refuse("apt cannot install it", "apt-get would install nothing for %s", what)
	case len(t.install) > transactionMax:
		return nil, refuse("too many packages", "installing %s would install or upgrade %d packages, over %d", what, len(t.install), transactionMax)
	}
	for _, n := range t.install {
		if fam, denied := Denied(n); denied {
			return nil, refuse("on the deny-list ("+fam.Name+")", "installing %s would also install or upgrade %s, which is on keelbox's deny-list (%s): %s", what, n, fam.Name, fam.Why)
		}
	}
	return todo, nil
}

// refreshLists runs apt-get update when this package last did so over
// updateEvery ago, so candidates are current and a download does not ask
// for a version the archive has replaced. A failure is left to the install.
func refreshLists(ctx context.Context, e *engine.Env, now time.Time) {
	if data, err := e.X.ReadFile(updateMark); err == nil {
		if last, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data))); err == nil && now.Sub(last) < updateEvery && !last.After(now) {
			return
		}
	}
	if engine.Apt(ctx, e, "update") != nil {
		return
	}
	if err := e.X.MkdirAll(Dir, 0o755, "root", "root"); err == nil {
		_ = e.X.WriteFile(updateMark, []byte(now.Format(time.RFC3339)+"\n"), 0o600, "root", "root")
	}
}

type policy struct{ installed, candidate string }

// parsePolicy reads apt-cache policy: for each package a line "name:", then
// indented lines, of which Installed and Candidate are kept.
func parsePolicy(out string) map[string]policy {
	pol := map[string]policy{}
	cur := ""
	for _, l := range strings.Split(out, "\n") {
		if l == "" {
			continue
		}
		if l[0] != ' ' && l[0] != '\t' {
			cur = strings.TrimSuffix(strings.TrimSpace(l), ":")
			pol[cur] = policy{}
			continue
		}
		if cur == "" {
			continue
		}
		p := pol[cur]
		k, v, _ := strings.Cut(strings.TrimSpace(l), ": ")
		switch k {
		case "Installed":
			p.installed = v
		case "Candidate":
			p.candidate = v
		}
		pol[cur] = p
	}
	return pol
}

// transaction is what a simulated apt-get install would do.
type transaction struct {
	install []string // installed new, or upgraded
	remove  []string
}

// parseSimulation reads apt-get -s install: "Inst name ..." for a package
// installed or upgraded, "Remv name ..." and "Purg name ..." for one
// removed, and the list under "will be REMOVED", which apt prints before it
// stops for --no-remove.
func parseSimulation(out string) transaction {
	var t transaction
	removed := false
	add := func(list *[]string, name string) {
		name, _, _ = strings.Cut(name, ":") // name:arch
		if nameRe.MatchString(name) && !contains(*list, name) {
			*list = append(*list, name)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		if removed && strings.HasPrefix(l, "  ") {
			for _, n := range f {
				add(&t.remove, strings.TrimSuffix(n, "*"))
			}
			continue
		}
		removed = strings.HasPrefix(l, "The following packages will be REMOVED")
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "Inst":
			add(&t.install, f[1])
		case "Remv", "Purg":
			add(&t.remove, f[1])
		}
	}
	sort.Strings(t.remove)
	return t
}

// aptSays is what apt gave as its error, on one line: its "E:" lines, or
// the end of what it printed.
func aptSays(err error) string {
	var ee *system.ExitError
	if !errors.As(err, &ee) {
		return plain.Line(err.Error())
	}
	var es, last []string
	for _, l := range strings.Split(ee.Result.Stderr+"\n"+ee.Result.Stdout, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case l == "":
		case strings.HasPrefix(l, "E: "):
			es = append(es, strings.TrimPrefix(l, "E: "))
		default:
			last = append(last, l)
		}
	}
	if len(es) == 0 {
		es = last[max(0, len(last)-2):]
	}
	s := plain.Line(strings.Join(es, "; "))
	if s == "" {
		s = fmt.Sprintf("exit %d", ee.Result.ExitCode)
	}
	if r := []rune(s); len(r) > 600 {
		s = string(r[:600]) + "..."
	}
	return s
}

// record writes a request's state to the logbook: who asked for what, and
// what became of it. The line holds checked package names and fixed words,
// never the agent's reason and never what apt printed.
func record(e *engine.Env, en Entry, now time.Time) {
	what := strings.Join(en.Packages, " ")
	rec := logbook.Record{Time: now, Kind: logbook.KindInstall, Box: e.Cfg.Box, Keelbox: version.Version, Result: logbook.ResultOK}
	var detail string
	switch en.State {
	case StatePending:
		detail = en.By + " asked for " + what + "; waiting for approval"
	case StateInstalled:
		switch {
		case len(en.Installed) == 0:
			detail = "nothing to install for " + en.By + ": " + what + " already installed"
		case en.Approved:
			detail = "installed " + strings.Join(en.Installed, " ") + " for " + en.By + ", approved by the owner"
		default:
			detail = "installed " + strings.Join(en.Installed, " ") + " for " + en.By
		}
	case StateRefused:
		detail = "refused " + en.By + "'s request: " + en.code
		switch {
		case what == "":
		case en.code == codeNoCandidate || en.code == codeNotExact:
			// Names apt does not have are words the agent chose, not
			// packages: the logbook counts them.
			detail += fmt.Sprintf(" (%d named)", len(en.Packages))
		default:
			detail += " (" + what + ")"
		}
	case StateFailed:
		rec.Result, rec.Error = logbook.ResultFailed, "apt-get install failed"
		detail = "apt-get did not install " + what + " for " + en.By
	case StateDenied:
		detail = "the owner denied " + en.By + "'s request for " + what
	default:
		return
	}
	rec.Changes = []engine.Change{{Module: "install", Step: "request-" + en.ID, Detail: detail}}
	if err := logbook.Action(e.X, rec); err != nil {
		e.Printf("keelbox install: logbook: %v\n", err)
	}
}

// ErrNoRequest is returned when no request waits under an ID.
var ErrNoRequest = errors.New("no such request waiting")

// Approve marks the request with id, or with all every request that waits,
// as approved by the owner, and returns them. The service installs them at
// its next run, with the same checks as a request it takes straight from
// the agent. A request already approved and not yet installed is returned
// again, so approving twice is how to start the service once more.
func Approve(e *engine.Env, id string, all bool, o Options) ([]Entry, error) {
	var out []Entry
	err := change(e, o, func(st *State) error {
		for _, en := range st.in(StatePending, StateApproved) {
			if all || en.ID == id {
				en.State, en.Approved = StateApproved, true
				st.set(en)
				out = append(out, en)
			}
		}
		if len(out) == 0 {
			return errUnchanged
		}
		return nil
	})
	if err == nil && len(out) == 0 && !all {
		err = ErrNoRequest
	}
	return out, err
}

// Deny marks the request with id as denied; nothing is installed. A
// request that was approved and that the service has not begun to install
// can still be denied.
func Deny(e *engine.Env, id string, o Options) (Entry, error) {
	var en Entry
	err := change(e, o, func(st *State) error {
		cur := st.Find(id)
		if cur == nil || cur.State != StatePending && cur.State != StateApproved {
			return ErrNoRequest
		}
		now := o.now()
		en = *cur
		en.State, en.Decided = StateDenied, &now
		st.set(en)
		record(e, en, now)
		return nil
	})
	return en, err
}

// PendingEvent is the event that tells the owner a request waits. Its text
// is root's: the box, the checked package names and the command to run,
// never the agent's reason.
func PendingEvent(box string, en Entry, now time.Time) events.Event {
	// A message names a few packages; keelbox requests lists them all.
	const show = 6
	names := strings.Join(en.Packages, ", ")
	if len(en.Packages) > show {
		names = fmt.Sprintf("%s and %d more", strings.Join(en.Packages[:show], ", "), len(en.Packages)-show)
	}
	return events.Event{Time: now, Source: events.SourceInstall, Kind: "install-request", Level: events.Warn, Box: box,
		Key: "install:" + en.ID, Fields: map[string]string{"packages": names, "id": en.ID, "fix": "keelbox approve " + box + " " + en.ID}}
}

// StillInstalled are the recorded packages dpkg still reports installed,
// by name.
func StillInstalled(ctx context.Context, e *engine.Env) []string {
	pkgs, err := ReadInstalled(e.X)
	if err != nil || len(pkgs) == 0 {
		return nil
	}
	var names []string
	for _, p := range pkgs {
		if nameRe.MatchString(p.Name) {
			names = append(names, p.Name)
		}
	}
	have, _ := engine.InstalledPackages(ctx, e, names)
	var out []string
	for _, n := range names {
		if have[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Unlisted are the packages of installed that neither host.packages nor
// keelbox's own base list names: a box rebuilt from the config would not
// have them.
func Unlisted(installed, base []string, c *config.Config) []string {
	var out []string
	for _, n := range installed {
		if !contains(c.Host.Packages, n) && !contains(base, n) {
			out = append(out, n)
		}
	}
	return out
}

// PackagesYAML is host.packages as the config should read for a rebuilt
// box to have unlisted too: the config's own list, then those.
func PackagesYAML(c *config.Config, unlisted []string) string {
	var b strings.Builder
	b.WriteString("host:\n  packages:\n")
	for _, n := range append(append([]string(nil), c.Host.Packages...), unlisted...) {
		b.WriteString("    - " + n + "\n")
	}
	return b.String()
}

// Told reports whether apply has already said that unlisted are missing
// from host.packages, and remembers that it now has. It is apply's alone.
func Told(x system.Executor, unlisted []string) bool {
	want := strings.Join(unlisted, " ")
	if data, err := x.ReadFile(toldPath); err == nil && strings.TrimSpace(string(data)) == want {
		return true
	}
	if err := x.MkdirAll(Dir, 0o755, "root", "root"); err == nil {
		_ = x.WriteFile(toldPath, []byte(want+"\n"), 0o644, "root", "root")
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
