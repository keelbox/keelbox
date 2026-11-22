# Notifications

```sh
keelbox notify test acme
```

Optional: the box sends nothing until the config has a channel. `keelbox status acme` shows what the box itself reports either way.

## Add a channel

A channel is a webhook, a Telegram bot, ntfy or Pushover. A webhook is any address that takes an HTTPS POST, kept in a file:

```sh
umask 077
cat > ~/.config/keelbox/webhook.url    # paste the address, Enter, then Ctrl-D
```

```yaml
notify:
  channels:
    - {name: team, type: webhook, format: json, url_file: ~/.config/keelbox/webhook.url}
```

Then `keelbox apply acme`. `format: json` posts an object whose `text` field is the one a Slack incoming webhook reads; keelbox's tests post to no such service.

A channel's secrets are in files, mode 600, that the config names. The fields of each channel type, `notify.mute` and `notify.quiet_hours` are in the [config reference](../reference/config.md).

## What is sent

| When | Kind | Level |
|---|---|---|
| An agent finished a turn | `agent-done` | info |
| An agent waits for an answer or a permission | `agent-needs-input` | warn |
| Claude Code stopped on a usage limit, billing or sign-in error | `agent-limit` | warn |
| Claude Code stopped on another API error | `agent-error` | info |
| A scheduled doctor check started failing | `doctor-failed` | urgent |
| That check passes again | `doctor-cleared` | info |
| A disk reached `host.disk.warn_percent` | `disk-warn` | warn |
| A disk reached `host.disk.fail_percent` | `disk-full` | urgent |
| Memory ran out and the kernel stopped processes of the agent's | `out-of-memory` | warn |
| The box is back after an unclean shutdown | `power-back` | warn |
| Security updates wait, or a reboot is needed | `updates-pending` | warn |
| An apply ended with no computer following it | `apply-finished`, `apply-failed` | info, urgent |
| A package request waits for `keelbox approve` | `install-request` | warn |

Doctor is keelbox's hourly check of the box. The config reference lists the remaining kinds. A channel's `level` is the lowest level it gets.

An agent's message carries the box name, the CLI, the kind and its reason, the project directory's name and the time. The prompt, the conversation and file contents stay on the box. Every channel is a third party that sees what a message carries.

## Test it

`keelbox notify test acme` sends a test message on every channel, and `--channel team` on one. It ignores mutes and quiet hours, and exits 1 if a channel fails.

`keelbox status acme` has a `notify` row with the last message sent and how many wait.
