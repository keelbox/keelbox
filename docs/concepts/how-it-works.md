# How keelbox works

```sh
keelbox plan acme      # list what apply would change; changes nothing
keelbox apply acme     # make the box match its config
keelbox doctor acme    # check the running box
```

Each box has one YAML [config file](../reference/config.md). Edit it, run `plan` to see the effect, then `apply`. A second apply changes nothing.

## Modules and steps

Apply runs these modules in order. Each is a list of steps: a check, and a change that makes the check pass.

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
| `doctor` | the scheduled doctor run |
| `watch` | the watch run |
| `permissions` | package installs the agent asks for |
| `agent-policy` | the agent CLIs' managed settings |
| `agents` | Claude Code and Codex, from the vendors' installers; Remote Control |

A failed step stops the apply. Two exceptions: a failure in one account's files skips only that account's later steps, and a failed agent CLI install is recorded.

## Where it runs

With `target.provider: local`, keelbox changes the machine it runs on. Otherwise it connects with your `ssh`, uploads its Linux binary and the config, and runs on the box with sudo, as a systemd unit that finishes without your computer.

Tool versions come from the [release manifest](../reference/release.md#release-manifest) inside keelbox, so a newer keelbox moves the box to newer versions at the next apply. Every apply, and every action an unattended run takes, adds a line to `/var/lib/keelbox/logbook.jsonl` on the box.

## What a box does unattended

No keelbox process stays running. systemd starts short keelbox commands as root:

| When | What |
|---|---|
| Every two minutes | The watch run writes the status snapshot and restarts the agent's tmux server if it is down; every 15 minutes it saves the tmux sessions. |
| Hourly, or on `doctor.schedule` | Doctor runs. A check that starts or stops failing raises an event. |
| When the agent asks for a package | With `users.agent.permissions: auto`, apt installs it. |
| On `backup.schedule` | The backup runs. |
| At night, when an update needs it | The watch run [reboots](../guides/upgrades.md#reboots) the idle box (`host.auto_reboot`). |

These runs reach the box's apt sources, your notification channels and the backup repository, never keelbox's own hosts. The agent CLIs [update themselves](../guides/upgrades.md#the-agent-clis).
