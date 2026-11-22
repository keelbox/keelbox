package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/events"
	"github.com/keelbox/keelbox/internal/plain"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/reports"
)

// keelbox report and keelbox reports (ADR 0062). The agent files a report
// on its box only after the user said yes in the conversation; it lands in
// root's inbox there. The owner reads the inbox from their laptop and sends
// a report themselves: keelbox shows the exact text, redacts it, lets them
// edit it, asks, and opens a prefilled GitHub issue that they submit.
// Nothing is ever sent by keelbox.

const (
	issuesURL   = "https://github.com/keelbox/keelbox/issues/new"
	advisoryURL = "https://github.com/keelbox/keelbox/security/advisories/new"
	// issueURLMax keeps the prefilled URL within what browsers and GitHub
	// take; a longer body is cut, and the owner pastes the rest.
	issueURLMax = 7000
	neverSent   = "Reports never leave your box unless you send them."
)

// cmdReport is keelbox report "TITLE" [--detail TEXT|-] [--area AREA], run by
// the agent on its box. It writes one line to the agent's own events file;
// root reads it into the inbox (ADR 0032's path, without a notification).
func cmdReport(o options, pos []string, stdin io.Reader, stdout, stderr io.Writer) int {
	box, agent, ok := onBox()
	if !ok || currentUser() != agent {
		failf(stderr, "keelbox report is for the agent on its box, after the user said yes. Read and send the reports on a box: keelbox reports BOX")
		return 2
	}
	if s, err := readSnapshot(); err == nil && s.ReportsOff {
		failf(stderr, "keelbox reports are off on %s (reports.enabled: false); nothing was saved. Tell the user the problem instead.\n", box)
		return 1
	}
	if len(pos) != 1 {
		fmt.Fprintln(stderr, `keelbox: report takes one title: keelbox report "expose: Nothing listens though vite runs" --area expose --detail "exact error"`)
		return 2
	}
	detail := o.detail
	if detail == "-" {
		b, _ := io.ReadAll(io.LimitReader(stdin, 4*reports.DetailMax+1))
		detail = string(b)
	}
	area := o.area
	if area == "" {
		area = "other"
	}
	id := reports.NewID()
	title, detail := strings.TrimSpace(pos[0]), strings.TrimSpace(detail)
	if err := reports.Check(id, title, area, detail); err != nil {
		failf(stderr, "report: %v", err)
		return 2
	}
	l := events.AgentLine{V: 1, Time: time.Now().UTC(), CLI: "keelbox", Kind: events.ReportKind, Session: id, Title: title, Area: area, Detail: detail}
	home := eventHome()
	if home == "" {
		failf(stderr, "report: no home directory for this account")
		return 1
	}
	if err := events.AppendAgent(filepath.Join(home, events.AgentFile), l); err != nil {
		failf(stderr, "report: %v (shorten --detail to the exact error)", err)
		return 1
	}
	if box == "" {
		box = "BOX"
	}
	fmt.Fprintf(stdout, "Saved keelbox report %s on %s as a draft: %q (area %s).\n", id, box, plain.Line(title), area)
	fmt.Fprintf(stdout, "Nothing was sent. %s\n", neverSent)
	fmt.Fprintf(stdout, "Tell the user how to review and send it, from their laptop:\n"+
		"  keelbox reports %s              list the drafts\n"+
		"  keelbox reports %s --send N     show the exact text, redact and edit it, then open a GitHub issue to submit\n", box, box)
	return 0
}

