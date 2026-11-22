# 0055: cron and at closed to the agent

Date: 2026-11-07. Status: accepted. Amends ADR 0048.

## Context
Ubuntu's cron is installed and open to every account. Its PAM stack has no `pam_systemd`, so a job runs in `system.slice/cron.service`, outside `user-UID.slice` and the agent's CPU and memory limits. `atd`, where installed, is the same. Cron is also a persistence path outside the units keelbox looks after. The agent network table matches on UID, so it still applies.

cron and at both read a deny file when there is no allow file. cron still runs an existing crontab of a denied user, and `crontab -u` then refuses that user even to root.

## Decision
- When `/usr/bin/crontab` or `/usr/bin/at` is installed, apply adds a line naming the agent to `/etc/cron.deny` or `/etc/at.deny` (other lines kept), then removes any crontab of the agent's from `/var/spool/cron/crontabs/` (`crontab -l -u` and `-r -u` refuse a denied account even to root, so the spool is read and the file unlinked directly) and its at jobs (`atq`, `atrm`).
- An `/etc/cron.allow` or `/etc/at.allow` that names the agent is the owner's: apply leaves it, and that step fails with the line to remove. Other steps go on.
- Doctor fails `agent-no-cron` or `agent-no-at` while the agent may schedule jobs or has one.
- The primer tells the agent to use a systemd user timer, which runs in its user manager, inside its slice.
- With `users.agent.sudo: true` the agent can undo this; the limits do not bind it anyway.

## Consequences
- Scheduled work the agent set up through cron stops at the first apply; the apply output names the step.
- A `pam_systemd` drop-in for cron would have put jobs in the agent's slice, but changes a PAM stack for every account and does not cover at.
- Checked by sim tests and the end-to-end test: `crontab -l` as the agent is refused, and a crontab root writes for it fails doctor until apply removes it.
