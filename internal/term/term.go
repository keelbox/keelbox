//go:build linux || darwin

// Package term is the little of the terminal keelbox needs, through the
// termios and window-size ioctls of the standard library: whether a file
// is a terminal, its width, echo off for a secret, key-at-a-time input for
// a menu, and the background colour a terminal reports. Nothing else in
// keelbox calls these ioctls.
package term

import (
	"bytes"
	"os"
	"strconv"
	"syscall"
	"time"
	"unsafe"
)

// IsTerminal reports whether f is a terminal: it answers the get-attributes
// ioctl. A character device alone is not enough, since /dev/null is one.
func IsTerminal(f *os.File) bool {
	_, err := getTermios(f)
	return err == nil
}

func getTermios(f *os.File) (*syscall.Termios, error) {
	var t syscall.Termios
	// The kernel only fills t; the pointer does not outlive the call.
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), ioctlGetTermios, uintptr(unsafe.Pointer(&t))) //nolint:gosec // G103: ioctl needs the address
	if errno != 0 {
		return nil, errno
	}
	return &t, nil
}

func setTermios(f *os.File, t *syscall.Termios) error {
	// The kernel only reads t; the pointer does not outlive the call.
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), ioctlSetTermios, uintptr(unsafe.Pointer(t))) //nolint:gosec // G103: ioctl needs the address
	if errno != 0 {
		return errno
	}
	return nil
}

// Width is f's width in columns, or 0 when f is not a terminal or does
// not say.
func Width(f *os.File) int {
	var ws struct{ Row, Col, X, Y uint16 }
	// The kernel only fills ws; the pointer does not outlive the call.
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws))) //nolint:gosec // G103: ioctl needs the address
	if errno != 0 {
		return 0
	}
	return int(ws.Col)
}

// EchoOff stops the terminal echoing what is typed, keeping the newline
// echo so the cursor still moves on Enter. The returned function restores
// the previous settings.
func EchoOff(f *os.File) (func(), error) {
	return set(f, func(t *syscall.Termios) {
		t.Lflag &^= syscall.ECHO
		t.Lflag |= syscall.ICANON | syscall.ECHONL
	})
}

// Keys puts the terminal in key-at-a-time mode: no line editing and no
// echo, so a question sees each key and draws the answer itself. Ctrl-C
// still interrupts (ISIG stays on) and Enter still arrives as a newline.
// The returned function restores the previous settings.
func Keys(f *os.File) (func(), error) {
	return set(f, func(t *syscall.Termios) {
		t.Lflag &^= syscall.ICANON | syscall.ECHO | syscall.ECHONL
		t.Lflag |= syscall.ISIG
		t.Cc[syscall.VMIN], t.Cc[syscall.VTIME] = 1, 0
	})
}

func set(f *os.File, change func(*syscall.Termios)) (func(), error) {
	old, err := getTermios(f)
	if err != nil {
		return nil, err
	}
	t := *old
	change(&t)
	if err := setTermios(f, &t); err != nil {
		return nil, err
	}
	return func() { _ = setTermios(f, old) }, nil
}

// Background asks the terminal on in and out for its background colour
// (OSC 11) and reports whether it is light. ok is false when the terminal
// does not say within timeout. The question is followed by a device
// attributes request every terminal answers, so one that does not know
// OSC 11 costs no wait; both answers are read and dropped.
func Background(in, out *os.File, timeout time.Duration) (light, ok bool) {
	restore, err := set(in, func(t *syscall.Termios) {
		t.Lflag &^= syscall.ICANON | syscall.ECHO | syscall.ECHONL
		t.Cc[syscall.VMIN], t.Cc[syscall.VTIME] = 0, 1 // a read returns after 0.1s
	})
	if err != nil {
		return false, false
	}
	defer restore()
	if _, err := out.WriteString("\x1b]11;?\x1b\\\x1b[c"); err != nil {
		return false, false
	}
	var got []byte
	buf := make([]byte, 256)
	for end := time.Now().Add(timeout); time.Now().Before(end); {
		n, err := in.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil || deviceAttributes(got) {
			break
		}
	}
	return parseBackground(got)
}

// deviceAttributes reports whether b holds a device attributes answer,
// ESC [ ? ... c.
func deviceAttributes(b []byte) bool {
	i := bytes.Index(b, []byte("\x1b[?"))
	return i >= 0 && bytes.IndexByte(b[i:], 'c') > 0
}

// parseBackground reads an OSC 11 answer, ESC ] 11 ; rgb:RRRR/GGGG/BBBB
// (one to four hex digits each), and reports whether the colour is light.
func parseBackground(b []byte) (light, ok bool) {
	i := bytes.Index(b, []byte("]11;rgb:"))
	if i < 0 {
		return false, false
	}
	rest := b[i+len("]11;rgb:"):]
	var c [3]float64
	for k := range c {
		j := 0
		for j < len(rest) && isHex(rest[j]) {
			j++
		}
		if j == 0 || j > 4 {
			return false, false
		}
		v, err := strconv.ParseUint(string(rest[:j]), 16, 16)
		if err != nil {
			return false, false
		}
		c[k] = float64(v) / float64(uint64(1)<<(4*j)-1)
		rest = rest[j:]
		if k < 2 {
			if len(rest) == 0 || rest[0] != '/' {
				return false, false
			}
			rest = rest[1:]
		}
	}
	// Relative luminance, roughly: a light background is above the middle.
	return 0.2126*c[0]+0.7152*c[1]+0.0722*c[2] > 0.5, true
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}
