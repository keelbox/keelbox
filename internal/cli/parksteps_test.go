package cli

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/ui"
)

// lockedBuffer is a terminal a step view's spinner and a test may both
// touch.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// shown is what a terminal shows after out, without styling.
func shown(out string) string { return ui.Strip(ui.Screen(out)) }

// termStyle is a capable terminal of that width, without colour.
func termStyle(width int) ui.Style { return ui.Style{Live: true, UTF8: true, Width: width} }

// On a terminal the steps are a list: a tick, what was done and the time
// it took at the right edge; what the cloud's side said stays under its
// step; a step that failed has a cross. Nothing is written to the log.
func TestStepViewListsTheSteps(t *testing.T) {
	const warning = "warning: the address keelbox-acme was not released (quota); Google Cloud bills it while nothing uses it. " +
		"keelbox resume acme uses it again; or release it: gcloud compute addresses delete keelbox-acme --region europe-west3 --project acme-prod"
	for _, width := range []int{60, 80, 120} {
		var out lockedBuffer
		logged := 0
		v := newStepView(&out, options{style: termStyle(width)}, func(string, ...any) { logged++ })
		clock := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
		v.now = func() time.Time { return clock }

		v.heading("Resuming acme", "from snapshot 1001, parked since 2026-11-14")
		v.begin("Creating its server from snapshot 1001", "")
		v.say("never on a terminal")
		v.note("creating acme from snapshot %d: cx23 in fsn1 (EUR 0.0060/h gross)", 1001)
		// While it runs: the step, and under it the last thing noted.
		if live := shown(out.String()); !strings.Contains(live, "Creating its server from snapshot 1001") || !strings.Contains(live, "⎿ creating acme from snapshot 1001") || strings.Contains(live, "✓") {
			t.Errorf("%d columns, while the step runs:\n%s", width, live)
		}
		clock = clock.Add(21 * time.Second)
		v.finished("Created server 1002 at 198.51.100.3", "")
		v.begin("Deleting snapshot keelbox-acme-parked-20261115T234621Z", "the apply passed, so the server holds the disk")
		if live := shown(out.String()); !strings.Contains(live, "⎿ the apply passed, so the server holds the disk") {
			t.Errorf("%d columns: no hint under a step that has said nothing:\n%s", width, live)
		}
		v.note("deleted snapshot keelbox-acme-parked-20261115T234621Z")
		v.note("released static IP keelbox-acme (203.0.113.7)")
		v.note("%s", warning)
		clock = clock.Add(83 * time.Second)
		v.finished("", "Deleted the snapshot")
		v.begin("Waiting for SSH at 198.51.100.3", "")
		clock = clock.Add(5 * time.Second)
		v.failed()
		v.did("set %s: %s in %s", "target.hetzner.server_type", "cpx22", "~/.config/keelbox/boxes/acme.yaml")
		v.note("the cloud firewall admits SSH from the internet (keelbox-acme: in tcp 22)")
		v.close()

		got := shown(out.String())
		edge := min(width-1, stepWidth)
		for _, l := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
			if w := ui.Width(l); w > edge {
				t.Errorf("%d columns: a line is %d wide: %q", width, w, l)
			}
			// A time is at the right edge, whatever the step is called.
			if regexp.MustCompile(`\d+s$`).MatchString(l) && ui.Width(l) != edge {
				t.Errorf("%d columns: the time is not at the right edge (%d): %q", width, edge, l)
			}
		}
		wantAll(t, fmt.Sprintf("%d columns", width), got, "  Resuming acme from snapshot 1001, parked since", "  ✓ Created server 1002 at 198.51.100.3 ", " 21s\n",
			"    ⎿ creating acme from snapshot 1001: cx23 in fsn1", " 1m23s\n", "    ⎿ released static IP keelbox-acme (203.0.113.7)\n",
			"  ✗ Waiting for SSH at 198.51.100.3 ", " 5s\n", "  ✓ Set target.hetzner.server_type: cpx22 in", "  • the cloud firewall admits SSH from the internet")
		// A deletion is named by what the cloud's side said first, and a
		// long line goes on under itself with no word lost or broken.
		wantAll(t, fmt.Sprintf("%d columns, as words", width), flat(got), "Resuming acme from snapshot 1001, parked since 2026-11-14 ✓ Created", "✓ Deleted snapshot keelbox-acme-parked-20261115T234621Z", "⎿ "+warning,
			"Set target.hetzner.server_type: cpx22 in ~/.config/keelbox/boxes/acme.yaml")
		if strings.Contains(got, "never on a terminal") || strings.Contains(got, "Deleted the snapshot") || strings.Count(got, "deleted snapshot") != 0 || logged != 0 {
			t.Errorf("%d columns: %d lines went to the log, or the list repeats one:\n%s", width, logged, got)
		}
	}
}

