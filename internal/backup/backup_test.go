package backup

import (
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/system"
)

func TestLabel(t *testing.T) {
	for in, want := range map[string]string{"/home/agent": "home-agent", "/srv/data/x": "srv-data-x", "/": "root"} {
		if got := Label(in); got != want {
			t.Errorf("Label(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAccountFor(t *testing.T) {
	agent := system.User{Name: "agent", Home: "/home/agent"}
	admin := system.User{Name: "admin", Home: "/home/admin"}
	for in, want := range map[string]string{
		"/home/agent":          "agent",
		"/home/agent/projects": "agent",
		"/home/agentx":         "root",
		"/home/admin/notes":    "admin",
		"/srv":                 "root",
	} {
		if got := accountFor(in, agent, admin); got != want {
			t.Errorf("accountFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReaderRunsAsTheOwnerWithACleanEnvironment(t *testing.T) {
	cmd := ReaderCommand(Source{Path: "/home/agent", Account: "agent", Label: "home-agent"}, []string{"node_modules", "a b"})
	got := strings.Join(cmd, " ")
	if !strings.HasPrefix(got, "/usr/sbin/runuser -u agent -- env -i PATH=/usr/bin:/bin LANG=C.UTF-8 /bin/sh -c ") {
		t.Errorf("reader does not start as the agent with a clean environment: %s", got)
	}
	if cmd[len(cmd)-1] != "--exclude=a b" || cmd[len(cmd)-3] != "/home/agent" {
		t.Errorf("path and excludes are not separate arguments: %q", cmd)
	}
	root := ReaderCommand(Source{Path: "/srv", Account: "root"}, nil)
	if root[0] != "env" {
		t.Errorf("root reader: %q", root)
	}
}

func TestParseSummary(t *testing.T) {
	out := `{"message_type":"status","percent_done":0.5}
{"message_type":"summary","snapshot_id":"4f1c2a9e0000","data_added":10,"total_bytes_processed":20}`
	s, ok := parseSummary(out)
	if !ok || s.SnapshotID != "4f1c2a9e0000" || s.DataAdded != 10 || s.TotalBytes != 20 {
		t.Errorf("parseSummary = %+v, %v", s, ok)
	}
	if _, ok := parseSummary("garbage"); ok {
		t.Error("parseSummary accepted garbage")
	}
}
