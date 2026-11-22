package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// install puts the skill and the rules in each CLI that is set up, keeps
// the user's own settings in their order, and remove takes back only what
// install added.
func TestAgentSkillInstallAndRemove(t *testing.T) {
	home := isolate(t)
	for _, d := range []string{".claude", ".codex"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	settings := filepath.Join(home, ".claude/settings.json")
	mine := `{"model": "opus", "permissions": {"allow": ["Bash(ls:*)"], "ask": ["Bash(rm:*)"]}, "env": {"A": "1"}}`
	if err := os.WriteFile(settings, []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := cmdAgentSkill("install", &out, &out); code != 0 {
		t.Fatal(out.String())
	}
	for _, p := range []string{".claude/skills/keelbox/SKILL.md", ".codex/skills/keelbox/SKILL.md", ".codex/rules/keelbox.rules"} {
		if b, err := os.ReadFile(filepath.Join(home, p)); err != nil || !bytes.Contains(b, []byte(managedMark)) {
			t.Errorf("%s: %v", p, err)
		}
	}
	b, _ := os.ReadFile(settings)
	s := string(b)
	if strings.Index(s, `"model"`) > strings.Index(s, `"permissions"`) || strings.Index(s, `"permissions"`) > strings.Index(s, `"env"`) {
		t.Errorf("order changed:\n%s", s)
	}
	var got struct {
		Permissions struct{ Allow, Ask, Deny []string }
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Permissions.Allow[0] != "Bash(ls:*)" || got.Permissions.Ask[0] != "Bash(rm:*)" || !contains(got.Permissions.Ask, "Bash(keelbox destroy:*)") ||
		!contains(got.Permissions.Deny, "Bash(keelbox approve:*)") {
		t.Errorf("%+v", got.Permissions)
	}
	// Twice is the same.
	cmdAgentSkill("install", &out, &out)
	if b2, _ := os.ReadFile(settings); !bytes.Equal(b, b2) {
		t.Errorf("second install changed it:\n%s", b2)
	}
	out.Reset()
	if code := cmdAgentSkill("remove", &out, &out); code != 0 {
		t.Fatal(out.String())
	}
	b, _ = os.ReadFile(settings)
	got.Permissions.Ask, got.Permissions.Deny = nil, nil
	_ = json.Unmarshal(b, &got)
	if len(got.Permissions.Ask) != 1 || len(got.Permissions.Deny) != 0 || !strings.Contains(string(b), `"model": "opus"`) || strings.Contains(string(b), `"deny"`) {
		t.Errorf("after remove:\n%s", b)
	}
	for _, p := range []string{".claude/skills/keelbox", ".codex/skills/keelbox", ".codex/rules/keelbox.rules"} {
		if _, err := os.Stat(filepath.Join(home, p)); err == nil {
			t.Errorf("%s left", p)
		}
	}
}

// A skill folder or settings keelbox did not write are left alone.
func TestAgentSkillLeavesTheUsersFiles(t *testing.T) {
	home := isolate(t)
	var out bytes.Buffer
	if code := cmdAgentSkill("install", &out, &out); code != 1 || !strings.Contains(out.String(), "neither Claude Code nor Codex") {
		t.Errorf("no CLIs: %d %s", code, out.String())
	}
	mine := filepath.Join(home, ".claude/skills/keelbox/SKILL.md")
	if err := os.MkdirAll(filepath.Dir(mine), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mine, []byte("my own"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude/settings.json"), []byte("{ // a comment\n}"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := cmdAgentSkill("install", &out, &out); code != 1 || !strings.Contains(out.String(), "is not keelbox's") {
		t.Errorf("%d %s", code, out.String())
	}
	if b, _ := os.ReadFile(mine); string(b) != "my own" {
		t.Error("replaced the user's skill")
	}
	if err := editClaudeSettings(filepath.Join(home, ".claude/settings.json"), func(h []string, _ string) []string { return h }); err == nil || !strings.Contains(err.Error(), "not plain JSON") {
		t.Errorf("JSON with a comment: %v", err)
	}
}

// Codex reads the rules as keelbox means them.
func TestCodexRulesParse(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("no codex here")
	}
	f := filepath.Join(t.TempDir(), "keelbox.rules")
	if err := os.WriteFile(f, []byte(codexRules()), 0o600); err != nil {
		t.Fatal(err)
	}
	for cmd, want := range map[string]string{"keelbox destroy acme": `"decision":"prompt"`, "keelbox approve acme": `"decision":"forbidden"`, "keelbox ls": `"matchedRules":[]`} {
		out, err := exec.Command("codex", append([]string{"execpolicy", "check", "--rules", f}, strings.Fields(cmd)...)...).Output()
		if err != nil || !strings.Contains(string(out), want) {
			t.Errorf("%s: %v %s", cmd, err, out)
		}
	}
}

// A settings file kept elsewhere through a link is changed there, and the
// link stays.
func TestAgentSkillFollowsALinkedSettingsFile(t *testing.T) {
	home := isolate(t)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(home, "dotfiles-settings.json")
	if err := os.WriteFile(real, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".claude/settings.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := cmdAgentSkill("install", &out, &out); code != 0 {
		t.Fatal(out.String())
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced")
	}
	if b, _ := os.ReadFile(real); !strings.Contains(string(b), "keelbox destroy") {
		t.Errorf("the linked file was not changed: %s", b)
	}
}

// A settings file without permissions gets none back from a remove.
func TestAgentSkillRemoveLeavesNoEmptyLists(t *testing.T) {
	home := isolate(t)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude/settings.json")
	if err := os.WriteFile(settings, []byte(`{"model": "opus"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := cmdAgentSkill("install", &out, &out); code != 0 {
		t.Fatal(out.String())
	}
	if code := cmdAgentSkill("remove", &out, &out); code != 0 {
		t.Fatal(out.String())
	}
	if b, _ := os.ReadFile(settings); strings.Contains(string(b), "permissions") || !strings.Contains(string(b), `"model": "opus"`) {
		t.Errorf("after remove:\n%s", b)
	}
}