// Off a terminal, with --verbose and for a screen reader the view is the
// log's lines and nothing else, as before there was a list.
func TestStepViewIsTheLogElsewhere(t *testing.T) {
	for name, o := range map[string]options{
		"not a terminal":  {},
		"--verbose":       {style: termStyle(100), verbose: true},
		"a screen reader": {style: ui.Style{Live: true, UTF8: true, Width: 100, Accessible: true}},
	} {
		var out bytes.Buffer
		v := newStepView(&out, o, func(f string, a ...any) { fmt.Fprintf(&out, "keelbox: "+f+"\n", a...) })
		v.heading("Resuming acme", "from snapshot 1001")
		v.begin("Shutting acme down", "a hint")
		v.say("shutting %s down", "acme")
		v.note("server %d did not shut down within 2m0s; switching it off", 42)
		v.finished("Shut acme down", "")
		v.begin("Deleting server 42", "")
		v.failed()
		v.did("set %s", "target.hetzner.server_type: cpx22")
		v.close()
		fmt.Fprint(v.writer(&out), "through\n")
		want := "keelbox: shutting acme down\nkeelbox: server 42 did not shut down within 2m0s; switching it off\nkeelbox: set target.hetzner.server_type: cpx22\nthrough\n"
		if out.String() != want {
			t.Errorf("%s:\n%s\nwant:\n%s", name, out.String(), want)
		}
	}
}

// What else writes to the terminal while a step runs, a question about a
// host key or an error, is not overwritten by the step's line, and the
// step is settled under it.
func TestStepViewGivesWayToOtherOutput(t *testing.T) {
	var out lockedBuffer
	v := newStepView(&out, options{style: termStyle(100)}, func(string, ...any) {})
	w := v.writer(&out)
	v.begin("Waiting for SSH at 198.51.100.3", "the server boots")
	fmt.Fprint(w, "First connection to 198.51.100.3. Its host keys are:\n  ssh-ed25519  SHA256:abc\nTrust and pin them? [y/N] ")
	at := len(out.String())
	// The spinner would have redrawn its line several times by now.
	time.Sleep(350 * time.Millisecond)
	if more := out.String()[at:]; more != "" {
		t.Errorf("the step's line was drawn over the question: %q", more)
	}
	v.finished("SSH answers at 198.51.100.3", "")
	fmt.Fprint(w, "keelbox: after\n")
	got := shown(out.String())
	want := "First connection to 198.51.100.3. Its host keys are:\n  ssh-ed25519  SHA256:abc\nTrust and pin them? [y/N] \n  ✓ SSH answers at 198.51.100.3"
	if !strings.HasPrefix(got, want) || !strings.HasSuffix(got, "s\nkeelbox: after\n") || strings.Contains(got, "Waiting for SSH") {
		t.Errorf("the terminal shows:\n%s", got)
	}
}

