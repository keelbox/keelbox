package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
)

// skillRoot is a folder the agent CLIs read a user's skills from, relative
// to the home folder. A skill is a folder in one with a SKILL.md.
type skillRoot struct{ rel, tool string }

var skillRoots = []skillRoot{
	{".claude/skills", "Claude Code"},
	{".agents/skills", "Codex"},
	{".codex/skills", "Codex"},
}

// skillHome is the home folder skills are read from; tests replace it.
var skillHome = os.UserHomeDir

// maxSkillBytes bounds one skill's files: a skill is instructions and a few
// scripts, and a folder larger than this is more likely something else.
const maxSkillBytes = 20 << 20

// skillName is the shape of a skill folder keelbox copies: it goes into a
// remote shell command and a path, so it is held to plain characters.
var skillName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// localSkill is one skill on this computer, in each root it is in with the
// same files.
type localSkill struct {
	name, desc string
	roots      []string // skillRoot.rel
	dir        string   // the folder read, symlinks resolved
	files      []skillFile
	left       []string // files left out, and why
	scripts    bool     // has files the agent might run
	sum        string   // what the box's copy is compared with (skillSum)
}

type skillFile struct {
	rel  string // slash-separated, under the skill's folder
	mode fs.FileMode
	data []byte
}

// tools names the CLIs that read the skill.
func (s localSkill) tools() string {
	var out []string
	for _, r := range skillRoots {
		for _, have := range s.roots {
			if have == r.rel && !contains(out, r.tool) {
				out = append(out, r.tool)
			}
		}
	}
	return strings.Join(out, " and ")
}

// localSkills reads the skills under home. A skill in two roots with the
// same files (one often links to the other) is one skill.
func localSkills(home string) []localSkill {
	var out []localSkill
	for _, r := range skillRoots {
		entries, err := os.ReadDir(filepath.Join(home, r.rel))
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !skillName.MatchString(name) {
				continue // .system and the like: the CLI's own
			}
			s, ok := readSkill(filepath.Join(home, r.rel, name))
			if !ok {
				continue
			}
			s.roots = []string{r.rel}
			merged := false
			for i := range out {
				if out[i].name == name && out[i].sum == s.sum {
					out[i].roots = append(out[i].roots, r.rel)
					merged = true
				}
			}
			if !merged {
				out = append(out, s)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// readSkill reads a skill's folder: files only, no links inside it (one
// could point anywhere on this computer), and no file that looks like a
// secret or a repository's history.
func readSkill(dir string) (localSkill, bool) {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return localSkill{}, false
	}
	// SKILL.md itself must be a file: the walk below copies no links.
	if fi, err := os.Lstat(filepath.Join(real, "SKILL.md")); err != nil || !fi.Mode().IsRegular() {
		return localSkill{}, false
	}
	md, err := os.ReadFile(filepath.Join(real, "SKILL.md"))
	if err != nil {
		return localSkill{}, false
	}
	s := localSkill{name: filepath.Base(dir), dir: real, desc: skillDescription(md)}
	// Files are read through the folder itself, so a link put in place
	// during the walk cannot lead outside it.
	root, err := os.OpenRoot(real)
	if err != nil {
		return localSkill{}, false
	}
	defer func() { _ = root.Close() }()
	total := 0
	err = filepath.WalkDir(real, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(real, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		why := leaveOut(d)
		if why == "" && strings.ContainsAny(rel, "\n\r\\") {
			why = "a line break or backslash in its name"
		}
		if why != "" {
			s.left = append(s.left, rel+" ("+why+")")
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			s.left = append(s.left, rel+" (a link or special file)")
			return nil
		}
		total += int(info.Size())
		if total > maxSkillBytes {
			return errTooBig
		}
		data, err := root.ReadFile(rel)
		if err != nil {
			return err
		}
		mode := info.Mode().Perm() & 0o755
		s.files = append(s.files, skillFile{rel: rel, mode: mode, data: data})
		s.scripts = s.scripts || mode&0o111 != 0 || isScript(rel)
		return nil
	})
	if err != nil {
		return localSkill{}, false
	}
	s.sum = skillSum(s.files)
	return s, true
}

var errTooBig = errors.New("larger than a skill")

// leaveOut says why a file or folder is not copied, or "".
func leaveOut(d fs.DirEntry) string {
	n := strings.ToLower(d.Name())
	switch {
	case d.IsDir() && (n == ".git" || n == "node_modules" || n == "__pycache__" || n == ".venv"):
		return "not part of the skill"
	case d.IsDir():
		return ""
	case n == ".env" || strings.HasPrefix(n, ".env.") || n == ".netrc" || n == ".npmrc" || n == ".pypirc":
		return "may hold a secret"
	case strings.HasPrefix(n, "id_rsa") || strings.HasPrefix(n, "id_ed25519") || strings.HasPrefix(n, "id_ecdsa"):
		return "may hold a secret"
	}
	switch strings.TrimSuffix(n, path.Ext(n)) {
	case "credentials", "credential", "secrets", "secret", "token", "tokens", "auth", "client_secret", "service-account", "service_account":
		return "may hold a secret"
	}
	switch path.Ext(n) {
	case ".pem", ".key", ".p12", ".pfx", ".jks", ".keystore":
		return "may hold a secret"
	}
	return ""
}

func isScript(rel string) bool {
	if strings.HasPrefix(rel, "scripts/") {
		return true
	}
	switch path.Ext(rel) {
	case ".sh", ".bash", ".py", ".js", ".mjs", ".cjs", ".ts", ".rb", ".pl", ".ps1":
		return true
	}
	return false
}

// skillDescription is the description in SKILL.md's front matter, on one line.
func skillDescription(md []byte) string {
	text := strings.ReplaceAll(string(md), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return ""
	}
	front, _, ok := strings.Cut(text[4:], "\n---")
	if !ok {
		return ""
	}
	var m struct {
		Description string `yaml:"description"`
	}
	if yaml.Unmarshal([]byte(front), &m) != nil {
		return ""
	}
	return strings.Join(strings.Fields(m.Description), " ")
}

// skillSum is what the box computes over its copy with skillSumOnBox: the
// sha256 of sha256sum's lines for each file, sorted by path.
func skillSum(files []skillFile) string {
	lines := make([]string, len(files))
	for i, f := range files {
		h := sha256.Sum256(f.data)
		lines[i] = hex.EncodeToString(h[:]) + "  ./" + f.rel + "\n"
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i][66:] < lines[j][66:] })
	h := sha256.Sum256([]byte(strings.Join(lines, "")))
	return hex.EncodeToString(h[:])
}

