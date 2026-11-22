package modules

import (
	"context"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/engine"
)

func sandboxFindings(t *testing.T, e *engine.Env) map[string]engine.Finding {
	t.Helper()
	got := map[string]engine.Finding{}
	for _, f := range (Agents{}).Verify(context.Background(), e) {
		if f.Check == "sandbox" || f.Check == "codex-sandbox" {
			got[f.Check] = f
		}
	}
	if len(got) != 2 {
		t.Fatalf("doctor has %d sandbox findings, want 2: %v", len(got), got)
	}
	return got
}

// On 24.04 the user namespace restriction is on and Ubuntu loads no profile
// for bwrap: apply takes Ubuntu's own from its apparmor-profiles package,
// which it downloads and does not install, loads it, and doctor's sandbox
// checks, failing before, pass.
func TestBwrapProfileOnUbuntu2404(t *testing.T) {
	s := newSim(t)
	s.AddFile(userNSRestriction, "1\n", 0o644)
	e := simEnv(t, s, testConfig(t, "agents: [claude-code, codex]\n"))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	got := s.content(bwrapProfilePath)
	if !strings.HasPrefix(got, "# Managed by keelbox.") || !strings.HasSuffix(got, simUbuntuBwrapProfile) || !strings.Contains(got, "apparmor-profiles package ("+simAppArmorProfiles+")") {
		t.Errorf("profile is not Ubuntu's under keelbox's header:\n%s", got)
	}
	if _, ok := s.pkgs[bwrapProfilePkg]; ok || s.Ran("apt-get install "+bwrapProfilePkg) {
		t.Error("the apparmor-profiles package was installed, with its other profiles")
	}
	for p := range s.Files {
		if strings.HasPrefix(p, "/tmp/keelbox.") {
			t.Errorf("left behind: %s", p)
		}
	}
	if !s.Ran("apparmor_parser --replace --write-cache " + bwrapProfilePath) {
		t.Error("profile not loaded")
	}
	for _, f := range sandboxFindings(t, e) {
		if f.Level != engine.LevelOK {
			t.Errorf("%s: %s %s", f.Check, f.Level, f.Detail)
		}
	}
	if r, _ := engine.Apply(ctx, e, All()); len(r.Changes) != 0 {
		t.Errorf("second apply changed %v", r.Changes)
	}

	// Without the profile, both checks fail with bwrap's own reason and
	// the fix.
	delete(s.Files, bwrapProfilePath)
	for _, f := range sandboxFindings(t, e) {
		if f.Level != engine.LevelFail || !strings.Contains(f.Detail, "keelbox apply testbox") {
			t.Errorf("%s without the profile: %s %s", f.Check, f.Level, f.Detail)
		}
	}
	if f := sandboxFindings(t, e)["sandbox"]; !strings.Contains(f.Detail, "RTM_NEWADDR") {
		t.Errorf("sandbox finding lacks bwrap's reason: %s", f.Detail)
	}
}

// Where Ubuntu ships its own profile (25.04 and later), or the restriction
// is off, keelbox adds none.
func TestBwrapProfileNotNeeded(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"ubuntu profile":  {userNSRestriction: "1\n", ubuntuBwrapPath: "profile bwrap /usr/bin/bwrap {}\n"},
		"restriction off": {userNSRestriction: "0\n"},
		"no such sysctl":  {},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSim(t)
			for p, c := range files {
				s.AddFile(p, c, 0o644)
			}
			e := simEnv(t, s, testConfig(t, "agents: [codex]\n"))
			if _, err := engine.Apply(context.Background(), e, All()); err != nil {
				t.Fatal(err)
			}
			if s.has(bwrapProfilePath) || s.Ran("apparmor_parser") || s.Ran("apt-get -o DPkg::Lock::Timeout=600 download") {
				t.Error("keelbox loaded a bwrap profile it does not need")
			}
		})
	}
}

// After a release upgrade brings Ubuntu's profile, apply removes keelbox's
// copy and reloads Ubuntu's (same profile names), never unloading bwrap's.
func TestBwrapProfileYieldsToUbuntus(t *testing.T) {
	s := newSim(t)
	s.AddFile(userNSRestriction, "1\n", 0o644)
	e := simEnv(t, s, testConfig(t, "agents: [codex]\n"))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	s.AddFile(ubuntuBwrapPath, "profile bwrap /usr/bin/bwrap {}\n", 0o644)
	r, err := engine.Apply(ctx, e, All())
	if err != nil {
		t.Fatal(err)
	}
	if s.has(bwrapProfilePath) {
		t.Error("keelbox's profile kept beside Ubuntu's")
	}
	if !s.Ran("apparmor_parser --replace --write-cache "+ubuntuBwrapPath) || s.Ran("apparmor_parser --remove") {
		t.Errorf("Ubuntu's profile not reloaded, or bwrap's unloaded: %v", r.Changes)
	}
	if r, _ := engine.Apply(ctx, e, All()); len(r.Changes) != 0 {
		t.Errorf("third apply changed %v", r.Changes)
	}
}

