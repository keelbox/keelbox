# 0043: Notifications: owner-chosen channels, managed hooks, budgets for the agent

Date: 2026-11-07. Status: accepted. Builds on ADR 0030 (day-two runs), 0032 (events), 0033 (agent policy files) and 0042 (scheduled doctor).

## Context
ADR 0032 gave the box a queue of events, from root's day-two runs and from the agent's own events file, and no way to deliver them. The owner wants to hear on their phone when an agent finishes, waits for an answer or hits a limit, and when the box needs them, without being woken at night for less than an emergency. The agent must not be able to read the channels' credentials, fake keelbox's own alerts, or use the owner's phone (and their channel's quota) as a firehose. The agent CLIs report what they are doing only through hooks, which run as the agent.

Vendor facts this rests on (verified on Ubuntu 26.04 with Claude Code 2.1.311 and Codex 0.166.0): managed Claude Code hooks in `/etc/claude-code/managed-settings.d/` fire even with the agent's `disableAllHooks: true`; `StopFailure` fires with an `error` field (`authentication_failed` seen with a bogus key); Codex runs `[[hooks.EVENT]]` hooks from `/etc/codex/requirements.toml` without a trust prompt. Whether a claude.ai plan usage limit ends a turn as `StopFailure` with `error: rate_limit` is not documented; the mapping below reports a limit either way.

## Decision

### Channels
- `internal/notify` implements `events.Sender` for four types: ntfy (JSON publish to the server's root, so the topic travels in the body), Pushover (form post, priority -1/0/1, never the emergency priority 2), Telegram (Bot API `sendMessage`, info silent) and webhooks (`json` with a `text` line, or `text/plain`). Standard library only; one HTTP client with a 10 second limit and no redirects.
- Plain `http://` only to loopback and the tailnet (addresses in 100.64.0.0/10 and fd7a:115c:a1e0::/48, `*.ts.net`, `localhost`); everything else must be `https://`. This is checked in the config for ntfy servers and at send time for a webhook URL, which lives in a secret file.
- Secrets are only ever files named by `*_file` keys, mode 600. Over SSH they are staged in `/run/keelbox` like backup secrets; the `notify` module copies each to `/etc/keelbox/notify/<channel>.<key>` (0600, directory 0700, root) and removes copies no channel names. The senders read only those copies. `keelbox notify test` also reads the files the given config names when they are on the box and only root can read them, so a channel can be tested before it is applied.
- A failed send returns a `SendError` built from fixed words: the channel, its type, the HTTP status or a named network problem, and what to check. Never the URL (a Telegram bot token is in its path; a webhook URL is its secret), a header or the response body. `*url.Error` text is never shown.

### What the owner hears
- Rendering is a fixed template per kind. An agent event is titled `<box> · <cli>`; only root's events say `keelbox`. Agent text appears only with `notify.agent_text: true`, and root drops any `text` field otherwise, whatever the agent wrote.
- Each channel skips muted kinds and kinds below its `level`, which counts as delivered. `agent-error` is sent by default: the turn ended and the agent is idle, which the owner wants as much as `agent-done`; `notify.mute` turns it off.
- Quiet hours (the box's timezone) hold events below urgent: a sender returns `events.HoldError`, and `events.Dispatch` keeps the event until the end without counting a failure and marks it held. When held events are due together, a sender that is an `events.Digester` gets them as one message.
- Each send is recorded per channel in `/var/lib/keelbox/notify/last.json` (time, kind, the `SendError` text); `status` and doctor read it. Doctor never sends.

### The agent path
- The `notify` policy source (ADR 0033) adds managed hooks when any channel exists: Claude Code `Stop`, `StopFailure` and `Notification`; Codex `Stop` and `PermissionRequest`. Each runs `/usr/local/bin/keelbox event --from <cli>` (plus `--text` with `agent_text`), async, 10 second timeout.
- `keelbox event` runs as the agent, reads no config, reads at most 64 KiB of stdin for at most 2 seconds, and keeps only the event name, `notification_type` or `error` (from fixed lists; an unknown error becomes `unknown`), an 8-hex hash of the session id and the working directory's base name with unsafe characters replaced. It appends one line through `events.AppendAgent` and always exits 0.
- Mapping: `Stop` is `agent-done`. `StopFailure` with `rate_limit`, `billing_error`, `authentication_failed`, `oauth_org_not_allowed` or `account_on_hold` is `agent-limit`; other errors `agent-error`. `Notification` `permission_prompt`, `idle_prompt`, `elicitation_dialog`, `elicitation_url_dialog` or `agent_needs_input` is `agent-needs-input`; `quota_auto_resume_*` is `agent-limit`. Codex `PermissionRequest` is `agent-needs-input`. If a plan usage limit does not arrive as `rate_limit`, it still arrives, as `agent-error` with its reason, and the quota notifications still report a limit.
- Root budgets agent events before they are queued (`/var/lib/keelbox/notify/ratelimit.json`): one per session and kind per 2 minutes; no `agent-done` within 30 seconds of a `needs-input` for the session; at most 30 an hour and 100 a day, then one `notify-throttled` an hour. Root's events have no budget.

## Consequences
- The agent can make its own events (any `agent-*` kind, at that kind's level, labelled as its CLI's), up to the budget. It cannot raise a root kind, pick a level, add a field or, with `agent_text` off, put its own words in front of the owner. It cannot read the secret copies or the queue.
- The agent can stop its own events (kill its CLI, break its hook input). It cannot remove the managed hooks or stop root's events.
- The agent can exhaust ntfy.sh's per-IP visitor limit by talking to ntfy.sh itself; the guide recommends a second channel for urgent events, or an ntfy account token.
- Channels see the box name, project names and timing. The guide says so.
- With channels configured, every agent turn ends with a short `keelbox event` process as the agent.
