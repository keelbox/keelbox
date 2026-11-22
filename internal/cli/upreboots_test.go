package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/keelbox/keelbox/internal/config"
)

// keelbox up asks when the box restarts for an update, nightly first and
// preselected; without a terminal --auto-reboot answers, and the default
// is nightly with no question (ADR 0090).
func TestUpReboots(t *testing.T) {
	const base = "--provider ssh --host 203.0.113.7 --ssh-user root --host-key= --agents codex --gh=false --tailscale=false --skip-sign-in"
	run := func(t *testing.T, input *string, extra ...string) (int, string) {
		t.Helper()
		home := isolate(t)
		key := laptopKey(t, home)
		args := append([]string{"up", "lab", "--ssh-key", key}, strings.Fields(base)...)
		return upWith(t, &fakeRunner{}, input, append(args, extra...)...)
	}
	written := func(t *testing.T) (*config.Config, string) {
		t.Helper()
		cfg, _ := loadBox(t, "lab")
		data, err := os.ReadFile(boxConfigPath("lab"))
		if err != nil {
			t.Fatal(err)
		}
		return cfg, string(data)
	}
	const menu = "When should the box restart for an update that needs it?\n" +
		"  1) Nightly (recommended)  between 01:00 and 06:00 UTC, the box's timezone, once nothing has been in use for 30 minutes\n" +
		"  2) Weekly                 the same, on Sunday night\n" +
		"  3) Manually               you run keelbox reboot lab when keelbox status says one is needed\n" +
		"Choose 1-3 [1]: "

	t.Run("Enter takes nightly", func(t *testing.T) {
		askingReboots(t)
		code, out := run(t, script("", ""))
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		cfg, text := written(t)
		if cfg.Host.AutoReboot != config.AutoRebootNightly || !strings.Contains(text, "\n  auto_reboot: nightly\n") {
			t.Errorf("auto_reboot %q:\n%s", cfg.Host.AutoReboot, text)
		}
		for _, want := range []string{menu, "reboots     nightly: between 01:00 and 06:00 UTC once idle, when an update needs it"} {
			if !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
		}
	})

	t.Run("3 takes manually", func(t *testing.T) {
		askingReboots(t)
		code, out := run(t, script("3", ""))
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if cfg, _ := written(t); cfg.Host.AutoReboot != config.AutoRebootNever {
			t.Errorf("auto_reboot %q", cfg.Host.AutoReboot)
		}
		if !strings.Contains(out, "manually: you run keelbox reboot lab when an update needs it") {
			t.Errorf("review card:\n%s", out)
		}
	})

	t.Run("the flag answers without a terminal", func(t *testing.T) {
		code, out := run(t, nil, "--yes", "--auto-reboot", "weekly")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if cfg, _ := written(t); cfg.Host.AutoReboot != config.AutoRebootWeekly {
			t.Errorf("auto_reboot %q", cfg.Host.AutoReboot)
		}
		if strings.Contains(out, "When should the box restart") {
			t.Error("asked without a terminal")
		}
	})

	t.Run("a wrong value says the right ones", func(t *testing.T) {
		code, out := run(t, nil, "--yes", "--auto-reboot", "daily")
		if code == 0 || !strings.Contains(out, `--auto-reboot "daily": use nightly, weekly or never, for example: --auto-reboot nightly`) {
			t.Errorf("exit %d\n%s", code, out)
		}
	})
}