// bwrapFailure is the bwrap step's failure in a run, "" when it passed.
func bwrapFailure(r engine.Applied) string {
	for _, f := range r.Failed {
		if f.Step == "bwrap-userns" {
			return f.Detail
		}
	}
	return ""
}

// A profile the parser refuses is rolled back and apply says so. The step
// is soft: the rest of the box, sshd and the firewall included, applies.
func TestBwrapProfileRefusedIsRolledBack(t *testing.T) {
	s := newSim(t)
	s.AddFile(userNSRestriction, "1\n", 0o644)
	s.Fail("apparmor_parser")
	e := simEnv(t, s, testConfig(t, ""))
	r, err := engine.Apply(context.Background(), e, All())
	if err != nil {
		t.Fatalf("a refused bwrap profile stopped the run: %v", err)
	}
	if got := bwrapFailure(r); !strings.Contains(got, "rolled back") {
		t.Errorf("failure: %q", got)
	}
	if s.has(bwrapProfilePath) {
		t.Error("refused profile left in /etc/apparmor.d")
	}
	if !s.has(sshdDropIn) {
		t.Error("sshd was not configured after the bwrap step failed")
	}
}

// Where the apparmor-profiles package is installed, its file is read in
// place and nothing is downloaded.
func TestBwrapProfileFromInstalledPackage(t *testing.T) {
	s := newSim(t)
	s.AddFile(userNSRestriction, "1\n", 0o644)
	s.AddFile(ubuntuBwrapExtra, simUbuntuBwrapProfile, 0o644)
	s.pkgs[bwrapProfilePkg] = "4.0.1-installed"
	e := simEnv(t, s, testConfig(t, ""))
	if _, err := engine.Apply(context.Background(), e, All()); err != nil {
		t.Fatal(err)
	}
	got := s.content(bwrapProfilePath)
	if !strings.HasSuffix(got, simUbuntuBwrapProfile) || !strings.Contains(got, "(4.0.1-installed)") {
		t.Errorf("profile:\n%s", got)
	}
	if s.Ran("apt-get -o DPkg::Lock::Timeout=600 download") {
		t.Error("downloaded a package that is installed")
	}
}

// A package from before Ubuntu added the profile: the step fails with what
// to do, leaves no file, and the run carries on.
func TestBwrapProfileMissingFromPackage(t *testing.T) {
	s := newSim(t)
	s.AddFile(userNSRestriction, "1\n", 0o644)
	s.noBwrapInPackage = true
	e := simEnv(t, s, testConfig(t, "agents: [codex]\n"))
	r, err := engine.Apply(context.Background(), e, All())
	if err != nil {
		t.Fatal(err)
	}
	if got := bwrapFailure(r); !strings.Contains(got, "noble-updates") || !strings.HasSuffix(got, "keelbox apply testbox") {
		t.Errorf("failure: %q", got)
	}
	if s.has(bwrapProfilePath) {
		t.Error("a profile file was written without Ubuntu's profile")
	}
	if f := sandboxFindings(t, e)["sandbox"]; f.Level != engine.LevelFail {
		t.Errorf("doctor's sandbox check: %s %s", f.Level, f.Detail)
	}
}

// An older copy of the profile has no mark of where it came from: apply
// replaces it with Ubuntu's own.
func TestBwrapProfileReplacesAnOlderCopy(t *testing.T) {
	s := newSim(t)
	s.AddFile(userNSRestriction, "1\n", 0o644)
	s.AddFile(bwrapProfilePath, "# Managed by keelbox. Ubuntu's bwrap-userns-restrict profile, for releases\nprofile bwrap /usr/bin/bwrap {}\n", 0o644)
	e := simEnv(t, s, testConfig(t, ""))
	ctx := context.Background()
	if _, err := engine.Apply(ctx, e, All()); err != nil {
		t.Fatal(err)
	}
	if got := s.content(bwrapProfilePath); !strings.HasPrefix(got, bwrapProfileMark) || !strings.HasSuffix(got, simUbuntuBwrapProfile) {
		t.Errorf("profile:\n%s", got)
	}
	if r, _ := engine.Apply(ctx, e, All()); len(r.Changes) != 0 {
		t.Errorf("second apply changed %v", r.Changes)
	}
}

// The fix a failing sandbox check prints names the published
// troubleshooting page and its section, not a path in the repository.
func TestSandboxFixLinksTheDocs(t *testing.T) {
	e := simEnv(t, newSim(t), testConfig(t, "agents: [codex]\n"))
	fix := sandboxFix(e)
	if !strings.Contains(fix, "https://keelbox.dev/docs/guides/troubleshooting#agent-sandboxes") || strings.Contains(fix, ".md") {
		t.Errorf("the fix does not link the published page: %s", fix)
	}
}
