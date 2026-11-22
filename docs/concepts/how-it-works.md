# How keelbox works

```sh
keelbox plan acme      # list what apply would change
keelbox apply acme     # make the box match its config
keelbox doctor acme    # check the running box
```

One YAML [config file](../reference/config.md) describes a box, and these three commands work from it.

## Modules and steps

Apply runs these modules in order. Each is a list of steps: a check, and a change that must make the check pass. A second apply changes nothing.

| Module | Sets up |
|---|---|
| `host` | packages, the admin and agent accounts, key-only SSH, the firewall |
| `access` | Tailscale |
| `runtime` | Docker, the GitHub CLI, Node and pnpm |
| `sessions` | tmux, restored after a reboot |
| `primer` | the notes agents read about the box |
| `logbook` | keelbox's state and its binary |
| `previews` | dev servers on the tailnet |
| `backup` | restic and its timer |
| `notify` | notification channels |
| `doctor` | the hourly doctor run |
| `watch` | the watch run |
| `permissions` | package installs the agent asks for |
| `agent-policy` | the CLIs' managed settings |
| `agents` | Claude Code and Codex, from the vendors' installers; Remote Control |

A failed step stops the apply, with two exceptions. A failure in one account's files skips that account's later steps only, and a failed agent CLI install is recorded.

## Where it runs

With `target.provider: local`, keelbox changes the machine it runs on. Otherwise it connects with your `ssh`, uploads its Linux binary and the config, and runs there with sudo. That apply is a systemd unit on the box, and finishes without your computer.

## The release manifest

Tool versions come from the [release manifest](../reference/release.md#release-manifest) inside the binary. A newer keelbox moves the box to newer versions at the next apply.

## The logbook

Every apply appends one JSON line to `/var/lib/keelbox/logbook.jsonl` on the box: the time, the release, the result and each step changed or failed. So does each action an unattended run takes.

## What a box does unattended

No keelbox process stays running. systemd starts short keelbox commands as root:

| When | What |
|---|---|
| Every two minutes | The watch run writes the status snapshot and restarts the agent's tmux server if it is down. Every 15 minutes it saves the tmux sessions. |
| Every hour | Doctor runs. A check that starts or stops failing raises an event. |
| The agent asks for a package | With `users.agent.permissions: auto`, apt installs it. |
| On `backup.schedule` | The backup runs. |
| At night, for an update | The watch run [reboots](../guides/upgrades.md#reboots) an idle box (`host.auto_reboot`). |

These runs reach the box's apt sources, your notification channels and the backup repository, never keelbox's own hosts. The agent CLIs [update themselves](../guides/upgrades.md#the-agent-clis) from their vendors, as on any computer.

Ubuntu's unattended upgrades and Tailscale run too.