// Reports is keelbox reports on the box, as root: list the inbox, show one
// (--show N, with --json for --send on the laptop), or delete one.
func Reports(e *engine.Env, o options, stdout, stderr io.Writer) int {
	list, err := reports.List(e.X)
	if err != nil {
		fail(stderr, err)
		return 1
	}
	switch {
	case o.del != "":
		_, r, ok := reports.Find(list, o.del)
		if !ok {
			failf(stderr, "no report %s on %s: keelbox reports %s lists them\n", o.del, e.Cfg.Box, e.Cfg.Box)
			return 1
		}
		if err := reports.Delete(e.X, r.ID); err != nil {
			fail(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "Deleted report %s (%s).\n", r.ID, plain.Line(r.Title))
		return 0
	case o.show != "":
		n, r, ok := reports.Find(list, o.show)
		if !ok {
			failf(stderr, "no report %s on %s: keelbox reports %s lists them\n", o.show, e.Cfg.Box, e.Cfg.Box)
			return 1
		}
		if o.json {
			return writeJSON(stdout, r, stderr)
		}
		fmt.Fprintf(stdout, "Report %d (%s), filed %s by the agent; its text is the agent's, not checked:\n\n", n, r.ID, r.Filed.Local().Format("2006-01-02 15:04"))
		fmt.Fprintf(stdout, "%s\narea: %s\n\n%s\n\nBox facts added by keelbox:\n", plain.Line(r.Title), r.Area, plain.Text(r.Detail))
		for _, l := range reports.FactLines(r.Facts) {
			fmt.Fprintln(stdout, "  "+l)
		}
		fmt.Fprintf(stdout, "\n%s Send it: keelbox reports %s --send %d\n", neverSent, e.Cfg.Box, n)
		return 0
	}
	if len(list) == 0 {
		fmt.Fprintf(stdout, "No keelbox reports on %s. The agent files one only after you say yes in a conversation. %s\n", e.Cfg.Box, neverSent)
		return 0
	}
	fmt.Fprintf(stdout, "%-3s %-8s  %-16s  %-14s  %s\n", "N", "ID", "FILED", "AREA", "TITLE")
	for i, r := range list {
		fmt.Fprintf(stdout, "%-3d %-8s  %-16s  %-14s  %s\n", i+1, r.ID, r.Filed.Local().Format("2006-01-02 15:04"), r.Area, plain.Line(r.Title))
	}
	fmt.Fprintf(stdout, "\n%s Read one: keelbox reports %s --show N. Send: --send N. Delete: --delete N.\n", neverSent, e.Cfg.Box)
	return 0
}

func writeJSON(out io.Writer, v any, stderr io.Writer) int {
	b, err := json.Marshal(v)
	if err != nil {
		fail(stderr, err)
		return 1
	}
	fmt.Fprintln(out, string(b))
	return 0
}

// The send flow's hands, which tests replace.
var (
	editText = func(ctx context.Context, text string) (string, error) {
		f, err := os.CreateTemp("", "keelbox-report-*.md")
		if err != nil {
			return "", err
		}
		defer os.Remove(f.Name())
		if _, err := f.WriteString(text); err != nil {
			f.Close()
			return "", err
		}
		f.Close()
		editor := os.Getenv("VISUAL")
		if editor == "" {
			editor = os.Getenv("EDITOR")
		}
		if editor == "" {
			editor = "vi"
		}
		c := exec.CommandContext(ctx, "sh", "-c", editor+` "$1"`, "editor", f.Name())
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			return "", fmt.Errorf("%s: %w", editor, err)
		}
		b, err := os.ReadFile(f.Name())
		return string(b), err
	}
	// fetchReport runs keelbox reports --show N --json on the box.
	fetchReport func(ctx context.Context, n string) (reports.Report, error)
)

