package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
	"github.com/keelbox/keelbox/internal/runs"
	"github.com/keelbox/keelbox/internal/ui"
)

// The laptop's half of a detached apply (docs/adr/0063-detached-apply.md):
// start the run on the box, or find it, then follow its log over SSH and
// show it as an attached apply would. The run does not depend on this
// process: Ctrl-C asks whether to leave it running or stop it after the
// current step, a lost connection is retried, and the next keelbox up,
// apply or status picks it up again.

// exitDetached is the exit code when keelbox stops following a run that
// carries on on the box: left running at Ctrl-C, or the connection lost.
const exitDetached = 4

// lostFor is how long a follower tries to reach the box again.
var lostFor = 3 * time.Minute

// retryEvery is the wait between those tries.
var retryEvery = 5 * time.Second

// runBox is what the follower does on the box; provider.SSH in use, a fake
// in tests.
type runBox interface {
	// Output runs a short command and returns its stdout.
	Output(ctx context.Context, remote string) (string, error)
	// Follow streams a long command's stdout to w until it ends or ctx is
	// cancelled.
	Follow(ctx context.Context, remote string, w io.Writer) error
	// Stop asks the run to stop after its current step.
	Stop(ctx context.Context, id string) error
}

// sshRunBox is runBox over provider.SSH.
type sshRunBox struct{ s *provider.SSH }

func (b sshRunBox) Output(ctx context.Context, remote string) (string, error) {
	return b.s.Output(ctx, remote)
}

func (b sshRunBox) Follow(ctx context.Context, remote string, w io.Writer) error {
	return b.s.Follow(ctx, remote, w)
}

func (b sshRunBox) Stop(ctx context.Context, id string) error {
	cmd, err := runs.StopCommand(id)
	if err != nil {
		return err
	}
	if !b.s.AskSudo {
		_, err := b.s.Output(ctx, "sudo -n "+cmd)
		return err
	}
	// This account's sudo asks for its password: on the terminal, as
	// for the start (ADR 0049).
	return b.s.Interactive(ctx, "sudo "+cmd, os.Stdin, os.Stdout, os.Stderr)
}

// follower follows one box's runs.
type follower struct {
	box    runBox
	cfg    *config.Config
	o      options
	user   string // the account it logs in as
	stdout io.Writer
	stderr io.Writer
	// term is where a question goes and where answers come from; nil
	// without a terminal, when Ctrl-C leaves the run.
	term *prompt.Prompter
	// sig delivers Ctrl-C (and SIGTERM) while following.
	sig  chan os.Signal
	skew time.Duration // this machine's clock minus the box's
}

func newFollower(s *provider.SSH, cfg *config.Config, o options, stdout, stderr io.Writer) *follower {
	f := &follower{box: sshRunBox{s}, cfg: cfg, o: o, user: s.User, stdout: stdout, stderr: stderr}
	if prompt.IsTerminal(os.Stdin) {
		out := io.Writer(os.Stdout)
		if !prompt.IsTerminal(os.Stdout) {
			out = os.Stderr
		}
		f.term = followPrompt(out)
	}
	return f
}

// again is the command that picks a run up again.
func (f *follower) again() string {
	if f.o.inUp {
		return "keelbox up " + f.cfg.Box
	}
	return "keelbox apply " + f.cfg.Box
}

// boxRun is the newest run on the box, as StateScript sees it.
type boxRun struct {
	id, state string
}

// state reads the newest run's id and state, and the box's clock.
func (f *follower) state(ctx context.Context) (boxRun, error) {
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	out, err := f.box.Output(cctx, "sh -c "+quoteSh(runs.StateScript()))
	if err != nil {
		return boxRun{}, err
	}
	now, id, st, err := runs.ParseState(out)
	if err != nil {
		return boxRun{}, err
	}
	f.skew = time.Until(now)
	return boxRun{id, st}, nil
}

