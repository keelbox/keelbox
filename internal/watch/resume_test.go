package watch

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/system"
)

const sessionID = "1c478115-31e5-4bdf-969c-92af7b92c176"

// writeClaudeSession writes Claude Code's own note on a running process.
func writeClaudeSession(t *testing.T, b *box, pid int, content string) {
	t.Helper()
	if err := b.UserFS("agent").WriteFile("/home/agent/.claude/sessions/"+strconv.Itoa(pid)+".json", []byte(content), 0o600, "agent", "agent"); err != nil {
		t.Fatal(err)
	}
}

func bootIs(t *testing.T, b *box, id string) {
	t.Helper()
	if err := b.WriteFile("/proc/sys/kernel/random/boot_id", []byte(id+"\n"), 0o444, "root", "root"); err != nil {
		t.Fatal(err)
	}
}

func readResume(t *testing.T, b *box) (Resume, bool) {
	t.Helper()
	data, err := b.ReadFile(ResumePath)
	if err != nil {
		return Resume{}, false
	}
	var r Resume
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r, true
}

// Before a reboot, each pane where Claude Code or Codex runs is noted with
// the command that opens its conversation again, keeping the flags that say
// how the agent works. Other programs, and agent CLIs that are not in a
// conversation, are left out.
func TestRestartNotesConversations(t *testing.T) {
	b := rebootBox(t, "")
	b.OK("systemd-run ", "")
	bootIs(t, b, "boot-1")
	b.OK("(as agent) tmux list-panes", "main:1.1\t100\t/home/agent/app\nwork:2.1\t200\t/home/agent/api\nmain:1.2\t300\t/home/agent\nmain:2.1\t400\t/home/agent\nmain:3.1\t500\t/home/agent/old\n")
	b.OK("ps -u agent", "  100     1 -bash\n  101   100 claude --dangerously-skip-permissions --model opus fix the tests\n"+
		"  200     1 -bash\n  201   200 codex -m gpt-5 --search --yolo\n  300     1 -bash\n  301   300 vim notes\n"+
		"  400     1 claude remote-control\n  500     1 -bash\n  501   500 /home/agent/.local/bin/claude\n")
	writeClaudeSession(t, b, 101, `{"sessionId":"`+sessionID+`","status":"idle"}`)
	if err := Restart(context.Background(), b.e, "asked", night); err != nil {
		t.Fatal(err)
	}
	r, ok := readResume(t, b)
	if !ok {
		t.Fatalf("nothing noted: %v", b.Commands())
	}
	want := []ResumePane{
		{Pane: "main:1.1", Dir: "/home/agent/app", Command: []string{"claude", "--resume", sessionID, "--dangerously-skip-permissions", "--model", "opus"}},
		{Pane: "work:2.1", Dir: "/home/agent/api", Command: []string{"codex", "resume", "--last", "-m", "gpt-5", "--search"}},
		// Without Claude Code's note: the directory's latest conversation.
		{Pane: "main:3.1", Dir: "/home/agent/old", Command: []string{"claude", "--continue"}},
	}
	if !reflect.DeepEqual(r.Panes, want) || r.Boot != "boot-1" || !r.Saved.Equal(night) {
		t.Errorf("noted %+v", r)
	}
	if f, _ := b.Stat(ResumePath); f.Mode.Perm() != 0o600 {
		t.Errorf("mode %v", f.Mode)
	}
}

