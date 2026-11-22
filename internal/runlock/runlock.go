// Package runlock serialises keelbox's runs on a box. An apply holds
// /run/keelbox.lock exclusively; day-two runs (ADR 0030) hold it shared,
// and skip while an apply holds it, so they never act on a half-applied
// box and never delay an apply by more than their own short run.
package runlock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// Paths of the locks.
const (
	// Path is held exclusively by apply and shared by day-two runs.
	Path = "/run/keelbox.lock"
	// DayTwoPath is held exclusively by each day-two run, so two of them
	// (the watch run and the events service, say) never work on the event
	// queue at once.
	DayTwoPath = "/run/keelbox-daytwo.lock"
	// InstallsPath is held by whatever acts on the agent's package
	// requests: the install service and keelbox approve and deny, one at a
	// time. Not the day-two lock, and not the apply lock while apt-get
	// runs, so an install the agent asked for never holds up the watch run
	// and never makes an apply give up.
	InstallsPath = "/run/keelbox-installs.lock"
)

// ErrBusy is returned when a lock is still held after the wait.
var ErrBusy = errors.New("locked")

// Lock is a held lock; Unlock releases it.
type Lock struct{ f *os.File }

// Unlock releases the lock.
func (l *Lock) Unlock() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
}

// poll is how often a waiting lock retries.
var poll = 100 * time.Millisecond

// take locks path with how (LOCK_EX or LOCK_SH), retrying until wait has
// passed. A zero wait tries once.
func take(path string, how int, wait time.Duration) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		if err == nil {
			return &Lock{f: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, fmt.Errorf("%s: %w", path, ErrBusy)
		}
		time.Sleep(poll)
	}
}

// Apply takes the apply lock exclusively. It waits up to wait for day-two
// runs to finish, which take seconds; another apply holds it longer.
func Apply(path string, wait time.Duration) (*Lock, error) {
	return take(path, syscall.LOCK_EX, wait)
}

// DayTwo takes the apply lock shared without waiting, then the day-two lock
// exclusively, waiting up to wait for another day-two run. ErrBusy from the
// first means an apply is running: the caller skips this run.
func DayTwo(applyPath, dayTwoPath string, wait time.Duration) (*Lock, *Lock, error) {
	shared, err := take(applyPath, syscall.LOCK_SH, 0)
	if err != nil {
		return nil, nil, err
	}
	own, err := take(dayTwoPath, syscall.LOCK_EX, wait)
	if err != nil {
		shared.Unlock()
		return nil, nil, err
	}
	return shared, own, nil
}

// Shared takes the apply lock shared without waiting. ErrBusy means an
// apply is running. A day-two run that reads the box for a while (doctor)
// holds only this while it reads, and takes the day-two lock (Own) only
// for the short work on shared state.
func Shared(applyPath string) (*Lock, error) {
	return take(applyPath, syscall.LOCK_SH, 0)
}

// Own takes the day-two lock exclusively, waiting up to wait.
func Own(dayTwoPath string, wait time.Duration) (*Lock, error) {
	return take(dayTwoPath, syscall.LOCK_EX, wait)
}
