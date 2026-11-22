package config

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const daytwoBase = "version: 1\nbox: acme\nusers:\n  admin:\n    ssh_authorized_keys: [\"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAdmin admin@laptop\"]\n"

func TestDayTwoDefaults(t *testing.T) {
	c, err := Parse([]byte(daytwoBase), "t.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.DoctorSchedule() != "hourly" || c.DoctorNotifyWarnings() || c.PowerSleep() != "allow" || !c.NotifyQuietHoursUrgent() || c.NotifyAgentText() {
		t.Error("doctor, power or notify defaults")
	}
	if w, f := c.DiskThresholds(); w != 85 || f != 95 {
		t.Errorf("disk %d %d", w, f)
	}
}

func TestDayTwoKeys(t *testing.T) {
	good := daytwoBase + `notify:
  channels:
    - {name: phone, type: ntfy, topic_file: ~/.config/keelbox/ntfy-topic, level: warn}
    - {name: lan, type: ntfy, server: "http://100.101.102.103:8080", topic_file: /etc/keelbox/t}
    - {name: tg, type: telegram, chat_id: "-1001234567890", bot_token_file: /etc/keelbox/tg}
    - {name: hook, type: webhook, format: text, url_file: /etc/keelbox/hook, header_file: /etc/keelbox/hdr}
    - {name: po, type: pushover, token_file: /etc/keelbox/po-app, user_file: /etc/keelbox/po-user}
  mute: [agent-done]
  quiet_hours: "22:00-07:00"
  agent_text: true
doctor: {schedule: "off", notify_warnings: true}
host: {power: {sleep: never}, disk: {warn_percent: 70, fail_percent: 90}}
`
	c, err := Parse([]byte(good), "t.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if c.DoctorSchedule() != "" || !c.Muted("agent-done") || c.Notify.Channels[0].LevelOf().String() != "warn" || c.Notify.Channels[0].NtfyServer() != "https://ntfy.sh" {
		t.Error("values not read")
	}
	// Marshalled and parsed again, the config means the same.
	out, _ := c.Marshal()
	again, err := Parse(out, "applied.yaml")
	if err != nil || again.Hash() != c.Hash() {
		t.Errorf("round trip: %v", err)
	}

	for bad, want := range map[string]string{
		"agent_updates: {mode: hold, keep: 3}":                                                               "delete the agent_updates block",
		"notify: {mute: [agent-finished]}":                                                                   "not an event kind",
		"notify: {quiet_hours: 22-7}":                                                                        "HH:MM-HH:MM",
		"notify: {channels: [{name: a, type: email}]}":                                                       "use ntfy, pushover, telegram or webhook",
		"notify: {channels: [{name: a, type: ntfy}]}":                                                        "topic_file: required",
		"notify: {channels: [{name: a, type: ntfy, topic_file: /t, chat_id: '1'}]}":                          "does not take chat_id",
		"notify: {channels: [{name: a, type: ntfy, topic_file: /t, server: 'http://ntfy.example.com'}]}":     "plain http only",
		"notify: {channels: [{name: a, type: ntfy, topic_file: /t, server: 'https://u:p@ntfy.sh'}]}":         "without credentials",
		"notify: {channels: [{name: a, type: ntfy, topic_file: /t}, {name: a, type: ntfy, topic_file: /u}]}": "used twice",
		"notify: {channels: [{name: a, type: ntfy, topic_file: /t, level: loud}]}":                           "use info, warn or urgent",
		"notify: {channels: [{name: a, type: telegram, chat_id: abc, bot_token_file: /t}]}":                  "chat_id",
		"notify: {channels: [{name: a, type: webhook, format: xml, url_file: /t}]}":                          "use json or text",
		"doctor: {schedule: \"every; reboot\"}":                                                              "doctor.schedule",
		"host: {power: {sleep: hybrid}}":                                                                     "host.power.sleep",
		"host: {disk: {warn_percent: 95, fail_percent: 90}}":                                                 "warn below fail",
		"notify: {token: abc}": "unknown field",
	} {
		_, err := Parse([]byte(daytwoBase+bad+"\n"), "t.yaml")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", bad, want, err)
		}
	}
}

// TestOlderConfigReadsApplied checks that a config that sets no day-two
// keys is applied without them, so an older keelbox that a rollback
// installs can parse it.
func TestOlderConfigReadsApplied(t *testing.T) {
	c, err := Parse([]byte(daytwoBase), "t.yaml")
	if err != nil {
		t.Fatal(err)
	}
	out, _ := c.Marshal()
	var doc map[string]any
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"agent_updates", "notify", "doctor"} {
		if _, ok := doc[k]; ok {
			t.Errorf("top-level %s in the applied config", k)
		}
	}
	host, _ := doc["host"].(map[string]any)
	for _, k := range []string{"power", "disk"} {
		if _, ok := host[k]; ok {
			t.Errorf("host.%s in the applied config", k)
		}
	}
}