// sendReport is keelbox reports BOX --send N, on the laptop. Nothing leaves
// until the owner has read the redacted text and said yes; then the browser
// opens GitHub's new-issue form, filled in, and the owner submits it there.
func sendReport(ctx context.Context, cfg *config.Config, n string, p *prompt.Prompter, stdout, stderr io.Writer) int {
	if !p.Interactive() {
		failf(stderr, "reports --send needs a terminal: you read the text and answer before anything opens")
		return 2
	}
	r, err := fetchReport(ctx, n)
	if err != nil {
		fail(stderr, err)
		return 1
	}
	title, body := reports.Issue(r)
	text := title + "\n\n" + body
	var keep []string
	if names := reports.Projects(text); len(names) > 0 {
		fmt.Fprintf(stdout, "The report names projects on the box: %s. Unless you keep them, keelbox replaces them, and the paths under them.\n", strings.Join(names, ", "))
		if ok, err := p.Confirm("Keep these project names in the issue?", false); err != nil {
			return promptExit(err, stderr)
		} else if ok {
			keep = names
		}
	}
	text, redacted := reports.Redact(text, cfg.Box, keep)
	for {
		fmt.Fprintf(stdout, "\nThe agent wrote this report; its text is not checked. Read all of it.\n%s\n%s\n%s\n", strings.Repeat("─", 60), plain.Text(text), strings.Repeat("─", 60))
		if len(redacted) == 0 {
			fmt.Fprintln(stdout, "Redacted: nothing matched (tokens, keys, email addresses, tailnet names and addresses, public IPs, the box name, home and project paths).")
		} else {
			var parts []string
			for _, r := range redacted {
				parts = append(parts, fmt.Sprintf("%d %s", r.Count, r.Kind))
			}
			fmt.Fprintf(stdout, "Redacted: %s.\n", strings.Join(parts, ", "))
		}
		edit, err := p.Confirm("Edit it first?", false)
		if err != nil {
			return promptExit(err, stderr)
		}
		if !edit {
			break
		}
		edited, err := editText(ctx, text)
		if err != nil {
			failf(stderr, "editor: %v", err)
			return 1
		}
		// The owner's edit is checked again, for anything pasted in.
		text, redacted = reports.Redact(edited, cfg.Box, keep)
	}
	title, body, _ = strings.Cut(strings.TrimSpace(text), "\n")
	title, body = strings.TrimSpace(title), strings.TrimSpace(body)
	if r.Area == "security" {
		fmt.Fprintf(stdout, "\nThis is a security report: it goes to keelbox's private advisory form, not a public issue. "+
			"Copy the text above into it.\n%s\n", advisoryURL)
		if ok, err := p.Confirm("Open the private advisory form in your browser?", false); err != nil || !ok {
			return promptExit(err, stderr)
		}
		openOrSay(advisoryURL, stdout)
		return 0
	}
	fmt.Fprintln(stdout, p.Style().Plain("Issues on github.com/keelbox/keelbox are public. The browser opens the form with this text; you submit it there.", 0))
	ok, err := p.Confirm("Open a GitHub issue with this text in your browser?", false)
	if err != nil || !ok {
		if err == nil {
			fmt.Fprintln(stdout, "Nothing sent.")
		}
		return promptExit(err, stderr)
	}
	u, cut := issueURL(title, body)
	if cut {
		fmt.Fprintln(stdout, "The text is longer than a link holds: the issue form gets the start; paste the rest from above.")
	}
	openOrSay(u, stdout)
	return 0
}

func promptExit(err error, stderr io.Writer) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, prompt.ErrInterrupted):
		return exitInterrupted
	}
	fail(stderr, err)
	return 1
}

func openOrSay(u string, stdout io.Writer) {
	if canOpenBrowser() && openBrowser(u) == nil {
		fmt.Fprintln(stdout, "Opened in your browser. Nothing is sent until you submit it there.")
		return
	}
	fmt.Fprintf(stdout, "Open this in a browser; nothing is sent until you submit it there:\n%s\n", u)
}

// issueURL is GitHub's new-issue form, prefilled; cut is true when the body
// was shortened to fit.
func issueURL(title, body string) (string, bool) {
	build := func(b string) string {
		return issuesURL + "?" + url.Values{"title": {title}, "body": {b}}.Encode()
	}
	u := build(body)
	if len(u) <= issueURLMax {
		return u, false
	}
	const note = "\n\n_(cut: the rest is in keelbox reports)_"
	lo, hi := 0, len(body)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if len(build(body[:mid]+note)) <= issueURLMax {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	b := []byte(body[:lo])
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return build(string(b) + note), true
}
