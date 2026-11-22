package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/keelbox/keelbox/internal/prompt"
)

// Ctrl-C while keelbox changes a box stops it between two steps, never in
// the middle of one (engine.Apply), so the box is left as a consistent
// prefix of the apply and running the same command again carries on. The
// first Ctrl-C (or SIGTERM, or the SIGHUP of a terminal that closes) cancels
// the command's context and says so; after it keelbox stops catching the
// signal, so a second Ctrl-C ends it at once. Without the SIGHUP an apply on
// this machine would end in the middle of a step when its window closes.

// envChild marks a keelbox started by another keelbox (up runs sudo keelbox
// apply on this machine): the parent has already told the user about the
// interrupt.
const envChild = "KEELBOX_CHILD"

const stepNote = "stopping after the current step; press Ctrl-C again to stop at once"

// interruptNote is what the first Ctrl-C prints; keelbox up clears it
// while it asks questions, which Ctrl-C simply ends.
var interruptNote atomic.Pointer[string]

func setInterruptNote(s string) { interruptNote.Store(&s) }

func init() { setInterruptNote(stepNote) }

// interruptible returns a context the first Ctrl-C cancels. notice says
// whether to tell the user what happens next.
func interruptible(stderr io.Writer, notice bool) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		select {
		case <-sig:
			signal.Stop(sig)
			// A question that waits is what the Ctrl-C ends: no step runs.
			if n := *interruptNote.Load(); notice && n != "" && os.Getenv(envChild) == "" && !prompt.Waiting() {
				fmt.Fprintln(stderr, "\nkeelbox: "+n)
			}
			cancel()
		case <-done:
		}
	}()
	return ctx, func() { close(done); signal.Stop(sig); cancel() }
}
