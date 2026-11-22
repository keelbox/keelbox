package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keelbox/keelbox/internal/config"
	"github.com/keelbox/keelbox/internal/prompt"
	"github.com/keelbox/keelbox/internal/ui"
)

// keelbox resize, park and resume on a real pseudo-terminal, answered with
// the keys a person presses: the drawn menu of sizes, the cards, the steps
// while they run, the apply's own view among them and the end, against
// each fake cloud.
//
// With KEELBOX_SHOTS=DIR set, the bytes the terminal received for each
// command are saved there (DIR/<width>-<theme>-<cloud>-<command>.ans, and
// -live for the moment a step runs), for rendering to images, and beside
// each the screen they leave as plain text (.txt), which is what the
// transcripts in docs/guides/resize-park-resume.md are; KEELBOX_SHOTS_WIDTH
// and KEELBOX_SHOTS_THEME (dark, light) pick the terminal.

// saveScreen saves what a terminal received and, as text, what it shows.
func saveScreen(t *testing.T, s *shots, name, out string) {
	t.Helper()
	if s == nil {
		return
	}
	s.save(t, name, out)
	if err := os.WriteFile(filepath.Join(s.dir, s.prefix+name+".txt"), []byte(shown(out)), 0o600); err != nil {
		t.Error(err)
	}
}

// terminalWorlds are the four clouds with the box acme on each, and the
// size one step down the menu from the one it has.
var terminalWorlds = []struct {
	cloud, size string
	world       func(t *testing.T) (cloud, *config.Config)
}{
	{"hetzner", "cpx22", func(t *testing.T) (cloud, *config.Config) { w := newParkWorld(t); return w.cl, w.cfg }},
	{"aws", "medium_3_0", func(t *testing.T) (cloud, *config.Config) { w := newAWSWorld(t); return w.cl, w.cfg }},
	{"digitalocean", "s-4vcpu-8gb", func(t *testing.T) (cloud, *config.Config) { w := newDOWorld(t); return w.cl, w.cfg }},
	{"gcp", "e2-standard-2", func(t *testing.T) (cloud, *config.Config) { w := newGCPWorld(t); return w.cl, w.cfg }},
}

// quietApply is an apply that finds nothing to change, as the box streams
// it.
const quietApply = "keelbox apply: acme (release 2026.11.3, Ubuntu 24.04 amd64)\nhost\n  ok      packages\nagents\n  ok      codex\n\nNo changes. The box matches the config (140 checks, 3s).\n"

