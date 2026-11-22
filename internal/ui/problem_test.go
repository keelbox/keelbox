package ui

import (
	"strings"
	"testing"
)

func live(width int) Style { return Style{Live: true, UTF8: true, Width: width} }

// The block leaves a box name, a program, a path or an address as it is
// written, and starts any other sentence with a capital.
func TestTroubleCapitalisesOnlyOrdinaryWords(t *testing.T) {
	st := live(100)
	for text, want := range map[string]string{
		"delta is parked since 2026-10-01; resume it first: keelbox resume delta":            "✗ delta is parked since 2026-10-01\n",
		"gcloud config list: exit status 1":                                                  "✗ gcloud config list: exit status 1\n",
		"doctl (the DigitalOcean CLI) is not installed or not on PATH":                       "✗ doctl (the DigitalOcean CLI) is not installed or not on PATH\n",
		"203.0.113.7 does not answer on port 22; check it is on: ssh agent@203.0.113.7":      "✗ 203.0.113.7 does not answer on port 22\n",
		"~/.ssh/id_ed25519.pub is empty":                                                     "✗ ~/.ssh/id_ed25519.pub is empty\n",
		"forget takes the box name: keelbox forget acme":                                     "✗ forget takes the box name\n",
		"the last apply did not finish; fix the cause above, then: keelbox up acme":          "✗ The last apply did not finish\n",
		"no box named \"gamma\"; list boxes: keelbox ls":                                     "✗ No box named \"gamma\"\n",
		"--host \"not a host\": an IP address or DNS name, such as 203.0.113.10":             "✗ --host \"not a host\": an IP address or DNS name, such as 203.0.113.10\n",
		"keelbox up has not finished delta; it does what is left: keelbox up delta":          "✗ keelbox up has not finished delta\n",
		"target.provider: \"nowhere\" is not one of local, ssh; fix it: keelbox plan broken": "✗ target.provider: \"nowhere\" is not one of local, ssh\n",
	} {
		got := st.Trouble(Trouble{Text: text, Names: []string{"delta"}})
		if !strings.Contains(got, want) {
			t.Errorf("%q:\n%s\nwant the title %q", text, got, want)
		}
	}
}

