//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestAgentOnBox: keelbox run by the agent on its box answers without sudo
// (status from the snapshot; laptop and root commands say why not), and a
// keelbox report the agent files lands in root's inbox, unsent, for the
// owner to read from the laptop (ADR 0062).
func TestAgentOnBox(t *testing.T) {
	ensureKey(t)
	build(t)
	cfg, host := agentsBox(t)

	// The watch run writes the snapshot every two minutes; status reads it.
	var out string
	deadline := time.Now().Add(4 * time.Minute)
	for {
		var err error
		out, err = sshRun(t, host, "agent", nil, "keelbox status 2>&1")
		if err == nil && strings.Contains(out, "size") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent status: %v\n%s", err, out)
		}
		time.Sleep(10 * time.Second)
	}
	for _, want := range []string{"box ", "keelbox ", "as of", "CPU", "previews", "needs root: ask the owner"} {
		if !strings.Contains(out, want) {
			t.Errorf("agent status lacks %q:\n%s", want, out)
		}
	}
	logf(t, "PASS agent status without sudo:\n%s", out)
	if out, err := sshRun(t, host, "agent", nil, "keelbox ssh 2>&1"); err == nil || !strings.Contains(out, "you are already on") {
		t.Errorf("keelbox ssh as the agent: %v\n%s", err, out)
	}
	if out, err := sshRun(t, host, "agent", nil, "timeout 20 keelbox doctor 2>&1"); err == nil || !strings.Contains(out, "will not ask for a password") {
		t.Errorf("keelbox doctor as the agent: %v\n%s", err, out)
	}
	logf(t, "PASS laptop and root commands as the agent say why, without sudo")

	// The agent files a report, with a token and a home path in it.
	token := "ghp_" + strings.Repeat("e2e0", 9)
	out = must(t, host, "agent", `keelbox report "e2e: expose says nothing listens" --area expose --detail "`+token+` at /home/agent/projects/shop-api/src"`,
		"Saved keelbox report", "Nothing was sent. Reports never leave your box unless you send them.", "--send N")
	logf(t, "PASS agent filed a report: %s", firstLine(out))
	if out, err := sshRun(t, host, "agent", nil, "ls /var/lib/keelbox/reports/ 2>&1"); err == nil {
		t.Errorf("the agent lists the inbox:\n%s", out)
	}

	deadline = time.Now().Add(time.Minute)
	for {
		out, _ = kb(t, "reports", cfg)
		if strings.Contains(out, "e2e: expose says nothing listens") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("report not in the inbox:\n%s", out)
		}
		time.Sleep(3 * time.Second)
	}
	show, err := kb(t, "reports", cfg, "--show", "1")
	if err != nil || !strings.Contains(show, "area: expose") || !strings.Contains(show, "Ubuntu: ") || !strings.Contains(show, "doctor failing:") {
		t.Fatalf("reports --show: %v\n%s", err, show)
	}
	logf(t, "PASS the laptop reads the report with its box facts:\n%s", show)

	// Unsent: nothing queued, nothing in the journal beyond the filing.
	must(t, host, "admin", "! sudo grep -rl keelbox-report /var/lib/keelbox/events/ 2>/dev/null")
	if out, err := kb(t, "reports", cfg, "--send", "1"); err == nil || !strings.Contains(out, "needs a terminal") {
		t.Errorf("--send without a terminal: %v\n%s", err, out)
	}
	if out, err := kb(t, "reports", cfg, "--delete", "1"); err != nil || !strings.Contains(out, "Deleted report") {
		t.Errorf("--delete: %v\n%s", err, out)
	}
	logf(t, "PASS nothing queued or sent; --send refuses without a terminal; --delete")
}
