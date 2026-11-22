package docscheck

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"
)

const (
	root    = "../.."
	docsDir = root + "/docs"
)

// page is one user page: its heading, and how many words of prose it may
// carry. Words are counted outside code blocks and HTML comments.
type page struct {
	path  string // under docs/
	title string // the page's first heading
	words int    // the budget
	// until names a second-level heading: the budget covers the page up
	// to it, and what follows is detail, held to the style rules only.
	until string
	// entry caps each third-level section after until (troubleshooting's
	// problems); 0 leaves them uncapped.
	entry int
}

// userPages is every user page, in the order docs/README.md lists them,
// with its budget. A page not in this table fails the test: add it here,
// with a budget, or put the text on a page that is.
var userPages = []page{
	// Start
	{path: "getting-started.md", title: "Your first box", words: 900},
	{path: "guides/where-to-run.md", title: "Where a box can run", words: 750},
	{path: "guides/installing.md", title: "Install keelbox", words: 400},
	// Other ways to set up
	{path: "guides/adopting-a-box.md", title: "A server you already have", words: 900},
	{path: "guides/machine-in-use.md", title: "A machine already in use, at home or this one", words: 900},
	{path: "guides/credentials.md", title: "Cloud sign-ins and permissions", words: 800},
	{path: "guides/automation.md", title: "Without the wizard: flags, CI, config by hand", words: 600},
	// Use it
	{path: "guides/signing-in.md", title: "Sign in", words: 530},
	{path: "guides/sessions.md", title: "Attach, leave and come back", words: 600},
	{path: "guides/remote-control.md", title: "The Claude app", words: 500},
	{path: "guides/work-on-the-box.md", title: "Work on the box: git, packages, root", words: 700},
	{path: "guides/previews.md", title: "Preview a dev server", words: 450},
	{path: "guides/agents-here.md", title: "Your computer's agents and your boxes", words: 450},
	// Look after it
	{path: "guides/troubleshooting.md", title: "Check the box and fix problems", words: 300, until: "Connecting", entry: 120},
	{path: "guides/upgrades.md", title: "Update keelbox and the agent CLIs", words: 600},
	{path: "guides/backups.md", title: "Back up and restore", words: 500},
	{path: "guides/notifications.md", title: "Notifications", words: 350},
	{path: "guides/reports.md", title: "Reports", words: 400},
	{path: "guides/resize-park-resume.md", title: "Change size or pause billing: resize, park and resume", words: 600},
	{path: "guides/public-ssh.md", title: "Closing public SSH, and getting back in when locked out", words: 500, until: "Getting back in"},
	{path: "guides/removing-a-box.md", title: "Remove a box", words: 750},
	// Understand
	{path: "concepts/how-it-works.md", title: "How keelbox works", words: 400},
	{path: "security-model.md", title: "Security model: what keelbox enforces and what it does not stop", words: 1000, until: "In detail"},
	// Reference: a ceiling, so that what the help text or the schema
	// already says is not written out again beside it.
	{path: "reference/cli.md", title: "Commands and flags", words: 4100},
	{path: "reference/config.md", title: "Config file", words: 4700},
	{path: "reference/services.md", title: "Known services", words: 900},
	{path: "reference/release.md", title: "Releases and the release manifest", words: 1400},
	{path: "reference/box-files.md", title: "Files keelbox manages on a box", words: 4400},
	{path: "reference/clouds.md", title: "The four clouds", words: 1500},
}

// indexWords is the budget of docs/README.md, the index.
const indexWords = 450

// transcriptLines caps a ```text block: a screen of output, not a session.
const transcriptLines = 12