// applyDetached is runSSH's apply: pick up a run on the box, or start one,
// and follow it.
func applyDetached(ctx context.Context, cfg *config.Config, s *provider.SSH, o options, stdout, stderr io.Writer) int {
	if err := s.TrustHost(ctx); err != nil {
		return exitCode(sshFailure(ctx, err, cfg, s, o, "apply"), stderr)
	}
	f := newFollower(s, cfg, o, stdout, stderr)
	if code, done := f.rejoin(ctx, true); done {
		return code
	}
	if o.askSudo {
		// Said on the terminal itself: stdout may be a buffer.
		fmt.Fprintf(os.Stderr, "keelbox: sudo on %s asks for %s's password (keelbox never sees it)\n", s.Host, s.User)
	}
	var out bytes.Buffer
	s.Stdout = &out
	err := s.Run(ctx, "apply", cfg, remoteExtra(o)...)
	s.Stdout = stdout
	id, already := "", false
	for _, l := range strings.Split(out.String(), "\n") {
		if rest, ok := strings.CutPrefix(l, runMarker+" "); ok {
			how, rid, _ := strings.Cut(rest, " ")
			if runs.ValidID(rid) {
				id, already = rid, how == "running"
			}
		} else if strings.TrimSpace(l) != "" {
			fmt.Fprintln(stdout, l)
		}
	}
	if err != nil {
		return exitCode(sshFailure(ctx, err, cfg, s, o, "apply"), stderr)
	}
	if id == "" {
		failf(stderr, "the box did not say which apply it started; see the box's journal: keelbox ssh %s, then journalctl -u '%s*'", cfg.Box, runs.UnitPrefix)
		return 1
	}
	if already {
		fmt.Fprintf(stdout, "keelbox: an apply is already running on %s; following it instead of starting another\n", cfg.Box)
	}
	if _, err := f.state(ctx); err != nil {
		f.skew = 0
	}
	return f.follow(ctx, id)
}

// rejoin picks up a run on the box before a command goes on: a running one
// is followed to its end; one this machine started and did not see end is
// reported once. done is set when the command has its result: the run's,
// for an apply. A run that died is reported and, for an apply, done is
// false: a new run carries on from where it stopped.
func (f *follower) rejoin(ctx context.Context, applying bool) (code int, done bool) {
	st, err := f.state(ctx)
	if err != nil || st.id == "-" {
		// Not known here: the start itself finds a running apply.
		return 0, false
	}
	if runs.Running(st.state) {
		fmt.Fprintf(f.stdout, "keelbox: setup is running on %s; following it (run %s)\n", f.cfg.Box, st.id)
		return f.follow(ctx, st.id), true
	}
	note, ok := loadRunNote(f.cfg.Box)
	if !ok || note.ID != st.id {
		return 0, false
	}
	// This machine started (or followed) that run and did not see it end.
	var buf bytes.Buffer
	script, _ := runs.FollowScript(st.id, 0)
	if err := f.box.Follow(ctx, script, &buf); err != nil {
		return 0, false
	}
	recs, _ := runs.Parse(buf.Bytes())
	sum := runs.Summarise(recs)
	forgetRunNote(f.cfg.Box)
	now := time.Now().Add(-f.skew)
	if sum.Done {
		w := f.stdout
		if sum.Code != 0 {
			w = f.stderr
			for _, r := range recs {
				if r.Kind == runs.KindErr {
					fmt.Fprintln(f.stderr, r.Text)
				}
			}
		}
		fmt.Fprintf(w, "keelbox: setup on %s %s\n", f.cfg.Box, sum.Describe(now))
		if !applying {
			return 0, false
		}
		return sum.Code, true
	}
	fmt.Fprintf(f.stderr, "keelbox: the last setup run on %s %s.\n", f.cfg.Box, strings.TrimPrefix(sum.Describe(now), "interrupted: "))
	if applying {
		fmt.Fprintln(f.stderr, "keelbox: every step before it is in place; applying again carries on from there")
	}
	return 0, false
}

