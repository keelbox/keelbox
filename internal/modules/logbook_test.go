package modules

import (
	"context"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/logbook"
)

// Doctor's last-apply check reads the newest apply, not a newer day-two
// record: a failed agent upgrade is not a failed apply.
func TestLastApplyIgnoresDayTwoRecords(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	if err := logbook.Append(s, logbook.Record{Time: time.Now().UTC(), Box: "testbox", Release: s.m.Release, Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := logbook.Action(s, logbook.Record{Kind: logbook.KindAgents, Result: logbook.ResultFailed, Error: "smoke test failed"}); err != nil {
		t.Fatal(err)
	}
	fs := Logbook{}.Verify(context.Background(), e)
	if len(fs) != 1 || fs[0].Level != engine.LevelOK {
		t.Errorf("last-apply: %+v", fs)
	}
}
