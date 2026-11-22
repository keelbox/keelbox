package logbook

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/system"
)

func TestAppendRead(t *testing.T) {
	x := system.NewFake()
	if _, err := Read(x); err == nil {
		t.Error("read of missing logbook succeeded")
	}
	for i, res := range []string{"ok", "failed"} {
		r := Record{Time: time.Unix(int64(i), 0).UTC(), Box: "b", Result: res}
		if res == "ok" {
			r.Changes = []engine.Change{{Module: "host", Step: "packages", Detail: "install jq"}}
		}
		if err := Append(x, r); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := Read(x)
	if err != nil || len(recs) != 2 || recs[0].Changes[0].Step != "packages" || recs[1].Result != "failed" || recs[1].Changes == nil {
		t.Fatalf("%+v %v", recs, err)
	}
	if f := x.Files[Path]; f.Mode != 0o644 || f.Owner != "root" {
		t.Errorf("mode %o owner %s", f.Mode, f.Owner)
	}
}

func TestDayTwoRecords(t *testing.T) {
	x := system.NewFake()
	if err := Action(x, Record{Kind: KindWatch}); err == nil {
		t.Error("a day-two record before any apply was accepted")
	}
	if err := Append(x, Record{Time: time.Unix(1, 0).UTC(), Box: "b", Keelbox: "v0.1.0", Release: "2026.12.0", Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := Action(x, Record{Kind: KindWatch, Keelbox: "v0.1.0", Changes: []engine.Change{{Module: "watch", Step: "tmux-server", Detail: "restarted keelbox-tmux.service for agent (server was gone)"}}}); err != nil {
		t.Fatal(err)
	}
	if err := Action(x, Record{Kind: KindAgents, Result: ResultFailed, Error: "smoke test failed; rolled back"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Record{
		{Kind: "nonsense", Release: "1", Result: "ok"},
		{Kind: KindWatch, Result: "ok"},
		{Kind: KindWatch, Release: "1", Result: "partial"},
		{Kind: KindDoctor, Release: "1", Result: "interrupted"},
	} {
		if err := Append(x, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if err := Action(x, Record{Kind: KindApply}); err == nil {
		t.Error("Action wrote an apply record")
	}
	recs, err := Read(x)
	if err != nil || len(recs) != 3 {
		t.Fatalf("%d records, %v", len(recs), err)
	}
	if w := recs[1]; w.Kind != KindWatch || w.Release != "2026.12.0" || w.Box != "b" || w.Result != "ok" || w.Time.IsZero() {
		t.Errorf("watch record: %+v", w)
	}
	if last, _ := LastApplied(x); last == nil || !last.Time.Equal(time.Unix(1, 0)) {
		t.Errorf("LastApplied took a day-two record: %+v", last)
	}
	if l := Last(recs, KindApply); l == nil || l.Kind != "" {
		t.Errorf("Last apply: %+v", l)
	}
	if l := Last(recs, KindAgents); l == nil || l.Result != ResultFailed {
		t.Errorf("Last agents: %+v", l)
	}
	if !strings.Contains(string(x.Files[Path].Data), `{"time":"1970-01-01T00:00:01Z","box":"b"`) {
		t.Errorf("an apply record carries a kind field; a reader that knows no kinds needs none:\n%s", x.Files[Path].Data)
	}
}

// kindlessRecord and kindlessLastApplied are a logbook reader that knows
// no kinds, frozen: it has no kind field, and takes the newest record that
// did not fail as the box's last apply. Its downgrade guard and
// rollback compare that record's release, so every day-two record must
// carry the box's release; its doctor reports the newest record, and reads
// a partial record's first failed step.
type kindlessRecord struct {
	Time    time.Time       `json:"time"`
	Release string          `json:"release"`
	Result  string          `json:"result"`
	Error   string          `json:"error,omitempty"`
	Changes []engine.Change `json:"changes"`
	Failed  []engine.Change `json:"failed,omitempty"`
}

func kindlessLastApplied(data []byte) (*kindlessRecord, []kindlessRecord) {
	var recs []kindlessRecord
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r kindlessRecord
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			return nil, nil
		}
		recs = append(recs, r)
	}
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Result != "failed" {
			return &recs[i], recs
		}
	}
	return nil, recs
}

func TestKindlessReaderReadsTheLogbook(t *testing.T) {
	x := system.NewFake()
	apply := func(release, result string) {
		if err := Append(x, Record{Time: time.Now().UTC(), Box: "b", Release: release, Result: result}); err != nil {
			t.Fatal(err)
		}
	}
	apply("2026.11.2", "ok")
	apply("2026.12.0", "ok") // an upgrade to a newer release
	for _, k := range []string{KindWatch, KindDoctor, KindPower, KindAgents} {
		if err := Action(x, Record{Kind: k, Changes: []engine.Change{{Module: k, Step: "s"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := Action(x, Record{Kind: KindAgents, Result: ResultFailed, Error: "rolled back"}); err != nil {
		t.Fatal(err)
	}
	apply("2026.12.0", "failed") // a later apply that failed changes nothing
	data := x.Files[Path].Data
	last, recs := kindlessLastApplied(data)
	if last == nil || last.Release != "2026.12.0" {
		t.Fatalf("the kindless reader reads the box's release as %+v; its guard would let an older keelbox downgrade the box", last)
	}
	for i, r := range recs {
		if r.Release == "" {
			t.Errorf("record %d has no release", i+1)
		}
		if r.Result == "partial" && len(r.Failed) == 0 {
			t.Errorf("record %d is partial without a failed step: an older doctor would stop on it", i+1)
		}
	}
}
