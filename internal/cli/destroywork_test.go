package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider/hetzner/hetznertest"
	"github.com/keelbox/keelbox/internal/snapshot"
	"github.com/keelbox/keelbox/internal/ui"
)

// destroyBox is a keelbox-made server acme in the fake Hetzner project,
// whose projects are what the test says (or an error: the box does not
// answer), with a person at the terminal or not.
func destroyBox(t *testing.T, p *snapshot.Projects, probeErr error, terminal bool) (*hetznertest.API, *int) {
	t.Helper()
	isolate(t)
	api, _ := fakeHetzner(t)
	api.Servers = []map[string]any{{"id": 42, "name": "acme", "status": "running",
		"labels":     map[string]string{"keelbox": "true", "keelbox-box": "acme"},
		"public_net": map[string]any{"ipv4": map[string]any{"ip": "203.0.113.7"}}}}
	oldProbe, oldTerm := boxProjects, stdinIsTerminal
	t.Cleanup(func() { boxProjects, stdinIsTerminal = oldProbe, oldTerm })
	probes := 0
	boxProjects = func(context.Context, *config.Config, string, options) (*snapshot.Projects, error) {
		probes++
		return p, probeErr
	}
	stdinIsTerminal = func() bool { return terminal }
	return api, &probes
}

func destroy(t *testing.T, o options, input string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	logf := func(f string, a ...any) { fmt.Fprintf(&out, "keelbox: "+f+"\n", a...) }
	cfg := hetznerCfg(t, "")
	code := cloudDestroy(context.Background(), &hetznerCloud{cl: newHetznerClient(goodToken), cfg: cfg}, cfg, o, strings.NewReader(input), logf, &out, &out)
	return code, out.String()
}

func deleted(api *hetznertest.API) bool {
	for _, c := range api.Calls() {
		if c == "DELETE /servers/42" {
			return true
		}
	}
	return false
}

var workOnBox = &snapshot.Projects{Dir: "~/projects", Total: 5, List: []snapshot.Project{
	{Name: "shop"}, {Name: "api", Repo: true, Uncommitted: 3, Unpushed: 2}, {Name: "blog", Repo: true, Unpushed: 1},
	{Name: `notes\x1b[2J`, Repo: true, NoRemote: true}, {Name: "site", Repo: true}}}

var cleanBox = &snapshot.Projects{Dir: "~/projects", Total: 1, List: []snapshot.Project{{Name: "site", Repo: true}}}

// Work that is only on the box is listed and asked about, No by default,
// before the box name is asked for; a yes to it does not replace typing
// the name (ADR 0064).
func TestDestroyAsksAboutWorkOnlyOnTheBox(t *testing.T) {
	api, _ := destroyBox(t, workOnBox, nil, true)
	code, out := destroy(t, options{}, "\n")
	want := "keelbox: checking acme for work that exists only there\n" +
		"~/projects on acme holds work that exists nowhere else:\n" +
		"  shop          not a git repository\n" +
		"  api           3 uncommitted changes, 2 commits not pushed\n" +
		"  blog          1 commit not pushed\n" +
		`  notes\x1b[2J  no remote` + "\n" +
		"Deleting the server deletes it. To keep it, commit and push as the agent (keelbox ssh acme), or copy it off, before you delete.\n" +
		"Delete this work with the server? [y/N] Nothing changed.\n"
	if code != 1 || out != want || deleted(api) {
		t.Errorf("Enter at the question: exit %d, deleted %v\n got %q\nwant %q", code, deleted(api), out, want)
	}
	if strings.Contains(out, "site") || strings.ContainsRune(out, '\x1b') {
		t.Errorf("a clean project listed, or an escape sequence printed:\n%q", out)
	}

	api, _ = destroyBox(t, workOnBox, nil, true)
	if code, out := destroy(t, options{}, "y\nwrong\n"); code != exitDeclined || deleted(api) || !strings.HasSuffix(out, "Type the box name to confirm: Nothing changed. \"wrong\" is not the box name, acme.\n") {
		t.Errorf("yes, then the wrong name: exit %d\n%s", code, out)
	}
	api, _ = destroyBox(t, workOnBox, nil, true)
	if code, out := destroy(t, options{}, "y\nacme\n"); code != 0 || !deleted(api) || !strings.Contains(out, "deleted acme (id 42)") {
		t.Errorf("yes, then the name: exit %d\n%s", code, out)
	}
	// --yes answers the box name, not the question about the work.
	api, _ = destroyBox(t, workOnBox, nil, true)
	if code, out := destroy(t, options{yes: true}, "n\n"); code != 1 || deleted(api) || !strings.Contains(out, "Delete this work with the server? [y/N]") {
		t.Errorf("--yes on a terminal, no: exit %d\n%s", code, out)
	}
}

