package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/ui"
)

// keelbox up on a real pseudo-terminal, answered with the arrow keys and
// Enter as a person would: the drawn menus, the masked token, the cards and
// the progress view, end to end against the fake Hetzner API.
//
// With KEELBOX_SHOTS=DIR set, the bytes the terminal received are saved
// there at each question (DIR/<width>-<theme>-<name>.ans), for rendering to
// images; KEELBOX_SHOTS_WIDTH and KEELBOX_SHOTS_THEME (dark, light) pick
// the terminal.

type ptyTerm struct {
	master, slave *os.File
	mu            sync.Mutex
	seen          bytes.Buffer
	done          chan struct{}
}

func openPTYTerm(t *testing.T, cols int) *ptyTerm {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals: %v", err)
	}
	var unlock int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); e != 0 { //nolint:gosec // G103: ioctl
		t.Fatalf("unlockpt: %v", e)
	}
	var n uint32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); e != 0 { //nolint:gosec // G103: ioctl
		t.Fatalf("ptsname: %v", e)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	ws := struct{ Row, Col, X, Y uint16 }{50, uint16(cols), 0, 0}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws))); e != 0 { //nolint:gosec // G103: ioctl
		t.Fatalf("set window size: %v", e)
	}
	pt := &ptyTerm{master: m, slave: s, done: make(chan struct{})}
	go func() {
		defer close(pt.done)
		buf := make([]byte, 4096)
		for {
			n, err := m.Read(buf)
			pt.mu.Lock()
			pt.seen.Write(buf[:n])
			pt.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { s.Close(); m.Close() })
	return pt
}

func (pt *ptyTerm) output() string {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	return pt.seen.String()
}

// expect waits for pattern in what the terminal shows after from, and
// returns where the match ends.
func (pt *ptyTerm) expect(t *testing.T, from int, pattern string) int {
	t.Helper()
	re := regexp.MustCompile("(?s)" + pattern)
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		out := pt.output()
		if loc := re.FindStringIndex(out[from:]); loc != nil {
			return from + loc[1]
		}
	}
	t.Fatalf("the terminal never showed %q:\n%s", pattern, ui.Strip(pt.output()[from:]))
	return 0
}

type shots struct {
	dir, prefix string
}

func newShots(cols int, theme string) *shots {
	dir := os.Getenv("KEELBOX_SHOTS")
	if dir == "" {
		return nil
	}
	return &shots{dir: dir, prefix: fmt.Sprintf("%d-%s-", cols, theme)}
}

func (s *shots) save(t *testing.T, name, data string) {
	if s == nil {
		return
	}
	if err := os.WriteFile(filepath.Join(s.dir, s.prefix+name+".ans"), []byte(data), 0o600); err != nil {
		t.Error(err)
	}
}

func shotsTerminal() (int, string) {
	cols, theme := 100, "dark"
	if v := os.Getenv("KEELBOX_SHOTS_WIDTH"); v != "" {
		_, _ = fmt.Sscan(v, &cols)
	}
	if v := os.Getenv("KEELBOX_SHOTS_THEME"); v != "" {
		theme = v
	}
	return cols, theme
}

// fullStyle is a capable truecolor terminal cols wide.
func fullStyle(cols int, light bool) ui.Style {
	return ui.Style{Colour: true, Live: true, UTF8: true, Depth: ui.DepthTrue, Light: light, Width: cols}
}