// After the reboot, each pane that is back at a shell gets its command
// typed, with a cd first when it came back elsewhere. A pane not back yet
// is tried at the next run.
func TestResumeAfterTheReboot(t *testing.T) {
	b := rebootBox(t, "")
	bootIs(t, b, "boot-1")
	writeResume(t, b, Resume{Saved: night, Boot: "boot-1", Panes: []ResumePane{
		{Pane: "main:1.1", Dir: "/home/agent/app", Command: []string{"claude", "--resume", sessionID}},
		{Pane: "work:2.1", Dir: "/home/agent/my app", Command: []string{"codex", "resume", "--last"}},
		{Pane: "late:1.1", Dir: "/home/agent", Command: []string{"claude", "--continue"}},
	}})
	b.OK("(as agent) tmux list-panes", "main:1.1\tbash\t/home/agent/app\nwork:2.1\tbash\t/home/agent\n")
	ctx := context.Background()

	resumeConversations(ctx, b.e, night.Add(time.Minute))
	if b.Ran("(as agent) tmux send-keys") {
		t.Fatal("typed before the reboot")
	}

	bootIs(t, b, "boot-2")
	resumeConversations(ctx, b.e, night.Add(2*time.Minute))
	typed := sent(b)
	want := []string{
		"-t main:1.1 -l -- claude --resume " + sessionID, "-t main:1.1 Enter",
		"-t work:2.1 -l -- cd -- '/home/agent/my app' && codex resume --last", "-t work:2.1 Enter",
	}
	if !reflect.DeepEqual(typed, want) {
		t.Errorf("typed %q", typed)
	}
	if r, _ := readResume(t, b); len(r.Panes) != 1 || r.Panes[0].Pane != "late:1.1" {
		t.Errorf("left %+v", r.Panes)
	}

	// A pane that is back but runs something else is not typed into.
	b.OK("(as agent) tmux list-panes", "late:1.1\tvim\t/home/agent\n")
	resumeConversations(ctx, b.e, night.Add(4*time.Minute))
	if len(sent(b)) != len(want) {
		t.Errorf("typed into a busy pane: %q", sent(b))
	}

	// It gives up once the panes have had their time.
	resumeConversations(ctx, b.e, night.Add(resumeFor+time.Minute))
	if _, ok := readResume(t, b); ok {
		t.Error("the note outlived its time")
	}
}

// A box that was off for days resumes too: the time runs from the boot,
// not from the note.
func TestResumeAfterDaysOff(t *testing.T) {
	b := rebootBox(t, "")
	bootIs(t, b, "boot-2")
	writeResume(t, b, Resume{Saved: night.Add(-72 * time.Hour), Boot: "boot-1", Panes: []ResumePane{
		{Pane: "main:1.1", Dir: "/home/agent/app", Command: []string{"claude", "--resume", sessionID}},
	}})
	up := night.Add(-3 * time.Minute)
	if err := b.WriteFile("/proc/stat", []byte("cpu  1 2 3\nbtime "+strconv.FormatInt(up.Unix(), 10)+"\n"), 0o444, "root", "root"); err != nil {
		t.Fatal(err)
	}
	b.OK("(as agent) tmux list-panes", "main:1.1\tbash\t/home/agent/app\n")
	resumeConversations(context.Background(), b.e, night)
	if got := sent(b); len(got) != 2 || got[0] != "-t main:1.1 -l -- claude --resume "+sessionID {
		t.Errorf("typed %q", got)
	}
	if _, ok := readResume(t, b); ok {
		t.Error("note kept after every pane resumed")
	}
}

// At an orderly shutdown keelbox-conversations.service notes the
// conversations, whoever started the reboot. A stop within a boot, and a
// box without session recovery, note nothing and leave the last note.
func TestNoteAtShutdown(t *testing.T) {
	setup := func(t *testing.T, extra, state string) *box {
		b := rebootBox(t, extra)
		bootIs(t, b, "boot-1")
		b.On("systemctl is-system-running", system.Result{Stdout: state + "\n", ExitCode: 1})
		b.OK("(as agent) tmux list-panes", "main:1.1\t100\t/home/agent/app\n")
		b.OK("ps -u agent", "  100     1 -bash\n  101   100 claude --model opus\n")
		writeClaudeSession(t, b, 101, `{"sessionId":"`+sessionID+`"}`)
		return b
	}
	ctx := context.Background()

	b := setup(t, "", "stopping")
	if got := NoteAtShutdown(ctx, b.e, night); !strings.HasPrefix(got, "1 conversation(s) noted") {
		t.Errorf("said %q", got)
	}
	r, ok := readResume(t, b)
	if !ok || len(r.Panes) != 1 || r.Boot != "boot-1" || !reflect.DeepEqual(r.Panes[0].Command, []string{"claude", "--resume", sessionID, "--model", "opus"}) {
		t.Errorf("noted %+v", r)
	}

	for _, c := range []string{"(as agent) tmux list-panes", "(as agent) tmux has-session", "(as agent) /home/agent/.tmux/plugins/tmux-resurrect/scripts/save.sh quiet"} {
		if l := lastCall(b, c); l.Name == "" || !l.NoScope {
			t.Errorf("%s: not run, or in a scope at shutdown, which systemd refuses: %+v", c, l)
		}
	}

	// tmux cannot be asked: keelbox's own note, taken before its reboot,
	// stays.
	b = setup(t, "", "stopping")
	b.Fail("(as agent) tmux list-panes")
	writeResume(t, b, Resume{Saved: night.Add(-time.Minute), Boot: "boot-1", Panes: []ResumePane{{Pane: "main:1.1", Command: []string{"claude", "--continue"}}}})
	if got := NoteAtShutdown(ctx, b.e, night); !strings.HasPrefix(got, "nothing noted, the last note kept: listing the tmux panes") {
		t.Errorf("said %q", got)
	}
	if r, ok := readResume(t, b); !ok || len(r.Panes) != 1 {
		t.Errorf("the note before the reboot was lost: %+v", r)
	}

	// The box goes down again before the first watch run replayed the
	// last boot's note: no pane runs an agent CLI yet, and the note stays.
	b = setup(t, "", "stopping")
	b.OK("(as agent) tmux list-panes", "main:1.1\t100\t/home/agent/app\n")
	b.OK("ps -u agent", "  100     1 -bash\n")
	writeResume(t, b, Resume{Saved: night.Add(-10 * time.Minute), Boot: "boot-0", Panes: []ResumePane{{Pane: "main:1.1", Command: []string{"claude", "--continue"}}}})
	NoteAtShutdown(ctx, b.e, night)
	if r, ok := readResume(t, b); !ok || r.Boot != "boot-0" {
		t.Errorf("an unreplayed note was dropped: %+v", r)
	}

	b = setup(t, "", "running")
	writeResume(t, b, Resume{Saved: night.Add(-time.Hour), Boot: "boot-0"})
	if got := NoteAtShutdown(ctx, b.e, night); !strings.Contains(got, "not shutting down (running)") {
		t.Errorf("said %q", got)
	}
	if r, _ := readResume(t, b); r.Boot != "boot-0" {
		t.Errorf("a stop within the boot replaced the note: %+v", r)
	}

	b = setup(t, "sessions:\n  recovery: false\n", "stopping")
	if got := NoteAtShutdown(ctx, b.e, night); !strings.Contains(got, "sessions.recovery is off") {
		t.Errorf("said %q", got)
	}
	if _, ok := readResume(t, b); ok {
		t.Error("noted without session recovery")
	}
}