// Without a terminal nothing is asked: destroy refuses, --yes or not, and
// names the flag that deletes the work too.
func TestDestroyRefusesWithoutATerminal(t *testing.T) {
	for _, o := range []options{{}, {yes: true}} {
		api, _ := destroyBox(t, workOnBox, nil, false)
		code, out := destroy(t, o, "y\nacme\n")
		if code != 1 || deleted(api) || !strings.Contains(out, "shop          not a git repository") ||
			!strings.Contains(out, "keelbox: not deleted: acme holds work that exists nowhere else. Push it or copy it off first, or delete it with the server: keelbox destroy acme --yes --discard-work") ||
			strings.Contains(out, "[y/N]") {
			t.Errorf("%+v: exit %d, deleted %v\n%s", o, code, deleted(api), out)
		}
	}
	api, probes := destroyBox(t, workOnBox, nil, false)
	if code, out := destroy(t, options{yes: true, discardWork: true}, ""); code != 0 || !deleted(api) || *probes != 0 {
		t.Errorf("--yes --discard-work: exit %d, %d probes\n%s", code, *probes, out)
	}
	// --discard-work alone still asks for the box name.
	api, _ = destroyBox(t, workOnBox, nil, false)
	if code, out := destroy(t, options{discardWork: true}, "\n"); code != exitDeclined || deleted(api) || !strings.HasSuffix(out, "Type the box name to confirm: Nothing changed.\n") {
		t.Errorf("--discard-work without the name: exit %d\n%s", code, out)
	}
}

// A box that does not answer could hold anything: destroy says it could
// not check and asks the same way.
func TestDestroyWhenTheBoxDoesNotAnswer(t *testing.T) {
	down := errors.New("ssh: connect to host 203.0.113.7 port 22: Connection timed out")
	api, _ := destroyBox(t, nil, down, true)
	code, out := destroy(t, options{yes: true}, "\n")
	if code != 1 || deleted(api) ||
		!strings.Contains(out, "keelbox: could not check acme for work that exists only there (ssh: connect to host 203.0.113.7 port 22: Connection timed out). Deleting the server deletes whatever is on it.\n") ||
		!strings.HasSuffix(out, "Delete it without knowing? [y/N] Nothing changed.\n") {
		t.Errorf("Enter: exit %d\n%s", code, out)
	}
	api, _ = destroyBox(t, nil, down, true)
	if code, out := destroy(t, options{yes: true}, "y\n"); code != 0 || !deleted(api) {
		t.Errorf("yes: exit %d\n%s", code, out)
	}
	api, _ = destroyBox(t, nil, down, false)
	code, out = destroy(t, options{yes: true}, "y\n")
	if code != 1 || deleted(api) || !strings.Contains(out, "keelbox: not deleted: could not check acme for work that exists only there") ||
		!strings.Contains(out, "or delete it without the check: keelbox destroy acme --yes --discard-work") {
		t.Errorf("no terminal: exit %d\n%s", code, out)
	}
}

// With every project clean and pushed, and with none, destroy goes on as
// before; directories past the ones read are not taken as clean.
func TestDestroyWithNothingOnlyOnTheBox(t *testing.T) {
	for _, p := range []*snapshot.Projects{cleanBox, {Dir: "~/projects"}} {
		api, _ := destroyBox(t, p, nil, false)
		code, out := destroy(t, options{yes: true}, "")
		if code != 0 || !deleted(api) || !strings.Contains(out, "keelbox: no work that is only on acme: "+p.Summary()) {
			t.Errorf("%+v: exit %d\n%s", p, code, out)
		}
	}
	many := &snapshot.Projects{Dir: "~/projects", Total: 60}
	for i := range 50 {
		many.List = append(many.List, snapshot.Project{Name: fmt.Sprint("p", i), Repo: true})
	}
	api, _ := destroyBox(t, many, nil, false)
	if code, out := destroy(t, options{yes: true}, ""); code != 1 || deleted(api) || !strings.Contains(out, "and 10 more that were not read") {
		t.Errorf("60 projects: exit %d\n%s", code, out)
	}
	// More than destroy lists.
	lots := &snapshot.Projects{Dir: "~/projects", Total: 14}
	for i := range 14 {
		lots.List = append(lots.List, snapshot.Project{Name: fmt.Sprint("p", i)})
	}
	api, _ = destroyBox(t, lots, nil, false)
	if _, out := destroy(t, options{yes: true}, ""); deleted(api) || strings.Count(out, "not a git repository") != snapshot.Shown || !strings.Contains(out, "  and 4 more\n") {
		t.Errorf("14 projects:\n%s", out)
	}
}

