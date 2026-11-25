package modules

import (
	"context"
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/watch"
)

func TestWatchUnits(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{watch.Timer, eventsPath} {
		if !s.units[u] {
			t.Errorf("%s not enabled and running", u)
		}
	}
	svc := s.content(unitDir + watch.Service)
	for _, want := range []string{"ExecCondition=/usr/local/bin/keelbox has-command watch-run", "ExecStart=/usr/local/bin/keelbox watch-run --local /etc/keelbox/keelbox.yaml", "StartLimitIntervalSec=0"} {
		if !strings.Contains(svc, want) {
			t.Errorf("watch service lacks %q:\n%s", want, svc)
		}
	}
	if p := s.content(unitDir + eventsPath); !strings.Contains(p, "PathModified=/home/agent/.local/state/keelbox/events.jsonl\nUnit=keelbox-events.service") {
		t.Errorf("events path unit:\n%s", p)
	}
	if !strings.Contains(s.content(unitDir+eventsService), "ExecStart=/usr/local/bin/keelbox events-sync --local") {
		t.Error("events service does not run events-sync")
	}
	for p, want := range map[string]string{events.Dir: "700 root", events.QueueDir: "700 root", watch.Dir: "755 root", "/home/agent/" + events.AgentDir: "700 agent"} {
		f := s.Files[p]
		if f == nil || !f.Dir {
			t.Errorf("%s missing", p)
			continue
		}
		if got := octal(f.Mode) + " " + f.Owner; got != want {
			t.Errorf("%s: %s, want %s", p, got, want)
		}
	}
	// Before the first watch run doctor warns; it never fails for that.
	for _, f := range (Watch{}).Verify(context.Background(), e) {
		if f.Level == engine.LevelFail {
			t.Errorf("doctor: %+v", f)
		}
	}
}

func octal(m fs.FileMode) string { return strconv.FormatUint(uint64(m.Perm()), 8) }

func TestBootMarkerUnit(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	if !s.units[watch.BootUnit] {
		t.Errorf("%s not enabled and started", watch.BootUnit)
	}
	unit := s.content(unitDir + watch.BootUnit)
	for _, want := range []string{
		"Type=oneshot\nRemainAfterExit=yes",
		"ExecCondition=/usr/local/bin/keelbox has-command boot-mark",
		"ExecStart=/usr/local/bin/keelbox boot-mark\n",
		"ExecStop=/usr/bin/touch /var/lib/keelbox/power/clean-shutdown",
		"RequiresMountsFor=/var/lib/keelbox/power",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("boot unit lacks %q:\n%s", want, unit)
		}
	}
	// Default dependencies: systemd stops it at shutdown, before /var/lib
	// goes away.
	if strings.Contains(unit, "DefaultDependencies=no") {
		t.Error("the boot unit must keep the default dependencies")
	}
	if f := s.Files[watch.PowerDir]; f == nil || f.Owner != "root" || octal(f.Mode) != "755" {
		t.Errorf("%s: %+v", watch.PowerDir, f)
	}
}

// The conversations unit's stop notes the agent's conversations at every
// orderly shutdown, and every user manager stops after it (ADR 0096).
func TestConversationsUnit(t *testing.T) {
	s := newSim(t)
	e := simEnv(t, s, testConfig(t, ""))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	if !s.units[ConversationsUnit] {
		t.Errorf("%s not enabled and started", ConversationsUnit)
	}
	unit := s.content(unitDir + ConversationsUnit)
	for _, want := range []string{
		"Type=oneshot\nRemainAfterExit=yes",
		"ExecStart=/bin/rm -f /run/keelbox-tmux-saved\n",
		"Before=keelbox-watch.service\n",
		"ExecStop=-/usr/local/bin/keelbox note-conversations --local /etc/keelbox/keelbox.yaml\n",
		"RequiresMountsFor=/var/lib/keelbox/watch",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("conversations unit lacks %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "DefaultDependencies=no") {
		t.Error("the conversations unit must keep the default dependencies")
	}
	if got := s.content(unitDir + "user@.service.d/50-keelbox-conversations.conf"); !strings.Contains(got, "[Unit]\nBefore="+ConversationsUnit+"\n") {
		t.Errorf("user managers not ordered before the unit:\n%s", got)
	}
}
