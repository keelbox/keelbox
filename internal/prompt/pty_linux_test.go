package prompt

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/keelbox/keelbox/internal/term"
	"github.com/keelbox/keelbox/internal/ui"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// openPTY opens a pseudo-terminal pair: what the test writes to the master
// is typed at the slave, and what the slave echoes comes back on the master.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals: %v", err)
	}
	var unlock int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); e != 0 {
		t.Fatalf("unlockpt: %v", e)
	}
	var n uint32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); e != 0 {
		t.Fatalf("ptsname: %v", e)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(); m.Close() })
	return m, s
}

// On a real terminal the typed secret is not echoed, the echo is restored
// afterwards, and a normal answer is echoed again.
func TestSecretOnARealTerminal(t *testing.T) {
	master, slave := openPTY(t)
	if !IsTerminal(slave) {
		t.Fatal("pty slave is not a terminal")
	}
	var out bytes.Buffer
	p := Terminal(slave, &out)
	var mu sync.Mutex
	var echo bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 256)
		for {
			n, err := master.Read(buf)
			mu.Lock()
			echo.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return // EIO once the slave is closed
			}
		}
	}()
	go func() {
		time.Sleep(200 * time.Millisecond)
		_, _ = master.WriteString("s3cret-value\n")
		time.Sleep(200 * time.Millisecond)
		_, _ = master.WriteString("visible\n")
	}()
	got, err := p.Secret("Token")
	if err != nil || got != "s3cret-value" {
		t.Fatalf("secret: %q %v", got, err)
	}
	name, err := p.Ask("Name", "")
	if err != nil || name != "visible" {
		t.Fatalf("ask: %q %v", name, err)
	}
	time.Sleep(200 * time.Millisecond)
	slave.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pty reader did not finish")
	}
	mu.Lock()
	e := echo.String()
	mu.Unlock()
	if strings.Contains(e, "s3cret") {
		t.Errorf("terminal echoed the secret: %q", e)
	}
	if !strings.Contains(e, "visible") {
		t.Errorf("echo not restored after the secret: %q", e)
	}
}

func TestDevNullIsNotATerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if IsTerminal(f) {
		t.Error("/dev/null reported as a terminal")
	}
}

// ptyStyle is a capable terminal 100 columns wide, as Terminal sees it.
func ptyStyle() ui.Style { return ui.Style{Live: true, UTF8: true, Width: 100} }

func readAll(master *os.File) (func() string, func()) {
	var mu sync.Mutex
	var b bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			mu.Lock()
			b.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return func() string { mu.Lock(); defer mu.Unlock(); return b.String() }, func() { <-done }
}

// Drawn questions on a real terminal: arrow keys and Enter, a number, a
// key typed; a secret shows only dots and a count; yes or no; a default.
func TestDrawnQuestionsOnARealTerminal(t *testing.T) {
	master, slave := openPTY(t)
	shown, _ := readAll(master)
	p := Terminal(slave, slave).WithStyle(ptyStyle())
	type q struct {
		keys string
		ask  func() (string, error)
		want string
	}
	cases := []q{
		{"\x1b[B\x1b[B\r", func() (string, error) { i, err := p.Choose("Where?", where, 0); return fmt.Sprint(i), err }, "2"},
		{"\x1b[A\r", func() (string, error) { i, err := p.Choose("Where?", where, 0); return fmt.Sprint(i), err }, "3"},
		{"3\r", func() (string, error) { i, err := p.Choose("Where?", where, 0); return fmt.Sprint(i), err }, "2"},
		{"hetzner\r", func() (string, error) { i, err := p.Choose("Where?", where, 0); return fmt.Sprint(i), err }, "2"},
		{"9\rlocal\r", func() (string, error) { i, err := p.Choose("Where?", where, 0); return fmt.Sprint(i), err }, "1"},
		{"s3cret-value\r", func() (string, error) { return p.Secret("Token") }, "s3cret-value"},
		{"n\r", func() (string, error) { ok, err := p.Confirm("Sure?", true); return fmt.Sprint(ok), err }, "false"},
		{"\r", func() (string, error) { return p.Ask("Name", "devbox") }, "devbox"},
		{"lab\x7fb2\r", func() (string, error) { return p.Ask("Name", "devbox") }, "lab2"},
	}
	for _, c := range cases {
		go func(k string) {
			time.Sleep(150 * time.Millisecond)
			_, _ = master.WriteString(k)
		}(c.keys)
		got, err := c.ask()
		if err != nil || got != c.want {
			t.Errorf("keys %q: %q %v, want %q", c.keys, got, err, c.want)
		}
	}
	time.Sleep(100 * time.Millisecond)
	out := shown()
	if strings.Contains(out, "s3cret") {
		t.Error("the secret was shown")
	}
	for _, w := range []string{"••••••••", "12 characters", "✓ Where? A new Hetzner server", "type a number from 1 to 4", "✓ Sure? No", "✓ Name: devbox"} {
		if !strings.Contains(out, w) {
			t.Errorf("the terminal did not show %q", w)
		}
	}
}

