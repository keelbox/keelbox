package modules

import (
	"strings"
	"testing"
)

// The backup units keep the text they had before the helper, so boxes
// see no change.
func TestBackupUnitsUnchanged(t *testing.T) {
	const service = `# Managed by keelbox. Backs up the box with restic; see keelbox backup.
[Unit]
Description=keelbox backup
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/keelbox backup --local /etc/keelbox/keelbox.yaml
Nice=10
IOSchedulingClass=idle
`
	const timer = `# Managed by keelbox (backup.schedule).
[Unit]
Description=keelbox backup schedule

[Timer]
OnCalendar=*-*-* 03:00
Persistent=true
RandomizedDelaySec=15min
Unit=keelbox-backup.service

[Install]
WantedBy=timers.target
`
	if got := backupService.text(); got != service {
		t.Errorf("backup service:\n%s", got)
	}
	if got := backupTimer("*-*-* 03:00").text(); got != timer {
		t.Errorf("backup timer:\n%s", got)
	}
}

func TestDayTwoServiceText(t *testing.T) {
	got := rootService{Unit: "keelbox-x.service", Header: ". Test.", Description: "keelbox x", Command: "x-run", DayTwo: true}.text()
	for _, want := range []string{
		"StartLimitIntervalSec=0\n",
		"ExecCondition=/usr/local/bin/keelbox has-command x-run\n",
		"ExecStart=/usr/local/bin/keelbox x-run --local /etc/keelbox/keelbox.yaml\n",
		"TimeoutStartSec=2min\n", "Nice=5\n", "CPUWeight=200\n", "IOWeight=200\n", "NoNewPrivileges=yes\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("day-two service lacks %q:\n%s", want, got)
		}
	}
	p := rootPath{Unit: "keelbox-x.path", Description: "watch", Service: "keelbox-x.service", Modified: "/home/agent/f"}.text()
	if !strings.Contains(p, "PathModified=/home/agent/f\nUnit=keelbox-x.service\n") || !strings.Contains(p, "WantedBy=paths.target") {
		t.Errorf("path unit:\n%s", p)
	}
}
