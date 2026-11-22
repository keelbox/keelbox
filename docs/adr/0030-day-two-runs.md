# 0030: Day-two runs are root oneshot units, not a daemon

Date: 2026-11-07. Status: accepted.

## Context
keelbox keeps a box working between applies: it restarts a dead tmux server, runs doctor on a schedule, sends events to the owner's phone and writes a status snapshot a laptop reads. Something has to run on the box to do that, and it must not become a second, unaudited way to change the box, block an apply, or be something the agent can stop or steer.

## Decision

### Units, not a daemon
- Each day-two job is a root system service, `Type=oneshot`, started by a timer or a path unit, that runs `/usr/local/bin/keelbox <command> --local /etc/keelbox/keelbox.yaml` against the applied config, as `keelbox-backup` does. There is no long-running keelbox process. The first is `keelbox-watch.timer` (90 s after boot, then every 2 minutes), whose run writes a heartbeat and the status snapshot and must finish in seconds.
- A day-two run never changes the config. It acts only to bring back the configured state (restart a unit) or to report (the snapshot, an event, a logbook record of kind `watch`, `doctor` or `power`, ADR 0031).
- It reads and writes a path in an account's home only as that account, through `Env.FS` (ADR 0015), like apply.
- Outbound traffic: none in the units this decision adds. Day-two runs reach only the notification channels the owner configures (ADR 0043), the backup repository (ADR 0020), and the box's apt sources for an install the agent requested (ADR 0074). keelbox has no phone-home.

### One helper for every unit
`internal/modules/rootunit.go` renders the service, timer and path units and their steps. A day-two service gets:
- `ExecCondition=/usr/local/bin/keelbox has-command <command>`. A keelbox that does not know the command, such as an older one after `keelbox rollback`, exits non-zero; systemd treats an `ExecCondition` exit from 1 to 254 as "skip this run", so a unit the installed keelbox cannot run stays quiet instead of failing every two minutes.
- `StartLimitIntervalSec=0`. A path unit whose service hits its start limit fails and stops watching. Checked on Ubuntu 26.04 (systemd 259): an account appending to a watched file 3,000 times with a fast service put the path unit into `failed` with `unit-start-limit-hit`; with no start limit and a run that takes about a second, a 20-second flood kept it active, and the service ran 20 times and saw every line.
- `Nice=5`, `CPUWeight=200`, `IOWeight=200`, `NoNewPrivileges=yes` and `TimeoutStartSec` (2 minutes by default): a busy agent does not starve the run, and a stuck run ends.
- Timers and path units are enabled and running; a changed timer is restarted so a new schedule applies at once. A schedule from the config is checked with `systemd-analyze calendar` before the timer is changed.
- The backup units use the helper too.

### Locks
- apply holds `/run/keelbox.lock` exclusively, and waits up to 30 seconds for it, since day-two runs hold it shared for a few seconds.
- A day-two run takes it shared without waiting, and exits 0 without doing anything while an apply holds it: it never acts on a half-applied box. It then takes `/run/keelbox-daytwo.lock` exclusively, so two day-two runs never work on the same state (the event queue) at once.
- A day-two run that would take long (a package install, ADR 0074) holds the shared lock only around the work that must not interleave with an apply.

## Consequences
- Between applies the box changes only in the ways each day-two run's ADR lists, and every action is in the logbook.
- An apply can wait a few seconds for a day-two run to finish.
- After a rollback to a keelbox that does not know a day-two command, its unit stays installed and skips every run until the box is applied again with a keelbox that knows it.
- A path unit watching a file in the agent's home reacts to the agent's writes, so the agent can make it run often. The runs are cheap, capped at one start per run's length, and read the file only as the agent.
- A write that lands while the service runs may not start another run; each run reads until the file stops changing, and the watch timer runs the same work every two minutes.
