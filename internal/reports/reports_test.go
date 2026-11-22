package reports

import (
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/system"
)

func TestRedact(t *testing.T) {
	in := strings.Join([]string{
		"token ghp_" + strings.Repeat("a1", 15) + " and github_pat_" + strings.Repeat("B2_", 10),
		"key sk-ant-api03-" + strings.Repeat("x", 30) + ", slack xoxb-1234567890-abcdef",
		"aws AKIAABCDEFGHIJKLMNOP aws_secret_access_key = wJalrXUtnFEMI/K7MDENG",
		"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEA\n-----END OPENSSH PRIVATE KEY-----",
		"mail sam@example.com, tskey-auth-kAbc123DEF-xyz",
		"at acme.tail1234.ts.net 100.101.102.103 fd7a:115c:a1e0::5 from 203.0.113.9 and 2a01:4f8:c0c:1::1",
		"local 127.0.0.1:5173 [::1]:5173 10.0.0.4 at 12:34:56",
		"acme: /home/agent/projects/acme-api/src/server.ts failed; /root/x; ~/projects/public-demo",
	}, "\n")
	out, rs := Redact(in, "acme", []string{"public-demo"})
	for _, gone := range []string{"ghp_", "github_pat_", "sk-ant", "xoxb", "AKIA", "wJalr", "eyJhbGci", "b3BlbnNz", "sam@", "tskey",
		"tail1234", "100.101", "fd7a:115c", "203.0.113", "2a01:4f8", "/home/agent", "/root", "acme-api", "server.ts", "acme:"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q survives redaction:\n%s", gone, out)
		}
	}
	for _, kept := range []string{"127.0.0.1:5173", "[::1]:5173", "10.0.0.4", "12:34:56", "~/projects/public-demo", "~/projects/<project>/<path> failed"} {
		if !strings.Contains(out, kept) {
			t.Errorf("%q lost:\n%s", kept, out)
		}
	}
	kinds := map[string]int{}
	for _, r := range rs {
		kinds[r.Kind] = r.Count
	}
	for k, n := range map[string]int{"GitHub token": 2, "public IP address": 2, "tailnet address": 2, "project name or path": 1, "home path": 2, "box name": 1} {
		if kinds[k] != n {
			t.Errorf("%s: %d redactions, want %d (%v)", k, kinds[k], n, rs)
		}
	}
	if p := Projects(in); strings.Join(p, " ") != "acme-api public-demo" {
		t.Errorf("projects %v", p)
	}
}

func TestInbox(t *testing.T) {
	x := system.NewFake()
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	r := Report{ID: "0a1b2c3d", Filed: now, Received: now, Title: "expose: says nothing listens\x1b[2J", Area: "expose", Detail: "exact error"}
	if err := Save(x, r); err != nil {
		t.Fatal(err)
	}
	f := x.Files[Dir+"/0a1b2c3d.json"]
	if f == nil || f.Mode != 0o600 || f.Owner != "root" {
		t.Fatalf("inbox file %+v", f)
	}
	list, _ := List(x)
	if len(list) != 1 || strings.Contains(list[0].Title, "\x1b") {
		t.Fatalf("list %+v", list)
	}
	if err := Save(x, r); err != nil {
		t.Errorf("the same report twice: %v", err)
	}
	for i := range DayMax {
		_ = Save(x, Report{ID: NewID(), Received: now.Add(time.Duration(i) * time.Minute), Title: "t", Area: "other"})
	}
	if list, _ := List(x); len(list) != DayMax {
		t.Errorf("a day's budget: %d reports kept, want %d", len(list), DayMax)
	}
	for _, bad := range []Report{
		{ID: "x", Title: "t", Area: "other"},
		{ID: "0a1b2c3e", Title: "", Area: "other"},
		{ID: "0a1b2c3e", Title: "two\nlines", Area: "other"},
		{ID: "0a1b2c3e", Title: "t", Area: "wherever"},
		{ID: "0a1b2c3e", Title: "t", Area: "other", Detail: strings.Repeat("x", DetailMax+1)},
	} {
		if Check(bad.ID, bad.Title, bad.Area, bad.Detail) == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if n, got, ok := Find(list, "1"); !ok || n != 1 || got.ID != "0a1b2c3d" {
		t.Errorf("find 1: %v %+v", ok, got)
	}
	if err := Delete(x, "0a1b2c3d"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Find(func() []Report { l, _ := List(x); return l }(), "0a1b2c3d"); ok {
		t.Error("deleted report still listed")
	}
	title, body := Issue(Report{Title: "T", Area: "codex", Detail: "err", Facts: Facts{Keelbox: "0.2.0", Release: "2026.11", Ubuntu: "24.04", Arch: "amd64", DoctorFailing: []string{"agents/codex-sandbox"}}})
	if title != "T" || !strings.Contains(body, "doctor failing: agents/codex-sandbox") || !strings.Contains(body, "**Area:** codex") {
		t.Errorf("issue %q\n%s", title, body)
	}
}
