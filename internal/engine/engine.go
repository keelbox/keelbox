// Package engine runs modules: plan (check only), apply (converge) and
// doctor (verify). A module is an ordered list of steps, each a check plus
// an apply, and a set of verify checks for the running box.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/manifest"
	"github.com/keelbox/keelbox/internal/plain"
	"github.com/keelbox/keelbox/internal/system"
)

// Env is everything a step can see.
type Env struct {
	X     system.Executor
	Cfg   *config.Config
	M     *manifest.Manifest
	Facts Facts
	Log   io.Writer
	// Self is the running keelbox binary, which apply installs on the box.
	// Empty when unknown, which leaves the installed binary alone.
	Self string

	// Adopt is set by --adopt: the user accepts that keelbox replaces
	// conflicting software it found on the box (Ubuntu's docker.io).
	Adopt bool

	// SSHConnection is $SSH_CONNECTION of the session this run arrived
	// over ("client port server port"), passed by the remote script. Empty
	// when keelbox runs on the box directly. The public SSH steps use it as
	// the proof that the laptop reached sshd over the tailnet.
	SSHConnection string

	// SSHAddrs is where the laptop reaches this box's sshd: the address
	// that session's ssh connected to and the box's address outside the
	// tailnet, as "address:port" pairs, comma-separated, resolved on the
	// laptop (docs/adr/0069-agent-ssh-to-outside-addresses.md). The box
	// cannot see them itself when the address is translated on the way in.
	// Empty when keelbox runs on the box directly. It only ever adds to
	// what the agent user may not connect to.
	SSHAddrs string

	// AptStale is set when apt sources changed and the next package
	// install must run apt-get update first.
	AptStale bool
}

// Facts are discovered once per run.
type Facts struct {
	OSID      string // ubuntu
	OSVersion string // 26.04
	Codename  string // resolute
	Arch      string // amd64
	// InvokingUser is the account that ran sudo keelbox (SUDO_USER), the
	// one whose session this run lives in. Empty when run as root directly.
	InvokingUser string
}

// Status is the result of a check.
type Status struct {
	OK     bool
	Detail string // what differs, shown in plan output
}

// Converged and Pending build Statuses.
func Converged() Status                         { return Status{OK: true} }
func Pending(format string, args ...any) Status { return Status{Detail: fmt.Sprintf(format, args...)} }

// Step is one idempotent unit: Check must have no side effects; Apply must
// bring Check to OK.
type Step struct {
	Name  string
	Check func(ctx context.Context, e *Env) (Status, error)
	Apply func(ctx context.Context, e *Env) error
	// Soft steps do not stop the run when they fail: the failure is
	// recorded and later steps still apply. Only for steps nothing later
	// depends on, such as one agent CLI from a vendor's moving installer.
	Soft bool
	// User is the non-root account whose files or session the step
	// changes. Its failure stops only that account's later steps, which are
	// recorded as skipped; the box's own steps carry on. An account that
	// breaks its home (a planted symlink, say) therefore cannot hold back
	// sshd, firewall or agent network changes. The run still exits 1 and
	// doctor fails until the home is repaired. FileStep, DirStep, LineStep
	// and BlockStep set it from the owner.
	User string
}

// forOwner sets s.User for a step that acts as owner.
func forOwner(s Step, owner string) Step {
	if owner != "" && owner != "root" {
		s.User = owner
	}
	return s
}

// DeferredError is returned by a step's Apply when the step must not run in
// this session (for example, locking the account keelbox is running as).
// The step stays pending and is reported; the run carries on.
type DeferredError struct{ Reason string }

func (d *DeferredError) Error() string { return "deferred: " + d.Reason }

// Defer returns a DeferredError.
func Defer(format string, args ...any) error {
	return &DeferredError{Reason: fmt.Sprintf(format, args...)}
}

// Level grades a doctor finding.
type Level int

const (
	LevelOK Level = iota
	LevelWarn
	LevelFail
)

func (l Level) String() string {
	switch l {
	case LevelOK:
		return "ok"
	case LevelWarn:
		return "warn"
	default:
		return "FAIL"
	}
}

// Finding is one doctor result.
type Finding struct {
	Module string
	Check  string
	Level  Level
	Detail string
	// Flaky marks a check that depends on the network or another machine
	// (the tailnet, a backup repository): scheduled doctor alerts on it
	// only when it fails twice in a row.
	Flaky bool
}

// Module is a unit of box configuration.
type Module interface {
	Name() string
	// Enabled reports whether the config asks for this module.
	Enabled(c *config.Config) bool
	// Steps are applied in order. They may depend on earlier steps.
	Steps(e *Env) []Step
	// Verify checks the module works, beyond its files being in place.
	Verify(ctx context.Context, e *Env) []Finding
}

// Change is one step that was, or would be, applied.
type Change struct {
	Module string `json:"module"`
	Step   string `json:"step"`
	Detail string `json:"detail,omitempty"`
}

// PlanItem is one checked step.
type PlanItem struct {
	Change
	OK  bool
	Err error
}