// stepsOn are the steps each cloud's park, resume and resize settle to on
// a terminal, as patterns over the words the terminal shows.
var stepsOn = map[string]map[string][]string{
	"hetzner": {
		"park":   {`✓ Shut acme down \d+s`, `✓ Saved its disk as snapshot \d+ \(2\.5 GB, about EUR 0\.04 a month\) \d+s`, `✓ Deleted acme \(id 42\) \d+s`},
		"resume": {`Resuming acme from snapshot \d+, parked since \d{4}-\d\d-\d\d`, `✓ Created server \d+ at 198\.51\.100\.3 \d+s ⎿ creating acme from snapshot \d+: cx23 in fsn1 \(EUR 0\.0060/h gross\)`, `✓ SSH answers at 198\.51\.100\.3 \d+s`, `✓ Deleted snapshot \d+ \d+s`},
		"resize": {`✓ Shut acme down \d+s`, `✓ Changed server 42 from cx23 to cpx22 \d+s`, `✓ Started server 42 at 203\.0\.113\.7 \d+s`, `✓ Set target\.hetzner\.server_type: cpx22 in ~/\.config/keelbox/boxes/acme\.yaml`, `✓ SSH answers at 203\.0\.113\.7 \d+s`},
	},
	"aws": {
		"park":   {`✓ Shut acme down \d+s`, `✓ Saved its disk as snapshot keelbox-acme-parked-\S+ \(60 GB disk\) \d+s`, `✓ Deleted instance acme \d+s ⎿ released static IP keelbox-acme \(203\.0\.113\.7\)`},
		"resume": {`Resuming acme from snapshot keelbox-acme-parked-\S+ parked since`, `✓ Created instance acme at \S+ \d+s ⎿ creating acme from snapshot keelbox-acme-parked-\S+ small_3_0 in eu-central-1a ⎿ set the firewall of instance acme: in tcp 22, in udp 41641 ⎿ attached static IP keelbox-acme`, `✓ SSH answers at \S+ \d+s`, `✓ Deleted snapshot keelbox-acme-parked-\S+ \d+s`},
		"resize": {`✓ Shut acme down \d+s`, `✓ Changed instance acme from small_3_0 to medium_3_0 \d+s ⎿ saving the disk of instance acme as a snapshot \(minutes\) ⎿ snapshot keelbox-acme-resize-\S+ is complete; deleting instance acme \(small_3_0\) to create it again as medium_3_0`, `⎿ deleted snapshot keelbox-acme-resize-\S+ instance acme has the disk`, `✓ Started instance acme at 203\.0\.113\.7 \d+s`, `✓ Set target\.aws\.bundle: medium_3_0 in`, `✓ SSH answers at 203\.0\.113\.7 \d+s`},
	},
	"digitalocean": {
		"park":   {`✓ Shut acme down \d+s`, `✓ Saved its disk as snapshot \d+ \(2\.5 GB, about USD 0\.15 a month\) \d+s`, `✓ Deleted Droplet acme \(id \d+\) \d+s`},
		"resume": {`Resuming acme from snapshot \d+, parked since \d{4}-\d\d-\d\d`, `✓ Created Droplet \d+ at \S+ \d+s ⎿ creating acme from snapshot \d+: s-2vcpu-4gb in fra1 \(about USD 24\.00/month before tax\)`, `✓ SSH answers at \S+ \d+s`, `✓ Deleted snapshot \d+ \d+s`},
		"resize": {`✓ Shut acme down \d+s`, `✓ Changed Droplet \d+ from s-2vcpu-4gb to s-4vcpu-8gb \d+s`, `✓ Started Droplet \d+ at 203\.0\.113\.7 \d+s`, `✓ Set target\.digitalocean\.size: s-4vcpu-8gb in`, `✓ SSH answers at 203\.0\.113\.7 \d+s`},
	},
	"gcp": {
		"park":   {`✓ Shut acme down \d+s`, `✓ Saved its disk as snapshot keelbox-acme-\S+ \(1\.3 GB stored of a 50 GB disk\) \d+s`, `✓ Deleted VM acme \(europe-west3-a\) and its disk \d+s ⎿ released address keelbox-acme \(203\.0\.113\.7\)`},
		"resume": {`Resuming acme from snapshot keelbox-acme-\S+ parked since`, `✓ Created VM acme at \S+ \d+s ⎿ reserved address keelbox-acme \(\S+\) in europe-west3 ⎿ creating acme from snapshot keelbox-acme-\S+ e2-medium in europe-west3-a, 50 GB disk`, `✓ SSH answers at \S+ \d+s`, `✓ Deleted snapshot keelbox-acme-\S+ \d+s`},
		"resize": {`✓ Shut acme down \d+s`, `✓ Changed VM acme from e2-medium to e2-standard-2 \d+s`, `✓ Started VM acme at 203\.0\.113\.7 \d+s`, `✓ Set target\.gcp\.machine_type: e2-standard-2 in`, `✓ SSH answers at 203\.0\.113\.7 \d+s`},
	},
}

