package config

import (
	"strings"
	"testing"
)

// agentYAML is a minimal config with lines under users.agent.
func agentYAML(lines ...string) string {
	y := minimal("")
	if len(lines) == 0 {
		return y
	}
	y += "  agent:\n"
	for _, l := range lines {
		y += "    " + l + "\n"
	}
	return y
}

func TestAgentPermissions(t *testing.T) {
	for name, tc := range map[string]struct {
		lines    []string
		want     string
		sudo     bool
		installs bool
	}{
		"no key is auto":            {nil, PermissionsAuto, false, true},
		"manual":                    {[]string{"permissions: manual"}, PermissionsManual, false, false},
		"auto":                      {[]string{"permissions: auto"}, PermissionsAuto, false, true},
		"full":                      {[]string{"permissions: full"}, PermissionsFull, true, true},
		"sudo true is full":         {[]string{"sudo: true"}, PermissionsFull, true, true},
		"sudo false is the default": {[]string{"sudo: false"}, PermissionsAuto, false, true},
		"sudo true with full":       {[]string{"sudo: true", "permissions: full"}, PermissionsFull, true, true},
		"sudo false with manual":    {[]string{"sudo: false", "permissions: manual"}, PermissionsManual, false, false},
		"sudo false with auto":      {[]string{"sudo: false", "permissions: auto"}, PermissionsAuto, false, true},
	} {
		c, err := Parse([]byte(agentYAML(tc.lines...)), "t.yaml")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got := c.AgentPermissions(); got != tc.want {
			t.Errorf("%s: permissions %q, want %q", name, got, tc.want)
		}
		if c.AgentSudo() != tc.sudo {
			t.Errorf("%s: AgentSudo %v, want %v", name, c.AgentSudo(), tc.sudo)
		}
		if c.AgentInstalls() != tc.installs {
			t.Errorf("%s: AgentInstalls %v, want %v", name, c.AgentInstalls(), tc.installs)
		}
	}
}

func TestAgentPermissionsErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		lines []string
		want  []string
	}{
		"unknown value":         {[]string{"permissions: bypass"}, []string{`users.agent.permissions: "bypass" is not one of manual, auto, full`}},
		"sudo true with auto":   {[]string{"sudo: true", "permissions: auto"}, []string{"users.agent.sudo: true and users.agent.permissions: auto disagree", "remove users.agent.sudo, or set users.agent.permissions: full"}},
		"sudo true with manual": {[]string{"sudo: true", "permissions: manual"}, []string{"users.agent.permissions: manual disagree", "remove users.agent.sudo"}},
		"sudo false with full":  {[]string{"sudo: false", "permissions: full"}, []string{"users.agent.sudo: false and users.agent.permissions: full disagree", "remove users.agent.sudo to keep full, or set users.agent.permissions: auto"}},
	} {
		ps := problems(t, agentYAML(tc.lines...))
		if len(ps) != 1 {
			t.Errorf("%s: problems %q, want one", name, ps)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(ps[0], w) {
				t.Errorf("%s: %q does not say %q", name, ps[0], w)
			}
		}
	}
}

// The permissions key is written only when it is not the default, so a
// new box's config stays readable by a keelbox that does not know the key.
func TestStarterPermissions(t *testing.T) {
	for mode, want := range map[string]string{
		"":                "    # permissions: auto\n",
		PermissionsAuto:   "    # permissions: auto\n",
		PermissionsManual: "    permissions: manual\n",
		PermissionsFull:   "    permissions: full\n",
	} {
		s := StarterFor("acme", ProviderLocal)
		s.Permissions = mode
		out, err := s.Render()
		if err != nil {
			t.Fatalf("%q: %v", mode, err)
		}
		if !strings.Contains(out, want) {
			t.Errorf("%q: the config has no line %q", mode, want)
		}
		if strings.Contains(out, "sudo: false") {
			t.Errorf("%q: the config still writes users.agent.sudo", mode)
		}
		c, err := Parse([]byte(out), "acme.yaml")
		if err != nil {
			t.Fatal(err)
		}
		wantMode := mode
		if wantMode == "" {
			wantMode = PermissionsAuto
		}
		if c.AgentPermissions() != wantMode {
			t.Errorf("%q: parsed as %q", mode, c.AgentPermissions())
		}
	}
	s := StarterFor("acme", ProviderLocal)
	s.Permissions = "bypass"
	if _, err := s.Render(); err == nil {
		t.Error("a starter with an unknown permissions value rendered")
	}
}
