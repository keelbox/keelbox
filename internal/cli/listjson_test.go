package cli

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
)

func TestListJSON(t *testing.T) {
	isolate(t)
	var b bytes.Buffer
	if cmdListJSON(&b) != 0 || b.String() != "[]\n" {
		t.Errorf("no boxes: %q", b.String())
	}
	register(t, config.StarterFor("acme", "hetzner"))
	register(t, config.StarterFor("cold", "hetzner"))
	acme, _ := config.Load(boxConfigPath("acme"))
	if err := saveRecord(acme, boxRecord{TailnetHost: "100.64.0.7", TailnetPort: 22}); err != nil {
		t.Fatal(err)
	}
	if err := saveParkMark("cold", parkMark{Since: time.Now()}); err != nil {
		t.Fatal(err)
	}
	b.Reset()
	cmdListJSON(&b)
	var got []listedBox
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Box != "acme" || got[0].Address != "100.64.0.7" || !got[0].Tailnet || got[0].Provider != "hetzner" ||
		got[1].State != stateParked || got[1].Address != "" {
		t.Errorf("%+v", got)
	}
}

func TestSessionsJSON(t *testing.T) {
	var b bytes.Buffer
	sessionsJSON([]session{{name: "main", windows: 2, age: "3h", attached: "attached"}, {name: "task", windows: 1, age: "1m"}}, &b)
	var got []listedSession
	if err := json.Unmarshal(b.Bytes(), &got); err != nil || len(got) != 2 || !got[0].Attached || got[1].Attached || got[1].Name != "task" {
		t.Errorf("%v %s", err, b.String())
	}
	b.Reset()
	sessionsJSON(nil, &b)
	if b.String() != "[]\n" {
		t.Errorf("none: %q", b.String())
	}
}
