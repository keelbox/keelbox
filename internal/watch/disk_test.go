package watch

import (
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/snapshot"
)

func TestDiskAlerts(t *testing.T) {
	b := newBox(t, "host:\n  disk: {warn_percent: 80, fail_percent: 90}\n")
	var st State
	kinds := func() []string {
		var out []string
		for _, e := range queued(t, b) {
			out = append(out, e.Kind+" "+e.Fields["mount"]+" "+e.Fields["percent"])
		}
		return out
	}
	at := func(min int, disks ...snapshot.Disk) {
		diskAlerts(b.e, &st, disks, t0.Add(time.Duration(min)*time.Minute))
	}
	at(0, snapshot.Disk{Mount: "/", Percent: 50})
	at(2, snapshot.Disk{Mount: "/", Percent: 81}, snapshot.Disk{Mount: "/home", Percent: 10})
	at(4, snapshot.Disk{Mount: "/", Percent: 85})
	at(6, snapshot.Disk{Mount: "/", Percent: 92})
	at(8, snapshot.Disk{Mount: "/", Percent: 86})
	at(10, snapshot.Disk{Mount: "/", Percent: 40})
	at(12, snapshot.Disk{Mount: "/", Percent: 82})
	at(12+24*60, snapshot.Disk{Mount: "/", Percent: 83})
	want := "disk-warn / 81|disk-full / 92|disk-warn / 82|disk-warn / 83"
	if got := strings.Join(kinds(), "|"); got != want {
		t.Errorf("alerts:\n got %s\nwant %s", got, want)
	}
	if q := queued(t, b); q[1].Level != events.Urgent || !strings.Contains(q[1].Fields["detail"], "alerts at 90%") {
		t.Errorf("disk-full: %+v", q[1])
	}
}
