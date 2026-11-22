package snapshot

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/system"
)

func TestWriteRead(t *testing.T) {
	x := system.NewFake()
	if _, found, err := Read(x); found || err != nil {
		t.Fatalf("no snapshot: %v %v", found, err)
	}
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	in := Status{Written: now, Box: "acme", Keelbox: "v0.1.0", Release: "2026.12.0",
		LastApply: &Apply{Time: now.Add(-time.Hour), Result: "ok", Keelbox: "v0.1.0"},
		Disk:      []Disk{{Mount: "/", Percent: 41}}}
	if err := Write(x, in); err != nil {
		t.Fatal(err)
	}
	f := x.Files[Path]
	if f.Mode != 0o644 || f.Owner != "root" {
		t.Errorf("snapshot %o %s: the admin reads it over plain ssh", f.Mode, f.Owner)
	}
	if !strings.Contains(string(f.Data), `"status": 1`) {
		t.Errorf("no schema version:\n%s", f.Data)
	}
	out, found, err := Read(x)
	if err != nil || !found || out.Box != "acme" || out.LastApply.Result != "ok" || out.Disk[0].Percent != 41 || out.Doctor != nil {
		t.Errorf("read back %+v %v %v", out, found, err)
	}
	if out.Stale(now.Add(5*time.Minute)) || !out.Stale(now.Add(11*time.Minute)) {
		t.Error("staleness")
	}
}

func TestParseRefusesWhatItCannotRead(t *testing.T) {
	for in, want := range map[string]string{
		`{"status": 2, "box": "acme"}`: "keelbox self-update",
		`{"box": "acme"}`:              "no schema version",
		`not json`:                     "not valid JSON",
	} {
		if _, err := Parse([]byte(in)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", in, err)
		}
	}
}

// The agent names a tmux session with terminal sequences: the snapshot keeps it as escaped text, and a laptop reading a box's
// raw snapshot gets the same.
func TestSnapshotStringsArePlain(t *testing.T) {
	x := system.NewFake()
	evil := "\x1b[2J\x1b]0;owned\x07ALL OK"
	in := Status{Written: time.Now(), Box: "acme", Sessions: &Sessions{Tmux: true, Names: []string{evil}}}
	if err := Write(x, in); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(x.Files[Path].Data), `\u001b`) {
		t.Errorf("snapshot kept the escape:\n%s", x.Files[Path].Data)
	}
	raw := `{"status":1,"box":"acme","sessions":{"tmux":true,"names":["\u001b[2Jmain"]}}`
	s, err := Parse([]byte(raw))
	if err != nil || s.Sessions.Names[0] != `\x1b[2Jmain` {
		t.Errorf("Parse: %q %v", s.Sessions.Names, err)
	}
}

func TestProjects(t *testing.T) {
	for want, p := range map[string]Project{
		"not a git repository":                       {},
		"clean and pushed":                           {Repo: true},
		"1 uncommitted change":                       {Repo: true, Uncommitted: 1},
		"3 uncommitted changes, 1 commit not pushed": {Repo: true, Uncommitted: 3, Unpushed: 1},
		"2 commits not pushed":                       {Repo: true, Unpushed: 2},
		"no remote":                                  {Repo: true, NoRemote: true, Unpushed: 4},
		"2 uncommitted changes, no remote":           {Repo: true, NoRemote: true, Uncommitted: 2},
		"could not be checked (git did not answer)":  {Repo: true, Unchecked: true},
	} {
		if got := p.State(); got != want {
			t.Errorf("%+v: %q, want %q", p, got, want)
		}
		if p.OnlyHere() != (want != "clean and pushed") {
			t.Errorf("%+v: only here %v", p, p.OnlyHere())
		}
	}
	var none *Projects
	if none.Summary() != "" || none.Rows() != nil || none.OnlyHere() != nil {
		t.Error("no projects section printed something")
	}
	p := &Projects{Dir: "~/projects"}
	if p.Summary() != "none in ~/projects" || len(p.Rows()) != 0 {
		t.Errorf("empty: %q %v", p.Summary(), p.Rows())
	}
	p = &Projects{Dir: "~/projects", Total: 2, List: []Project{{Name: "api", Repo: true}, {Name: "site", Repo: true}}}
	if p.Summary() != "2 in ~/projects, all clean and pushed" {
		t.Errorf("clean: %q", p.Summary())
	}
	// At most Shown lines, then how many more; the count is of every
	// directory, read or not.
	p = &Projects{Dir: "~/projects", Total: 60}
	for i := range 50 {
		p.List = append(p.List, Project{Name: "p" + strconv.Itoa(i), Repo: i > 2})
	}
	rows := p.Rows()
	if len(rows) != Shown+1 || rows[0] != "p0: not a git repository" || rows[Shown] != "and 50 more" {
		t.Errorf("rows: %q", rows)
	}
	if p.Summary() != "60 in ~/projects, at least 3 with work that is only on this box" {
		t.Errorf("summary: %q", p.Summary())
	}
}

// A project's name reaches the laptop through the snapshot; whatever wrote
// it, it is one plain line once parsed.
func TestProjectNamesArePlain(t *testing.T) {
	s, err := Parse([]byte(`{"status":1,"box":"acme","projects":{"dir":"~/projects","total":1,"list":[{"name":"a\u001b[2Jb\nc","repo":false}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Projects.List[0].Name; got != `a\x1b[2Jb c` {
		t.Errorf("name %q", got)
	}
}