// Ctrl-C at a drawn menu ends the question and gives the terminal back as
// it was: line editing and echo on, the cursor shown.
func TestDrawnMenuInterrupted(t *testing.T) {
	master, slave := openPTY(t)
	shown, _ := readAll(master)
	before, err := term.Keys(slave) // the settings to come back to
	if err != nil {
		t.Fatal(err)
	}
	before()
	intr := make(chan struct{})
	p := Terminal(slave, slave).WithStyle(ptyStyle()).OnInterrupt(intr)
	go func() {
		time.Sleep(150 * time.Millisecond)
		close(intr)
	}()
	if _, err := p.Choose("Where?", where, 0); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("got %v, want ErrInterrupted", err)
	}
	time.Sleep(100 * time.Millisecond)
	if out := shown(); !strings.HasSuffix(strings.TrimRight(out, "\r\n"), "\x1b[?25h") && !strings.Contains(out, "\x1b[?25h") {
		t.Errorf("cursor not shown again: %q", out)
	}
	// Echo is back: a typed line shows on the terminal.
	_, _ = master.WriteString("visible\n")
	time.Sleep(150 * time.Millisecond)
	if !strings.Contains(shown(), "visible") {
		t.Error("echo not restored after Ctrl-C")
	}
}

// Ctrl-C at every kind of drawn question gives the terminal back as it
// was, marks the question with the dash of a stop and not the mark of a
// failure, and tells the command.
func TestEveryDrawnQuestionInterrupted(t *testing.T) {
	for name, ask := range map[string]func(p *Prompter) error{
		"yes or no": func(p *Prompter) error { _, err := p.Confirm("Forget alpha?", false); return err },
		"a line":    func(p *Prompter) error { _, err := p.Ask("Box name", "devbox"); return err },
		"a secret":  func(p *Prompter) error { _, err := p.Secret("Hetzner API token"); return err },
		"a menu":    func(p *Prompter) error { _, err := p.Choose("Where?", where, 0); return err },
		"wait":      func(p *Prompter) error { return p.Wait("Press Enter once it has run.") },
	} {
		master, slave := openPTY(t)
		shown, _ := readAll(master)
		intr := make(chan struct{})
		told := false
		p := Terminal(slave, slave).WithStyle(ptyStyle()).OnInterrupt(intr).OnStop(func() { told = true })
		go func() {
			time.Sleep(150 * time.Millisecond)
			close(intr)
		}()
		if err := ask(p); !errors.Is(err, ErrInterrupted) {
			t.Fatalf("%s: got %v, want ErrInterrupted", name, err)
		}
		if !told {
			t.Errorf("%s: the command was not told", name)
		}
		_, _ = master.WriteString("visible\n")
		time.Sleep(150 * time.Millisecond)
		out := shown()
		if !strings.Contains(out, "visible") {
			t.Errorf("%s: echo not restored after Ctrl-C", name)
		}
		if i, j := strings.LastIndex(out, "\x1b[?25l"), strings.LastIndex(out, "\x1b[?25h"); j < i {
			t.Errorf("%s: cursor left hidden: %q", name, out)
		}
		screen := ui.Screen(out)
		if !strings.Contains(screen, "\n- ") && !strings.HasPrefix(screen, "- ") || strings.Contains(screen, "✗") {
			t.Errorf("%s: the stopped question is not marked with a dash:\n%s", name, screen)
		}
	}
}

