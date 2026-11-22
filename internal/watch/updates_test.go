package watch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/system"
)

// Pending security updates and a needed reboot reach the snapshot and raise
// updates-pending when they appear, not every run.
func TestUpdatesPending(t *testing.T) {
	b := newBox(t, "")
	b.AddFile(AptCheck, "", 0o755)
	b.On(AptCheck, system.Result{Stderr: "137;12"})
	var st State
	ctx := context.Background()
	at := func(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

	s := updates(ctx, b.e, &st, at(0))
	if s.Security != 12 || s.RebootRequired != nil {
		t.Fatalf("snapshot %+v", s)
	}
	if q := queued(t, b); len(q) != 1 || q[0].Kind != "updates-pending" || q[0].Fields["security"] != "12" || !strings.Contains(q[0].Fields["detail"], "sudo unattended-upgrade") {
		t.Fatalf("events %+v", q)
	}
	updates(ctx, b.e, &st, at(2))
	b.AddFile(RebootRequired, "*** System restart required ***\n", 0o644)
	s = updates(ctx, b.e, &st, at(4))
	if s.RebootRequired == nil || !s.RebootRequired.Equal(at(4)) {
		t.Fatalf("reboot not recorded: %+v", s)
	}
	updates(ctx, b.e, &st, at(6))
	// The reboot raises a second event; the runs between raise nothing.
	q := queued(t, b)
	if len(q) != 2 || !strings.Contains(q[1].Fields["detail"], "needs a reboot") || !strings.Contains(q[1].Fields["detail"], "to reboot now: keelbox reboot acme") {
		t.Errorf("events %+v", q)
	}
	if st.Updates.SentReason != "reboot,security" || !st.Updates.Sent.Equal(at(4)) {
		t.Errorf("state %+v", st.Updates)
	}
	// The count is refreshed hourly, not every run.
	n := 0
	for _, c := range b.Commands() {
		if strings.HasPrefix(c, AptCheck) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("apt-check ran %d times in 6 minutes", n)
	}
	// After the reboot, and with nothing pending, it is forgotten.
	delete(b.Files, RebootRequired)
	b.On(AptCheck, system.Result{Stderr: "0;0"})
	if s := updates(ctx, b.e, &st, at(120)); s.RebootRequired != nil || s.Security != 0 {
		t.Errorf("after reboot %+v", s)
	}
}

// Without apt-check the count is unknown and nothing is raised for it.
func TestUpdatesWithoutAptCheck(t *testing.T) {
	b := newBox(t, "")
	var st State
	if s := updates(context.Background(), b.e, &st, t0); s.Security != 0 || s.RebootRequired != nil {
		t.Errorf("%+v", s)
	}
	if q := queued(t, b); len(q) != 0 {
		t.Errorf("events %+v", q)
	}
}
