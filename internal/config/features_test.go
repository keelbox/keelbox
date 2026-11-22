package config

import (
	"strings"
	"testing"
)

func TestPreviewsDefaults(t *testing.T) {
	c, err := Parse([]byte(minimal("")), "t")
	if err != nil {
		t.Fatal(err)
	}
	if lo, hi := c.PreviewPorts(); !c.PreviewsEnabled() || lo != 8401 || hi != 8499 {
		t.Errorf("previews default: %v %d-%d", c.PreviewsEnabled(), lo, hi)
	}
	c, err = Parse([]byte(minimal("access: {tailscale: {enabled: false}}\n")), "t")
	if err != nil {
		t.Fatal(err)
	}
	if c.PreviewsEnabled() {
		t.Error("previews on without tailscale")
	}
	if c.BackupEnabled() {
		t.Error("backup on by default")
	}
}

func TestFeatureValidation(t *testing.T) {
	for _, tc := range []struct{ yaml, want string }{
		{"previews: {ports: 80-90}\n", "previews.ports"},
		{"previews: {ports: 9000-8000}\n", "previews.ports"},
		{"previews: {ports: 2000-9000}\n", "previews.ports"},
		{"previews: {ports: 8443-8443}\nhost: {firewall: {allow: [{service: unifi}]}}\n", "none is left for previews"},
		{"previews: {enabled: true}\naccess: {tailscale: {enabled: false}}\n", "previews.enabled"},
		{"backup: {schedule: daily}\n", "backup.repository: required"},
		{"backup: {repository: s3:https://key:secret@s3.example.com/b, credentials_file: /c, password_file: /p}\n", "without credentials in the URL"},
		{"backup: {repository: b2:bucket, credentials_file: /c, password_file: /p}\n", "backup.repository"},
		{"backup: {repository: s3:https://s3.example.com/b}\n", "backup.credentials_file: required"},
		{"backup: {repository: s3:https://s3.example.com/b, credentials_file: /c, password_file: /c}\n", "must differ"},
		{"backup: {repository: s3:https://s3.example.com/b, credentials_file: /c, password_file: /p, paths: [home/agent]}\n", "backup.paths"},
		{"backup: {repository: s3:https://s3.example.com/b, credentials_file: /c, password_file: /p, paths: [/]}\n", "whole machine"},
		{"backup: {repository: s3:https://s3.example.com/b, credentials_file: /c, password_file: /p, exclude: [--anything]}\n", "backup.exclude"},
		{"backup: {repository: s3:https://s3.example.com/b, credentials_file: /c, password_file: /p, keep: {daily: 0, weekly: 0, monthly: 0}}\n", "keeps no snapshot"},
		{"backup: {repository: s3:https://s3.example.com/b, credentials_file: /c, password_file: /p, stale_after: 5m}\n", "backup.stale_after"},
		{"backup: {repository: s3:https://s3.example.com/b, credentials_file: /c, password_file: /p, schedule: \"daily; rm -rf /\"}\n", "backup.schedule"},
	} {
		got := strings.Join(problems(t, minimal(tc.yaml)), "\n")
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: want a problem with %q, got %q", strings.TrimSpace(tc.yaml), tc.want, got)
		}
	}
	c, err := Parse([]byte(minimal("backup: {repository: s3:http://127.0.0.1:9000/kb/box, credentials_file: /c, password_file: /p, keep: {daily: 3}}\n")), "t")
	if err != nil {
		t.Fatal(err)
	}
	if d, w, m := c.KeepPolicy(); d != 3 || w != 4 || m != 6 || c.Backup.Schedule != "daily" || c.BackupStaleAfter().Hours() != 48 {
		t.Errorf("backup defaults: %d %d %d %s %s", d, w, m, c.Backup.Schedule, c.BackupStaleAfter())
	}
	if len(c.BackupExclude()) == 0 || c.BackupExclude()[0] != "node_modules" {
		t.Errorf("default exclusions: %v", c.BackupExclude())
	}
}
