# 0062: Agent-filed keelbox reports, sent only by the owner

Date: 2026-11-10. Status: accepted. Builds on ADR 0032 (agent events) and ADR 0043 (notification secrets).

## Context
An agent that works around a keelbox problem leaves the owner of the box to learn of it only by asking. Agents should be able to report keelbox problems. Anything like that must not look or act like telemetry: an agent CLI's text can carry what it read, a repository's instructions can steer it, and notification services are third parties.

## Decision
- **Consent in the conversation.** The primer tells the agent to say what blocked it and ask the user whether to file a keelbox report, and never to file one without that yes. One report per problem, the exact error, no code, secrets or file contents beyond it. keelbox cannot check the yes; the rest is built so that a report filed without one costs nothing.
- **A local draft.** `keelbox report TITLE [--detail TEXT|-] [--area AREA]`, run as the agent, appends one line to the agent's own events file (ADR 0032's path: written with the agent's rights, read by root as the agent, every field checked again). Root's events service puts it in `/var/lib/keelbox/reports/` (root, 0700; files 0600) with a fixed list of box facts: keelbox version and release, Ubuntu version, architecture, machine size, failing doctor check names. At most 5 a day and 30 kept; the title one line of 120 characters, the detail 2000. No event is queued and no notification sent.
- **Only the owner sends.** `keelbox reports BOX` lists and shows drafts. `--send N` runs on the laptop: it reads the draft over SSH, replaces token and key shapes, email addresses, tailnet names and addresses, public IPs, the box name, home paths and project names under `~/projects` unless the owner keeps them, shows the whole text and what was replaced, offers `$EDITOR` (and redacts the edit again), and asks y/N with a reminder that the issue is public. Then it opens GitHub's new-issue form, prefilled, in the browser; the owner submits it there. keelbox posts nothing and holds no token. Security-area reports open the private advisory form.
- **Off switch.** `reports.enabled` (default true). When false the primer has no reports section, the status snapshot tells the agent's `keelbox report` to refuse, and root drops report lines.

## Consequences
- Reports never leave the box unless the owner sends them; keelbox has no telemetry, and the docs say so.
- An agent can still fill the inbox with drafts it was not asked for, within the budget, and can put anything in their text. The owner reads every draft before it goes anywhere, redacted, and decides.
- Redaction is pattern-based and misses secrets of other shapes; the preview and the explicit yes are the control, and the security model says so.
- The prefilled link carries at most about 7,000 characters; a longer body is cut and the owner pastes the rest.
