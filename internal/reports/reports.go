// Package reports is the box's inbox of keelbox reports: problems with
// keelbox that the agent wrote up, with the user's yes in the conversation,
// for the owner to review and send (ADR 0062). Nothing here sends anything:
// the inbox is root's, on the box, and a report leaves it only when the
// owner runs keelbox reports BOX --send N and submits the issue themselves.
package reports

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/keelbox/keelbox/internal/agentlimits"
	"github.com/keelbox/keelbox/internal/engine"
	"github.com/keelbox/keelbox/internal/plain"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/system"
	"github.com/keelbox/keelbox/internal/version"
)

// Dir is the inbox: root's, 0700, one file per report.
const Dir = "/var/lib/keelbox/reports"

// Limits. A report is a title and the error, not a log.
const (
	TitleMax  = 120
	DetailMax = 2000
	// InboxMax is the most drafts kept; past it new ones are dropped
	// until the owner deletes some.
	InboxMax = 30
	// DayMax is the most reports taken in a day: an agent made to file
	// report after report fills nothing.
	DayMax = 5
)

// Areas are the parts of keelbox a report can be about.
var Areas = []string{
	"apply", "doctor", "status", "expose", "previews", "sandbox", "codex", "claude-code",
	"remote-control", "sessions", "tailnet", "agents", "notifications", "backups", "docs", "security", "other",
}

// Facts are what the box adds to a report: a fixed list, nothing else.
// Never secrets, the environment, paths, or anything the agent CLIs keep.
type Facts struct {
	Keelbox string `json:"keelbox"`
	Release string `json:"release"`
	Ubuntu  string `json:"ubuntu"`
	Arch    string `json:"arch"`
	Size    string `json:"size,omitempty"`
	// DoctorFailing names the checks failing at the last doctor run, as
	// module/check.
	DoctorFailing []string `json:"doctor_failing,omitempty"`
}

// Report is one draft in the inbox. Title, Area and Detail are the agent's
// and untrusted; Facts and the times are root's.
type Report struct {
	V        int       `json:"v"`
	ID       string    `json:"id"`
	Filed    time.Time `json:"filed"`
	Received time.Time `json:"received"`
	Title    string    `json:"title"`
	Area     string    `json:"area"`
	Detail   string    `json:"detail,omitempty"`
	Facts    Facts     `json:"facts"`
}

var idRe = regexp.MustCompile(`^[0-9a-f]{8}$`)

// NewID is a random report ID: 8 hex digits.
func NewID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Check says what is wrong with a report's fields, as the agent wrote them.
func Check(id, title, area, detail string) error {
	switch {
	case !idRe.MatchString(id):
		return fmt.Errorf("id %q is not 8 hex digits", id)
	case strings.TrimSpace(title) == "":
		return errors.New("the title is empty: say what went wrong in a line")
	case utf8.RuneCountInString(title) > TitleMax:
		return fmt.Errorf("the title is longer than %d characters", TitleMax)
	case strings.ContainsAny(title, "\r\n"):
		return errors.New("the title is more than one line")
	case !slices.Contains(Areas, area):
		return fmt.Errorf("area %q is not one of %s", area, strings.Join(Areas, ", "))
	case utf8.RuneCountInString(detail) > DetailMax:
		return fmt.Errorf("the detail is longer than %d characters: keep the exact error and what you ran, not whole logs", DetailMax)
	}
	return nil
}

// FactsOf reads the box's facts for a report.
func FactsOf(ctx context.Context, e *engine.Env) Facts {
	f := Facts{Keelbox: version.Version, Release: e.M.Release, Ubuntu: e.Facts.OSVersion, Arch: e.Facts.Arch}
	if f.Arch == "" {
		f.Arch = runtime.GOARCH
	}
	if h := snapshot.ReadHost(ctx, e.X); h != nil && h.CPUs > 0 {
		f.Size = fmt.Sprintf("%d CPU, %s memory", h.CPUs, agentlimits.Size(h.Memory))
	}
	if s, ok, _ := snapshot.Read(e.X); ok && s.Doctor != nil {
		f.DoctorFailing = s.Doctor.Failing
	}
	return f
}

// Save puts a report in the inbox, unless the inbox is full or a day's
// reports are in. The agent's fields are cleaned of characters that are not
// printable, so a terminal shows them as text.
func Save(x system.Executor, r Report) error {
	if err := Check(r.ID, r.Title, r.Area, r.Detail); err != nil {
		return err
	}
	r.V, r.Title, r.Detail = 1, plain.Line(r.Title), plain.Text(r.Detail)
	have, err := List(x)
	if err != nil {
		return err
	}
	day := 0
	for _, h := range have {
		if h.ID == r.ID {
			return nil // read again after a replaced file: already in
		}
		if r.Received.Sub(h.Received) < 24*time.Hour {
			day++
		}
	}
	switch {
	case len(have) >= InboxMax:
		return fmt.Errorf("the inbox holds %d reports already; the owner deletes them with keelbox reports BOX --delete N", InboxMax)
	case day >= DayMax:
		return fmt.Errorf("%d reports in the last day already", DayMax)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := x.MkdirAll(Dir, 0o700, "root", "root"); err != nil {
		return err
	}
	return x.WriteFile(Dir+"/"+r.ID+".json", append(b, '\n'), 0o600, "root", "root")
}

// List reads the inbox, oldest first.
func List(x system.Executor) ([]Report, error) {
	names, err := x.ReadDir(Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Report
	for _, n := range names {
		id, ok := strings.CutSuffix(n, ".json")
		if !ok || !idRe.MatchString(id) {
			continue
		}
		data, err := x.ReadFile(Dir + "/" + n)
		if err != nil {
			return nil, err
		}
		var r Report
		if json.Unmarshal(data, &r) != nil || r.ID != id {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Received.Before(out[j].Received) })
	return out, nil
}

// Find is the report numbered n (from 1, as List orders them) or with ID n.
func Find(list []Report, n string) (int, Report, bool) {
	for i, r := range list {
		if n == fmt.Sprint(i+1) || n == r.ID {
			return i + 1, r, true
		}
	}
	return 0, Report{}, false
}

// Delete removes a report from the inbox.
func Delete(x system.Executor, id string) error {
	if !idRe.MatchString(id) {
		return fmt.Errorf("id %q is not 8 hex digits", id)
	}
	return x.Remove(Dir + "/" + id + ".json")
}

// FactLines are the box facts as the issue shows them.
func FactLines(f Facts) []string {
	out := []string{
		"keelbox: " + f.Keelbox + " (release " + f.Release + ")",
		"Ubuntu: " + f.Ubuntu + ", " + f.Arch,
	}
	if f.Size != "" {
		out = append(out, "size: "+f.Size)
	}
	failing := "none"
	if len(f.DoctorFailing) > 0 {
		failing = strings.Join(f.DoctorFailing, ", ")
	}
	return append(out, "doctor failing: "+failing)
}

// Issue is the report as a GitHub issue: its title and Markdown body.
func Issue(r Report) (title, body string) {
	var b strings.Builder
	fmt.Fprintf(&b, "**Area:** %s\n\n", r.Area)
	if d := strings.TrimSpace(r.Detail); d != "" {
		fmt.Fprintf(&b, "%s\n\n", d)
	}
	b.WriteString("**Box**\n\n")
	for _, l := range FactLines(r.Facts) {
		fmt.Fprintf(&b, "- %s\n", l)
	}
	b.WriteString("\n_Written by an AI coding agent on the box with keelbox report; reviewed and sent by the owner._\n")
	return r.Title, b.String()
}