// On a terminal a question is asked until it is answered: no number of
// refused answers ends it. The reason shows under the field, once, and
// the refused text is gone, so the corrected answer is not added to it.
func TestRefusedAnswersAreAskedAgainWithoutLimit(t *testing.T) {
	master, slave := openPTY(t)
	shown, _ := readAll(master)
	p := Terminal(slave, slave).WithStyle(ptyStyle())
	if !p.OnTerminal() {
		t.Fatal("a pty is not a terminal")
	}
	host := func(s string) error {
		if strings.ContainsAny(s, " !") {
			return errors.New("an IP address or DNS name, such as 203.0.113.10")
		}
		return nil
	}
	typeKeys := func(keys ...string) {
		go func() {
			for _, k := range keys {
				time.Sleep(120 * time.Millisecond)
				_, _ = master.WriteString(k)
			}
		}()
	}
	bad := []string{"not a host!\r", "still not!\r", "no no\r", "again no\r", "five !\r"}
	typeKeys(append(bad, "203.0.113.73\r")...)
	got, err := p.AskValid("Server address", "", host)
	if err != nil || got != "203.0.113.73" {
		t.Errorf("a line: %q %v", got, err)
	}
	typeKeys("maybe\r", "x\r", "q\r", "perhaps\r", "y\r")
	if ok, err := p.Confirm("Forget alpha?", false); err != nil || !ok {
		t.Errorf("yes or no: %v %v", ok, err)
	}
	typeKeys("9\r", "8\r", "7\r", "6\r", "2\r")
	if i, err := p.Choose("Where?", where, 0); err != nil || i != 1 {
		t.Errorf("a menu: %d %v", i, err)
	}
	time.Sleep(100 * time.Millisecond)
	out := shown()
	// While it was refused, the reason stood once under an empty field.
	if n := strings.Count(out, "an IP address or DNS name, such as 203.0.113.10"); n < len(bad) {
		t.Errorf("the reason was shown %d times for %d refused answers", n, len(bad))
	}
	if strings.Contains(out, "not a host!203") || strings.Contains(out, "maybey") {
		t.Errorf("the refused text was kept in the field: %q", out)
	}
	screen := ui.Screen(out)
	for _, w := range []string{"✓ Server address: 203.0.113.73", "✓ Forget alpha? Yes", "✓ Where? This machine"} {
		if !strings.Contains(screen, w) {
			t.Errorf("the screen lacks %q:\n%s", w, screen)
		}
	}
	if strings.Contains(screen, "✗") || strings.Contains(screen, "such as 203.0.113.10") {
		t.Errorf("a reason is left on the screen after the answer:\n%s", screen)
	}
}

// The same on a terminal that takes plain lines only (TERM=dumb, a screen
// reader): asked until answered, the reason after each refused answer.
func TestPlainTerminalAsksUntilAnswered(t *testing.T) {
	master, slave := openPTY(t)
	shown, _ := readAll(master)
	p := Terminal(slave, slave) // no style: one plain line at a time
	go func() {
		for _, k := range []string{"maybe\n", "x\n", "q\n", "perhaps\n", "n\n"} {
			time.Sleep(120 * time.Millisecond)
			_, _ = master.WriteString(k)
		}
	}()
	if ok, err := p.Confirm("Forget alpha?", true); err != nil || ok {
		t.Errorf("got %v %v", ok, err)
	}
	time.Sleep(100 * time.Millisecond)
	if n := strings.Count(shown(), "answer y or n"); n != 4 {
		t.Errorf("the reason was shown %d times for 4 refused answers", n)
	}
}

