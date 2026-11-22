package cli

import (
	"bytes"
	"os"
	"syscall"
	"testing"
	"time"
)

// A terminal that closes sends SIGHUP: it cancels the command's context
// as Ctrl-C does, so an apply on this machine ends between two steps and
// not wherever it was.
func TestAClosedTerminalStopsBetweenSteps(t *testing.T) {
	var errs bytes.Buffer
	ctx, done := interruptible(&errs, false)
	defer done()
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("SIGHUP did not cancel the command's context")
	}
}