// banned are phrases a user page does not use, with what to do instead.
var banned = []struct{ re, why string }{
	{`(?i)\bby itself\b`, "say who or what does it"},
	{`(?i)\bin our tests\b`, "test provenance belongs in docs/contributing/testing.md"},
	{`\bAs on\b`, "say the fact once, on the page that owns it"},
	{`\bDesign:`, "user pages do not link decision records"},
	{`\(example\b`, "docs/README.md says once that output is an example"},
	{`\(why\)`, "user pages do not link decision records"},
	{"—", "no em dashes"},
}

// notUserDocs are the parts of docs/ this table does not govern.
func notUserDocs(rel string) bool {
	return strings.HasPrefix(rel, "adr/") || strings.HasPrefix(rel, "contributing/") || rel == "maintaining.md" || rel == "README.md"
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type block struct {
	lang  string
	lines int
	at    int // line number of the opening fence
	help  bool
}

var commentRe = regexp.MustCompile(`(?s)<!--.*?-->`)

// prose is a page's text outside fenced code blocks, line for line (a
// line inside a block is empty), and the blocks.
func prose(doc string) ([]string, []block) {
	var out []string
	var blocks []block
	in, help := false, false
	for i, l := range strings.Split(doc, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "```") && !in:
			in = true
			blocks = append(blocks, block{lang: strings.TrimPrefix(t, "```"), at: i + 1, help: help})
			out = append(out, "")
		case strings.HasPrefix(t, "```"):
			in = false
			out = append(out, "")
		case in:
			blocks[len(blocks)-1].lines++
			out = append(out, "")
		default:
			// A help block in reference/cli.md is generated, not a transcript.
			help = strings.HasPrefix(t, "<!-- keelbox ") && strings.HasSuffix(t, ": begin -->")
			out = append(out, commentRe.ReplaceAllString(l, ""))
		}
	}
	return out, blocks
}

