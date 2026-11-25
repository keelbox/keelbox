package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/provider"
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

// refuse has the fake runner's first n creates refused as Hetzner
// refuses the box's server type in its location, with e; the creates
// after them succeed.
func refuse(t *testing.T, r *fakeRunner, n int, e hetznertest.Error) {
	t.Helper()
	types := fakeTypes(t)
	tries := 0
	r.onCreate = func(o options) {
		tries++
		r.createCode = 0
		if tries > n {
			o.hooks.server("192.0.2.44")
			return
		}
		r.createCode = 1
		cfg, err := config.Load(o.configFile)
		if err != nil {
			t.Fatal(err)
		}
		h := cfg.Target.Hetzner
		re := hetzner.NewRefusedError(&hetzner.APIError{Status: e.Status, Code: e.Code, Message: e.Message}, cfg.Box, h.Location, h.ServerType, types,
			"No server was created, and keelbox deleted the firewall it had made for it; your SSH key stays in the project for the next try")
		if !o.hooks.refused(hetznerRefusal(re).(*cloudRefusal)) {
			t.Error("up did not take the refusal")
		}
	}
}

// refusedBox is acme configured by up without a terminal, its first
// create refused with e; r creates the rest.
func refusedBox(t *testing.T, e hetznertest.Error, flags ...string) (*fakeRunner, string) {
	t.Helper()
	r, out, _ := refusedBoxOn(t, e, flags...)
	return r, out
}

// refusedBoxOn is refusedBox, with the fake Hetzner API it runs on.
func refusedBoxOn(t *testing.T, e hetznertest.Error, flags ...string) (*fakeRunner, string, *hetznertest.API) {
	t.Helper()
	home := isolate(t)
	key := laptopKey(t, home)
	api, store := fakeHetzner(t)
	_ = store.Set(context.Background(), "hetzner", goodToken)
	r := &fakeRunner{signed: true}
	refuse(t, r, 1, e)
	args := append([]string{"up", "acme", "--provider", "hetzner", "--ssh-key", key, "--yes"}, flags...)
	code, out := upWith(t, r, nil, args...)
	if os.Getenv("SHOW") != "" {
		t.Log(out)
	}
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	r.calls = nil
	return r, out, api
}

const moveCmds = "keelbox up acme --provider aws --location eu-central-1, keelbox up acme --provider digitalocean --location fra1 or keelbox up acme --provider gcp --location europe-west3-a"