// follow shows a run's log from its start and follows it to its end. The
// result is the run's exit code, exitDetached when it is left running.
func (f *follower) follow(ctx context.Context, id string) int {
	saveRunNote(f.cfg.Box, id)
	bg := context.WithoutCancel(ctx)
	if f.sig == nil {
		f.sig = make(chan os.Signal, 2)
	}
	signal.Notify(f.sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(f.sig)
	// The question replaces the note the first Ctrl-C would print.
	note := *interruptNote.Load()
	setInterruptNote("")
	defer setInterruptNote(note)

	v := &runView{f: f}
	defer v.close()
	var offset int64
	var lost time.Time
	stopping, final, stalls := false, false, 0
	for {
		sctx, cancel := context.WithCancel(bg)
		rs := &recordStream{take: v.take}
		script, err := runs.FollowScript(id, offset)
		if err != nil {
			cancel()
			fail(f.stderr, err)
			return 1
		}
		done := make(chan error, 1)
		began := time.Now()
		go func() { done <- f.box.Follow(sctx, script, rs) }()
		var ferr error
	wait:
		for {
			select {
			case ferr = <-done:
				break wait
			case sig := <-f.sig:
				if sig != os.Interrupt || f.leaveOrStop(bg, v, id, &stopping) {
					cancel()
					if err := <-done; err != nil && f.o.verbose {
						fmt.Fprintln(f.stderr, "keelbox: stopped following:", err)
					}
					return f.left(v, id, false)
				}
			}
		}
		cancel()
		offset += rs.taken()
		if end := v.ended(); end != nil {
			v.close()
			forgetRunNote(f.cfg.Box)
			return end.Code
		}
		// The stream ended before the run's end record: ask the box how
		// the run is, until it answers again.
		st, qerr := f.state(bg)
		for ; qerr != nil; st, qerr = f.state(bg) {
			if lost.IsZero() {
				lost = time.Now()
				v.note(fmt.Sprintf("keelbox: lost the connection to %s; setup carries on there. Trying again for %s (Ctrl-C to stop trying)", f.cfg.Box, ui.Duration(lostFor)))
			}
			if time.Since(lost) > lostFor {
				return f.left(v, id, true)
			}
			select {
			case <-time.After(retryEvery):
			case <-f.sig:
				return f.left(v, id, false)
			}
		}
		if !lost.IsZero() {
			lost = time.Time{}
			v.note("keelbox: reconnected to " + f.cfg.Box)
		}
		switch {
		case st.id == "-":
			v.note(fmt.Sprintf("keelbox: %s cannot read the box's run log (not in the sudo group, and sudo asks for a password)", f.user))
			return f.left(v, id, false)
		case st.id == id && runs.Running(st.state):
			if rs.taken() > 0 || time.Since(began) > 10*time.Second {
				stalls = 0
				continue
			}
			// Connected, the run going, and nothing read: the log could
			// not be read. Give it a few tries, then leave it.
			if stalls++; stalls > 3 {
				v.note(fmt.Sprintf("keelbox: cannot follow the run's log: %v", ferr))
				return f.left(v, id, false)
			}
			time.Sleep(retryEvery)
			continue
		case !final:
			// Not running: read once more for the lines it wrote as it
			// ended.
			final = true
			continue
		}
		return f.died(v, id)
	}
}

// leaveOrStop asks, at Ctrl-C, whether to leave the run going or stop it
// after its current step. Without a terminal, or at a Ctrl-C after a stop
// was asked for, it leaves; SIGTERM and a closed terminal (SIGHUP) leave
// without asking. It returns true to stop following.
func (f *follower) leaveOrStop(ctx context.Context, v *runView, id string, stopping *bool) bool {
	if f.term == nil || *stopping {
		return true
	}
	leave := true
	v.hold(func() {
		next := make(chan struct{})
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			select {
			case <-f.sig:
				close(next)
			case <-stop:
			}
		}()
		fmt.Fprintln(f.term.Out())
		c, err := f.term.OnInterrupt(next).Choose(fmt.Sprintf("Setup carries on on %s without this window. What now?", f.cfg.Box), []prompt.Choice{
			{Key: "leave", Label: "Leave it running", Detail: "pick it up again: " + f.again()},
			{Key: "stop", Label: "Stop it after the current step", Detail: "nothing is left half-done; the next apply carries on"},
		}, 0)
		if err != nil || c == 0 {
			return
		}
		leave = false
		if err := f.box.Stop(ctx, id); err != nil {
			fmt.Fprintf(f.term.Out(), "keelbox: could not stop it: %v; it carries on\n", err)
			return
		}
		*stopping = true
		fmt.Fprintf(f.term.Out(), "keelbox: stopping %s's setup after the current step\n", f.cfg.Box)
	})
	return leave
}

// left ends following a run that carries on.
func (f *follower) left(v *runView, id string, lost bool) int {
	module, step := v.leave()
	at := ""
	if module != "" {
		at = " (now at " + module
		if step != "" {
			at += "/" + step
		}
		at += ")"
	}
	why := "Left setup running on " + f.cfg.Box
	if lost {
		why = fmt.Sprintf("Lost the connection to %s for %s; setup carries on there", f.cfg.Box, ui.Duration(lostFor))
	}
	fmt.Fprintf(f.stderr, "keelbox: %s%s; closing this window or losing the connection does not stop it.\n", why, at)
	fmt.Fprintf(f.stderr, "keelbox: pick it up again, or see how it ended: %s\n", f.again())
	return exitDetached
}