// One arrow line, inside the block, and no "Then." left of the words that
// led to it; a note about the state comes last before it.
func TestTroubleHasOneArrowAndNoDanglingLeadIn(t *testing.T) {
	text := "the AWS CLI (the aws command) is not installed or not on PATH; keelbox creates AWS servers through it. " +
		"Install it: https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html, then sign in: aws login. Then: keelbox up devbox"
	got := live(100).Trouble(Trouble{Severity: NeedsYou, Text: text, Note: "Nothing was changed. Your answers so far are kept."})
	want := `  ! The AWS CLI (the aws command) is not installed or not on PATH
    keelbox creates AWS servers through it. Install it:
    https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html, then sign in:
      aws login
    Nothing was changed. Your answers so far are kept.
    → keelbox up devbox
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if strings.Count(got, "→") != 1 || strings.Contains(got, "Then") {
		t.Errorf("want one arrow and no lead-in left:\n%s", got)
	}
}

// A command to copy is on its own line, whole, at every width: here the
// one that is longer than any of them.
func TestTroubleNeverWrapsACommand(t *testing.T) {
	cmd := `doctl auth login --scopes "account:read regions:read sizes:read image:read droplet:create droplet:read droplet:delete firewall:create firewall:read firewall:update firewall:delete ssh_key:create ssh_key:read snapshot:read snapshot:delete image:delete"`
	text := "doctl (the DigitalOcean CLI) is not installed or not on PATH; keelbox creates Droplets through it. Install it: " +
		"https://docs.digitalocean.com/reference/doctl/how-to/install/, then sign in: " + cmd + ". Then: keelbox up devbox"
	for _, w := range []int{60, 80, 100, 120} {
		for name, st := range map[string]Style{
			"live":   live(w),
			"colour": {Live: true, Colour: true, UTF8: true, Depth: Depth256, Width: w},
			"plain":  {Width: w},
		} {
			got := Strip(st.Trouble(Trouble{Severity: NeedsYou, Text: text, Prefix: "keelbox up: "}))
			found := false
			for _, l := range strings.Split(got, "\n") {
				found = found || strings.TrimSpace(l) == cmd
				if strings.TrimSpace(l) != cmd && Width(l) > w {
					t.Errorf("%d %s: a line of prose is %d columns: %q", w, name, Width(l), l)
				}
			}
			if !found {
				t.Errorf("%d %s: the command is not on a line of its own, whole:\n%s", w, name, got)
			}
			if !strings.Contains(got, "keelbox up devbox\n") || strings.Contains(got, "Then") {
				t.Errorf("%d %s: the last command or its lead-in:\n%s", w, name, got)
			}
		}
	}
}

// The commands of an error are painted in the accent colour.
func TestTroubleCommandsAreAccent(t *testing.T) {
	st := Style{Live: true, Colour: true, UTF8: true, Depth: Depth256, Width: 100}
	got := st.Trouble(Trouble{Text: "admin@203.0.113.7 refuses your key; put it there: ssh-copy-id -i ~/.ssh/id_ed25519.pub admin@203.0.113.7, then: keelbox up devbox"})
	for _, cmd := range []string{"ssh-copy-id -i ~/.ssh/id_ed25519.pub admin@203.0.113.7", "keelbox up devbox"} {
		if !strings.Contains(got, st.Cmd(cmd)) {
			t.Errorf("%q is not in the accent colour:\n%q", cmd, got)
		}
	}
}

// The lines of an error that is a list stay lines.
func TestTroubleKeepsListLines(t *testing.T) {
	text := "~/.config/keelbox/boxes/broken.yaml: 2 problems:\n" +
		"  - target.provider: \"nowhere\" is not one of local, ssh, hetzner, aws, digitalocean, gcp (keelbox init --provider ssh writes an example)\n" +
		"  - users.admin: at least one SSH key is required: the admin's only login, so a box without one would lock you out\n" +
		"Fix ~/.config/keelbox/boxes/broken.yaml, then run keelbox up broken again"
	got := live(80).Trouble(Trouble{Text: text})
	want := `  ✗ ~/.config/keelbox/boxes/broken.yaml: 2 problems:
    - target.provider: "nowhere" is not one of local, ssh, hetzner, aws,
      digitalocean, gcp (keelbox init --provider ssh writes an example)
    - users.admin: at least one SSH key is required: the admin's only login, so
      a box without one would lock you out
    Fix ~/.config/keelbox/boxes/broken.yaml, then run keelbox up broken again.
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// What is not a command stays in its sentence: a program's name that
// starts a clause about the program.
func TestCommandLen(t *testing.T) {
	for s, want := range map[string]string{
		"ssh timed out (is it off or asleep?)":                "",
		"ssh admin@203.0.113.7 true":                          "ssh admin@203.0.113.7 true",
		"ssh -p 2222 admin@203.0.113.7. Then: keelbox up lab": "ssh -p 2222 admin@203.0.113.7",
		"gcloud has no price list":                            "",
		"gcloud auth login, then: keelbox up devbox":          "gcloud auth login",
		"sudo reads it there itself":                          "",
		"sudo tailscale up --hostname=acme":                   "sudo tailscale up --hostname=acme",
		"keelbox never sees the credentials":                  "",
		"keelbox forget acme (list boxes: keelbox ls)":        "keelbox forget acme",
		"keelbox ls)": "keelbox ls",
		"keelbox up nas --host-key SHA256:... --yes":                       "keelbox up nas --host-key SHA256:... --yes",
		"keelbox up devbox --host <the server's IP address>":               "keelbox up devbox --host <the server's IP address>",
		`doctl auth login --scopes "a:read, b:read". Then`:                 `doctl auth login --scopes "a:read, b:read"`,
		"keelbox up echo or keelbox forget echo":                           "keelbox up echo",
		"rm ~/.config/keelbox/boxes/x.up.json, then keelbox up x":          "rm ~/.config/keelbox/boxes/x.up.json",
		"keelbox destroy acme; or delete it in the console":                "keelbox destroy acme",
		"aws [options] <command> <subcommand>":                             "",
		"hcloud context list; create it: hcloud context create keelbox":    "hcloud context list",
		"keelbox plan acme.":                                               "keelbox plan acme",
		"ssh-keygen -R 203.0.113.7 -f ~/.config/keelbox/known_hosts":       "ssh-keygen -R 203.0.113.7 -f ~/.config/keelbox/known_hosts",
		"curl -fsSL https://get.keelbox.dev | sh":                          "curl -fsSL https://get.keelbox.dev | sh",
		"journalctl --user -u keelbox-claude-remote-control.service\nmore": "journalctl --user -u keelbox-claude-remote-control.service",
		// A sentence about a command is not a command to run.
		"keelbox destroy deleted it on 2026-11-01; create it":     "",
		"keelbox up acme creates the server again":                "",
		"keelbox reports acme lists them":                         "",
		"keelbox up stopped before it created one":                "",
		"keelbox up does what is left, the steps above":           "",
		"keelbox login lab claude-code, and answer its questions": "keelbox login lab claude-code",
		"keelbox notify test acme --channel phone":                "keelbox notify test acme --channel phone",
		"keelbox up NAME --provider hetzner (or aws)":             "keelbox up NAME --provider hetzner",
		"keelbox destroy acme --yes --discard-work":               "keelbox destroy acme --yes --discard-work",
	} {
		if got := s[:commandLen(s)]; got != want {
			t.Errorf("%q: the command is %q, want %q", s, got, want)
		}
	}
}

// Where the width is not known the text is one line, as it was written.
func TestTroubleOffATerminalIsTheText(t *testing.T) {
	text := "no box named \"gamma\"; list boxes: keelbox ls"
	if got := (Style{}).Trouble(Trouble{Text: text, Prefix: "keelbox: "}); got != "keelbox: "+text+"\n" {
		t.Errorf("got %q", got)
	}
	got := (Style{Width: 60}).Trouble(Trouble{Text: text, Prefix: "keelbox: "})
	if want := "keelbox: no box named \"gamma\"\n  List boxes.\n  Run: keelbox ls\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Two commands offered as alternatives each keep the words that lead to
// them: neither is the one that continues, so there is no arrow.
func TestTroubleKeepsAlternativesApart(t *testing.T) {
	got := live(100).Trouble(Trouble{Names: []string{"echo"},
		Text: "echo has no server (keelbox destroy deleted it on 2026-11-01); create it: keelbox up echo, or remove the box from keelbox: keelbox forget echo"})
	want := `  ✗ echo has no server (keelbox destroy deleted it on 2026-11-01)
    Create it:
      keelbox up echo
    Or remove the box from keelbox:
      keelbox forget echo
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// The title is what went wrong and no more: the first sentence where the
// text has no "; ", with the words that led to the command dropped.
func TestTroubleTitleIsTheFirstSentence(t *testing.T) {
	for text, want := range map[string]string{
		"192.0.2.10 does not accept your SSH key for agent. Check which key logs in: ssh agent@192.0.2.10 true":                                       "  ✗ 192.0.2.10 does not accept your SSH key for agent\n    Check which key logs in.\n    → ssh agent@192.0.2.10 true\n",
		"box name \"My_Box\": use lowercase letters, digits and hyphens, for example: keelbox up my-box":                                              "  ✗ Box name \"My_Box\": use lowercase letters, digits and hyphens\n    → keelbox up my-box\n",
		"web is not set up yet (waiting for the server): keelbox up web":                                                                              "  ✗ web is not set up yet (waiting for the server)\n    → keelbox up web\n",
		"the server exists but apply failed; fix the cause above and run keelbox up acme (or keelbox apply acme), or remove it: keelbox destroy acme": "  ✗ The server exists but apply failed\n    Fix the cause above and run keelbox up acme (or keelbox apply acme), or remove it:\n      keelbox destroy acme\n",
	} {
		if got := live(100).Trouble(Trouble{Text: text, Names: []string{"web"}}); got != want {
			t.Errorf("%q:\ngot:\n%s\nwant:\n%s", text, got, want)
		}
	}
}
