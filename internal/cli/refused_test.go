package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/provider/hetzner"
	"github.com/keelbox/keelbox/internal/provider/hetzner/hetznertest"
)

// forbidden is Hetzner turning an account away, in the shape reported
// for it; the API's exact code for that is not documented.
var forbidden = hetznertest.Error{Status: 403, Code: "forbidden", Message: "creating servers in fsn1 is not possible for this account at the moment"}

const forbiddenText = "hetzner api: 403 forbidden: creating servers in fsn1 is not possible for this account at the moment"

// fakeTypes is the fake API's catalogue as the client reads it.
func fakeTypes(t *testing.T) []hetzner.ServerType {
	t.Helper()
	data, _ := json.Marshal(hetznertest.ServerTypes())
	var types []hetzner.ServerType
	if err := json.Unmarshal(data, &types); err != nil {
		t.Fatal(err)
	}
	return types
}

// refusedOnce has the fake runner's first create refused as Hetzner
// refuses cx23 in fsn1; the creates after it succeed.
func refusedOnce(t *testing.T, r *fakeRunner) {
	t.Helper()
	types := fakeTypes(t)
	n := 0
	r.onCreate = func(o options) {
		n++
		r.createCode = 0
		if n > 1 {
			o.hooks.server("192.0.2.44")
		}
		if n == 1 {
			r.createCode = 1
			re := &hetzner.RefusedError{API: &hetzner.APIError{Status: forbidden.Status, Code: forbidden.Code, Message: forbidden.Message},
				Action: "create", Box: "acme", Location: "fsn1", ServerType: "cx23", Types: types,
				Left:           "No server was created, and keelbox deleted the firewall it had made for it; your SSH key stays in the project for the next try",
				OtherLocations: []string{"ash"}, OtherTypes: []string{"cax11", "cpx02", "cpx22", "ccx13"}}
			if !o.hooks.refused(hetznerRefusal(re).(*cloudRefusal)) {
				t.Error("up did not take the refusal")
			}
		}
	}
}

// Without a terminal, up says what Hetzner said and prints a command for
// each way on; each of them works as printed.
func TestUpHetznerRefusedWithoutATerminal(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	_, store := fakeHetzner(t)
	_ = store.Set(context.Background(), "hetzner", goodToken)
	r := &fakeRunner{signed: true}
	refusedOnce(t, r)
	code, out := upWith(t, r, nil, "up", "acme", "--provider", "hetzner", "--agents", "codex", "--ssh-key", key, "--yes")
	if os.Getenv("SHOW") != "" {
		t.Log(out)
	}
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	text := flat(out)
	for _, want := range []string{
		"Hetzner refused to create acme's server, cx23 in fsn1 (" + forbiddenText + ")",
		"Hetzner may be limiting new servers for this account or in fsn1",
		"No server was created, and keelbox deleted the firewall it had made for it",
		"Try another location: keelbox up acme --location ash; another size: keelbox up acme --size cax11 (or cpx02, cpx22); " +
			"another cloud, with your other answers kept: keelbox up acme --provider aws --yes (or digitalocean, gcp); or the same again later: keelbox up acme",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(text), "or the same again later: keelbox up acme") {
		t.Errorf("does not end with the command:\n%s", out)
	}
	_, st := loadBox(t, "acme")
	if st.Stage != stageConfigured || st.Applying || st.Answers["agents"] != "codex" || st.Answers["provider"] != "hetzner" {
		t.Fatalf("state %+v", st)
	}
	// forget knows no server was created.
	if plan := forgetPlanFor("acme", mustLoad(t, "acme")); plan.stays != "" {
		t.Errorf("forget would say a server may exist: %+v", plan)
	}

	// Another location, as printed.
	r.calls = nil
	code, out = upWith(t, r, nil, "up", "acme", "--location", "ash")
	cfg, st := loadBox(t, "acme")
	if code != 0 || cfg.Target.Hetzner.Location != "ash" || cfg.Target.Hetzner.ServerType != "cx23" || !strings.HasPrefix(strings.Join(r.calls, "; "), "create acme") {
		t.Fatalf("exit %d, %+v, calls %v\n%s", code, cfg.Target.Hetzner, r.calls, out)
	}
	if strings.Contains(out, "change nothing") || !strings.Contains(out, "set target.hetzner.location: ash in ~/.config/keelbox/boxes/acme.yaml") {
		t.Errorf("output:\n%s", out)
	}
	if st.Answers != nil {
		t.Errorf("answers kept past the create: %v", st.Answers)
	}
}