// Plan checks every step without changing anything.
func Plan(ctx context.Context, e *Env, mods []Module) []PlanItem {
	var out []PlanItem
	for _, m := range mods {
		if !m.Enabled(e.Cfg) {
			continue
		}
		for _, s := range m.Steps(e) {
			st, err := s.Check(ctx, e)
			out = append(out, PlanItem{Change: Change{Module: m.Name(), Step: s.Name, Detail: plain.Text(st.Detail)}, OK: err == nil && st.OK, Err: err})
		}
	}
	return out
}

// Applied is the outcome of Apply.
type Applied struct {
	Changes  []Change
	Deferred []Change // steps left pending on purpose; Detail says why
	Failed   []Change // soft steps that failed; Detail is the error
	Checked  int
	Duration time.Duration
}

// ErrInterrupted is Apply's error when its context was cancelled (Ctrl-C):
// it stopped between two steps, so every step before is in place and the
// next apply carries on from the first that is not.
var ErrInterrupted = errors.New("interrupted")

// Apply converges every enabled module in order and stops at the first
// step that fails or does not converge, since later steps may depend on it.
// Deferred steps and failed soft steps are recorded and skipped. A failed
// step that acts for an account (Step.User) is recorded, and that account's
// later steps are skipped and recorded, while the rest of the box applies.
//
// Cancelling ctx stops the run before the next step, with ErrInterrupted.
// The step running then finishes first: steps get a context that is not
// cancelled, so an interrupt never leaves apt or a file half-done. A
// second Ctrl-C, which the CLI no longer catches, stops the process.
func Apply(ctx context.Context, e *Env, mods []Module) (Applied, error) {
	start := time.Now()
	var res Applied
	run := context.WithoutCancel(ctx)
	blocked := map[string]string{} // account -> the step of its that failed
	for _, m := range mods {
		if !m.Enabled(e.Cfg) {
			continue
		}
		e.printf("%s\n", m.Name())
		for _, s := range m.Steps(e) {
			if ctx.Err() != nil {
				res.Duration = time.Since(start)
				return res, fmt.Errorf("%w before %s/%s", ErrInterrupted, m.Name(), s.Name)
			}
			res.Checked++
			if first, ok := blocked[s.User]; ok && s.User != "" {
				e.printf("  skip    %s: %s's step %s failed\n", s.Name, s.User, first)
				res.Failed = append(res.Failed, Change{Module: m.Name(), Step: s.Name, Detail: "skipped: " + s.User + "'s step " + first + " failed"})
				continue
			}
			change, err := applyStep(run, e, m.Name(), s)
			var d *DeferredError
			switch {
			case errors.As(err, &d):
				e.printf("  defer   %s: %s\n", s.Name, d.Reason)
				res.Deferred = append(res.Deferred, Change{Module: m.Name(), Step: s.Name, Detail: d.Reason})
			case err != nil && s.Soft:
				e.printf("  FAILED  %s: %v\n", s.Name, err)
				res.Failed = append(res.Failed, Change{Module: m.Name(), Step: s.Name, Detail: plain.Text(err.Error())})
			case err != nil && s.User != "":
				e.printf("  FAILED  %s: %v (%s's later steps are skipped; the rest of the box applies)\n", s.Name, err, s.User)
				res.Failed = append(res.Failed, Change{Module: m.Name(), Step: s.Name, Detail: plain.Text(err.Error())})
				blocked[s.User] = m.Name() + "/" + s.Name
			case err != nil:
				res.Duration = time.Since(start)
				return res, err
			case change != nil:
				res.Changes = append(res.Changes, *change)
			}
		}
	}
	res.Duration = time.Since(start)
	return res, nil
}

// applyStep converges one step and returns the change made, if any.
func applyStep(ctx context.Context, e *Env, module string, s Step) (*Change, error) {
	st, err := s.Check(ctx, e)
	if err != nil {
		return nil, fmt.Errorf("%s/%s: check: %w", module, s.Name, err)
	}
	if st.OK {
		e.printf("  ok      %s\n", s.Name)
		return nil, nil
	}
	e.printf("  change  %s: %s\n", s.Name, st.Detail)
	if err := s.Apply(ctx, e); err != nil {
		var d *DeferredError
		if errors.As(err, &d) {
			return nil, err
		}
		return nil, fmt.Errorf("%s/%s: %w", module, s.Name, err)
	}
	after, err := s.Check(ctx, e)
	if err != nil {
		return nil, fmt.Errorf("%s/%s: re-check: %w", module, s.Name, err)
	}
	if !after.OK {
		return nil, fmt.Errorf("%s/%s: still not converged after apply: %s", module, s.Name, after.Detail)
	}
	return &Change{Module: module, Step: s.Name, Detail: plain.Text(st.Detail)}, nil
}