func TestResizeParkResumeOnARealTerminal(t *testing.T) {
	widths, theme := []int{80, 120}, "dark"
	if os.Getenv("KEELBOX_SHOTS") != "" {
		var cols int
		cols, theme = shotsTerminal()
		widths = []int{cols}
	}
	for _, width := range widths {
		for _, c := range terminalWorlds {
			t.Run(fmt.Sprintf("%s at %d columns", c.cloud, width), func(t *testing.T) {
				cl, cfg := c.world(t)
				// The box takes a moment to answer, so that the step that waits
				// for it is seen running, and the apply shows its own view.
				boxComesBack = func(context.Context, *config.Config, addr, io.Writer, io.Writer) error {
					time.Sleep(400 * time.Millisecond)
					return nil
				}
				applyExisting = func(_ context.Context, _ cloud, _ *config.Config, _ *cloudServer, _ addr, o options, _ func(string, ...any), stdout, stderr io.Writer) int {
					out, _, done := applyViewModules([]string{"host", "agents"}, o, stdout, stderr)
					_, _ = io.WriteString(out, quietApply)
					done()
					return 0
				}
				st := fullStyle(width, theme == "light")
				shot := newShots(width, theme)
				// run starts a command on a terminal of its own and returns the
				// terminal and where its exit code arrives.
				run := func(cmd string) (*ptyTerm, chan int) {
					pt := openPTYTerm(t, width)
					fmt.Fprintf(pt.slave, "$ keelbox %s acme\n", cmd)
					p := prompt.Terminal(pt.slave, pt.slave).WithStyle(st)
					o := options{style: st, configFile: boxConfigPath("acme")}
					logf := func(f string, a ...any) { fmt.Fprintf(pt.slave, "keelbox: "+f+"\n", a...) }
					code := make(chan int, 1)
					go func() {
						ctx := context.Background()
						switch cmd {
						case "resize":
							code <- cloudResize(ctx, cl, cfg, o, p, logf, pt.slave, pt.slave)
						case "park":
							code <- cloudPark(ctx, cl, cfg, o, p, logf, pt.slave, pt.slave)
						default:
							code <- cloudResume(ctx, cl, cfg, o, logf, pt.slave, pt.slave)
						}
					}()
					return pt, code
				}
				// done waits for the command, saves what the terminal received
				// and checks what it shows.
				done := func(cmd string, pt *ptyTerm, code chan int, at int, last string, want ...string) {
					t.Helper()
					select {
					case c := <-code:
						if c != 0 {
							t.Fatalf("%s: exit %d:\n%s", cmd, c, ui.Strip(pt.output()))
						}
					case <-time.After(20 * time.Second):
						t.Fatalf("%s did not finish:\n%s", cmd, ui.Strip(pt.output()))
					}
					pt.expect(t, at, last)
					time.Sleep(100 * time.Millisecond)
					out := pt.output()
					saveScreen(t, shot, c.cloud+"-"+cmd, out)
					wantAll(t, cmd, shown(out), want...)
					for _, l := range strings.FieldsFunc(out, func(r rune) bool { return r == '\n' || r == '\r' }) {
						if w := ui.Width(l); w >= width {
							t.Errorf("%s: a line is %d columns on a %d column terminal: %q", cmd, w, width, l)
						}
					}
				}
				// live saves the terminal while the step that waits for SSH runs:
				// its line is there and not yet settled.
				live := func(cmd string, pt *ptyTerm, at int) int {
					t.Helper()
					at = pt.expect(t, at, `Waiting for SSH at [0-9.]+`)
					time.Sleep(150 * time.Millisecond)
					out := pt.output()
					saveScreen(t, shot, c.cloud+"-"+cmd+"-live", out)
					if now := shown(out); strings.Contains(now, "SSH answers") || !strings.Contains(now, "Waiting for SSH at ") {
						t.Errorf("%s, while it waits for SSH:\n%s", cmd, now)
					}
					return at
				}

				pt, code := run("resize")
				at := pt.expect(t, 0, `Size for acme.*Enter.*choose`)
				saveScreen(t, shot, c.cloud+"-resize-menu", pt.output())
				if menu := shown(pt.output()); !strings.Contains(menu, "the size now") || !strings.Contains(flat(menu), sizeAdvice) {
					t.Errorf("the menu:\n%s", menu)
				}
				_, _ = pt.master.WriteString("\x1b[B") // down once: the next size
				at = pt.expect(t, at, `›[^\n]*`+c.size)
				_, _ = pt.master.WriteString("\r")
				at = pt.expect(t, at, `Resize acme\?.*\[y/N\]`)
				_, _ = pt.master.WriteString("y\r")
				at = live("resize", pt, at)
				done("resize", pt, code, at, `After the resize.*start again[^\n]*keelbox attach acme.*╯`,
					"now): "+c.size+"\n", "╭─ Resize acme from ", "✓ Resize acme? Yes\n", "  ✓ Shut acme down ", "  ✓ Started ", "  ✓ Set target.", "  ✓ SSH answers at 203.0.113.7 ",
					"  ✓ acme is up to date", "\n\n  ✓ acme is "+c.size+" now ", "╭─ After the resize ", "│ start again  keelbox attach acme")

				pt, code = run("park")
				at = pt.expect(t, 0, `Park acme\?.*\[y/N\]`)
				_, _ = pt.master.WriteString("y\r")
				done("park", pt, code, at, `While acme is parked.*bring it back[^\n]*keelbox resume acme.*╯`,
					"╭─ Park acme ", "│ saves ", "│ still billed ", "│ takes ", "│ comes back ", "✓ Park acme? Yes\n", "  ✓ Shut acme down ", "  ✓ Saved its disk as snapshot ", "  ✓ Deleted ",
					"\n\n  ✓ acme is parked ", "╭─ While acme is parked ", "│ bring it back  keelbox resume acme")

				pt, code = run("resume")
				at = live("resume", pt, 0)
				done("resume", pt, code, at, `check it[^\n]*keelbox doctor acme.*╯`,
					"  Resuming acme from snapshot ", "  ✓ Created ", "  ✓ SSH answers at ", "  ✓ acme is up to date", "  ✓ Deleted snapshot ",
					"\n\n  ✓ acme is back ", "╭─ Start working again ", "│ start again   keelbox attach acme", "(it was 203.0.113.7)")
			})
		}
	}
}
