# Notifications

The box sends nothing until its config has a notification channel: a webhook, a Telegram bot, ntfy or Pushover.

## Add a channel

Keep each channel's secret in a mode 600 file that the config names. For a webhook, any address that takes an HTTPS POST:

```sh
umask 077
cat > ~/.config/keelbox/webhook.url    # paste the address, Enter, then Ctrl-D
```

```yaml
notify:
  channels:
    - {name: team, type: webhook, format: json, url_file: ~/.config/keelbox/webhook.url}
```

Then run `keelbox apply acme`. `format: json` posts an object with a `text` field, the field a Slack incoming webhook reads. The fields for each channel type are under [channels](../reference/config.md#channels).

## Test it

```sh
keelbox notify test acme                  # every channel
keelbox notify test acme --channel team   # one channel
```

The test ignores mutes and quiet hours, and exits 1 if a channel fails. `keelbox status acme` shows the last message sent and how many wait.

## What is sent

The box sends a message when an agent finishes a turn, waits for your answer or stops on a usage limit or error; when a scheduled doctor check starts failing or passes again; when a disk fills, memory runs out or the box comes back after an unclean shutdown; when updates or a reboot wait; when an apply ends with no computer following it; and when a package request waits for `keelbox approve`.

Each kind has a level (info, warn or urgent). A channel's `level` is the lowest it receives. `notify.mute` drops kinds you do not want, and during `notify.quiet_hours` messages below urgent wait for a digest. Every kind is listed under [events](../reference/config.md#events).

An agent's message carries the box name, the CLI, the kind and reason, the project directory's name and the time. Prompts, conversations and file contents stay on the box. Each channel is a third party that sees what a message carries.
