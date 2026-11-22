# 0034: Day-two config keys are checked, and never in the applied config by default

Date: 2026-11-07. Status: accepted.

## Context
Notifications, scheduled doctor, sleep and disk thresholds each have config. The applied config (`/etc/keelbox/keelbox.yaml`) is what `keelbox rollback` hands to the previous keelbox, which parses it strictly: one unknown key and the rollback stops.

## Decision
- One file per section: `notify.go`, `doctor.go`, `power.go` (`host.power`, `host.disk`). Each is parsed strictly, validated and documented in `docs/reference/config.md`.
- Every day-two field is `omitempty`, and defaults come from accessors (`DoctorSchedule()`, `DiskThresholds()` and so on). Defaults are never written into the struct, so a config that sets no day-two key marshals without any, and its applied config and config hash carry none.
- `notify.mute` is checked against the event kinds in `internal/events`. Channel secrets are named only by `*_file` keys, and a key the channel's type does not take is an error. Plain `http://` ntfy servers are accepted only on loopback or tailnet addresses. There is no email channel.

## Consequences
- A config that uses no day-two key rolls back to a keelbox that does not know them. One that sets any must drop those keys first; that keelbox's error names the key.