// words counts the words in lines: runs of non-space with a letter or a
// digit in them, so a table's bars and rules do not count.
func words(lines []string) int {
	n := 0
	for _, l := range lines {
		for _, f := range strings.Fields(l) {
			if strings.IndexFunc(f, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0 {
				n++
			}
		}
	}
	return n
}

// TestUserPagesWithinBudget fails when a user page is missing, is not in
// the table, has another heading than the table's, or carries more prose
// than its budget. go test ./internal/docscheck -run Budget -v prints the
// counts.
func TestUserPagesWithinBudget(t *testing.T) {
	known := map[string]bool{}
	total := 0
	for _, p := range userPages {
		known[p.path] = true
		doc, err := os.ReadFile(filepath.Join(docsDir, p.path))
		if err != nil {
			t.Errorf("docs/%s: %v", p.path, err)
			continue
		}
		lines, _ := prose(string(doc))
		if len(lines) == 0 || lines[0] != "# "+p.title {
			t.Errorf("docs/%s: the first line is %q, want %q", p.path, lines[0], "# "+p.title)
		}
		total += words(lines)
		head, detail := lines, []string(nil)
		if p.until != "" {
			at := -1
			for i, l := range lines {
				if l == "## "+p.until {
					at = i
					break
				}
			}
			if at < 0 {
				t.Errorf("docs/%s: no heading %q; its budget counts the page up to it", p.path, "## "+p.until)
				continue
			}
			head, detail = lines[:at], lines[at:]
		}
		n := words(head)
		t.Logf("%-32s %5d of %5d  %s", p.path, n, p.words, p.title)
		if n > p.words {
			t.Errorf("docs/%s: %d words of prose, over its budget of %d. Cut, or move detail to a reference page", p.path, n, p.words)
		}
		if p.entry == 0 {
			continue
		}
		name, from := "", 0
		check := func(to int) {
			if name == "" {
				return
			}
			if n := words(detail[from:to]); n > p.entry {
				t.Errorf("docs/%s: %q has %d words, over the %d an entry may have", p.path, name, n, p.entry)
			}
		}
		for i, l := range detail {
			if strings.HasPrefix(l, "### ") || strings.HasPrefix(l, "## ") {
				check(i)
				name, from = "", i
				if strings.HasPrefix(l, "### ") {
					name = strings.TrimPrefix(l, "### ")
				}
			}
		}
		check(len(detail))
	}
	t.Logf("%-32s %5d", "all user pages, detail included", total)
	index, _ := prose(read(t, docsDir+"/README.md"))
	if n := words(index); n > indexWords {
		t.Errorf("docs/README.md: %d words, over its budget of %d", n, indexWords)
	}
	err := filepath.WalkDir(docsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		rel, _ := filepath.Rel(docsDir, path)
		if rel = filepath.ToSlash(rel); !notUserDocs(rel) && !known[rel] {
			t.Errorf("docs/%s is a user page without a budget: add it to userPages in internal/docscheck, or move its text to a page that is there", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

var adrLinkRe = regexp.MustCompile(`\]\((\.\./)*adr/`)

// TestUserPagesStyle is the lint: the phrases user pages do not use, no
// links to decision records, and transcripts no longer than a screen.
func TestUserPagesStyle(t *testing.T) {
	for _, p := range append([]page{{path: "README.md"}}, userPages...) {
		doc, err := os.ReadFile(filepath.Join(docsDir, p.path))
		if err != nil {
			continue // TestUserPagesWithinBudget reports a missing page
		}
		lines, blocks := prose(string(doc))
		for i, l := range lines {
			for _, b := range banned {
				if regexp.MustCompile(b.re).MatchString(l) {
					t.Errorf("docs/%s:%d: %q: %s", p.path, i+1, regexp.MustCompile(b.re).FindString(l), b.why)
				}
			}
			if p.path != "README.md" && adrLinkRe.MatchString(l) {
				t.Errorf("docs/%s:%d: links a decision record; a user page states the fact and a contributor page may link the record", p.path, i+1)
			}
		}
		for _, b := range blocks {
			if b.lang == "text" && !b.help && b.lines > transcriptLines {
				t.Errorf("docs/%s:%d: a transcript of %d lines; %d at most", p.path, b.at, b.lines, transcriptLines)
			}
		}
	}
}

var headingRe = regexp.MustCompile(`^#{1,6}\s+(.*)$`)

// slug is the anchor GitHub and the site give a heading: lower case,
// punctuation dropped, spaces as hyphens.
func slug(heading string) string {
	heading = regexp.MustCompile("`|\\*|\\[|\\]\\([^)]*\\)").ReplaceAllString(heading, "")
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(heading)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

var anchorCache = map[string]map[string]bool{}

// anchors are the headings of a markdown file, as anchors.
func anchors(path string) (map[string]bool, error) {
	if a, ok := anchorCache[path]; ok {
		return a, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	a := map[string]bool{}
	lines, _ := prose(string(data))
	for _, l := range lines {
		if m := headingRe.FindStringSubmatch(l); m != nil {
			s := slug(m[1])
			for i := 1; a[s]; i++ { // a repeated heading gets -1, -2
				s = fmt.Sprintf("%s-%d", slug(m[1]), i)
			}
			a[s] = true
		}
	}
	anchorCache[path] = a
	return a, nil
}

// resolve checks that a file exists and, for a markdown file, that it has
// the anchor. It returns what is wrong, or "".
func resolve(path, anchor string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "no such file"
	}
	if anchor == "" || info.IsDir() || !strings.HasSuffix(path, ".md") {
		return ""
	}
	a, err := anchors(path)
	if err != nil {
		return err.Error()
	}
	if !a[anchor] {
		return "no heading for #" + anchor
	}
	return ""
}

var mdLinkRe = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// TestDocsLinksResolve follows every relative link in the user and
// contributor docs and in the repository's top-level pages. The decision
// records are left out: they are not edited, and a few name pages as
// they were when the decision was taken.
func TestDocsLinksResolve(t *testing.T) {
	files := []string{root + "/README.md", root + "/SECURITY.md", root + "/CONTRIBUTING.md", root + "/CONTEXT.md", root + "/AGENTS.md", root + "/CHANGELOG.md"}
	err := filepath.WalkDir(docsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		if rel, _ := filepath.Rel(docsDir, path); !strings.HasPrefix(filepath.ToSlash(rel), "adr/") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		lines, _ := prose(read(t, f))
		name, _ := filepath.Rel(root, f)
		for i, l := range lines {
			for _, m := range mdLinkRe.FindAllStringSubmatch(l, -1) {
				target := m[1]
				if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
					continue
				}
				file, anchor, _ := strings.Cut(target, "#")
				path := f
				if file != "" {
					path = filepath.Join(filepath.Dir(f), file)
				}
				if why := resolve(path, anchor); why != "" {
					t.Errorf("%s:%d: link %s: %s", name, i+1, target, why)
				}
			}
		}
	}
}

var (
	// A docs URL written out: in a string, a golden file, the installer.
	urlRe = regexp.MustCompile(`https://keelbox\.dev/docs((?:/[A-Za-z0-9_-]+)*)(#[A-Za-z0-9_-]+)?`)
	// One built from the docsURL constant: docsURL+"/guides/x", or a
	// format string "%s/guides/x" that docsURL fills.
	builtRe = regexp.MustCompile(`(?:docsURL\s*\+\s*"|%s)((?:/(?:guides|reference|concepts|contributing|security-model|getting-started)[A-Za-z0-9_/-]*))(#[A-Za-z0-9_-]+)?`)
	// A path in the repository: docs/guides/x.md#anchor.
	repoRe = regexp.MustCompile(`\bdocs/((?:[A-Za-z0-9_-]+/)*[A-Za-z0-9_.-]+\.md)(#[A-Za-z0-9_-]+)?`)
)

// TestPrintedDocsLinksResolve reads every file under internal/ and get/,
// golden files and comments included, for docs URLs and docs paths, and
// checks that each leads to a page, and to a heading on it, that exists
// in docs/. A page that moves without the string that points at it fails
// here.
func TestPrintedDocsLinksResolve(t *testing.T) {
	type ref struct{ page, anchor string }
	seen := map[ref][]string{}
	for _, dir := range []string{root + "/internal", root + "/get"} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if strings.Contains(filepath.ToSlash(path), "internal/docscheck/") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(data)
			name, _ := filepath.Rel(root, path)
			add := func(page, anchor string) {
				r := ref{page, strings.TrimPrefix(anchor, "#")}
				seen[r] = append(seen[r], name)
			}
			for _, m := range urlRe.FindAllStringSubmatch(text, -1) {
				add(strings.TrimPrefix(m[1], "/"), m[2])
			}
			if strings.HasSuffix(path, ".go") {
				for _, m := range builtRe.FindAllStringSubmatch(text, -1) {
					add(strings.TrimPrefix(m[1], "/"), m[2])
				}
			}
			for _, m := range repoRe.FindAllStringSubmatch(text, -1) {
				add(strings.TrimSuffix(m[1], ".md"), m[2])
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) < 10 {
		t.Fatalf("found %d docs links under internal/ and get/; the patterns no longer match how they are written", len(seen))
	}
	var refs []ref
	for r := range seen {
		refs = append(refs, r)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].page+"#"+refs[i].anchor < refs[j].page+"#"+refs[j].anchor })
	for _, r := range refs {
		path := filepath.Join(docsDir, r.page+".md")
		switch {
		case r.page == "":
			path = docsDir + "/README.md"
		case strings.HasSuffix(r.page, "/"):
			path = filepath.Join(docsDir, r.page)
		}
		if why := resolve(path, r.anchor); why != "" {
			files := seen[r]
			sort.Strings(files)
			t.Errorf("docs/%s.md#%s (%s): %s", r.page, r.anchor, files[0], why)
		}
	}
}
