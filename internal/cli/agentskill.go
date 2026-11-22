package cli

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// keelbox agent-skill install puts keelbox's skill where Claude Code and
// Codex on this computer read skills, so an agent here knows how to use
// keelbox, and adds rules to each CLI that ask the user before a command
// that changes a box and refuse the ones only the owner may run.

//go:embed agentskill.md
var agentSkillText []byte

// managedMark is in every file keelbox writes for the skill; a file
// without it is the user's, and keelbox leaves it.
const managedMark = "Managed by keelbox"

// agentSkillAsk are the keelbox commands an agent here runs only after the
// user says yes; agentSkillDeny those it never runs.
var (
	agentSkillAsk = []string{"up", "create", "apply", "add", "update", "rollback", "restore", "resize", "park", "resume",
		"reboot", "destroy", "forget", "forget-token", "self-update", "skills", "ssh-config", "login", "agent-skill"}
	agentSkillDeny = []string{"approve", "deny"}
)

// agentCLI is an agent CLI on this computer that the skill is for.
type agentCLI struct {
	name  string
	home  string // ~/.claude, ~/.codex
	skill string // its skill folder for keelbox
	rules string // the file holding keelbox's rules
}

func agentCLIs(home string) []agentCLI {
	return []agentCLI{
		{"Claude Code", filepath.Join(home, ".claude"), filepath.Join(home, ".claude/skills/keelbox"), filepath.Join(home, ".claude/settings.json")},
		{"Codex", filepath.Join(home, ".codex"), filepath.Join(home, ".codex/skills/keelbox"), filepath.Join(home, ".codex/rules/keelbox.rules")},
	}
}

// codexRules is ~/.codex/rules/keelbox.rules, in Codex's rules language.
func codexRules() string {
	var b strings.Builder
	b.WriteString("# " + managedMark + ": keelbox agent-skill install writes it, keelbox agent-skill remove removes it.\n")
	for _, c := range agentSkillAsk {
		fmt.Fprintf(&b, "prefix_rule(pattern = [\"keelbox\", %q], decision = \"prompt\", justification = \"changes a keelbox box or this computer's keelbox setup\")\n", c)
	}
	for _, c := range agentSkillDeny {
		fmt.Fprintf(&b, "prefix_rule(pattern = [\"keelbox\", %q], decision = \"forbidden\", justification = \"only the owner decides on the box agent's requests\")\n", c)
	}
	return b.String()
}

// claudeRules are the permission rules for ~/.claude/settings.json.
func claudeRules() (ask, deny []string) {
	for _, c := range agentSkillAsk {
		ask = append(ask, "Bash(keelbox "+c+":*)")
	}
	for _, c := range agentSkillDeny {
		deny = append(deny, "Bash(keelbox "+c+":*)")
	}
	return ask, deny
}

// agentSkillMissing reports whether an agent CLI is set up under home
// without keelbox's skill.
func agentSkillMissing(home string) bool {
	for _, c := range agentCLIs(home) {
		if fi, err := os.Stat(c.home); err != nil || !fi.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(c.skill, "SKILL.md")); err != nil {
			return true
		}
	}
	return false
}

// linkTarget is the file a symbolic link at path points to, so that a file
// kept elsewhere (a dotfiles repository) is changed there and the link
// stays; path itself when it is no link or does not exist.
func linkTarget(path string) string {
	if t, err := filepath.EvalSymlinks(path); err == nil {
		return t
	}
	return path
}

// isManaged reports whether path is absent or keelbox's own.
func isManaged(path string) bool {
	b, err := os.ReadFile(path)
	return errors.Is(err, os.ErrNotExist) || err == nil && bytes.Contains(b, []byte(managedMark))
}

// writeManaged writes a file keelbox owns, refusing one that is the user's.
func writeManaged(path string, data []byte) error {
	if !isManaged(path) {
		return fmt.Errorf("%s is not keelbox's, so keelbox leaves it; move it, then run keelbox agent-skill install again", tildePath(path))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".keelbox-tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// cmdAgentSkill is keelbox agent-skill install|remove.
func cmdAgentSkill(sub string, stdout, stderr io.Writer) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fail(stderr, err)
		return 1
	}
	var found []agentCLI
	for _, c := range agentCLIs(home) {
		if fi, err := os.Stat(c.home); err == nil && fi.IsDir() {
			found = append(found, c)
		}
	}
	switch sub {
	case "install":
		if len(found) == 0 {
			failf(stderr, "neither Claude Code nor Codex is set up on this computer (no ~/.claude or ~/.codex). Install one and run it once, then: keelbox agent-skill install")
			return 1
		}
		code := 0
		for _, c := range found {
			if err := installAgentSkill(c); err != nil {
				failf(stderr, "%s: %v", c.name, err)
				code = 1
				continue
			}
			fmt.Fprintf(stdout, "%s: the keelbox skill in %s, and rules in %s that ask before keelbox commands that change a box and refuse approve and deny.\n",
				c.name, tildePath(c.skill), tildePath(c.rules))
		}
		if code == 0 {
			fmt.Fprintln(stdout, "A session started from now on has it. Remove it: keelbox agent-skill remove")
		}
		return code
	case "remove":
		code := 0
		for _, c := range found {
			if err := removeAgentSkill(c); err != nil {
				failf(stderr, "%s: %v", c.name, err)
				code = 1
				continue
			}
			fmt.Fprintf(stdout, "%s: removed keelbox's skill and rules.\n", c.name)
		}
		return code
	}
	failf(stderr, "agent-skill takes install or remove: keelbox agent-skill install")
	return 2
}