// On a terminal each of the three commands shows its steps as a list on
// every cloud, in order, with what the cloud's side said under them; the
// log's lines are not there as well, and nothing overflows the terminal.
func TestParkResumeAndResizeShowTheirSteps(t *testing.T) {
	for _, c := range goAheadWorlds {
		for _, width := range []int{60, 80, 120} {
			for _, cmd := range []string{"park", "resume", "resize"} {
				do := c.world(t)
				o, input := options{style: termStyle(width)}, str("y\n")
				switch cmd {
				case "resume":
					if code, out := do(t, "park", options{yes: true}, nil); code != 0 {
						t.Fatalf("%s: park: exit %d\n%s", c.cloud, code, out)
					}
					input = nil
				case "resize":
					o.size = c.size
				}
				code, out := do(t, cmd, o, input)
				name := fmt.Sprintf("%s %s at %d columns", c.cloud, cmd, width)
				if code != 0 {
					t.Fatalf("%s: exit %d\n%s", name, code, out)
				}
				got := shown(out)
				words, at := flat(got), 0
				for _, step := range stepsOn[c.cloud][cmd] {
					loc := regexp.MustCompile(step).FindStringIndex(words[at:])
					if loc == nil {
						t.Errorf("%s: no %q after the steps before it:\n%s", name, step, got)
						continue
					}
					at += loc[1]
				}
				for _, l := range strings.Split(got, "\n") {
					if ui.Width(l) >= width {
						t.Errorf("%s: a line is %d wide: %q", name, ui.Width(l), l)
					}
				}
				for _, logged := range []string{"keelbox: shutting acme down", "waiting for SSH", "keelbox: deleted", "keelbox: set target", "is off;"} {
					if strings.Contains(got, logged) {
						t.Errorf("%s: the log's line %q is on the terminal beside the list:\n%s", name, logged, got)
					}
				}
			}
		}
	}
}

