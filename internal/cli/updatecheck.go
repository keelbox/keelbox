package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/release"
	"github.com/keelbox/keelbox/internal/term"
	"github.com/keelbox/keelbox/internal/ui"
	"github.com/keelbox/keelbox/internal/version"
)

// The update check (docs/adr/0089-update-check.md): at most once a day, a
// command run at a terminal on your computer reads the channel index, as
// self-update does, and says when a newer keelbox is out or when this one
// has a security notice. It sends the user agent (release.UserAgent) and
// nothing else. A box never runs it. KEELBOX_NO_UPDATE_CHECK turns it off.

// checkCommands are the commands after which keelbox says it is out of
// date: what a person runs at their computer. self-update and update read
// the index themselves; ssh, attach and login hand over the terminal.
var checkCommands = map[string]bool{
	"up": true, "ls": true, "boxes": true, "status": true, "doctor": true, "plan": true, "apply": true,
	"create": true, "destroy": true, "rollback": true, "forget": true, "resize": true, "park": true,
	"resume": true, "add": true, "version": true, "init": true,
}

// checkEvery is how often the index is read, and how often a newer
// release is mentioned.
const checkEvery = 24 * time.Hour

// How the update check reads the index, keeps what it learnt and decides
// whether to run; tests stand in for each.
var (
	checkIndex = func(ctx context.Context) (*release.Index, error) {
		f, err := release.NewFetcher()
		if err != nil {
			return nil, err
		}
		return f.Index(ctx)
	}
	checkStatePath = func() string {
		dir, err := os.UserCacheDir()
		if err != nil {
			return ""
		}
		return filepath.Join(dir, "keelbox", "update-check.json")
	}
	checkNow        = time.Now
	checkTerminal   = func(w io.Writer) bool { f, ok := w.(*os.File); return ok && term.IsTerminal(f) }
	checkOnBox      = func() bool { _, _, ok := onBox(); return ok || exists(boxStateDir) }
	checkWait       = time.Second // how long a command's end waits for the index
	checkTimeout    = 3 * time.Second
	checkVersion    = func() string { return version.Version }
	checkExecutable = os.Executable
)

// boxStateDir is keelbox's state on a box; a computer that only reaches
// boxes has none.
const boxStateDir = "/var/lib/keelbox"

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// updateState is what the last check learnt, in the user cache directory.
type updateState struct {
	CheckedAt time.Time                `json:"checked_at"`
	Latest    string                   `json:"latest,omitempty"`
	Security  []release.SecurityNotice `json:"security,omitempty"`
	// ToldAt is when a newer release was last mentioned.
	ToldAt time.Time `json:"told_at,omitzero"`
}

func loadUpdateState() updateState {
	var st updateState
	if p := checkStatePath(); p != "" {
		if data, err := os.ReadFile(p); err == nil {
			_ = json.Unmarshal(data, &st)
		}
	}
	// Checked again: the file is this user's to change.
	st.Security = (&release.Index{Security: st.Security}).Notices()
	return st
}