// Doctor runs every enabled module's verify checks, and reports drift
// (steps apply would change) as warnings.
func Doctor(ctx context.Context, e *Env, mods []Module) []Finding {
	var out []Finding
	for _, m := range mods {
		if !m.Enabled(e.Cfg) {
			continue
		}
		for _, s := range m.Steps(e) {
			st, err := s.Check(ctx, e)
			switch {
			case err != nil && s.User != "":
				// apply skips this account's steps until it is fixed.
				out = append(out, Finding{Module: m.Name(), Check: s.Name, Level: LevelFail, Detail: "check failed in " + s.User + "'s files: " + err.Error()})
			case err != nil:
				out = append(out, Finding{Module: m.Name(), Check: s.Name, Level: LevelWarn, Detail: "check failed: " + err.Error()})
			case !st.OK:
				out = append(out, Finding{Module: m.Name(), Check: s.Name, Level: LevelWarn, Detail: "drift, apply would change: " + st.Detail})
			}
		}
		out = append(out, m.Verify(ctx, e)...)
	}
	// Details carry program output, some of it the agent's (a CLI's
	// --version, a tool's error): printed plain wherever they go.
	for i := range out {
		out[i].Detail = plain.Text(out[i].Detail)
	}
	return out
}

// Failed reports whether any finding is a failure.
func Failed(fs []Finding) bool {
	for _, f := range fs {
		if f.Level == LevelFail {
			return true
		}
	}
	return false
}

func (e *Env) printf(format string, args ...any) {
	if e.Log != nil {
		fmt.Fprint(e.Log, plain.Text(fmt.Sprintf(format, args...)))
	}
}

// Printf writes progress output.
func (e *Env) Printf(format string, args ...any) { e.printf(format, args...) }

// AgentUser is the agent account with its home resolved; before the account
// exists the home is the useradd default.
func (e *Env) AgentUser() system.User { return e.user(e.Cfg.Users.Agent.Name) }

// FS is file access as the account that owns a managed path: root's for
// root ("" means root), otherwise a process running as that account. Steps
// that touch a file a non-root account owns, or a path in its home, go
// through the owner's FS, so a symlink the account planted gets apply no
// further than the account could go itself (ADR 0015).
func (e *Env) FS(owner string) system.FS {
	if owner == "" || owner == "root" {
		return e.X
	}
	return e.X.UserFS(owner)
}

// AgentTool is the path of the agent's own tool name as the agent's shell
// finds it (system.AccountLookPath), for running a tool the agent installed,
// as the agent: a vendor-installed CLI or a mise shim. With nothing found it
// returns name, which a command resolves in system.AccountPath only.
func (e *Env) AgentTool(name string) string {
	a := e.AgentUser()
	if p, err := system.AccountLookPath(e.X.UserFS(a.Name), a.Home, name); err == nil {
		return p
	}
	return name
}

// AdminUser is the admin account, resolved the same way.
func (e *Env) AdminUser() system.User { return e.user(e.Cfg.Users.Admin.Name) }

func (e *Env) user(name string) system.User {
	u, err := e.X.LookupUser(name)
	if err != nil {
		return system.User{Name: name, UID: -1, GID: -1, Home: "/home/" + name}
	}
	return u
}

// GatherFacts reads the OS identity and architecture.
func GatherFacts(ctx context.Context, x system.Executor) (Facts, error) {
	data, err := x.ReadFile("/etc/os-release")
	if err != nil {
		return Facts{}, fmt.Errorf("read /etc/os-release: %w", err)
	}
	kv := ParseEnvFile(string(data))
	f := Facts{OSID: kv["ID"], OSVersion: kv["VERSION_ID"], Codename: kv["VERSION_CODENAME"]}
	if f.Codename == "" {
		f.Codename = kv["UBUNTU_CODENAME"]
	}
	arch, err := system.Output(ctx, x, system.C("dpkg", "--print-architecture"))
	if err != nil {
		return f, fmt.Errorf("dpkg --print-architecture: %w", err)
	}
	f.Arch = arch
	return f, nil
}

// ErrUnsupported is returned for platforms outside the manifest matrix.
var ErrUnsupported = errors.New("unsupported platform")

// CheckPlatform refuses platforms the manifest was not tested on.
func CheckPlatform(f Facts, m *manifest.Manifest) error {
	if f.OSID != "ubuntu" {
		return fmt.Errorf("%w: keelbox supports Ubuntu only, found %q", ErrUnsupported, f.OSID)
	}
	if !m.Supports(f.OSVersion, f.Arch) {
		return fmt.Errorf("%w: Ubuntu %s on %s is not in release %s (supported: Ubuntu %s on %s)",
			ErrUnsupported, f.OSVersion, f.Arch, m.Release, strings.Join(m.OS.Ubuntu, ", "), strings.Join(m.OS.Arch, ", "))
	}
	if f.Codename == "" {
		return fmt.Errorf("%w: could not read the Ubuntu codename", ErrUnsupported)
	}
	return nil
}

// ParseEnvFile parses KEY=value lines, stripping quotes.
func ParseEnvFile(s string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[k] = strings.Trim(v, `"'`)
	}
	return out
}
