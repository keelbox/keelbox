package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// parkingCloud is a cloud that resizes by replacing the server, and could
// not create one again: the box is left parked.
type parkingCloud struct {
	*hetznerCloud
	since time.Time
}

func (c parkingCloud) resize(context.Context, *cloudServer, string, func(string, ...any)) (*cloudServer, error) {
	return nil, &errLeftParked{parked: &cloudParked{ref: "snapshot 7", since: c.since, detail: "40 GB"}, cause: errors.New("no capacity for cpx22 or cx23")}
}

// A resize that leaves the box parked says so and names resume; it does
// not start a server that is gone, and the config keeps the old size.
func TestResizeThatLeavesTheBoxParkedSaysSo(t *testing.T) {
	w := newParkWorld(t)
	since := time.Now().Add(-time.Minute).Truncate(time.Second)
	code, out := w.doWith(t, parkingCloud{w.cl, since}, "resize", options{size: "cpx22", yes: true}, nil)
	want := "keelbox: the size was not changed: no capacity for cpx22 or cx23\n" +
		"keelbox: acme is parked: snapshot 7 holds its disk. Bring it back: keelbox resume acme\n"
	if code != 1 || !strings.HasSuffix(out, want) || strings.Contains(out, "is still cx23") {
		t.Errorf("exit %d\n%s", code, out)
	}
	if got := strings.Join(w.writes(), " "); got != "POST /servers/42/actions/shutdown" {
		t.Errorf("calls after the failed resize: %s", got)
	}
	if mark, ok := loadParkMark("acme"); !ok || !mark.Since.Equal(since) || mark.Address != "203.0.113.7" || mark.Snapshot != "snapshot 7" {
		t.Errorf("mark %+v %v", mark, ok)
	}
	if data, _ := os.ReadFile(boxConfigPath("acme")); !strings.Contains(string(data), "server_type: cx23") || len(w.applied) != 0 {
		t.Errorf("config or apply after the failed resize: %v\n%s", w.applied, data)
	}
}
