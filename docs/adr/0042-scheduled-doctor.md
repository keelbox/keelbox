# 0042: Scheduled doctor alerts on changes, not on state

Date: 2026-11-07. Status: accepted. Builds on ADR 0030 (day-two runs) and 0032 (events).

## Context
`keelbox doctor` tells the owner whether the box works, but only when they think to run it. Drift and breakage between applies (a stopped timer, a full disk, an expired backup) go unnoticed until something else fails. Running doctor on a timer is cheap; telling the owner about every failing check on every run would train them to ignore it, and a check that depends on the network fails now and then for reasons that fix themselves.

## Decision
- The `doctor` module installs `keelbox-doctor.timer` (`doctor.schedule`, hourly by default, checked with `systemd-analyze calendar`; also five minutes after boot; up to five minutes' random delay) and `keelbox-doctor.service`, a day-two service (ADR 0030) running `keelbox doctor-run`, with a 5 minute limit. `doctor.schedule: off` removes both units.
- `doctor-run` runs the same `engine.Doctor` as `keelbox doctor` (every step's check, then every module's verify) while holding only the apply lock shared, so a slow run never makes the watch run or the events service skip. It then takes the day-two lock to record, queue and send.
- The result goes to `/var/lib/keelbox/doctor/last.json` (0644 root): time, keelbox, release, run time, counts, the findings that are not ok, and per check a streak and whether its alert is out. Findings are what doctor prints already, which carries no secret.
- A check (module/check, the worst finding if it appears twice) that starts failing raises `doctor-failed` (urgent) once; one that passes again, or is no longer checked, raises `doctor-cleared` (info). With `doctor.notify_warnings: true`, warnings alert too, as `doctor-warning` (warn). Checks tagged `engine.Finding.Flaky` (the tailnet connection, the last backup's age) must want an alert on two runs in a row. Each run with a change writes one `doctor` logbook record.
- The status snapshot and `keelbox status` show the last run; doctor warns when the last run is over 25 hours old or took over a minute.
- A laptop's `keelbox doctor` and `keelbox doctor --local` record nothing and never alert.

## Consequences
- Every box gets the two units, also without a channel: events then wait in the queue for a day, and `status` still shows the last run.
- A failure that comes and goes on every run alerts on every change. Only checks marked flaky are smoothed, and only by one run.
- An hourly run costs one doctor's work an hour: about as long as `keelbox doctor` on the box, mostly the account checks run as the agent.
- The agent can make account checks fail (ADR 0017); that raises an alert, which is the right outcome.