// skillState is the box's copy of a skill, by exit code only: 0 the same
// files, 3 none, 4 different.
func skillState(root, name, sum string) string {
	return fmt.Sprintf(`cd "$HOME/%s/%s" 2>/dev/null || exit 3; `+
		`h=$(find . -type f -print | LC_ALL=C sort | xargs -r -d '\n' sha256sum | sha256sum); [ "${h%%%% *}" = %s ] || exit 4`, root, name, sum)
}

// skillCopy reads a skill's tar from its input into root, as the account
// that runs it, replacing what was there only once the new copy is whole.
func skillCopy(root, name string) string {
	return fmt.Sprintf(`set -e; d="$HOME/%s"; mkdir -p "$d"; t=$(mktemp -d "$d/.keelbox-XXXXXX"); trap 'rm -rf "$t"' EXIT; `+
		`tar -xzf - -C "$t" --no-same-owner --no-same-permissions; rm -rf "$d/%s"; mv "$t/%s" "$d/%s"`, root, name, name, name)
}

// skillTar is the skill's files under its name, as a gzipped tar.
func skillTar(s localSkill) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	dirs := map[string]bool{}
	for _, f := range s.files {
		// Each folder before its files, so tar makes it with its mode.
		for d := path.Dir(f.rel); d != "."; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	names := make([]string, 0, len(dirs))
	for d := range dirs {
		names = append(names, d)
	}
	sort.Strings(names)
	if err := tw.WriteHeader(&tar.Header{Name: s.name + "/", Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
		return nil, err
	}
	for _, d := range names {
		if err := tw.WriteHeader(&tar.Header{Name: s.name + "/" + d + "/", Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
			return nil, err
		}
	}
	for _, f := range s.files {
		if err := tw.WriteHeader(&tar.Header{Name: s.name + "/" + f.rel, Mode: int64(f.mode), Size: int64(len(f.data)), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// skillsWarning goes before the choice: what copying a skill gives the agent.
func skillsWarning(box string) string {
	return "The agent on " + box + " can read every file copied and send it anywhere it can reach, and can run a skill's scripts. Copy only skills you would hand it."
}

// cmdSkills is keelbox skills BOX: copy skills from this computer to the
// agent's account on the box, chosen from a list or named with --skill.
func cmdSkills(ctx context.Context, cfg *config.Config, o options, p *prompt.Prompter, stdout, stderr io.Writer) int {
	home, err := skillHome()
	if err != nil {
		fail(stderr, err)
		return 1
	}
	skills := localSkills(home)
	if len(skills) == 0 {
		fmt.Fprintf(stdout, "No skills on this computer: keelbox looks in ~/%s, ~/%s and ~/%s, for a folder with a SKILL.md.\n", skillRoots[0].rel, skillRoots[1].rel, skillRoots[2].rel)
		return 0
	}
	if o.list {
		var rows [][]string
		for _, s := range skills {
			scripts := ""
			if s.scripts {
				scripts = "yes"
			}
			rows = append(rows, []string{s.name, s.tools(), scripts, shorten(s.desc, 60)})
		}
		fmt.Fprint(stdout, o.style.Table([]string{"SKILL", "FOR", "SCRIPTS", "DESCRIPTION"}, rows))
		return 0
	}
	picked, code := pickSkills(cfg, o, p, skills, stdout, stderr)
	if code != 0 || len(picked) == 0 {
		return code
	}
	return copySkills(ctx, cfg, o, p, picked, stdout, stderr)
}

// pickSkills is the skills named by --skill, or chosen on the terminal.
func pickSkills(cfg *config.Config, o options, p *prompt.Prompter, skills []localSkill, stdout, stderr io.Writer) ([]localSkill, int) {
	if o.skill != "" {
		var out []localSkill
		for _, n := range strings.Split(o.skill, ",") {
			n = strings.TrimSpace(n)
			found := false
			for _, s := range skills {
				if s.name == n { // each, where Claude Code's and Codex's differ
					out, found = append(out, s), true
				}
			}
			if !found {
				failf(stderr, "no skill named %s on this computer; list them: keelbox skills %s --list", n, cfg.Box)
				return nil, 2
			}
		}
		st := p.Style()
		sayAsIs(stdout, st, st.Bang(), skillsWarning(cfg.Box))
		return out, 0
	}
	if !p.Interactive() {
		failf(stderr, "skills asks which to copy and needs a terminal; or name them: keelbox skills %s --skill %s", cfg.Box, skills[0].name)
		return nil, 2
	}
	st := p.Style()
	fmt.Fprintln(stdout)
	sayAsIs(stdout, st, st.Bang(), skillsWarning(cfg.Box))
	choices := make([]prompt.Choice, len(skills))
	for i, s := range skills {
		detail := s.tools()
		if s.scripts {
			detail += "; has scripts"
		}
		if s.desc != "" {
			detail += ". " + shorten(s.desc, 70)
		}
		label := s.name
		for j, o := range skills {
			if j != i && o.name == s.name {
				label += " (" + s.tools() + ")" // two of that name, with different files
			}
		}
		choices[i] = prompt.Choice{Label: label, Detail: detail}
	}
	idx, err := p.MultiChoose("Copy which skills to "+cfg.Box+"?", choices)
	switch {
	case errors.Is(err, prompt.ErrInterrupted):
		return nil, exitInterrupted
	case err != nil:
		fail(stderr, err)
		return nil, 1
	}
	out := make([]localSkill, len(idx))
	for i, j := range idx {
		out[i] = skills[j]
	}
	return out, 0
}

// copySkills puts each skill in the agent's home on the box, in the same
// folders it is in here, as the agent. A different skill of the same name
// there is replaced only after a yes, or with --overwrite.
func copySkills(ctx context.Context, cfg *config.Config, o options, p *prompt.Prompter, skills []localSkill, stdout, stderr io.Writer) int {
	sh, err := boxShell(ctx, cfg, o, cfg.Users.Agent.Name, stdout, stderr)
	if err != nil {
		fail(stderr, err)
		return 1
	}
	st := p.Style()
	copied, failed := 0, 0
	for _, s := range skills {
		for _, why := range s.left {
			say(stdout, st, st.Faint(st.Bullet()), s.name+": left out "+why)
		}
		data, err := skillTar(s)
		if err != nil {
			fail(stderr, err)
			return 1
		}
		for _, root := range s.roots {
			where := "~/" + root + "/" + s.name
			switch sh.exitCode(ctx, skillState(root, s.name, s.sum)) {
			case 0:
				say(stdout, st, st.Tick(), fmt.Sprintf("%s is already on %s, in %s", s.name, cfg.Box, where))
				continue
			case 3:
			case 4:
				replace := o.overwrite
				if !replace && p.Interactive() {
					replace, err = p.Confirm(fmt.Sprintf("%s has a different %s in %s. Replace it with this computer's?", cfg.Box, s.name, where), false)
					if errors.Is(err, prompt.ErrInterrupted) {
						return exitInterrupted
					}
				}
				if !replace {
					say(stdout, st, st.Faint(st.Bullet()), fmt.Sprintf("kept %s's own %s in %s; replace it, %s", cfg.Box, s.name, where,
						orOwner("without a terminal: keelbox skills "+cfg.Box+" --skill "+s.name+" --overwrite", "keelbox skills "+cfg.Box+" --skill "+s.name)))
					continue
				}
			default:
				failed++
				failf(stderr, "could not check %s on %s; check the box answers: keelbox doctor %s", where, cfg.Box, cfg.Box)
				continue
			}
			if err := sh.pipe(ctx, skillCopy(root, s.name), bytes.NewReader(data)); err != nil {
				failed++
				failf(stderr, "copying %s to %s: %v. Then: keelbox skills %s --skill %s", s.name, cfg.Box, err, cfg.Box, s.name)
				continue
			}
			copied++
			say(stdout, st, st.Tick(), fmt.Sprintf("copied %s to %s, in %s", s.name, cfg.Box, where))
		}
	}
	if copied > 0 {
		fmt.Fprintf(stdout, "Claude Code and Codex on %s read new skills when a session starts.\n", cfg.Box)
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// shorten cuts s to n characters at a word, with an ellipsis.
func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n-1])
	if i := strings.LastIndexByte(cut, ' '); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:") + "…"
}
