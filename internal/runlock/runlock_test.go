package runlock

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestApplyAndDayTwoRuns(t *testing.T) {
	poll = time.Millisecond
	dir := t.TempDir()
	apply, daytwo := filepath.Join(dir, "keelbox.lock"), filepath.Join(dir, "daytwo.lock")

	a, err := Apply(apply, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DayTwo(apply, daytwo, time.Second); !errors.Is(err, ErrBusy) {
		t.Errorf("a day-two run during an apply: %v, want ErrBusy (skip)", err)
	}
	if _, err := Apply(apply, 10*time.Millisecond); !errors.Is(err, ErrBusy) {
		t.Errorf("a second apply: %v", err)
	}
	a.Unlock()

	s1, o1, err := DayTwo(apply, daytwo, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := DayTwo(apply, daytwo, 10*time.Millisecond); !errors.Is(err, ErrBusy) {
		t.Errorf("two day-two runs at once: %v", err)
	}
	// An apply waits for a day-two run rather than refusing.
	done := make(chan error, 1)
	go func() {
		l, err := Apply(apply, 5*time.Second)
		if err == nil {
			l.Unlock()
		}
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	o1.Unlock()
	s1.Unlock()
	if err := <-done; err != nil {
		t.Errorf("apply did not get the lock once the day-two run ended: %v", err)
	}
}