func installAgentSkill(c agentCLI) error {
	if err := writeManaged(filepath.Join(c.skill, "SKILL.md"), agentSkillText); err != nil {
		return err
	}
	if strings.HasSuffix(c.rules, ".rules") {
		return writeManaged(c.rules, []byte(codexRules()))
	}
	ask, deny := claudeRules()
	return editClaudeSettings(c.rules, func(have []string, list string) []string {
		add := ask
		if list == "deny" {
			add = deny
		}
		for _, r := range add {
			if !contains(have, r) {
				have = append(have, r)
			}
		}
		return have
	})
}

func removeAgentSkill(c agentCLI) error {
	md := filepath.Join(c.skill, "SKILL.md")
	if !isManaged(md) {
		return fmt.Errorf("%s is not keelbox's, so keelbox leaves it", tildePath(md))
	}
	_ = os.Remove(md)
	_ = os.Remove(c.skill) // only when empty
	if strings.HasSuffix(c.rules, ".rules") {
		if !isManaged(c.rules) {
			return fmt.Errorf("%s is not keelbox's, so keelbox leaves it", tildePath(c.rules))
		}
		if err := os.Remove(c.rules); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if _, err := os.Stat(c.rules); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	ask, deny := claudeRules()
	ours := append(append([]string{}, ask...), deny...)
	return editClaudeSettings(c.rules, func(have []string, _ string) []string {
		var keep []string
		for _, r := range have {
			if !contains(ours, r) {
				keep = append(keep, r)
			}
		}
		return keep
	})
}

// jsonField is one member of a JSON object, in the order it was written.
type jsonField struct {
	key string
	val json.RawMessage
}

// readObject reads a JSON object's members in order, each value as written.
func readObject(data []byte) ([]jsonField, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	var out []jsonField
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := t.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, jsonField{key, v})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("more after the object")
	}
	return out, nil
}

func writeObject(fields []jsonField) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(f.key)
		b.Write(k)
		b.WriteByte(':')
		b.Write(f.val)
	}
	b.WriteByte('}')
	var out bytes.Buffer
	if err := json.Indent(&out, b.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// setField replaces a member's value, or adds it at the end.
func setField(fields []jsonField, key string, val json.RawMessage) []jsonField {
	for i := range fields {
		if fields[i].key == key {
			fields[i].val = val
			return fields
		}
	}
	return append(fields, jsonField{key, val})
}

func dropField(fields []jsonField, key string) []jsonField {
	out := fields[:0:0]
	for _, f := range fields {
		if f.key != key {
			out = append(out, f)
		}
	}
	return out
}

func getField(fields []jsonField, key string) json.RawMessage {
	for _, f := range fields {
		if f.key == key {
			return f.val
		}
	}
	return nil
}

// editClaudeSettings changes permissions.ask and permissions.deny in
// Claude Code's settings and keeps everything else, in its order. A file
// that is not plain JSON is left as it is, with the rules to add by hand.
func editClaudeSettings(path string, edit func(have []string, list string) []string) error {
	path = linkTarget(path)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data, err = []byte("{}"), nil
	}
	if err != nil {
		return err
	}
	top, err := readObject(data)
	if err != nil {
		return fmt.Errorf("%s is not plain JSON (%v), so keelbox leaves it; add the rules under permissions by hand: %s/guides/agents-here#teach-them-keelbox", tildePath(path), err, docsURL)
	}
	perms := []jsonField{}
	if getField(top, "permissions") == nil && len(edit(nil, "ask"))+len(edit(nil, "deny")) == 0 {
		return nil // nothing to add, and no permissions to take from
	}
	if raw := getField(top, "permissions"); raw != nil {
		if perms, err = readObject(raw); err != nil {
			return fmt.Errorf("%s: permissions is not an object, so keelbox leaves the file", tildePath(path))
		}
	}
	for _, list := range []string{"ask", "deny"} {
		var have []string
		if raw := getField(perms, list); raw != nil {
			if err := json.Unmarshal(raw, &have); err != nil {
				return fmt.Errorf("%s: permissions.%s is not a list of rules, so keelbox leaves the file", tildePath(path), list)
			}
		}
		next := edit(have, list)
		if len(next) == 0 {
			// Emptied by a remove, or never there: no list, rather than [].
			perms = dropField(perms, list)
			continue
		}
		v, _ := json.Marshal(next)
		perms = setField(perms, list, v)
	}
	if len(perms) == 0 {
		top = dropField(top, "permissions")
	} else {
		pv, err := writeObject(perms)
		if err != nil {
			return err
		}
		top = setField(top, "permissions", bytes.TrimSpace(pv))
	}
	out, err := writeObject(top)
	if err != nil {
		return err
	}
	if bytes.Equal(out, data) {
		return nil
	}
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".keelbox-tmp"
	if err := os.WriteFile(tmp, out, mode); err != nil { //nolint:gosec // G703: ~/.claude/settings.json, a fixed path
		return err
	}
	return os.Rename(tmp, path)
}