func TestUpHetznerOnARealTerminal(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	_, store := fakeHetzner(t)
	cols, theme := shotsTerminal()
	shot := newShots(cols, theme)
	pt := openPTYTerm(t, cols)
	st := fullStyle(cols, theme == "light")
	defer func(f func() bool) { localLikely = f }(localLikely)
	localLikely = func() bool { return false }
	// Both CLIs installed, so the cloud question reads the same on any machine.
	defer func(a, d func() bool) { awsInstalled, doInstalled = a, d }(awsInstalled, doInstalled)
	awsInstalled, doInstalled = func() bool { return true }, func() bool { return true }

	r := &fakeRunner{log: progressLog, logDelay: 150 * time.Millisecond}
	o, pos, err := parse("up", nil, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	o.style = st
	withLaptopGit(t, "Ada Lovelace", "ada@example.com")
	p := prompt.Terminal(pt.slave, pt.slave).WithStyle(st)
	code := make(chan int, 1)
	go func() { code <- cmdUp(context.Background(), o, pos, p, r, pt.slave, pt.slave) }()

	at := pt.expect(t, 0, `Where should the box run\?.*Enter.*choose`)
	shot.save(t, "up-menu", pt.output())
	type step struct{ pattern, keys, shot string }
	steps := []step{
		{"", "\x1b[B\x1b[B", ""}, // down twice: A new cloud server
		{`›.*A new cloud server`, "\r", ""},
		{`Which cloud\?.*Enter.*choose`, "\r", "cloud-menu"}, // Hetzner, the default
		{`Box name.*›`, "acme\r", ""},
		{`Hetzner API token.*›`, goodToken[:10], ""},
		{`10 characters`, goodToken[10:], "token-card"},
		{`\d+ characters`, "\r", ""},
		{`Which location\?.*Enter.*choose`, "\r", ""},
		{`Which size\?.*Enter.*choose`, "\r", "size-menu"},
		{`Which agents\?.*Enter.*choose`, "\r", ""},
		{`GitHub CLI.*\[Y/n\]`, "\r", ""},
		{`Commit as Ada Lovelace <ada@example\.com>.*\[Y/n\]`, "\r", "git-identity"},
		{`How much may the agent change this box\?.*Enter.*choose`, "\r", "permissions-menu"}, // Auto, the default
		{`Tailscale.*\[Y/n\]`, "\r", ""},
		{`When should the box restart.*Enter.*choose`, "\r", "reboots-menu"},         // nightly, the default
		{`Create the server \(about EUR [0-9.]+ a month.*\[Y/n\]`, "\r", "go-ahead"}, // the question wraps on a narrow terminal
	}
	for _, s := range steps {
		if s.pattern != "" {
			at = pt.expect(t, at, s.pattern)
		}
		if s.shot != "" {
			time.Sleep(50 * time.Millisecond)
			shot.save(t, s.shot, pt.output())
		}
		_, _ = pt.master.WriteString(s.keys)
	}
	at = pt.expect(t, at, `Developer tools`)
	time.Sleep(250 * time.Millisecond)
	shot.save(t, "progress-live", pt.output())
	// After the Claude Code sign-in, not with the other choices.
	at = pt.expect(t, at, `Turn on Remote Control, so the Claude app.*\[y/N\]`)
	shot.save(t, "remote-control-question", pt.output())
	_, _ = pt.master.WriteString("y\r")
	var c int
	select {
	case c = <-code:
	case <-time.After(20 * time.Second):
		t.Fatalf("up did not finish:\n%s", ui.Strip(pt.output()))
	}
	pt.expect(t, at, `acme is set up`)
	time.Sleep(100 * time.Millisecond)
	out := pt.output()
	shot.save(t, "up-full", out)
	if c != 0 {
		t.Fatalf("exit %d:\n%s", c, ui.Strip(out))
	}
	plainOut := ui.Strip(out)
	for _, want := range []string{
		"✓ Where should the box run? A new cloud server", "✓ Which cloud? Hetzner", "Step 2 of 7 · Hetzner token", "Create a Hetzner API token",
		"✓ Hetzner API token: ••••••••", "About to set up acme", "Hardening the server", "✓ Applied in", "✓ acme is set up", "Start working",
	} {
		if !strings.Contains(plainOut, want) {
			t.Errorf("the terminal did not show %q", want)
		}
	}
	if strings.Contains(out, goodToken) {
		t.Error("the token was shown")
	}
	if cfg, _ := loadBox(t, "acme"); cfg.Target.Hetzner.Location != "fsn1" || !cfg.RemoteControl() {
		t.Errorf("answers not in the config: %+v", cfg.Target.Hetzner)
	}
	if s, _ := store.Get(context.Background(), "hetzner"); s != goodToken {
		t.Error("token not stored")
	}
	for _, l := range strings.FieldsFunc(out, func(r rune) bool { return r == '\n' || r == '\r' }) {
		if w := ui.Width(l); w >= cols {
			t.Errorf("a line is %d columns on a %d column terminal: %q", w, cols, l)
		}
	}
}

// progressLog is an apply of a fresh Hetzner server, as the box streams it.
const progressLog = `keelbox apply: acme (release 2026.11.3, Ubuntu 24.04 amd64)
host
  change  packages: install
  change  timezone: set UTC
  change  firewall: ufw: enable
access
  change  tailscale: install
runtime
  change  docker-packages: install docker-ce
  change  node: install node 24
sessions
  change  tmux: write /etc/tmux.conf
logbook
  ok      logbook-dir
watch
  change  watch-timer: enable
agent-policy
  change  managed-settings: write
agents
  change  claude-code: install
  change  codex: install

Applied 11 changes in 1m38s. Recorded in /var/lib/keelbox/logbook.jsonl.
`

var _ = config.ProviderHetzner