// A soft reboot keeps the boot id and time, but the agent's tmux server
// starts again with the panes: that is a reboot for the resume.
func TestResumeAfterSoftReboot(t *testing.T) {
	b := rebootBox(t, "")
	bootIs(t, b, "boot-1")
	if err := b.WriteFile("/proc/stat", []byte("btime "+strconv.FormatInt(night.Add(-48*time.Hour).Unix(), 10)+"\n"), 0o444, "root", "root"); err != nil {
		t.Fatal(err)
	}
	writeResume(t, b, Resume{Saved: night, Boot: "boot-1", Panes: []ResumePane{{Pane: "main:1.1", Dir: "/home/agent", Command: []string{"claude", "--continue"}}}})
	b.OK("(as agent) tmux list-panes", "main:1.1\tbash\t/home/agent\n")
	b.OK("(as agent) tmux display-message -p #{start_time}", strconv.FormatInt(night.Add(-time.Hour).Unix(), 10)+"\n")
	resumeConversations(context.Background(), b.e, night.Add(time.Minute))
	if len(sent(b)) != 0 {
		t.Fatalf("typed into a server that was there before the note: %q", sent(b))
	}
	b.OK("(as agent) tmux display-message -p #{start_time}", strconv.FormatInt(night.Add(time.Minute).Unix(), 10)+"\n")
	resumeConversations(context.Background(), b.e, night.Add(2*time.Minute))
	if got := sent(b); len(got) != 2 || got[0] != "-t main:1.1 -l -- claude --continue" {
		t.Errorf("typed %q", got)
	}
}

// lastCall is the last command run that starts with prefix.
func lastCall(b *box, prefix string) system.Cmd {
	var last system.Cmd
	for _, c := range b.Calls {
		if strings.HasPrefix(c.String(), prefix) {
			last = c
		}
	}
	return last
}

func writeResume(t *testing.T, b *box, r Resume) {
	t.Helper()
	data, _ := json.Marshal(r)
	if err := b.WriteFile(ResumePath, data, 0o600, "root", "root"); err != nil {
		t.Fatal(err)
	}
}

// sent is what was typed into panes, as send-keys's arguments.
func sent(b *box) []string {
	var out []string
	for _, c := range b.Commands() {
		if rest, ok := strings.CutPrefix(c, "(as agent) tmux send-keys "); ok {
			out = append(out, rest)
		}
	}
	return out
}

func TestShellLine(t *testing.T) {
	got := shellLine([]string{"claude", "--model", "it's", "--add-dir", "/a b", "--x=y"})
	if want := `claude --model 'it'\''s' --add-dir '/a b' --x=y`; got != want {
		t.Errorf("got %s", got)
	}
}