func saveUpdateState(st updateState) {
	p := checkStatePath()
	if p == "" {
		return
	}
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	// Written whole or not at all: the process may end mid-write.
	tmp, err := os.CreateTemp(filepath.Dir(p), ".update-check-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(append(data, '\n'))
	if cerr := tmp.Close(); werr != nil || cerr != nil || os.Rename(tmp.Name(), p) != nil {
		_ = os.Remove(tmp.Name())
	}
}

// updateCheck is one command's check: started with the command, finished
// after it.
type updateCheck struct {
	st   updateState
	done chan updateState // nil when the index is not read this time
	// first is the first check on this computer, which says what it sends.
	first bool
}

// startUpdateCheck reads the index in the background when the last check
// is a day old, for a release build run at a terminal off a box. It
// returns nil when keelbox says nothing after this command.
func startUpdateCheck(args []string, stderr io.Writer) *updateCheck {
	if len(args) == 0 || !checkCommands[args[0]] || os.Getenv("KEELBOX_NO_UPDATE_CHECK") != "" || os.Getenv("DO_NOT_TRACK") != "" || os.Getenv("CI") != "" ||
		!release.IsRelease(checkVersion()) || !checkTerminal(stderr) || checkOnBox() {
		return nil
	}
	c := &updateCheck{st: loadUpdateState()}
	now := checkNow()
	if now.Sub(c.st.CheckedAt) < checkEvery && !c.st.CheckedAt.After(now) {
		return c
	}
	// Saved before the read: one that fails, or outlasts the command, waits
	// a day too, with no retry on every command while offline.
	c.first = c.st.CheckedAt.IsZero()
	c.st.CheckedAt = now
	saveUpdateState(c.st)
	c.done = make(chan updateState, 1)
	go func(st updateState) {
		ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
		defer cancel()
		if idx, err := checkIndex(ctx); err == nil && idx != nil {
			st.Latest, st.Security = idx.Version, idx.Notices()
			saveUpdateState(st)
		}
		c.done <- st
	}(c.st)
	return c
}

// finish says, after the command, that a newer keelbox is out (once a
// day) or that this one has a security notice (every time).
func (c *updateCheck) finish(stderr io.Writer) {
	if c == nil {
		return
	}
	if c.done != nil {
		select {
		case c.st = <-c.done:
		case <-time.After(checkWait): // slow: what the last check learnt still stands
		}
	}
	st := errStyle(stderr)
	if c.first {
		fmt.Fprintln(stderr)
		sayAsIs(stderr, st, st.Bang(), "keelbox reads https://get.keelbox.dev/stable.json once a day to say when a newer release is out, sending its version and platform and nothing else. "+st.Cmd("KEELBOX_NO_UPDATE_CHECK=1")+" turns that off.")
	}
	have := checkVersion()
	update := "keelbox self-update"
	if exe, err := checkExecutable(); err == nil {
		if _, cmd := packageManager(exe); cmd != "" {
			update = cmd
		}
	}
	for _, n := range c.st.Security {
		if n.Affects(have) {
			fmt.Fprintln(stderr)
			sayAsIs(stderr, st, st.Bang(), noticeText(n, "keelbox "+have+" has", "Update: "+st.Cmd(update)))
			return
		}
	}
	now := checkNow()
	if c.st.Latest == "" || release.CompareVersions(c.st.Latest, have) <= 0 || now.Sub(c.st.ToldAt) < checkEvery && !c.st.ToldAt.After(now) {
		return
	}
	fmt.Fprintln(stderr)
	sayAsIs(stderr, st, st.Bang(), fmt.Sprintf("keelbox %s is out; this is %s. Update: %s", c.st.Latest, have, st.Cmd(update)))
	c.st.ToldAt = now
	saveUpdateState(c.st)
}

// boxSecurityNote says, after keelbox status, that the box was last
// applied from here with a release that has a security notice. It uses
// what the last update check learnt and reaches nothing.
func boxSecurityNote(cfg *config.Config, style ui.Style, stdout io.Writer) {
	applied, ok := loadAppliedNote(cfg.Box)
	if !ok {
		return
	}
	for _, n := range loadUpdateState().Security {
		if !n.Affects(applied.Keelbox) {
			continue
		}
		subject := fmt.Sprintf("%s was last applied from here with keelbox %s, which has", cfg.Box, applied.Keelbox)
		fmt.Fprintln(stdout)
		if style.Live {
			sayAsIs(stdout, style, style.Bang(), noticeText(n, subject, "Update it: "+style.Cmd("keelbox update "+cfg.Box)))
			return
		}
		fmt.Fprintln(stdout, style.Plain(noticeText(n, subject, "Update it: keelbox update "+cfg.Box), 0))
		return
	}
}

// noticeText is a security notice as keelbox says it. The summary and the
// link come from the index, which is not signed, so they are quoted as the
// notice's words and never styled as a command; the command is keelbox's.
func noticeText(n release.SecurityNotice, subject, update string) string {
	return fmt.Sprintf("%s a security problem, fixed in %s. %s. Details: %s The notice reads %q", subject, n.Fixed, update, n.URL, n.Summary)
}