// died reports a run that stopped without finishing.
func (f *follower) died(v *runView, id string) int {
	v.close()
	forgetRunNote(f.cfg.Box)
	at := ""
	if s := v.summary(); s.At() != "" {
		at = " after " + s.At()
	}
	failf(f.stderr, "setup on %s stopped without finishing%s: the box restarted, or keelbox was stopped there. Every step before it is in place; carry on: %s", f.cfg.Box, at, f.again())
	return 1
}

// runView shows a followed run as an attached apply shows itself: the
// progress view on a terminal, every line otherwise. It is made at the
// first record, with the modules the run said it applies.
type runView struct {
	f    *follower
	mu   sync.Mutex
	recs []runs.Record
	end  *runs.Record
	out  io.Writer
	err  io.Writer
	p    *ui.Progress
	shut bool
}

func (v *runView) open(modules []string) {
	o, f := v.f.o, v.f
	if !o.style.Live || o.verbose {
		v.out, v.err = f.stdout, f.stderr
		return
	}
	if len(modules) == 0 {
		modules = moduleNames(f.cfg)
	}
	v.p = ui.NewProgress(f.stdout, o.style, modules).Start()
	v.out, v.err = v.p, v.p.Err(f.stderr)
}

// take shows one record.
func (v *runView) take(r runs.Record) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.shut {
		return
	}
	if v.out == nil {
		v.open(r.Modules)
	}
	v.recs = append(v.recs, r)
	if r.Kind == runs.KindEnd {
		rc := r
		v.end = &rc
	}
	line, stderr, ok := r.Line()
	switch {
	case !ok:
	case stderr:
		fmt.Fprintln(v.err, line)
	case v.p != nil:
		_, _ = v.p.WriteAt(r.Time.Add(v.f.skew), []byte(line+"\n"))
	default:
		fmt.Fprintln(v.out, line)
	}
}

func (v *runView) ended() *runs.Record {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.end
}

func (v *runView) summary() runs.Summary {
	v.mu.Lock()
	defer v.mu.Unlock()
	return runs.Summarise(v.recs)
}

// note is keelbox's own line, above the live view.
func (v *runView) note(s string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	w := v.err
	if w == nil {
		w = v.f.stderr
	}
	fmt.Fprintln(w, s)
}

// hold pauses the view while fn asks a question.
func (v *runView) hold(fn func()) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.p != nil {
		v.p.Hold(fn)
		return
	}
	fn()
}

// close settles the view.
func (v *runView) close() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.shut {
		return
	}
	v.shut = true
	if v.p != nil {
		v.p.Close()
	}
}

// leave stops the view without settling the running module, and returns
// it and its step.
func (v *runView) leave() (string, string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.shut {
		return "", ""
	}
	v.shut = true
	if v.p != nil {
		return v.p.Leave()
	}
	s := runs.Summarise(v.recs)
	return s.Module, s.Step
}

// recordStream parses a run log as it arrives, one record per complete
// line, and counts the bytes taken so a new stream starts after them.
type recordStream struct {
	mu   sync.Mutex
	buf  []byte
	n    int64
	take func(runs.Record)
}

func (s *recordStream) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf = append(s.buf, b...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			break
		}
		line := s.buf[:i]
		s.buf = s.buf[i+1:]
		s.n += int64(i + 1)
		var r runs.Record
		if json.Unmarshal(line, &r) == nil {
			s.take(r)
		}
	}
	return len(b), nil
}

func (s *recordStream) taken() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// runNote is the run this machine started or followed on a box and has not
// seen end, kept in boxes/<box>.run.json, so the next command reports how it
// ended, once.
type runNote struct {
	ID      string    `json:"id"`
	Started time.Time `json:"started"`
}

func runNotePath(box string) string { return filepath.Join(boxesDir(), box+".run.json") }

func saveRunNote(box, id string) {
	if n, ok := loadRunNote(box); ok && n.ID == id {
		return
	}
	data, _ := json.Marshal(runNote{ID: id, Started: time.Now().UTC()})
	if os.MkdirAll(boxesDir(), 0o700) == nil {
		_ = os.WriteFile(runNotePath(box), append(data, '\n'), 0o600)
	}
}

func loadRunNote(box string) (runNote, bool) {
	data, err := os.ReadFile(runNotePath(box))
	if err != nil {
		return runNote{}, false
	}
	var n runNote
	if json.Unmarshal(data, &n) != nil || !runs.ValidID(n.ID) {
		return runNote{}, false
	}
	return n, true
}

func forgetRunNote(box string) { _ = os.Remove(runNotePath(box)) }
