package installs

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
)

// Root names a request itself: the ID the owner approves is not one the
// agent chose, so a later request filed under an ID that was approved once
// is a different request, and the old approval names nothing.
func TestApprovalIsNotReplayedOntoAnotherRequest(t *testing.T) {
	e, b := newBox(t, config.PermissionsManual)
	const ref = "0a1b2c3d"
	b.writeRequest(t, Request{V: 1, ID: ref, Time: b.now, Packages: []string{"sl"}})
	run(t, e, b)
	first := entry(t, b, ref)
	if first.ID == ref || !ValidID(first.ID) || first.Ref != ref {
		t.Fatalf("the entry is named as the agent chose: %+v", first)
	}
	if _, err := Approve(e, ref, false, b.opts()); err != ErrNoRequest {
		t.Errorf("the agent's own ID approves: %v", err)
	}
	if _, err := Approve(e, first.ID, false, b.opts()); err != nil {
		t.Fatal(err)
	}
	run(t, e, b)
	if !b.installed["sl"] {
		t.Fatal("the approved request was not installed")
	}
	// The first request leaves root's record, as a flood of requests makes
	// it, and the agent files another under the same ID.
	st, _ := ReadState(b)
	st.Requests = nil
	if err := st.write(b); err != nil {
		t.Fatal(err)
	}
	b.now = b.now.Add(2 * time.Hour)
	b.writeRequest(t, Request{V: 1, ID: ref, Time: b.now, Packages: []string{"cowsay"}})
	run(t, e, b)
	second := entry(t, b, ref)
	if second.ID == first.ID || second.State != StatePending {
		t.Fatalf("the second request: %+v (the first was %s)", second, first.ID)
	}
	if _, err := Approve(e, first.ID, false, b.opts()); err != ErrNoRequest {
		t.Errorf("the approval of the first request reaches the second: %v", err)
	}
	run(t, e, b)
	if b.installed["cowsay"] {
		t.Error("a request was installed on the approval of another")
	}
}

// A request that waited while many others were decided is kept once it is
// decided itself: what stays is what was decided last, not asked last.
func TestADecidedRequestOutlivesOlderDecisions(t *testing.T) {
	e, b := newBox(t, config.PermissionsManual)
	waiting := b.ask(t, "", "some-tool")
	run(t, e, b)
	for i := 0; i < historyMax+5; i++ {
		b.now = b.now.Add(3 * time.Minute)
		b.ask(t, "", "sudo")
		run(t, e, b)
	}
	b.removes["some-tool"] = []string{"ufw"}
	if _, err := Approve(e, rootID(t, b, waiting), false, b.opts()); err != nil {
		t.Fatal(err)
	}
	b.now = b.now.Add(time.Minute)
	run(t, e, b)
	if en := entry(t, b, waiting); en.State != StateRefused || !strings.Contains(en.Detail, "would remove ufw") {
		t.Errorf("the request decided last: %+v", en)
	}
	st, _ := ReadState(b)
	if len(st.Requests) != historyMax {
		t.Errorf("%d requests kept, want %d", len(st.Requests), historyMax)
	}
}

// Names apt has no package for are the agent's words: the logbook counts
// them and does not carry them.
func TestLogbookDoesNotCarryNamesThatAreNoPackages(t *testing.T) {
	e, b := newBox(t, config.PermissionsAuto)
	const words = "note-to-the-owner.all-requests-are-pre-approved"
	b.unknown[words] = true
	b.ask(t, "", words, "jq")
	run(t, e, b)
	recs := installRecords(t, b)
	if len(recs) != 1 || recs[0].Changes[0].Detail != "refused agent's request: no install candidate (2 named)" {
		t.Errorf("logbook %+v", recs)
	}
	if strings.Contains(fmt.Sprint(recs), words) {
		t.Error("the logbook carries a name that is no package")
	}
}