// An answered question reads the same way whatever it asked: a question
// keeps its question mark, a typed value follows a colon, and an answer
// left empty says what that meant instead of leaving the bare question.
func TestAnsweredQuestionsReadTheSameWay(t *testing.T) {
	master, slave := openPTY(t)
	shown, _ := readAll(master)
	p := Terminal(slave, slave).WithStyle(ptyStyle())
	any := func(string) error { return nil }
	for _, q := range []struct {
		keys string
		ask  func() error
	}{
		{"203.0.113.7\r", func() error { _, err := p.AskValid("Server's IP address or DNS name", "", any); return err }},
		{"\r", func() error { _, err := p.AskOptional("Host key fingerprint", any); return err }},
		{"\r", func() error { _, err := p.AskOr("AWS CLI profile", "your default", "your default", any); return err }},
		{"work\r", func() error { _, err := p.AskOr("AWS CLI profile", "your default", "your default", any); return err }},
		{"\r", func() error { _, err := p.Choose("Which location?", where, 0); return err }},
		{"\r", func() error { _, err := p.Confirm("Use Tailscale to reach the box?", true); return err }},
	} {
		go func(k string) {
			time.Sleep(120 * time.Millisecond)
			_, _ = master.WriteString(k)
		}(q.keys)
		if err := q.ask(); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	screen := ui.Screen(shown())
	for _, want := range []string{
		"✓ Server's IP address or DNS name: 203.0.113.7\n",
		"✓ Host key fingerprint: skipped\n",
		"✓ AWS CLI profile: your default\n",
		"✓ AWS CLI profile: work\n",
		"✓ Which location? An existing server (SSH)\n",
		"✓ Use Tailscale to reach the box? Yes\n",
	} {
		if !strings.Contains(screen, want) {
			t.Errorf("the screen lacks %q:\n%s", want, screen)
		}
	}
	if !strings.Contains(shown(), "(Enter: your default)") || !strings.Contains(shown(), "(Enter: skip)") {
		t.Errorf("the field does not say what Enter does:\n%q", shown())
	}
}

// A secret's field stays inside a narrow terminal while it is typed: the
// dots give way to the count and the hint beside them.
func TestSecretFieldFitsANarrowTerminal(t *testing.T) {
	master, slave := openPTY(t)
	ws := struct{ Row, Col, X, Y uint16 }{50, 60, 0, 0}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws))); e != 0 {
		t.Fatalf("set window size: %v", e)
	}
	shown, _ := readAll(master)
	p := Terminal(slave, slave).WithStyle(ui.Style{Live: true, UTF8: true, Width: 60})
	go func() {
		time.Sleep(150 * time.Millisecond)
		_, _ = master.WriteString(strings.Repeat("x", 24))
		time.Sleep(150 * time.Millisecond)
		_, _ = master.WriteString("\r")
	}()
	if got, err := p.Secret("Hetzner API token"); err != nil || len(got) != 24 {
		t.Fatalf("%q %v", got, err)
	}
	time.Sleep(100 * time.Millisecond)
	out := shown()
	if !strings.Contains(out, "24 characters") || !strings.Contains(out, "(input hidden)") {
		t.Errorf("the count or the hint is gone:\n%q", out)
	}
	for _, frame := range strings.Split(out, "\x1b[J") { // each redraw of the question
		for _, l := range strings.Split(frame, "\n") {
			if w := ui.Width(strings.TrimRight(l, "\r")); w >= 60 {
				t.Errorf("a line is %d columns on a 60 column terminal: %q", w, l)
			}
		}
	}
}

// A drawn list to tick: Space ticks, the arrows move, a ticks all and then
// none, Enter alone after nothing ticked picks none.
func TestDrawnMultiOnARealTerminal(t *testing.T) {
	master, slave := openPTY(t)
	shown, _ := readAll(master)
	p := Terminal(slave, slave).WithStyle(ptyStyle())
	choices := []Choice{{Label: "alpha", Detail: "first"}, {Label: "beta"}, {Label: "gamma"}}
	for _, c := range []struct {
		keys string
		want string
	}{
		{" \x1b[B\x1b[B \r", "[0 2]"},
		{"a\r", "[0 1 2]"},
		{"aa\r", "[]"},
		{"\r", "[]"},
	} {
		go func(k string) {
			time.Sleep(150 * time.Millisecond)
			_, _ = master.WriteString(k)
		}(c.keys)
		got, err := p.MultiChoose("Copy which?", choices)
		if err != nil || fmt.Sprint(got) != c.want {
			t.Errorf("keys %q: %v %v, want %s", c.keys, got, err, c.want)
		}
	}
	time.Sleep(100 * time.Millisecond)
	out := shown()
	for _, w := range []string{"✓ Copy which? alpha, gamma", "✓ Copy which? none", "Space", "[x]"} {
		if !strings.Contains(out, w) {
			t.Errorf("the terminal did not show %q", w)
		}
	}
}
