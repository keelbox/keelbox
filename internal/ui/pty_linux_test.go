package ui

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

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

// On a real terminal the view is live and redraws its line while a step
// runs; on a pipe nothing styles the output.
func TestProgressOnARealTerminal(t *testing.T) {
	master, slave := openPTY(t)
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	t.Setenv("LANG", "en_GB.UTF-8")
	st := For(slave)
	if !st.Live || !st.Colour || !st.UTF8 {
		t.Fatalf("pty style: %+v", st)
	}
	got := make(chan string)
	go func() {
		var b bytes.Buffer
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			b.Write(buf[:n])
			if err != nil || strings.Contains(b.String(), "Applied") {
				got <- b.String()
				return
			}
		}
	}()
	p := NewProgress(slave, st, []string{"host"}).Start()
	_, _ = p.Write([]byte("host\n  change  packages: install\n"))
	time.Sleep(400 * time.Millisecond) // a few spinner frames
	_, _ = p.Write([]byte("\nApplied 1 change(s) in 1s.\n"))
	p.Close()
	var out string
	select {
	case out = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing read from the pty")
	}
	if n := strings.Count(out, "\r\x1b[K"); n < 3 {
		t.Errorf("live line redrawn %d times, want the spinner to redraw it:\n%q", n, out)
	}
	if !strings.Contains(out, "✓\x1b[0m Hardening the server") {
		t.Errorf("no settled module line:\n%q", out)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if st := For(w); st.Live || st.Colour {
		t.Errorf("a pipe got %+v", st)
	}
}

// A terminal that takes plain text only (TERM=dumb, a screen reader) is
// not live, but its width is known, so plain text wraps to it; a pipe has
// no width and gets each message on one line.
func TestPlainModesKnowTheTerminalWidth(t *testing.T) {
	_, slave := openPTY(t)
	ws := struct{ Row, Col, X, Y uint16 }{50, 60, 0, 0}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws))); e != 0 {
		t.Fatalf("set window size: %v", e)
	}
	for name, env := range map[string][2]string{"dumb": {"dumb", ""}, "accessible": {"xterm-256color", "1"}} {
		t.Setenv("TERM", env[0])
		t.Setenv("ACCESSIBLE", env[1])
		st := For(slave)
		if st.Live || st.Width != 60 {
			t.Errorf("%s: %+v, want not live and 60 columns", name, st)
		}
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if st := For(w); st.Width != 0 {
		t.Errorf("a pipe got a width: %+v", st)
	}
}