// A park that fails on a terminal says so in the list: the step that
// failed has a cross, the server is started again as a step of its own,
// and the messages are where they were. The server is not deleted.
func TestParkThatFailsShowsItInTheSteps(t *testing.T) {
	w := newParkWorld(t)
	w.api.Fail = map[string]bool{"create_image": true}
	code, out := w.do(t, "park", options{yes: true, style: termStyle(100)}, nil)
	got := shown(out)
	if code != 1 || len(w.api.Servers) != 1 || w.api.Servers[0]["status"] != "running" || len(w.api.Images) != 0 {
		t.Fatalf("exit %d, %d servers, %d snapshots\n%s", code, len(w.api.Servers), len(w.api.Images), got)
	}
	steps := regexp.MustCompile(`(?s)  ✓ Shut acme down +\d+s\n  ✗ Saving its disk as a snapshot +\d+s\nkeelbox: the snapshot was not completed: [^\n]+\n  ✓ Server 42 runs again at 203\.0\.113\.7 +\d+s\n` +
		`keelbox: acme is not parked: server 42 was not deleted and runs again at 203\.0\.113\.7\. Nothing is lost; try again: keelbox park acme\n$`)
	if !steps.MatchString(got) {
		t.Errorf("the terminal shows:\n%s", got)
	}
	// A resize the cloud refuses, the same way.
	w = newParkWorld(t)
	w.api.Fail = map[string]bool{"change_type": true}
	code, out = w.do(t, "resize", options{yes: true, size: "cpx22", style: termStyle(100)}, nil)
	got = shown(out)
	steps = regexp.MustCompile(`(?s)  ✓ Shut acme down +\d+s\n  ✗ Changing server 42 from cx23 to cpx22 +\d+s\nkeelbox: the size was not changed: [^\n]+\n  ✓ Server 42 runs again at 203\.0\.113\.7 +\d+s\n` +
		`keelbox: acme is still cx23 and runs again at 203\.0\.113\.7\. Try again: keelbox resize acme\n$`)
	if code != 1 || !steps.MatchString(got) {
		t.Errorf("resize: exit %d, the terminal shows:\n%s", code, got)
	}
}

// --verbose on a terminal keeps the log's lines, as it does for an apply.
func TestParkVerboseKeepsTheLog(t *testing.T) {
	w := newParkWorld(t)
	code, out := w.do(t, "park", options{yes: true, verbose: true, style: termStyle(100)}, nil)
	if code != 0 || strings.Contains(out, "✓ Shut") {
		t.Errorf("exit %d\n%s", code, out)
	}
	wantAll(t, "park --verbose", out, "keelbox: shutting acme down\n", "keelbox: server 42 is off; saving its disk as a snapshot (minutes for a full disk)\n",
		"keelbox: snapshot 1001 is complete (2.5 GB, about EUR 0.04 a month)\n", "keelbox: deleted acme (id 42)\n")
}

// A command with nothing to do says how it found the box in one sentence:
// a "keelbox:" line in a log, and on a terminal a line under a bullet that
// fits the width, with the amount whole and the command that follows.
func TestNothingToDoIsOneSentence(t *testing.T) {
	for _, width := range []int{60, 80, 120} {
		w := newParkWorld(t)
		say := func(cmd string, o options) string {
			t.Helper()
			o.style, o.yes = termStyle(width), true
			code, out := w.do(t, cmd, o, nil)
			if code != 0 || len(w.writes()) != 0 || !strings.HasPrefix(out, "  • acme is ") {
				t.Errorf("%s at %d columns: exit %d, %v\n%s", cmd, width, code, w.writes(), out)
			}
			for _, l := range strings.Split(out, "\n") {
				if f := strings.Fields(l); ui.Width(l) >= width || len(f) > 0 && f[len(f)-1] == "EUR" {
					t.Errorf("%s at %d columns: a line is %d wide, or an amount is broken: %q", cmd, width, ui.Width(l), l)
				}
			}
			return flat(out)
		}
		if got, want := say("resize", options{size: "cx23"}), "• acme is cx23 already. Nothing changed."; got != want {
			t.Errorf("%d columns: %q", width, got)
		}
		if got, want := say("resume", options{}), "• acme is not parked: server 42 runs at 203.0.113.7. Nothing to resume."; got != want {
			t.Errorf("%d columns: %q", width, got)
		}
		if code, out := w.do(t, "park", options{yes: true}, nil); code != 0 {
			t.Fatalf("park: exit %d\n%s", code, out)
		}
		w.forgetCalls()
		want := "• acme is already parked, since " + day(time.Now()) + ": snapshot 1001 holds its disk (2.5 GB, about EUR 0.04 a month) and is what is billed. Bring it back: keelbox resume acme"
		if got := say("park", options{}); got != want {
			t.Errorf("%d columns: %q", width, got)
		}
	}
}
