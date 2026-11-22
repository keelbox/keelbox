package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// keepSession opens a session again only when it was open and ssh then
// lost it, and once the box answers.
func TestKeepSessionOpensALostSessionAgain(t *testing.T) {
	ctx := context.Background()
	lost := exitStatus(255)
	type run struct {
		opened bool
		err    error
	}
	play := func(wait func() error, runs ...run) (opens int, said []string, err error) {
		i := 0
		open := func() (bool, error) {
			r := runs[i]
			i++
			return r.opened, r.err
		}
		err = keepSession(ctx, open, wait, func(l string) { said = append(said, l) }, "acme")
		return i, said, err
	}
	answers := func() error { return nil }
	old := shortSession
	shortSession = 0 // these sessions end at once; none counts as short
	t.Cleanup(func() { shortSession = old })

	// Lost twice, then the user detached.
	opens, said, err := play(answers, run{true, lost}, run{true, lost}, run{true, nil})
	if opens != 3 || err != nil || len(said) != 4 {
		t.Fatalf("opens %d, err %v, said %q", opens, err, said)
	}
	if want := "lost the connection to acme. A session left open there keeps running. Waiting for acme to answer, then opening it again; Ctrl-C stops."; said[0] != want || said[1] != "acme answers again." {
		t.Errorf("said %q", said[:2])
	}
	// Never opened: a connection ssh could not make is not waited for.
	if opens, said, err := play(answers, run{false, lost}); opens != 1 || err != lost || said != nil {
		t.Errorf("never opened: opens %d, err %v, said %q", opens, err, said)
	}
	// The session's own exit code, and a clean end, are the end.
	left := exitStatus(3)
	if opens, _, err := play(answers, run{true, left}); opens != 1 || err != left {
		t.Errorf("own exit code: opens %d, err %v", opens, err)
	}
	// Ctrl-C while waiting.
	if opens, _, err := play(func() error { return errStoppedWaiting }, run{true, lost}); opens != 1 || !errors.Is(err, errStoppedWaiting) {
		t.Errorf("stopped: opens %d, err %v", opens, err)
	}
	// A box that takes the connection and drops it at once, three times:
	// keelbox stops and says what ssh's exit code means.
	shortSession = time.Hour
	opens, said, err = play(answers, run{true, lost}, run{true, lost}, run{true, lost}, run{true, nil})
	if opens != 3 || err != lost || len(said) != 4 || !strings.HasPrefix(said[2], "lost the connection") {
		t.Errorf("dropped at once: opens %d, err %v, said %q", opens, err, said)
	}
}

func TestWaitForSSH(t *testing.T) {
	old := reconnectEvery
	reconnectEvery = time.Millisecond
	t.Cleanup(func() { reconnectEvery = old })
	tries := 0
	prev := sshAnswers
	sshAnswers = func(context.Context, addr) error {
		if tries++; tries < 3 {
			return errNoRoute
		}
		return nil
	}
	t.Cleanup(func() { sshAnswers = prev })
	if err := waitForSSH(context.Background(), addr{"192.0.2.10", 22}); err != nil || tries != 3 {
		t.Errorf("err %v after %d tries", err, tries)
	}
	sshAnswers = func(context.Context, addr) error { return errNoRoute }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForSSH(ctx, addr{"192.0.2.10", 22}); !errors.Is(err, errStoppedWaiting) {
		t.Errorf("after Ctrl-C: %v", err)
	}
}
