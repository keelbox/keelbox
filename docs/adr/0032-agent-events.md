# 0032: Events: a root queue, and an agent's events file read as the agent

Date: 2026-11-07. Status: accepted.

## Context
keelbox tells the owner when an agent finishes, waits for an answer or hits a limit, and when the box needs them (a watchdog that gave up, a doctor check that started failing, a filling disk). The first kind comes from the agent CLIs' hooks, which run as the agent; the second from root's own day-two runs (ADR 0030). The agent must not be able to fake root's alerts, silence them, read the channels' credentials, or use the path to get root to read or write something on its behalf. It has no sudo, so it cannot hand an event to root directly.

## Decision

### One event type, a fixed set of kinds
`internal/events` defines `Event` (time, source, kind, level, box, a dedup key, fields) and a fixed table of kinds. Each kind names the sources that may emit it, its level and the fields it may carry, each with a check. Only `agent-*` kinds may come from the agent, and only they. `Event.Validate` enforces all of it; `Emit` refuses anything that does not pass.

### The queue
`Emit` writes one file per event in `/var/lib/keelbox/notify/queue/` (0600 root, directories 0700), named by time. `Dispatch` sends each due event to every channel (a `Sender`) that does not have it yet, retries failures after 2, 4, 8 minutes and so on (at most an hour apart), and drops an event 24 hours after it was raised. With no channel configured nothing is sent, and events wait out the 24 hours. Channels, rendering, rate limits and quiet hours belong to notifications (ADR 0043).

### The agent's events file
- The agent appends lines to `~/.local/state/keelbox/events.jsonl` (0600, its own). `AppendAgent` is what its hook command calls (ADR 0043); past 1 MiB it keeps the newest 256 KiB.
- `keelbox-events.path` (root, `PathModified=`) starts `keelbox-events.service`, which reads the file through `Env.FS(agent)`: a process with the agent's rights, so a link to a root-only file gets a permission error, not the file (ADR 0015). Reads are capped at 16 MiB.
- Root keeps a cursor in `/var/lib/keelbox/notify/cursor.json`: the offset read, a hash of the file's first 64 bytes, and the newest line time taken. A file that shrank or whose first bytes changed is read from the start, and lines not newer than the last one taken are skipped, so a trimmed or replaced file does not repeat events. An incomplete last line waits for its newline.
- Each line must decode, with no unknown field, as version 1 of `AgentLine`: an agent kind, the CLI (`claude-code` or `codex`), an 8-hex session hash, a project basename, a reason word and, only when the owner turns it on (`notify.agent_text`), at most 200 characters of printable text. Its time must be within the last hour. Root builds the event itself: source `agent`, the kind's level, the box's name, the key `agent:<session>:<kind>`, and only the checked fields. Every other line is skipped and counted.
- One run turns at most 100 lines into events, and the queue holds at most 200 agent events; the rest of a flood is skipped. Root's own events have no such cap, so an agent flood never keeps them out.

## Consequences
- The agent can write any line to its own file. The most it gets is an `agent-*` event, labelled as the agent's, at that kind's level. It cannot raise a root kind, choose a level, add a field, or (with agent text off) put its own words or links in front of the owner.
- The agent can stop its own events (stop writing, or write nothing valid). It cannot stop root's: their sources, the queue and the units are root's.
- A link or a FIFO in place of the file gets root nothing: the read runs as the agent and accepts only a regular file.
- The agent can make the path unit run often; each run is short and the unit cannot hit a start limit (ADR 0030). Its flood costs at most 100 skipped lines' work per run and 200 queue files.
- `Dispatch` holds no lock of its own: day-two runs take `/run/keelbox-daytwo.lock` (ADR 0030), so two never send the same event at once.