// Without a terminal, up says what Hetzner said and prints a command for
// each way on; each of them works as printed.
func TestUpHetznerRefusedWithoutATerminal(t *testing.T) {
	r, out := refusedBox(t, forbidden, "--agents", "codex")
	text := flat(out)
	for _, want := range []string{
		"Hetzner refused to create acme's server, cx23 in fsn1 (" + forbiddenText + ")",
		"Hetzner may be limiting new servers for this account or in fsn1 (status.hetzner.com)",
		"No server was created, and keelbox deleted the firewall it had made for it",
		"Try another location (ash at EUR 5.49/month): keelbox up acme --location ash --yes; another size in fsn1 (cpx22 at EUR 7.49/month, ccx13 at EUR 14.49/month, cpx02 at EUR 5.99/month): keelbox up acme --size cpx22 --yes; " +
			"another cloud, with your other answers kept: " + moveCmds + "; or the same again later: keelbox up acme",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q\n%s", want, out)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(text), "or the same again later: keelbox up acme") || strings.Contains(text, "(or ") || strings.Contains(text, "by itself") {
		t.Errorf("does not end with the command, or names choices inside one:\n%s", out)
	}
	_, st := loadBox(t, "acme")
	if st.Stage != stageConfigured || st.Applying || st.Answers["agents"] != "codex" || st.Answers["provider"] != "hetzner" {
		t.Fatalf("state %+v", st)
	}
	// forget knows no server was created.
	if plan := forgetPlanFor("acme", mustLoad(t, "acme")); plan.stays != "" {
		t.Errorf("forget would say a server may exist: %+v", plan)
	}
	// A location Hetzner does not have is refused before anything changes.
	if code, out := upWith(t, r, nil, "up", "acme", "--location", "mars1"); code != 2 || !strings.Contains(out, `--location "mars1" is not a Hetzner location; use one of: fsn1, ash, for example (cx23 in ash at EUR 5.49/month): keelbox up acme --location ash --yes`) {
		t.Errorf("exit %d\n%s", code, out)
	}

	// Another location, as printed: it costs more, so up says so and
	// stops for --yes; --provider naming the same cloud changes nothing.
	code, out := upWith(t, r, nil, "up", "acme", "--provider", "hetzner", "--location", "ash")
	const more = "cx23 in ash costs about EUR 5.49 a month, against EUR 3.79 for cx23 in fsn1, billed by Hetzner until keelbox destroy acme"
	if code != 2 || !strings.Contains(out, more) || !strings.HasSuffix(strings.TrimSpace(flat(out)), "pass --yes, for example: keelbox up acme --location ash --yes") || len(r.calls) != 0 {
		t.Fatalf("exit %d, calls %v\n%s", code, r.calls, out)
	}
	if cfg := mustLoad(t, "acme"); cfg.Target.Hetzner.Location != "fsn1" {
		t.Fatalf("changed before the go-ahead: %+v", cfg.Target.Hetzner)
	}
	code, out = upWith(t, r, nil, "up", "acme", "--provider", "hetzner", "--location", "ash", "--yes")
	cfg, st := loadBox(t, "acme")
	if code != 0 || cfg.Target.Hetzner.Location != "ash" || cfg.Target.Hetzner.ServerType != "cx23" || !strings.HasPrefix(strings.Join(r.calls, "; "), "create acme") {
		t.Fatalf("exit %d, %+v, calls %v\n%s", code, cfg.Target.Hetzner, r.calls, out)
	}
	if strings.Contains(out, "change nothing") || strings.Contains(out, "changes nothing") || !strings.Contains(out, "set target.hetzner.location: ash in ~/.config/keelbox/boxes/acme.yaml") {
		t.Errorf("output:\n%s", out)
	}
	if st.Answers != nil {
		t.Errorf("answers kept past the create: %v", st.Answers)
	}
}

// Another size, as printed.
func TestUpRefusedSizeAsPrinted(t *testing.T) {
	r, _ := refusedBox(t, forbidden)
	code, out := upWith(t, r, nil, "up", "acme", "--size", "cpx22", "--yes")
	cfg, _ := loadBox(t, "acme")
	if code != 0 || cfg.Target.Hetzner.ServerType != "cpx22" || cfg.Target.Hetzner.Location != "fsn1" || !strings.HasPrefix(strings.Join(r.calls, "; "), "create acme") {
		t.Fatalf("exit %d, %+v, calls %v\n%s", code, cfg.Target.Hetzner, r.calls, out)
	}
}

// A project at its limit is refused in every location: another cloud
// comes first, and no other location or size is offered.
func TestUpRefusedAtTheProjectLimit(t *testing.T) {
	limit := hetznertest.Error{Status: 403, Code: "resource_limit_exceeded", Message: "server limit exceeded for this project"}
	_, out := refusedBox(t, limit)
	text := flat(out)
	want := "The project is at one of its Hetzner limits, such as its number of servers or cores, in every location. Ask Hetzner for a higher limit in the Hetzner Console, or delete a server you no longer need. " +
		"No server was created, and keelbox deleted the firewall it had made for it; your SSH key stays in the project for the next try. " +
		"The limit holds in every location. Try another cloud, with your other answers kept: " + moveCmds + "; or, once Hetzner has raised the limit or a server is deleted: keelbox up acme"
	if !strings.Contains(text, want) || strings.Contains(text, "--location ash") || strings.Contains(text, "may be limiting") {
		t.Errorf("output lacks %q\n%s", want, text)
	}
}

func mustLoad(t *testing.T, box string) *config.Config {
	t.Helper()
	cfg, _ := loadBox(t, box)
	return cfg
}

// After a refusal, --provider moves the box to another cloud with the
// other answers, as printed.
func TestUpRefusedBoxMovesToAnotherCloud(t *testing.T) {
	r, _ := refusedBox(t, forbidden, "--agents", "codex", "--tailscale=false")
	fakeDO(t)
	code, out := upWith(t, r, nil, "up", "acme", "--provider", "digitalocean", "--location", "fra1", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	cfg, st := loadBox(t, "acme")
	if cfg.Target.Provider != config.ProviderDigitalOcean || st.Via != viaDO || cfg.Target.DigitalOcean.Region != "fra1" {
		t.Fatalf("target %+v, state %+v", cfg.Target, st)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0] != "codex" || cfg.Tailscale() {
		t.Errorf("answers not carried: agents %v, tailscale %v", cfg.Agents, cfg.Tailscale())
	}
	if !strings.Contains(out, "moving acme from hetzner to digitalocean") || !strings.HasPrefix(strings.Join(r.calls, "; "), "create acme") {
		t.Errorf("calls %v\n%s", r.calls, out)
	}
}

// An error during a move ends with the command that carries it on.
func TestUpMoveErrorNamesTheMove(t *testing.T) {
	r, _ := refusedBox(t, forbidden)
	cli := fakeDO(t)
	cli.SignedIn = false
	code, out := upWith(t, r, nil, "up", "acme", "--provider", "digitalocean", "--location", "fra1", "--yes")
	if code != exitNeedsYou || !strings.HasSuffix(strings.TrimSpace(flat(out)), "save it: doctl auth init. Then: keelbox up acme --provider digitalocean --location fra1 --yes") || strings.Count(out, "Then") != 1 {
		t.Errorf("exit %d\n%s", code, out)
	}
	if cfg, _ := loadBox(t, "acme"); cfg.Target.Provider != config.ProviderHetzner {
		t.Errorf("moved half-way: %+v", cfg.Target)
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

// interactiveHetzner is the script of keelbox up on a terminal up to the
// go-ahead for a Hetzner box, with Codex as the agent.
func interactiveHetzner(more ...string) *string {
	return script(append([]string{
		"3",       // where: a new cloud server
		"1",       // which cloud: Hetzner
		"acme",    // name
		goodToken, // token (hidden)
		"",        // location: default fsn1 (or the flag's)
		"",        // size: default cx23
		"3",       // agents: Codex
		"",        // GitHub CLI: yes
		"",        // Tailscale: yes
		"",        // go ahead
	}, more...)...)
}

// On a terminal, up asks how to go on and carries on in the same run,
// with the price in each location.
func TestUpHetznerRefusedAsksForAnotherLocation(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	fakeHetzner(t)
	r := &fakeRunner{signed: true}
	refuse(t, r, 1, forbidden)
	code, out := upWith(t, r, interactiveHetzner("1", "1"), "up") // another location: ash
	if os.Getenv("SHOW") != "" {
		t.Log(out)
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{forbiddenText, "How should keelbox up go on?", "Another location", "cx23 can be ordered in ash",
		"Another size in fsn1", "cpx22, ccx13, cpx02 can be ordered there", "Another cloud", "AWS (Lightsail), DigitalOcean or Google Cloud; your other answers are kept",
		"Stop here", "Which location?", "cx23 at EUR 5.49/month (EUR 3.79/month in fsn1)"} {
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

// Another size offers the nearest one first.
func TestUpHetznerRefusedAnotherSize(t *testing.T) {
	r, _ := refusedBox(t, forbidden)
	refuse(t, r, 1, forbidden)
	code, out := upWith(t, r, script("2", ""), "up", "acme") // another size: the one offered first
	if code != 0 || !strings.Contains(out, "Which size in fsn1?") || !strings.Contains(out, "[3]") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if cfg, _ := loadBox(t, "acme"); cfg.Target.Hetzner.ServerType != "cpx22" {
		t.Errorf("size %s", cfg.Target.Hetzner.ServerType)
	}
}

// A location where the size is not sold asks for a size there, offering
// the nearest of the same architecture.
func TestUpHetznerRefusedAsksForASizeAtTheNewLocation(t *testing.T) {
	r, _ := refusedBox(t, forbidden, "--size", "cpx22")
	refuse(t, r, 1, forbidden)
	code, out := upWith(t, r, script("1", "1", ""), "up", "acme") // another location: ash, then its first offer
	if code != 0 || !strings.Contains(out, "cpx22 is not sold there; you pick another size") || !strings.Contains(out, "cpx22 cannot be ordered in ash now") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if cfg, _ := loadBox(t, "acme"); cfg.Target.Hetzner.Location != "ash" || cfg.Target.Hetzner.ServerType != "cx23" {
		t.Errorf("target %+v", cfg.Target.Hetzner)
	}
}

// Stop here ends with exit 3 and the command that tries again.
func TestUpHetznerRefusedStopHere(t *testing.T) {
	r, _ := refusedBox(t, forbidden)
	refuse(t, r, 1, forbidden)
	code, out := upWith(t, r, script("4"), "up", "acme")
	if code != exitNeedsYou || !strings.Contains(out, "stopped; nothing was changed. Continue: keelbox up acme") {
		t.Errorf("exit %d\n%s", code, out)
	}
	if _, st := loadBox(t, "acme"); st.Applying || st.Stage != stageConfigured {
		t.Errorf("state %+v", st)
	}
}

// cancelAtEnd gives its text, then presses Ctrl-C at the next question.
type cancelAtEnd struct {
	r      *strings.Reader
	cancel func()
}

func (c *cancelAtEnd) Read(b []byte) (int, error) {
	if c.r.Len() > 0 {
		return c.r.Read(b)
	}
	c.cancel()
	select {}
}

// Ctrl-C at the question after a refusal changes nothing.
func TestUpHetznerRefusedCtrlCAtTheQuestion(t *testing.T) {
	r, _ := refusedBox(t, forbidden)
	refuse(t, r, 1, forbidden)
	o, pos, err := parse("up", []string{"acme"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	code := cmdUp(ctx, o, pos, prompt.New(&cancelAtEnd{strings.NewReader(""), cancel}, &out, true), r, &out, &out)
	if code != exitInterrupted || !strings.Contains(out.String(), "How should keelbox up go on?") || !strings.Contains(out.String(), "stopped; nothing was changed. Continue: keelbox up acme") {
		t.Errorf("exit %d\n%s", code, out.String())
	}
	if cfg, st := loadBox(t, "acme"); st.Applying || cfg.Target.Hetzner.Location != "fsn1" {
		t.Errorf("state %+v", st)
	}
}

// Another cloud from the question drops the refused cloud's flags, asks
// only the new cloud's questions and the go-ahead again.
func TestUpHetznerRefusedMovesToAnotherCloudInTheSameRun(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	fakeHetzner(t)
	fakeDO(t)
	r := &fakeRunner{signed: true}
	refuse(t, r, 1, forbidden)
	in := script(
		"3",       // where: a new cloud server
		"1",       // which cloud: Hetzner
		"acme",    // name
		goodToken, // token (hidden); --location and --size answer the next two
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
	code, out := upWith(t, r, in, "up", "--location", "fsn1", "--size", "cx23")
	if os.Getenv("SHOW") != "" {
		t.Log(out)
	}
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	cfg, st := loadBox(t, "acme")
	if cfg.Target.Provider != config.ProviderDigitalOcean || st.Via != viaDO || st.Stage != stageReady || cfg.Target.DigitalOcean.Region != "fra1" {
		t.Fatalf("target %+v, state %+v", cfg.Target, st)
	}
	if len(cfg.Agents) != 1 || cfg.Agents[0] != "codex" {
		t.Errorf("agents %v", cfg.Agents)
	}
	if n := strings.Count(out, "Which agents?"); n != 1 {
		t.Errorf("asked for the agents %d times\n%s", n, out)
	}
	if strings.Count(out, "Where the box runs") != 1 || strings.Count(out, "set up a box for AI coding agents") != 1 {
		t.Errorf("the start of up shown again mid-run\n%s", out)
	}
}

// A no at the go-ahead of a move leaves the box where it was, and says so
// once.
func TestUpHetznerRefusedMoveDeclined(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	fakeHetzner(t)
	fakeDO(t)
	r := &fakeRunner{signed: true}
	refuse(t, r, 1, forbidden)
	_, out := upWith(t, r, interactiveHetzner("3", "2", "", "", "", "n"), "up")
	if !strings.Contains(out, "nothing changed; acme stays on hetzner, with no server. Move it later: keelbox up acme --provider digitalocean") ||
		strings.Contains(strings.ToLower(out), "your answers are kept") || strings.Count(out, "stays on hetzner") != 1 {
		t.Errorf("output:\n%s", out)
	}
	if cfg, _ := loadBox(t, "acme"); cfg.Target.Provider != config.ProviderHetzner {
		t.Errorf("target %+v", cfg.Target)
	}
	if _, ok := loadDraft(); ok {
		t.Error("a draft is left that nothing offers")
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
		"Other locations for cx23 now: ash at EUR 5.49/month. Other sizes in fsn1 now: cpx22 at EUR 7.49/month, ccx13 at EUR 14.49/month, cpx02 at EUR 5.99/month. " +
			"To try one, set target.hetzner.location: ash or target.hetzner.server_type: cpx22 in ~/.config/keelbox/boxes/acme.yaml, then: keelbox create acme. " +
			"A box on another cloud: keelbox up acme-2 --provider aws --location eu-central-1. Or try again later: keelbox create acme",
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
		"acme is still parked: snapshot 1001 is kept, and nothing else changed. Other locations for cx23 now: ash at EUR 5.49/month. Other sizes in fsn1 now: cpx22 at EUR 7.49/month, ccx13 at EUR 14.49/month. To try one, set target.hetzner.location: ash or target.hetzner.server_type: cpx22 in ~/.config/keelbox/boxes/acme.yaml, then: keelbox resume acme.",
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

// keelbox create run by hand on a box up configured: a refusal leaves it
// free to move; from an order Hetzner may have taken on, up no longer
// moves it.
func TestCreateByHandOnABoxUpConfigured(t *testing.T) {
	r, _, api := refusedBoxOn(t, forbidden, "--agents", "codex")
	create := func() (int, string) {
		cfg := mustLoad(t, "acme")
		var out strings.Builder
		logf := func(f string, a ...any) { out.WriteString("keelbox: " + fmt.Sprintf(f, a...) + "\n") }
		code := cloudCreate(context.Background(), &hetznerCloud{cl: newHetznerClient(goodToken), cfg: cfg}, cfg, options{}, logf, &out, &out)
		return code, flat(out.String())
	}
	api.Refuse = map[string]hetznertest.Error{"create_server": forbidden}
	code, out := create()
	_, st := loadBox(t, "acme")
	if code != 1 || st.Applying || !notCreated(mustLoad(t, "acme")) || st.Answers["agents"] != "codex" || !strings.Contains(out, "Try another location (ash at EUR 5.49/month): keelbox up acme --location ash --yes") {
		t.Fatalf("exit %d, state %+v\n%s", code, st, out)
	}
	api.Refuse, api.Fail = nil, map[string]bool{"create_server": true}
	if code, out := create(); code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if _, st := loadBox(t, "acme"); !st.Applying {
		t.Fatalf("state %+v", st)
	}
	fakeDO(t)
	code, out = upWith(t, r, nil, "up", "acme", "--provider", "digitalocean", "--location", "fra1", "--yes")
	if cfg := mustLoad(t, "acme"); cfg.Target.Provider != config.ProviderHetzner || !strings.Contains(out, "--provider, --location change nothing") {
		t.Errorf("exit %d, target %+v\n%s", code, cfg.Target, out)
	}
}

// A server keelbox created for the box, which up's progress does not
// know of, stops a move and a change of location: up records it and
// carries on with it there.
func TestUpRefusedBoxWithAServerAfterAll(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		then string
	}{
		{"move", []string{"--provider", "digitalocean", "--location", "fra1", "--yes"},
			"keelbox destroy acme, then keelbox forget acme, then: keelbox up acme --provider digitalocean --location fra1 --yes"},
		{"location", []string{"--location", "ash"},
			"keelbox destroy acme, then set target.hetzner.location: ash in ~/.config/keelbox/boxes/acme.yaml, then: keelbox up acme"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, _, api := refusedBoxOn(t, forbidden)
			fakeDO(t)
			api.Servers = []map[string]any{hetznertest.Server(42, "acme", "203.0.113.7")}
			code, out := upWith(t, r, nil, append([]string{"up", "acme"}, c.args...)...)
			want := "acme has a server on hetzner after all, server acme (id 42, 203.0.113.7), which keelbox created; a box has one server, so nothing changed. Set it up there: keelbox up acme. Or delete it first: " + c.then
			if code != 2 || !strings.HasSuffix(strings.TrimSpace(flat(out)), want) || len(r.calls) != 0 {
				t.Fatalf("exit %d, calls %v\n%s", code, r.calls, out)
			}
			cfg, st := loadBox(t, "acme")
			if cfg.Target.Provider != config.ProviderHetzner || cfg.Target.Hetzner.Location != "fsn1" || st.Stage != stageServer || st.Address != "203.0.113.7" || !st.Applying {
				t.Errorf("target %+v, state %+v", cfg.Target, st)
			}
		})
	}
}

// A size Hetzner does not have is refused before anything changes, and
// the box can still move.
func TestUpRefusedMistypedSize(t *testing.T) {
	r, _ := refusedBox(t, forbidden)
	code, out := upWith(t, r, nil, "up", "acme", "--size", "cpx222")
	if code != 2 || !strings.Contains(flat(out), `--size "cpx222" is not a Hetzner server type; use one that can be ordered in fsn1 now: cx23, cax11, cpx02, cpx22, ccx13, for example (cpx22 in fsn1 at EUR 7.49/month): keelbox up acme --size cpx22 --yes`) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if cfg := mustLoad(t, "acme"); cfg.Target.Hetzner.ServerType != "cx23" || !notCreated(cfg) || len(r.calls) != 0 {
		t.Errorf("target %+v, calls %v", cfg.Target.Hetzner, r.calls)
	}
}

// A move as printed stops at the go-ahead, which shows the new price and
// the command with --yes; that command moves the box.
func TestUpRefusedMoveAsksTheGoAhead(t *testing.T) {
	r, _ := refusedBox(t, forbidden)
	fakeDO(t)
	code, out := upWith(t, r, nil, "up", "acme", "--provider", "digitalocean", "--location", "fra1")
	text := flat(out)
	if code != 2 || !strings.Contains(text, "cost") || !strings.HasSuffix(strings.TrimSpace(text), "pass --yes, for example: keelbox up acme --provider digitalocean --location fra1 --yes") || len(r.calls) != 0 {
		t.Fatalf("exit %d, calls %v\n%s", code, r.calls, out)
	}
	if cfg := mustLoad(t, "acme"); cfg.Target.Provider != config.ProviderHetzner {
		t.Fatalf("moved before the go-ahead: %+v", cfg.Target)
	}
	if code, out := upWith(t, r, nil, "up", "acme", "--provider", "digitalocean", "--location", "fra1", "--yes"); code != 0 || mustLoad(t, "acme").Target.Provider != config.ProviderDigitalOcean {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// Moving to Google Cloud, where gcloud has no project set, names the flag
// in the move's own command.
func TestUpRefusedMoveToGCPWithoutAProject(t *testing.T) {
	r, _ := refusedBox(t, forbidden)
	fakeGCP(t).Project = ""
	code, out := upWith(t, r, nil, "up", "acme", "--provider", "gcp", "--location", "europe-west3-a")
	if code != 2 || !strings.HasSuffix(strings.TrimSpace(flat(out)), "pass --gcp-project, for example: keelbox up acme --provider gcp --gcp-project my-project --location europe-west3-a") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// keelbox create's other-cloud suggestion runs as printed: up asks for
// the go-ahead with the price, and the command it prints creates the box.
func TestCreateRefusedOtherCloudAsPrinted(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	fakeAWS(t)
	r := &fakeRunner{signed: true}
	code, out := upWith(t, r, nil, "up", "acme-2", "--provider", "aws", "--location", "eu-central-1")
	const yes = "pass --yes, for example: keelbox up acme-2 --provider aws --location eu-central-1 --yes"
	if code != 2 || !strings.Contains(flat(out), "cost") || !strings.HasSuffix(strings.TrimSpace(flat(out)), yes) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if code, out := upWith(t, r, nil, strings.Fields(strings.TrimPrefix(yes, "pass --yes, for example: keelbox "))...); code != 0 || mustLoad(t, "acme-2").Target.AWS.Region != "eu-central-1" {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// --yes was the go-ahead for Hetzner's price: moving to another cloud in
// the same run asks again.
func TestUpRefusedMoveInTheSameRunAsksTheGoAheadAgain(t *testing.T) {
	home := isolate(t)
	laptopKey(t, home)
	fakeHetzner(t)
	fakeDO(t)
	r := &fakeRunner{signed: true}
	refuse(t, r, 1, forbidden)
	in := script(
		"3", "1", "acme", goodToken, // a Hetzner box; --location and --size answer the next two
		"3", "", "", // Codex, GitHub CLI, Tailscale; --yes is the go-ahead
		"3", "2", "", "", "", // refused: DigitalOcean, its context, region and size
		"n", // the go-ahead for DigitalOcean: no
	)
	code, out := upWith(t, r, in, "up", "--location", "fsn1", "--size", "cx23", "--yes")
	if code != 0 || !strings.Contains(out, "nothing changed; acme stays on hetzner, with no server. Move it later: keelbox up acme --provider digitalocean") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if cfg := mustLoad(t, "acme"); cfg.Target.Provider != config.ProviderHetzner || len(r.calls) != 1 {
		t.Errorf("target %+v, calls %v", cfg.Target, r.calls)
	}
}

// On a terminal, a new price is asked about before the config changes;
// the same price carries on without a question.
func TestUpRefusedNewPlaceAsksAboutThePrice(t *testing.T) {
	t.Run("declined", func(t *testing.T) {
		r, _ := refusedBox(t, forbidden)
		code, out := upWith(t, r, script("n"), "up", "acme", "--location", "ash", "--size", "cpx02")
		if code != 0 || !strings.Contains(out, "Create the server as cpx02 in ash (about EUR 6.49 a month until you destroy it)?") ||
			!strings.Contains(out, "nothing changed; acme stays cx23 in fsn1, with no server. To create it there later: keelbox up acme --location ash --size cpx02 --yes") || len(r.calls) != 0 {
			t.Fatalf("exit %d, calls %v\n%s", code, r.calls, out)
		}
		if cfg := mustLoad(t, "acme"); cfg.Target.Hetzner.Location != "fsn1" || cfg.Target.Hetzner.ServerType != "cx23" || !notCreated(cfg) {
			t.Errorf("target %+v", cfg.Target.Hetzner)
		}
	})
	t.Run("accepted", func(t *testing.T) {
		r, _ := refusedBox(t, forbidden)
		code, out := upWith(t, r, script(""), "up", "acme", "--location", "ash", "--size", "cpx02")
		if cfg := mustLoad(t, "acme"); code != 0 || cfg.Target.Hetzner.Location != "ash" || cfg.Target.Hetzner.ServerType != "cpx02" || !strings.HasPrefix(strings.Join(r.calls, "; "), "create acme") {
			t.Fatalf("exit %d, target %+v\n%s", code, cfg.Target.Hetzner, out)
		}
	})
	t.Run("same price", func(t *testing.T) {
		r, _ := refusedBox(t, forbidden)
		code, out := upWith(t, r, nil, "up", "acme", "--size", "cax11")
		if cfg := mustLoad(t, "acme"); code != 0 || cfg.Target.Hetzner.ServerType != "cax11" || !strings.Contains(out, "cax11 in fsn1 costs about EUR 3.79 a month, as before") {
			t.Fatalf("exit %d, target %+v\n%s", code, cfg.Target.Hetzner, out)
		}
	})
}

// A failed lookup in Hetzner's lists changes nothing and says how to try
// again.
func TestUpRefusedPlaceLookupFails(t *testing.T) {
	r, _, api := refusedBoxOn(t, forbidden)
	api.Fail = map[string]bool{"list_server_types": true}
	code, out := upWith(t, r, nil, "up", "acme", "--size", "cpx22")
	if code != 2 || !strings.HasPrefix(strings.TrimSpace(flat(out)), "keelbox up: nothing changed; hetzner api: 500") || !strings.HasSuffix(strings.TrimSpace(flat(out)), "Try again: keelbox up acme --size cpx22") {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// keelbox create orders nothing when it cannot note the order in up's
// progress: the box would otherwise look movable with a server.
func TestCreateByHandStopsWhenTheMarkCannotBeWritten(t *testing.T) {
	_, _, api := refusedBoxOn(t, forbidden)
	if err := os.Chmod(upStatePath("acme"), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(upStatePath("acme"), 0o600) })
	cfg := mustLoad(t, "acme")
	var out strings.Builder
	code := cloudCreate(context.Background(), &hetznerCloud{cl: newHetznerClient(goodToken), cfg: cfg}, cfg, options{}, t.Logf, &out, &out)
	text := flat(out.String())
	if code != 1 || !strings.Contains(text, "nothing was ordered: keelbox could not note in ~/.config/keelbox/boxes/acme.up.json that acme's server is being created") ||
		!strings.HasSuffix(strings.TrimSpace(text), "Make it writable, then: keelbox create acme") {
		t.Fatalf("exit %d\n%s", code, text)
	}
	for _, c := range api.Calls() {
		if c == "POST /servers" {
			t.Errorf("ordered: %v", api.Calls())
		}
	}
}

// A type Hetzner has deprecated there but still sells is created as any
// other: its price is shown and asked about.
func TestUpRefusedDeprecatedSizeIsPriced(t *testing.T) {
	r, _ := refusedBox(t, forbidden)
	code, out := upWith(t, r, nil, "up", "acme", "--size", "cx33")
	text := flat(out)
	if code != 2 || !strings.Contains(text, "cx33 in fsn1 costs about EUR 6.49 a month, against EUR 3.79 for cx23 in fsn1") ||
		!strings.HasSuffix(strings.TrimSpace(text), "pass --yes, for example: keelbox up acme --size cx33 --yes") || len(r.calls) != 0 {
		t.Fatalf("exit %d, calls %v\n%s", code, r.calls, out)
	}
	if cfg := mustLoad(t, "acme"); cfg.Target.Hetzner.ServerType != "cx23" {
		t.Errorf("size %s", cfg.Target.Hetzner.ServerType)
	}
}

// hetznerType is a type sold in each of prices' locations at its monthly
// price (gross, as Hetzner's list gives it).
func hetznerType(name string, prices map[string]string) hetzner.ServerType {
	t := hetzner.ServerType{Name: name, Architecture: "x86", Cores: 2, Memory: 4}
	for l, m := range prices {
		t.Prices = append(t.Prices, hetzner.Price{Location: l, PriceHourly: hetzner.Amount{Gross: "0.0100"}, PriceMonthly: hetzner.Amount{Gross: m}})
		t.Locations = append(t.Locations, hetzner.TypeLocation{Name: l, Available: true})
	}
	return t
}

// A type Hetzner's list gives no monthly price for is never created
// without the go-ahead.
func TestNewPriceWithoutAPrice(t *testing.T) {
	isolate(t)
	types := []hetzner.ServerType{hetznerType("cx23", map[string]string{"fsn1": "3.79"}), hetznerType("odd", map[string]string{"fsn1": ""})}
	var out bytes.Buffer
	w := &wizard{p: prompt.New(strings.NewReader(""), &out, false), out: &out, errOut: &out, box: "acme", o: options{set: map[string]bool{"size": true}, size: "odd"}}
	ok, err := w.newPrice(types, "cx23", "fsn1", "odd", "fsn1")
	if ok || err == nil || !strings.Contains(err.Error(), "the go-ahead for odd in fsn1 (no price in Hetzner's list)") || !strings.Contains(out.String(), "odd in fsn1 has no price in Hetzner's list") {
		t.Fatalf("%v %v\n%s", ok, err, out.String())
	}
}

// Both place flags wrong: each is named, and the example replaces both
// with ones that work together.
func TestUpRefusedBothPlaceFlagsWrong(t *testing.T) {
	r, _ := refusedBox(t, forbidden)
	code, out := upWith(t, r, nil, "up", "acme", "--location", "mars1", "--size", "cpx222")
	want := `--location "mars1" is not a Hetzner location; use one of: fsn1, ash; --size "cpx222" is not a Hetzner server type; use one that can be ordered in ash now: cx23, cpx02, for example (cpx02 in ash at EUR 6.49/month): keelbox up acme --location ash --size cpx02 --yes`
	if code != 2 || !strings.HasSuffix(strings.TrimSpace(flat(out)), want) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	// The example, as printed, creates the box there.
	code, out = upWith(t, r, nil, "up", "acme", "--location", "ash", "--size", "cpx02", "--yes")
	if cfg := mustLoad(t, "acme"); code != 0 || cfg.Target.Hetzner.Location != "ash" || cfg.Target.Hetzner.ServerType != "cpx02" {
		t.Fatalf("exit %d\n%s", code, out)
	}
}

// With nothing else to order where the box is, the example names a real
// location that has a type; with nothing anywhere, it names none.
func TestCheckPlaceWithNothingElseThere(t *testing.T) {
	w := &wizard{box: "acme", o: options{set: map[string]bool{"size": true}, size: "cpx222"}}
	h := config.Hetzner{Location: "fsn1", ServerType: "cx23"}
	types := []hetzner.ServerType{hetznerType("cx23", map[string]string{"fsn1": "3.79"}), hetznerType("cpx02", map[string]string{"ash": "6.49"})}
	_, _, err := w.checkPlace(types, nil, h)
	if err == nil || !strings.HasSuffix(err.Error(), "use one that can be ordered in fsn1 now: cx23, for example (cpx02 in ash at EUR 6.49/month): keelbox up acme --location ash --size cpx02 --yes") {
		t.Errorf("got %v", err)
	}
	_, _, err = w.checkPlace(types[:1], nil, h)
	if err == nil || !strings.HasSuffix(err.Error(), "Hetzner lists nothing else to order now; try again later: keelbox up acme") || strings.Contains(err.Error(), "LOCATION") {
		t.Errorf("got %v", err)
	}
}

// While another keelbox command holds the box, neither a move, nor a new
// place, nor a create by hand goes ahead.
func TestRefusedBoxLocked(t *testing.T) {
	r, _, api := refusedBoxOn(t, forbidden)
	fakeDO(t)
	unlock, err := lockBox("acme", "keelbox up acme")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	const held = "another keelbox command is working on acme; nothing changed. Wait for it to finish, then: "
	for _, args := range [][]string{
		{"up", "acme", "--provider", "digitalocean", "--location", "fra1", "--yes"},
		{"up", "acme", "--location", "ash", "--yes"},
	} {
		code, out := upWith(t, r, nil, args...)
		// Each names its own command, to run once the other is done.
		if code != 2 || !strings.HasSuffix(strings.TrimSpace(flat(out)), held+"keelbox "+strings.Join(args, " ")) || len(r.calls) != 0 {
			t.Errorf("%v: exit %d\n%s", args, code, out)
		}
	}
	cfg := mustLoad(t, "acme")
	var out strings.Builder
	if code := cloudCreate(context.Background(), &hetznerCloud{cl: newHetznerClient(goodToken), cfg: cfg}, cfg, options{}, t.Logf, &out, &out); code != 1 ||
		!strings.HasSuffix(strings.TrimSpace(flat(out.String())), held+"keelbox create acme") {
		t.Errorf("exit %d\n%s", code, out.String())
	}
	for _, c := range api.Calls() {
		if strings.HasPrefix(c, "POST /servers") || strings.HasPrefix(c, "POST /firewalls") {
			t.Errorf("sent %s", c)
		}
	}
	if cfg := mustLoad(t, "acme"); cfg.Target.Provider != config.ProviderHetzner || cfg.Target.Hetzner.Location != "fsn1" {
		t.Errorf("target %+v", cfg.Target)
	}
}

// A create by hand that loaded the box before a move finished orders
// nothing.
func TestCreateByHandAfterAMove(t *testing.T) {
	_, _, api := refusedBoxOn(t, forbidden)
	cfg := mustLoad(t, "acme")
	s := config.StarterFor("acme", "digitalocean")
	s.Keys = []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKey you@laptop"}
	register(t, s)
	var out strings.Builder
	code := cloudCreate(context.Background(), &hetznerCloud{cl: newHetznerClient(goodToken), cfg: cfg}, cfg, options{}, t.Logf, &out, &out)
	if code != 1 || !strings.Contains(flat(out.String()), "acme moved to digitalocean while this command was starting; nothing was ordered. Carry on with it there: keelbox up acme") {
		t.Fatalf("exit %d\n%s", code, out.String())
	}
	for _, c := range api.Calls() {
		if strings.HasPrefix(c, "POST /servers") || strings.HasPrefix(c, "POST /firewalls") {
			t.Errorf("sent %s", c)
		}
	}
}

// createdCloud is a cloud whose create makes the server at once.
type createdCloud struct {
	*hetznerCloud
	ip string
}

func (c *createdCloud) create(context.Context, provider.HostKeyPair, func(string, ...any)) (*cloudServer, error) {
	return &cloudServer{ref: "server 9", about: "server acme (id 9)", ip: c.ip}, nil
}

// keelbox create notes the server it made for a box up configured, as
// soon as it exists, and keelbox up carries on with it.
func TestCreateByHandRecordsTheServerItMade(t *testing.T) {
	r, _ := refusedBox(t, forbidden)
	// An ssh that answers at once and with nothing: create gets past the
	// server's creation, then fails at the host keys.
	bin := t.TempDir()
	if err := os.WriteFile(bin+"/ssh", []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil { //nolint:gosec // G306: a test's fake ssh must run
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	cfg := mustLoad(t, "acme")
	var out strings.Builder
	cl := &createdCloud{&hetznerCloud{cl: newHetznerClient(goodToken), cfg: cfg}, "203.0.113.9"}
	if code := cloudCreate(context.Background(), cl, cfg, options{}, t.Logf, &out, &out); code == 0 {
		t.Fatalf("the fake ssh set the box up\n%s", out.String())
	}
	_, st := loadBox(t, "acme")
	if st.Stage != stageServer || st.Address != "203.0.113.9" || !st.Applying || st.Answers != nil || notCreated(cfg) {
		t.Fatalf("state %+v\n%s", st, out.String())
	}
	code, out2 := upWith(t, r, nil, "up", "acme")
	if code != 0 || strings.Contains(strings.Join(r.calls, "; "), "create acme") || !strings.Contains(strings.Join(r.calls, "; "), "apply acme") {
		t.Fatalf("exit %d, calls %v\n%s", code, r.calls, out2)
	}
}

// Any change to the target since keelbox create read it stops the order;
// on a box up does not own, the fix is create again.
func TestCreateByHandAfterAChangeOfPlace(t *testing.T) {
	w := newParkWorld(t)
	w.api.Servers, w.api.Firewalls = nil, nil
	// Changed on disk, by another command: w.cfg is what create read.
	if _, err := setConfigKey(mustLoad(t, "acme"), boxConfigPath("acme"), "target.hetzner.location", "ash"); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	code := cloudCreate(context.Background(), w.cl, w.cfg, options{}, t.Logf, &out, &out)
	if code != 1 || !strings.Contains(flat(out.String()), "acme changed to cx23 in ash while this command was starting; nothing was ordered. Carry on with it: keelbox create acme") {
		t.Fatalf("exit %d\n%s", code, out.String())
	}
	if err := os.WriteFile(boxConfigPath("acme"), []byte("not: [yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := cloudCreate(context.Background(), w.cl, w.cfg, options{}, t.Logf, &out, &out); code != 1 || !strings.Contains(flat(out.String()), "nothing was ordered: reading ~/.config/keelbox/boxes/acme.yaml again failed") {
		t.Fatalf("exit %d\n%s", code, out.String())
	}
	for _, c := range w.api.Calls() {
		if strings.HasPrefix(c, "POST /servers") {
			t.Errorf("sent %s", c)
		}
	}
}

// A keelbox create that finished just before up took the box: up changes
// nothing and carries on with the box as it is.
func TestUpRefusedBoxCreatedJustBefore(t *testing.T) {
	for _, args := range [][]string{
		{"up", "acme", "--provider", "digitalocean", "--location", "fra1", "--yes"},
		{"up", "acme", "--location", "ash", "--yes"},
	} {
		t.Run(args[2], func(t *testing.T) {
			r, _ := refusedBox(t, forbidden)
			fakeDO(t)
			lockedHook = func(box string) { _ = recordServer(mustLoad(t, box), "203.0.113.9") }
			defer func() { lockedHook = nil }()
			code, out := upWith(t, r, nil, args...)
			if code != 2 || !strings.HasSuffix(strings.TrimSpace(flat(out)), "another keelbox command changed acme just now; nothing changed. Carry on with it as it is: keelbox up acme") || len(r.calls) != 0 {
				t.Fatalf("exit %d, calls %v\n%s", code, r.calls, out)
			}
			if cfg := mustLoad(t, "acme"); cfg.Target.Provider != config.ProviderHetzner || cfg.Target.Hetzner.Location != "fsn1" {
				t.Errorf("target %+v", cfg.Target)
			}
		})
	}
}

// An alternative with no price: its command has no --yes and stops at the
// go-ahead, and picking it on a terminal asks for one.
func TestRefusalAlternativeWithoutAPrice(t *testing.T) {
	r := &cloudRefusal{err: errors.New("refused"), location: "fsn1", size: "cx23", locationKey: "target.hetzner.location", sizeKey: "target.hetzner.server_type",
		otherSizes: []string{"odd"}, otherLocations: []string{"ash"},
		price: func(size, loc string) string {
			if size == "cx23" && loc == "ash" {
				return "EUR 5.49/month"
			}
			return ""
		},
		sizesAt: func(string) ([]prompt.Choice, int) {
			return []prompt.Choice{{Key: "cx23", Label: "cx23"}, {Key: "odd", Label: "odd"}}, 0
		},
	}
	fix := r.upFix("acme", config.ProviderHetzner, "")
	if !strings.Contains(fix, "keelbox up acme --location ash --yes;") || !strings.Contains(fix, "another size in fsn1 (odd): keelbox up acme --size odd;") {
		t.Errorf("got %s", fix)
	}
	refusedBox(t, forbidden)
	var out bytes.Buffer
	w := &wizard{p: prompt.New(strings.NewReader(*script("1", "", "n")), &out, true), out: &out, errOut: &out, box: "acme"}
	if err := w.afterRefusal(context.Background(), mustLoad(t, "acme"), r); !errors.Is(err, errStopHere) || !strings.Contains(out.String(), "Hetzner's list gives no price for odd in fsn1. Create the server as odd there anyway?") {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if cfg := mustLoad(t, "acme"); cfg.Target.Hetzner.ServerType != "cx23" {
		t.Errorf("size %s", cfg.Target.Hetzner.ServerType)
	}
}
