# 0083: Memory that ran out is reported

Date: 2026-11-16. Status: accepted. Builds on [ADR 0032](0032-agent-events.md) and [ADR 0082](0082-default-memory-ceiling.md).

## Context
When the kernel stops processes of the agent's for memory, the user should not have to find out by asking the agent what happened. A doctor count of the kills (`agent-throttling`, ADR 0048) is seen once an hour at best, and raises no event. The agent needs to know what memory it has on every box, not only a small one.

## Decision
- **An event, `out-of-memory`, level warn.** The watch run raises it when the kill count of the agent's slice went up since the run before, so within a couple of minutes. The count is the slice's own (`memory.events`), which has every kill in it, at the ceiling or because the whole box ran out.
- **It names what was stopped.** Root reads the kernel log since the run before and lists up to four of the agent's processes with the memory each held: `node (3.4 GB)`. A process chooses its own name, so anything but letters, digits and a few marks becomes `?`. Without the log the event still gives the count.
- **Doctor warns for a day after the last kill**, then reports the count in a line that passes. `keelbox status` gives the count and when the last one was.
- **The primer says what the agent has, on every box:** the size its work shares, what happens at the ceiling, where it slows down if the config's ceiling has a mark, and to look at `keelbox status` before starting more. Without a ceiling it says what running out does.

## Consequences
- The names are in the notification, which goes to the channels the user set up. They are process names, not arguments.
- A kill between the last watch run and a reboot is reported after the reboot, since the count starts again: all of it is then new.