func mustLoad(t *testing.T, box string) *config.Config {
	t.Helper()
	cfg, _ := loadBox(t, box)
	return cfg
}

// After a refusal, --provider moves the box to another cloud with the
// other answers.
func TestUpRefusedBoxMovesToAnotherCloud(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	_, store := fakeHetzner(t)
	_ = store.Set(context.Background(), "hetzner", goodToken)
	fakeDO(t)
	r := &fakeRunner{signed: true}
	refusedOnce(t, r)
	if code, out := upWith(t, r, nil, "up", "acme", "--provider", "hetzner", "--agents", "codex", "--tailscale=false", "--ssh-key", key, "--yes"); code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	r.calls = nil
	code, out := upWith(t, r, nil, "up", "acme", "--provider", "digitalocean", "--doctl-context", "work", "--location", "fra1", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	cfg, st := loadBox(t, "acme")
	if cfg.Target.Provider != config.ProviderDigitalOcean || st.Via != viaDO || cfg.Target.DigitalOcean.Context != "work" {
		t.Fatalf("target %+v, state %+v", cfg.Target, st)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0] != "codex" || cfg.Tailscale() {
		t.Errorf("answers not carried: agents %v, tailscale %v", cfg.Agents, cfg.Tailscale())
	}
	if !strings.Contains(out, "moving acme from hetzner to digitalocean") || !strings.HasPrefix(strings.Join(r.calls, "; "), "create acme") {
		t.Errorf("calls %v\n%s", r.calls, out)
	}
}

// --provider never rewrites a box whose server may exist.
func TestUpMovesOnlyABoxWithNoServer(t *testing.T) {
	home := isolate(t)
	key := laptopKey(t, home)
	_, store := fakeHetzner(t)
	_ = store.Set(context.Background(), "hetzner", goodToken)
	// A create that failed after the order: the server may exist.
	r := &fakeRunner{createCode: 1}
	if code, out := upWith(t, r, nil, "up", "acme", "--provider", "hetzner", "--ssh-key", key, "--yes"); code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	_, out := upWith(t, r, nil, "up", "acme", "--provider", "digitalocean", "--location", "ash", "--yes")
	cfg, _ := loadBox(t, "acme")
	if cfg.Target.Provider != config.ProviderHetzner || cfg.Target.Hetzner.Location != "fsn1" || !strings.Contains(out, "--provider, --location change nothing") {
		t.Errorf("target %+v\n%s", cfg.Target, out)
	}
}

// On a terminal, up asks how to go on and carries on in the same run.
func TestUpHetznerRefusedAsksForAnotherLocation(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	fakeHetzner(t)
	r := &fakeRunner{signed: true}
	refusedOnce(t, r)
	in := script(
		"3",       // where: a new cloud server
		"1",       // which cloud: Hetzner
		"acme",    // name
		goodToken, // token (hidden)
		"",        // location: default fsn1
		"",        // size: default cx23
		"",        // agents: both
		"",        // GitHub CLI: yes
		"",        // Tailscale: yes
		"",        // go ahead
		"1",       // refused: another location, ash, the only other one
		"n",       // Remote Control
	)
	code, out := upWith(t, r, in, "up")
	if os.Getenv("SHOW") != "" {
		t.Log(out)
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{forbiddenText, "How should keelbox up go on?", "Another location", "cx23 can be ordered in ash",
		"Another size in fsn1", "Another cloud", "AWS (Lightsail), DigitalOcean or Google Cloud; your other answers are kept", "Stop here"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
	cfg, st := loadBox(t, "acme")
	if cfg.Target.Hetzner.Location != "ash" || cfg.Target.Hetzner.ServerType != "cx23" || st.Stage != stageReady {
		t.Errorf("target %+v, state %+v", cfg.Target.Hetzner, st)
	}
	if n := strings.Count(strings.Join(r.calls, "; "), "create acme"); n != 2 {
		t.Errorf("%d creates: %v", n, r.calls)
	}
}

// Another cloud from the question asks only that cloud's questions and
// the go-ahead again.
func TestUpHetznerRefusedMovesToAnotherCloudInTheSameRun(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	fakeHetzner(t)
	fakeDO(t)
	r := &fakeRunner{signed: true}
	refusedOnce(t, r)
	in := script(
		"3",       // where: a new cloud server
		"1",       // which cloud: Hetzner
		"acme",    // name
		goodToken, // token (hidden)
		"",        // location: default fsn1
		"",        // size: default cx23
		"3",       // agents: Codex
		"",        // GitHub CLI: yes
		"",        // Tailscale: yes
		"",        // go ahead
		"3",       // refused: another cloud
		"2",       // DigitalOcean
		"",        // context: the current one
		"",        // region: the first
		"",        // size: default
		"",        // go ahead
	)
	code, out := upWith(t, r, in, "up")
	if os.Getenv("SHOW") != "" {
		t.Log(out)
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	cfg, st := loadBox(t, "acme")
	if cfg.Target.Provider != config.ProviderDigitalOcean || st.Via != viaDO || st.Stage != stageReady {
		t.Fatalf("target %+v, state %+v", cfg.Target, st)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0] != "codex" {
		t.Errorf("agents %v", cfg.Agents)
	}
	if n := strings.Count(out, "Which agents?"); n != 1 {
		t.Errorf("asked for the agents %d times\n%s", n, out)
	}
}

// keelbox create on a config written by hand names the keys to change.
func TestCreateRefused(t *testing.T) {
	w := newParkWorld(t)
	w.api.Servers, w.api.Firewalls = nil, nil
	w.api.Refuse = map[string]hetznertest.Error{"create_server": forbidden}
	laptopKey(t, os.Getenv("HOME"))
	var out strings.Builder
	o := options{configFile: boxConfigPath("acme")}
	logf := func(f string, a ...any) {
		out.WriteString("keelbox: " + strings.TrimSpace(fmt.Sprintf(f, a...)) + "\n")
	}
	code := cloudCreate(context.Background(), w.cl, w.cfg, o, logf, &out, &out)
	text := flat(out.String())
	if code != 1 || len(w.api.Firewalls) != 0 || len(w.api.Servers) != 0 {
		t.Fatalf("exit %d, firewalls %d\n%s", code, len(w.api.Firewalls), text)
	}
	for _, want := range []string{
		"keelbox: deleted firewall keelbox-acme",
		forbiddenText,
		"To try elsewhere, set target.hetzner.location to ash, or target.hetzner.server_type to cax11 (or cpx02, cpx22) in ~/.config/keelbox/boxes/acme.yaml, then: keelbox create acme.",
		"A box on another cloud: keelbox up acme-2 --provider aws (or digitalocean, gcp)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q\n%s", want, text)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(text), "Or try again later: keelbox create acme") {
		t.Errorf("does not end with the command:\n%s", text)
	}
}

func TestResumeRefused(t *testing.T) {
	w := newParkWorld(t)
	if code, out := w.do(t, "park", options{yes: true}, nil); code != 0 {
		t.Fatalf("park: %d\n%s", code, out)
	}
	w.api.Refuse = map[string]hetznertest.Error{"create_server": forbidden}
	code, out := w.do(t, "resume", options{}, nil)
	text := flat(out)
	for _, want := range []string{
		"Hetzner refused to create acme's server again from its snapshot, cx23 in fsn1 (" + forbiddenText + ")",
		"acme is still parked: snapshot 1001 is kept, and nothing else changed. To try elsewhere, set target.hetzner.location to ash, or target.hetzner.server_type to cpx22 (or ccx13) in ~/.config/keelbox/boxes/acme.yaml, then: keelbox resume acme.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q\n%s", want, text)
		}
	}
	if code != 1 || !strings.HasSuffix(strings.TrimSpace(text), "Or try again later: keelbox resume acme") {
		t.Errorf("exit %d\n%s", code, text)
	}
	if len(w.api.Images) != 1 || len(w.api.Firewalls) != 1 {
		t.Errorf("snapshots %d, firewalls %d", len(w.api.Images), len(w.api.Firewalls))
	}
}

func TestResizeRefused(t *testing.T) {
	w := newParkWorld(t)
	w.api.Refuse = map[string]hetznertest.Error{"change_type": forbidden}
	code, out := w.do(t, "resize", options{size: "cpx22", yes: true, set: map[string]bool{"size": true}}, nil)
	text := flat(out)
	for _, want := range []string{
		"the size was not changed: Hetzner refused to change server 42 to cpx22 (" + forbiddenText + "). Hetzner may be limiting changes to servers for this account or in fsn1",
		"acme is still cx23 and runs again at 203.0.113.7. Try another size, or cpx22 again later: keelbox resize acme",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q\n%s", want, text)
		}
	}
	if code != 1 || w.api.Servers[0]["status"] != "running" {
		t.Errorf("exit %d, server %v", code, w.api.Servers[0]["status"])
	}
}
