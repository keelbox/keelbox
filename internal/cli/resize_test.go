package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/ui"
)

// sizeWorlds are the four clouds with the box acme on each, as the size
// question sees them.
func sizeWorlds(t *testing.T) map[string]cloud {
	t.Helper()
	return map[string]cloud{
		"hetzner": newParkWorld(t).cl, "aws": newAWSWorld(t).cl, "digitalocean": newDOWorld(t).cl, "gcp": newGCPWorld(t).cl,
	}
}

// On a drawn menu the sizes compare at a glance: each figure in a column
// of its own, the prices one under the other, the size the box has marked
// and selected. At any width no line overflows and no figure is split from
// its unit or its currency.
func TestResizeMenuAlignsTheSizes(t *testing.T) {
	for name, cl := range sizeWorlds(t) {
		srv, err := cl.find(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		sizes, _, err := cl.sizes(context.Background(), srv)
		if err != nil {
			t.Fatal(err)
		}
		cur := -1
		for i, s := range sizes {
			if s.current {
				cur = i
			}
		}
		plain, drawn := sizeChoices(sizes, false), sizeChoices(sizes, true)
		for _, width := range []int{60, 80, 120} {
			st := ui.Style{Live: true, UTF8: true, Width: width}
			lines := prompt.MenuLines(st, drawn, cur, st.Cols())
			for _, l := range lines {
				if ui.Width(l) >= width {
					t.Errorf("%s at %d columns: a line is %d wide: %q", name, width, ui.Width(l), l)
				}
				f := strings.Fields(l)
				if n := len(f); n > 0 && (f[n-1] == "USD" || f[n-1] == "EUR" || f[0] == "vCPU" || f[0] == "GB" || f[0] == "TB") {
					t.Errorf("%s at %d columns: a figure is split from its unit or its currency: %q", name, width, l)
				}
			}
			// The same words as the plain list, whose separators are commas.
			var want []string
			for i, c := range plain {
				want = append(want, fmt.Sprint(i+1), c.Label, strings.NewReplacer(",", "", ";", "").Replace(c.Detail))
			}
			got := strings.NewReplacer("›", "", ",", "").Replace(strings.Join(lines, " "))
			if strings.Join(strings.Fields(got), " ") != strings.Join(strings.Fields(strings.Join(want, " ")), " ") {
				t.Errorf("%s at %d columns: the menu says\n%s\nand the plain list\n%s", name, width, strings.Join(lines, "\n"), strings.Join(want, "\n"))
			}
			if width < 120 {
				continue
			}
			// With room for it, one size a line and every price in the same
			// columns.
			if len(lines) != len(sizes) {
				t.Errorf("%s at %d columns: %d lines for %d sizes:\n%s", name, width, len(lines), len(sizes), strings.Join(lines, "\n"))
				continue
			}
			at := -1
			for i, l := range lines {
				j := strings.Index(l, "/month")
				if j >= 0 {
					j = ui.Width(l[:j])
				}
				if sizes[i].price > 0 && (j < 0 || at >= 0 && j != at) {
					t.Errorf("%s: the prices are not in one column:\n%s", name, strings.Join(lines, "\n"))
				}
				if j >= 0 {
					at = j
				}
				if strings.Contains(l, "the size now") != (i == cur) || strings.HasPrefix(l, "  › ") != (i == cur) {
					t.Errorf("%s: the size the box has is not the one marked and selected:\n%s", name, strings.Join(lines, "\n"))
				}
			}
		}
	}
	// The rows themselves, on Hetzner.
	w := newParkWorld(t)
	srv, _ := w.cl.find(context.Background())
	sizes, _, _ := w.cl.sizes(context.Background(), srv)
	st := ui.Style{Live: true, UTF8: true, Width: 100}
	got := strings.Join(prompt.MenuLines(st, sizeChoices(sizes, true), 0, st.Cols()), "\n")
	want := "  › 1  cx23    2 vCPU (shared)      4 GB   EUR  3.79/month   the size now\n" +
		"    2  cpx22   2 vCPU (shared)      4 GB   EUR  7.49/month\n" +
		"    3  ccx13   2 vCPU (dedicated)   8 GB   EUR 14.49/month"
	if got != want {
		t.Errorf("the menu:\n%s\nwant:\n%s", got, want)
	}
}

func configText(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(boxConfigPath("acme"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// resize shuts down, changes the type with the disk left as it is, starts
// the server, rewrites the size in the config and applies.
func TestResize(t *testing.T) {
	w := newParkWorld(t)
	before := configText(t)
	code, out := w.do(t, "resize", options{size: "cpx22", yes: true, sshUser: "root"}, nil)
	if code != 0 {
		t.Fatalf("resize: exit %d\n%s", code, out)
	}
	if got, want := strings.Join(w.writes(), "\n"), "POST /servers/42/actions/shutdown\nPOST /servers/42/actions/change_type\nPOST /servers/42/actions/poweron"; got != want {
		t.Errorf("calls:\n%s\nwant:\n%s", got, want)
	}
	srv := w.api.Servers[0]
	// cpx22's own disk is 80 GB; the server's stays 40, so it can go back.
	if srv["server_type"].(map[string]any)["name"] != "cpx22" || srv["primary_disk_size"] != 40 || srv["status"] != "running" {
		t.Errorf("server after resize: %v", srv)
	}
	// Waited for and applied as the admin, at the address it kept.
	if len(w.waited) != 1 || w.waited[0] != "203.0.113.7" || len(w.applied) != 1 || !strings.HasPrefix(w.applied[0], `203.0.113.7 as ""`) {
		t.Errorf("waited %v, applied %v", w.waited, w.applied)
	}
	wantAll(t, "resize", out, "keelbox: shutting acme down\n", "keelbox: server 42 is off; changing it from cx23 to cpx22\n",
		"keelbox: set target.hetzner.server_type: cpx22 in ~/.config/keelbox/boxes/acme.yaml\n",
		"\nacme is cpx22 now in ")
	wantAll(t, "the end of resize", flat(out), resizedWords("cpx22: 2 vCPU (shared), 4 GB, EUR 7.49/month", "cx23: 2 vCPU (shared), 4 GB, EUR 3.79/month",
		"costs about EUR 3.70 a month more than before ", "its 40 GB disk, its address and everything on it", "cx23"))
	if !strings.HasSuffix(flat(out), "change back keelbox resize acme --size cx23") {
		t.Errorf("resize does not end on the way back:\n%s", out)
	}
	// The config: that one value changed, the comment and every other line kept.
	after := configText(t)
	if want := strings.Replace(before, "server_type: cx23 ", "server_type: cpx22", 1); after != want || !strings.Contains(after, "    server_type: cpx22                 # keelbox up lists what can be ordered, with prices\n") {
		t.Errorf("config after resize:\n%s", after)
	}
	if w.cfg.Target.Hetzner.ServerType != "cpx22" {
		t.Errorf("the config the apply ships says %s", w.cfg.Target.Hetzner.ServerType)
	}

	// And back down: the 40 GB disk still fits cx23.
	code, out = w.do(t, "resize", options{size: "cx23", yes: true}, nil)
	if code != 0 || w.api.Servers[0]["server_type"].(map[string]any)["name"] != "cx23" || configText(t) != before {
		t.Errorf("resize down: exit %d\n%s", code, out)
	}
	// A smaller size is said to save what the bigger one cost.
	wantAll(t, "the end of a resize down", flat(out), resizedWords("cx23: 2 vCPU (shared), 4 GB, EUR 3.79/month", "cpx22: 2 vCPU (shared), 4 GB, EUR 7.49/month",
		"saves about EUR 3.70 a month ", "its 40 GB disk, its address and everything on it", "cpx22"))
	// The size it has: said, nothing sent.
	w.forgetCalls()
	if code, out := w.do(t, "resize", options{size: "cx23", yes: true}, nil); code != 0 || len(w.writes()) != 0 || !strings.Contains(out, "keelbox: acme is cx23 already. Nothing changed.") {
		t.Errorf("resize to the size it has: exit %d\n%s", code, out)
	}
}

// Without a terminal nothing is asked and nothing changes: exit 2, with
// the command to run.
func TestResizeWithoutATerminal(t *testing.T) {
	w := newParkWorld(t)
	code, out := w.do(t, "resize", options{}, nil)
	if code != 2 || len(w.writes()) != 0 {
		t.Errorf("no --size: exit %d, %v", code, w.writes())
	}
	// The list a terminal would show, and last the command.
	if want := "keelbox: resize needs a size, and there is no terminal to ask on. acme is cx23 now (2 vCPU (shared), 4 GB, EUR 3.79/month) and can change to:\n" +
		"  cpx22  2 vCPU (shared), 4 GB, EUR 7.49/month\n" +
		"  ccx13  2 vCPU (dedicated), 8 GB, EUR 14.49/month\n" +
		"Name one, for example: keelbox resize acme --size cpx22 --yes\n"; out != want {
		t.Errorf("no --size:\n%s\nwant:\n%s", out, want)
	}
	code, out = w.do(t, "resize", options{size: "cpx22"}, nil)
	if code != 2 || len(w.writes()) != 0 || !strings.Contains(out, "keelbox: resize asks first, because the box restarts; without a terminal: keelbox resize acme --size cpx22 --yes") {
		t.Errorf("no --yes: exit %d, %v\n%s", code, w.writes(), out)
	}
	// A size the server cannot change to: too small a disk, another
	// architecture, not sold in the location, or no such size.
	for _, size := range []string{"cpx02", "cax11", "cpx11", "nope"} {
		code, out = w.do(t, "resize", options{size: size, yes: true}, nil)
		if code != 2 || len(w.writes()) != 0 || !strings.Contains(out, `--size "`+size+`" is not a size acme can change to and keep its disk. It is cx23 now and can change to cpx22, ccx13: keelbox resize acme --size cpx22`+"\n") {
			t.Errorf("--size %s: exit %d, %v\n%s", size, code, w.writes(), out)
		}
	}
}

// On a terminal resize lists the sizes with prices, marks the one the
// server has, and says before the go-ahead that the box restarts.
func TestResizeAsks(t *testing.T) {
	w := newParkWorld(t)
	code, out := w.do(t, "resize", options{}, str("2\n\n"))
	if code != 1 || len(w.writes()) != 0 {
		t.Errorf("Enter at the go-ahead: exit %d, %v", code, w.writes())
	}
	wantAll(t, "the questions", out,
		"Prices are estimates from the Hetzner API for fsn1, with VAT, and Hetzner bills by the hour. The disk stays 40 GB at every size, so you can change back from any size listed. 2 vCPU and 4 GB suit one or two agents.",
		"Size for acme (it is cx23 now):",
		"  1) cx23   2 vCPU (shared), 4 GB, EUR 3.79/month; the size now\n",
		"  2) cpx22  2 vCPU (shared), 4 GB, EUR 7.49/month\n",
		"  3) ccx13  2 vCPU (dedicated), 8 GB, EUR 14.49/month\n",
		"Resize acme? [y/N] ", "Nothing changed.\n")
	wantAll(t, "the go-ahead", flat(out),
		"Resize acme from cx23 to cpx22: from cx23: 2 vCPU (shared), 4 GB, EUR 3.79/month "+
			"to cpx22: 2 vCPU (shared), 4 GB, EUR 7.49/month "+
			"costs about EUR 3.70 a month more than now "+
			"what happens acme restarts: running sessions, agents, dev servers and containers stop. It is back in 1 to 2 minutes, typically "+
			"what stays its 40 GB disk, its address and everything on it "+
			"change back keelbox resize acme --size cx23 Resize acme? [y/N] Nothing changed.")
	// The disk is said once, where it stays. A type's own disk, 80 GB for
	// cpx22, is not what the server gets, and is not shown beside the sizes.
	if strings.Contains(out, "80 GB") {
		t.Errorf("the disk of the type itself is shown:\n%s", out)
	}
	if strings.Contains(out, "cpx02") || strings.Contains(out, "cax11") || strings.Contains(out, "cx33") {
		t.Errorf("a size the server cannot change to is listed:\n%s", out)
	}
	// Enter at the list keeps the size it has.
	if code, out := w.do(t, "resize", options{}, str("\n")); code != 0 || len(w.writes()) != 0 || !strings.Contains(out, "acme is cx23 already. Nothing changed.") {
		t.Errorf("Enter at the list: exit %d\n%s", code, out)
	}
	if code, out := w.do(t, "resize", options{}, str("ccx13\ny\n")); code != 0 || w.api.Servers[0]["server_type"].(map[string]any)["name"] != "ccx13" {
		t.Errorf("by name, then yes: exit %d\n%s", code, out)
	}
}

// A change the cloud refuses leaves the box as it was, running, and the
// config untouched.
func TestResizeThatFailsStartsTheServerAgain(t *testing.T) {
	for _, call := range []string{"shutdown", "change_type"} {
		w := newParkWorld(t)
		before := configText(t)
		w.api.Fail = map[string]bool{call: true}
		code, out := w.do(t, "resize", options{size: "cpx22", yes: true}, nil)
		srv := w.api.Servers[0]
		if code != 1 || srv["status"] != "running" || srv["server_type"].(map[string]any)["name"] != "cx23" || configText(t) != before || len(w.applied) != 0 {
			t.Errorf("%s fails: exit %d, server %v\n%s", call, code, srv, out)
		}
		wantAll(t, call+" fails", out, "keelbox: acme is still cx23 and runs again at 203.0.113.7. Try again: keelbox resize acme\n")
	}
	// Changed, but the apply fails: the config says the new size, and the
	// next step is an apply.
	w := newParkWorld(t)
	w.applyCode = 1
	code, out := w.do(t, "resize", options{size: "cpx22", yes: true}, nil)
	if code != 1 || !strings.Contains(configText(t), "server_type: cpx22 ") || !strings.Contains(out, "acme is cpx22 now, but the apply did not pass; fix the cause above, then: keelbox apply acme") {
		t.Errorf("apply fails: exit %d\n%s", code, out)
	}
}

// A config that is not written the way keelbox writes it is left as it
// is, and the line to set is printed.
func TestResizePrintsTheLineForAConfigItCannotEdit(t *testing.T) {
	w := newParkWorld(t)
	flow := "version: 1\nbox: acme\ntarget: {provider: hetzner, hetzner: {server_type: cx23, location: fsn1, ssh_keys: [k]}}\n" +
		"users: {admin: {ssh_authorized_keys: [\"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop\"]}}\n"
	if err := os.WriteFile(boxConfigPath("acme"), []byte(flow), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := w.do(t, "resize", options{size: "cpx22", yes: true}, nil)
	if code != 0 || configText(t) != flow {
		t.Errorf("exit %d, config:\n%s", code, configText(t))
	}
	wantAll(t, "resize", out, "keelbox: could not set target.hetzner.server_type in ~/.config/keelbox/boxes/acme.yaml (the key is not on a line of its own). "+
		"Set it yourself, so the config says what the server is; under target.hetzner:\n      server_type: cpx22\n", "acme is cpx22 now")
}

func TestSetYAMLKey(t *testing.T) {
	path := []string{"target", "hetzner", "server_type"}
	for _, c := range []struct{ name, in, want string }{
		{"quoted, with a comment in its column",
			"target:\n  provider: hetzner\n  hetzner:\n    server_type: \"cx23\"                # sizes\n    location: fsn1\n",
			"target:\n  provider: hetzner\n  hetzner:\n    server_type: \"cpx32\"               # sizes\n    location: fsn1\n"},
		{"plain, no comment, a longer value",
			"# top\nversion: 1\ntarget:\n  # where\n  hetzner:\n\n    location: fsn1\n    server_type: cx23\nhost:\n  hostname: x\n",
			"# top\nversion: 1\ntarget:\n  # where\n  hetzner:\n\n    location: fsn1\n    server_type: cpx32\nhost:\n  hostname: x\n"},
		{"a comment hard against the value",
			"target:\n  hetzner:\n    server_type: cx23 # was cx22\n",
			"target:\n  hetzner:\n    server_type: cpx32 # was cx22\n"},
		{"the same key elsewhere is left alone",
			"other:\n  hetzner:\n    server_type: a\ntarget:\n  ssh:\n    server_type: b\n  hetzner:\n    server_type: 'cx23'\n",
			"other:\n  hetzner:\n    server_type: a\ntarget:\n  ssh:\n    server_type: b\n  hetzner:\n    server_type: 'cpx32'\n"},
		{"no newline at the end, CRLF kept",
			"target:\r\n  hetzner:\r\n    server_type: cx23",
			"target:\r\n  hetzner:\r\n    server_type: cpx32"},
	} {
		got, err := setYAMLKey(c.in, path, "cpx32")
		if err != nil || got != c.want {
			t.Errorf("%s: %v\n got %q\nwant %q", c.name, err, got, c.want)
		}
		if v, err := yamlValue([]byte(got), path); err != nil || v != "cpx32" {
			t.Errorf("%s: the result reads %q %v", c.name, v, err)
		}
	}
	for name, in := range map[string]string{
		"flow mappings":          "target: {hetzner: {server_type: cx23}}\n",
		"a flow mapping inside":  "target:\n  hetzner: {server_type: cx23}\n",
		"the key is missing":     "target:\n  hetzner:\n    location: fsn1\n",
		"only in a comment":      "target:\n  hetzner:\n    # server_type: cx23\n    location: fsn1\n",
		"the section is missing": "target:\n  provider: ssh\nhetzner:\n  server_type: cx23\n",
		"a value on other lines": "target:\n  hetzner:\n    server_type:\n      cx23\n",
		"an anchor for a value":  "target:\n  hetzner:\n    server_type: *size\n",
	} {
		if got, err := setYAMLKey(in, path, "cpx32"); err == nil {
			t.Errorf("%s: edited to %q", name, got)
		}
	}
	if _, err := setYAMLKey("target:\n  hetzner:\n    server_type: cx23\n", path, "a b: c"); err == nil {
		t.Error("wrote a value that is not a plain word")
	}
}

func TestResizeOfTheStarterConfigs(t *testing.T) {
	// The size key each cloud names is in the config keelbox writes for
	// it, on a line setYAMLKey can edit.
	for provider, key := range map[string]string{
		config.ProviderHetzner: "target.hetzner.server_type", config.ProviderAWS: "target.aws.bundle",
		config.ProviderDigitalOcean: "target.digitalocean.size", config.ProviderGCP: "target.gcp.machine_type",
	} {
		s := config.StarterFor("acme", provider)
		s.ServerType, s.Bundle, s.Size, s.MachineType = "old", "old", "old", "e2-small"
		doc, err := s.Render()
		if err != nil {
			t.Fatal(err)
		}
		path := strings.Split(key, ".")
		out, err := setYAMLKey(doc, path, "new-size_1.x")
		if err != nil {
			t.Errorf("%s: %v", key, err)
			continue
		}
		if v, err := yamlValue([]byte(out), path); err != nil || v != "new-size_1.x" {
			t.Errorf("%s: reads %q %v", key, v, err)
		}
	}
}