// On a styled terminal the list keeps to the terminal's width.
func TestDestroyWorkListFits80Columns(t *testing.T) {
	long := &snapshot.Projects{Dir: "~/projects", Total: 2, List: []snapshot.Project{
		{Name: strings.Repeat("n", 40), Repo: true, Uncommitted: 120, Unpushed: 45}, {Name: "shop"}}}
	out := workList(ui.Style{Colour: true, Live: true, UTF8: true, Width: 80}, "acme", long)
	for _, l := range strings.Split(out, "\n") {
		if w := ui.Width(l); w > 80 {
			t.Errorf("%d columns: %q", w, l)
		}
	}
	if flat := strings.Join(strings.Fields(ui.Strip(out)), " "); !strings.Contains(flat, "120 uncommitted changes, 45 commits not pushed") ||
		!strings.Contains(flat, "commit and push as the agent (keelbox ssh acme)") {
		t.Errorf("wrapped text lost words:\n%s", out)
	}
}

// Ctrl-C at either of destroy's questions deletes nothing and leaves it
// to Main to say that nothing changed, once, with exit 130.
func TestDestroyStoppedAtAQuestion(t *testing.T) {
	for name, o := range map[string]options{"the work": {}, "the box name": {discardWork: true}} {
		api, _ := destroyBox(t, workOnBox, nil, true)
		ctx, cancel := context.WithCancel(context.Background())
		go func() { // Ctrl-C once the question waits
			for !prompt.Waiting() {
				time.Sleep(time.Millisecond)
			}
			cancel()
		}()
		stoppedAtQuestion.Store(false)
		var out bytes.Buffer
		cfg := hetznerCfg(t, "")
		code := cloudDestroy(ctx, &hetznerCloud{cl: newHetznerClient(goodToken), cfg: cfg}, cfg, o, blocked{}, func(string, ...any) {}, &out, &out)
		if code != exitInterrupted || deleted(api) || !stoppedAtQuestion.Load() || strings.Contains(out.String(), "Nothing changed") {
			t.Errorf("%s: exit %d, deleted %v, told %v\n%s", name, code, deleted(api), stoppedAtQuestion.Load(), out.String())
		}
		stoppedAtQuestion.Store(false)
	}
}

// Before the server goes, the box signs out of Tailscale; afterwards
// destroy lists what is still valid and where to revoke it. A sign-out
// that fails does not stop the deletion.
func TestDestroySignsOutAndListsWhatStaysValid(t *testing.T) {
	old := signOutTailscale
	t.Cleanup(func() { signOutTailscale = old })
	for _, fails := range []bool{false, true} {
		api, _ := destroyBox(t, cleanBox, nil, false)
		calls := 0
		signOutTailscale = func(_ context.Context, _ *config.Config, ip string, _ options) error {
			calls++
			if deleted(api) {
				t.Error("signed out after the server was deleted")
			}
			if fails {
				return errors.New("ssh: connect: timed out")
			}
			return nil
		}
		code, out := destroy(t, options{yes: true}, "")
		if code != 0 || !deleted(api) || calls != 1 {
			t.Fatalf("fails=%v: exit %d, %d sign-outs\n%s", fails, code, calls, out)
		}
		for _, want := range []string{
			"Still valid after the server is gone; revoke what acme had:",
			"https://login.tailscale.com/admin/machines",
			"https://github.com/settings/personal-access-tokens",
			"/guides/removing-a-box#what-stays-elsewhere",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("fails=%v: no %q in\n%s", fails, want, out)
			}
		}
		said := map[bool]string{false: "asked acme to sign out of Tailscale", true: "could not sign acme out of Tailscale (ssh: connect: timed out); remove it in the Tailscale admin console"}[fails]
		if !strings.Contains(out, said) {
			t.Errorf("fails=%v: no %q in\n%s", fails, said, out)
		}
	}
}
