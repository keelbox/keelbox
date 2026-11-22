# 0057: Events pacing and service recognition

Date: 2026-11-07. Status: accepted. Amends ADR 0032 and ADR 0047.

## Context
Two weak points, without this decision:
- The events path unit could be triggered without limit (`StartLimitIntervalSec=0`), and each run would contend for the day-two lock.
- Recognising a known service from a process name or a `*.service` in any cgroup path would have adoption and `up` offer a user unit, or a process of any account named `jellyfin`, as Jellyfin.

## Decision
- **Events.** The service records when it starts (`/var/lib/keelbox/notify/events-run`) and returns at once if the last start was under 5 seconds ago, and when the agent's file has not grown past the cursor, both before it takes the day-two lock. It reads at most 2 MiB a run. The path unit stays as it is: missing a trigger costs nothing, because the next run reads from the cursor and the watch run starts the service too.
- **Recognition.** A process name counts only when `/proc/PID/status` gives a uid below 1000; a unit only from a `/system.slice/` cgroup path; a container only from the system Docker daemon. A listener of another account is shown as "run by UID N, not a system service", and `up` does not offer it as a known service.

## Consequences
- An agent writing its events file in a loop costs one short run every 5 seconds at most.
- A known service run by a login account is not recognised; its ports are still listed.
