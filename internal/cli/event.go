package cli

import (
	"flag"
	"fmt"
	"io"
	"os/user"
	"path/filepath"
	"time"

	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/notify"
)

// hookInputMax is the most of a hook's JSON keelbox event reads.
const hookInputMax = 64 << 10

// hookReadWait bounds the wait for the hook's JSON: the CLI writes it at
// once and closes stdin.
var hookReadWait = 2 * time.Second

// eventHome is the account's home from the password database, not $HOME;
// tests replace it.
var eventHome = func() string {
	if u, err := user.Current(); err == nil {
		return u.HomeDir
	}
	return ""
}

// cmdEvent is keelbox event, run as the agent by the managed hooks of its
// CLIs: it turns the hook's JSON into one line of the agent's own events
// file, which root reads as the agent and checks again (ADR 0032). It reads
// no config and needs no rights beyond the agent's. It always exits 0, so a
// hook never breaks or slows the CLI; a problem goes to stderr, which the
// CLI keeps in its debug log.
func cmdEvent(args []string, stdin io.Reader, stderr io.Writer) int {
	fs := flag.NewFlagSet("event", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	from := fs.String("from", "", "")
	text := fs.Bool("text", false, "")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "keelbox event: usage: keelbox event --from claude-code|codex [--text] < hook.json")
		return 0
	}
	raw := readHook(stdin)
	l, ok := notify.HookLine(*from, raw, time.Now(), *text)
	if !ok {
		return 0
	}
	home := eventHome()
	if home == "" {
		fmt.Fprintln(stderr, "keelbox event: no home directory for this account")
		return 0
	}
	if err := events.AppendAgent(filepath.Join(home, events.AgentFile), l); err != nil {
		fmt.Fprintln(stderr, "keelbox event:", err)
	}
	return 0
}

// readHook reads at most hookInputMax bytes, giving up after hookReadWait.
func readHook(r io.Reader) []byte {
	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(r, hookInputMax))
		done <- b
	}()
	select {
	case b := <-done:
		return b
	case <-time.After(hookReadWait):
		return nil
	}
}
