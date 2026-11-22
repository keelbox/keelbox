package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/manifest"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/reports"
	"github.com/keelbox/keelbox/internal/system"
)

// The agent's keelbox report writes one line to its own events file and
// tells it how the owner reviews and sends it; nothing is sent.
func TestReportAsTheAgent(t *testing.T) {
	onBoxAs(t, "agent")
	home := t.TempDir()
	old := eventHome
	eventHome = func() string { return home }
	t.Cleanup(func() { eventHome = old })
	var out, errb bytes.Buffer
	if code := Main([]string{"report", "expose: says nothing listens", "--area", "expose", "--detail", "Nothing listens on port 5173 yet"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"Saved keelbox report", "on devbox as a draft", "Nothing was sent. Reports never leave your box unless you send them.", "keelbox reports devbox --send N"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	data, err := os.ReadFile(filepath.Join(home, events.AgentFile))
	if err != nil {
		t.Fatal(err)
	}
	var l events.AgentLine
	if err := json.Unmarshal(data, &l); err != nil || l.Kind != events.ReportKind || l.Area != "expose" || l.Title != "expose: says nothing listens" {
		t.Errorf("line %s: %v", data, err)
	}

	if code := Main([]string{"report", "x", "--area", "wherever"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "is not one of") {
		t.Errorf("bad area: exit %d %s", code, errb.String())
	}
	// reports.enabled: false reaches the agent through the snapshot.
	s, _ := os.ReadFile(snapshotPath)
	_ = os.WriteFile(snapshotPath, bytes.Replace(s, []byte(`"agent":"agent",`), []byte(`"agent":"agent","reports_off":true,`), 1), 0o644)
	errb.Reset()
	if code := Main([]string{"report", "x"}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "reports are off") {
		t.Errorf("reports off: exit %d %s", code, errb.String())
	}
}

func TestReportNotTheAgent(t *testing.T) {
	onBoxAs(t, "admin")
	var out, errb bytes.Buffer
	if code := Main([]string{"report", "x"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "keelbox reports BOX") {
		t.Errorf("exit %d %s", code, errb.String())
	}
}

func reportEnv(t *testing.T, enabled bool) *engine.Env {
	t.Helper()
	cfg := &config.Config{Box: "acme"}
	cfg.Reports.Enabled = &enabled
	return &engine.Env{X: system.NewFake(), Cfg: cfg, M: &manifest.Manifest{Release: "2026.11.4"}, Facts: engine.Facts{OSVersion: "24.04", Arch: "amd64"}}
}

// The events run files reports with the box's facts, or drops them with
// reports.enabled: false.
func TestFileReports(t *testing.T) {
	line := events.AgentLine{V: 1, Time: time.Now().UTC(), Kind: events.ReportKind, Session: "0a1b2c3d", Title: "t", Area: "codex", Detail: "d"}
	e := reportEnv(t, true)
	fileReports(context.Background(), e, []events.AgentLine{line})
	list, _ := reports.List(e.X)
	if len(list) != 1 || list[0].Facts.Ubuntu != "24.04" || list[0].Facts.Release != "2026.11.4" {
		t.Fatalf("inbox %+v", list)
	}
	var out, errb bytes.Buffer
	if code := Reports(e, options{}, &out, &errb); code != 0 || !strings.Contains(out.String(), "0a1b2c3d") || !strings.Contains(out.String(), "never leave your box") {
		t.Errorf("list: %d %s %s", code, out.String(), errb.String())
	}
	out.Reset()
	if Reports(e, options{show: "1", json: true}, &out, &errb); !strings.Contains(out.String(), `"id":"0a1b2c3d"`) {
		t.Errorf("show json: %s", out.String())
	}
	if Reports(e, options{del: "1"}, &out, &errb); func() bool { l, _ := reports.List(e.X); return len(l) != 0 }() {
		t.Error("not deleted")
	}

	off := reportEnv(t, false)
	if p := fileReports(context.Background(), off, []events.AgentLine{line}); len(p) != 1 || !strings.Contains(p[0], "reports.enabled is false") {
		t.Errorf("problems %v", p)
	}
	if list, _ := reports.List(off.X); len(list) != 0 {
		t.Error("filed with reports.enabled: false")
	}
}

// --send shows the redacted text, opens nothing on no, and on yes opens
// GitHub's issue form with the redacted text; keelbox posts nothing.
func TestSendReport(t *testing.T) {
	t.Setenv("DISPLAY", ":0")
	oldFetch, oldOpen, oldEdit := fetchReport, openBrowser, editText
	t.Cleanup(func() { fetchReport, openBrowser, editText = oldFetch, oldOpen, oldEdit })
	r := reports.Report{ID: "0a1b2c3d", Title: "codex sandbox fails on acme", Area: "sandbox",
		Detail: "token ghp_" + strings.Repeat("x", 30) + " in /home/agent/projects/shop-api/.env", Facts: reports.Facts{Keelbox: "0.2.0"}}
	fetchReport = func(context.Context, string) (reports.Report, error) { return r, nil }
	var opened []string
	openBrowser = func(u string) error { opened = append(opened, u); return nil }
	editText = func(_ context.Context, s string) (string, error) { return s + "\nadded by owner sam@example.com", nil }
	cfg := &config.Config{Box: "acme"}
	run := func(answers string) string {
		var out, errb bytes.Buffer
		sendReport(context.Background(), cfg, "1", prompt.New(strings.NewReader(answers), &out, true), &out, &errb)
		return out.String() + errb.String()
	}

	// Keep project names: no. Edit: no. Open: no.
	out := run("n\nn\nn\n")
	if len(opened) != 0 || !strings.Contains(out, "Nothing sent") {
		t.Fatalf("opened on no: %v\n%s", opened, out)
	}
	_, shown, _ := strings.Cut(out, "Read all of it.")
	for _, gone := range []string{"ghp_", "/home/agent", "shop-api", "on acme"} {
		if strings.Contains(shown, gone) {
			t.Errorf("%q shown unredacted:\n%s", gone, out)
		}
	}
	if !strings.Contains(out, "public") || !strings.Contains(out, "Redacted: ") {
		t.Errorf("no public warning or redaction list:\n%s", out)
	}

	// Edit once (the edit is redacted again), then open.
	run("n\ny\nn\ny\n")
	if len(opened) != 1 || !strings.HasPrefix(opened[0], issuesURL+"?") {
		t.Fatalf("opened %v", opened)
	}
	u, _ := url.Parse(opened[0])
	body := u.Query().Get("body")
	if strings.Contains(body, "ghp_") || strings.Contains(body, "sam@") || !strings.Contains(body, "<email>") || u.Query().Get("title") != "codex sandbox fails on <box>" {
		t.Errorf("issue %q\n%s", u.Query().Get("title"), body)
	}

	// Security reports go to the private advisory form.
	r.Area, opened = "security", nil
	run("n\nn\ny\n")
	if len(opened) != 1 || opened[0] != advisoryURL {
		t.Errorf("security report opened %v", opened)
	}
}

func TestIssueURLFits(t *testing.T) {
	u, cut := issueURL("t", strings.Repeat("é long body ", 2000))
	if !cut || len(u) > issueURLMax {
		t.Errorf("cut %v, %d bytes", cut, len(u))
	}
	if _, err := url.Parse(u); err != nil {
		t.Error(err)
	}
}
