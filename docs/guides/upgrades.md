# Update keelbox and the agent CLIs

```sh
keelbox update acme    # keelbox on this computer, then the box, in one command
```

The same in steps, to look before the box changes:

```sh
keelbox self-update    # keelbox on this computer; lists the boxes still on an older one
keelbox plan acme      # what the new release would change on the box
keelbox apply acme     # move the box to it
```

Every keelbox carries a [release manifest](../reference/release.md#release-manifest): the versions of Docker, Node, pnpm and the tmux plugins it was tested with. Apply makes a box match its config and that manifest.

The apply installs the new keelbox on the box and keeps the old one for a rollback.

## What updates between applies

| What | How | To change it |
|---|---|---|
| Ubuntu security updates | unattended-upgrades, daily | `host.unattended_upgrades: false` |
| A reboot those updates need | the box, nightly or weekly once idle: [reboots](#reboots) | `host.auto_reboot` |
| Claude Code and Codex | themselves, as on any computer | [the agent CLIs](#the-agent-clis) |

keelbox on the box, and every other pinned tool, moves only when you apply.

## Reboots

Some updates take effect only at a reboot. `keelbox up` asks when: nightly (the default), weekly on Sunday night, or manually. It waits until the box has been idle for 30 minutes. Programs in tmux stop; Claude Code and Codex start again in their conversations. Idle: [auto_reboot](../reference/config.md#auto_reboot). To reboot now: `keelbox reboot acme`.

## The update check

Once a day at most, a command you type at a terminal reads the [channel index](../reference/release.md#the-stable-channel) while it runs. When a newer keelbox is out, it says so once that day, with the command that updates it:

```text
keelbox: keelbox v0.1.1 is out; this is v0.1.0. Update: keelbox self-update
```

When the release you run has a security notice, it says so after every command until you update. `keelbox status acme` also says when keelbox on your computer last applied a release with a notice to acme, with `keelbox update acme`. It knows only the applies made from this computer.

The check never downloads or installs anything, and it waits at most a second at the end of a command. It does not run on a box, in CI, for a development build, or for `ssh`, `attach` and `login`. `KEELBOX_NO_UPDATE_CHECK=1` or `DO_NOT_TRACK=1` turns it off, and the first check says so. What the request sends is in the [security model](../security-model.md#what-keelboxs-hosts-learn).

## The downgrade guard

An apply from a keelbox whose release is older than the box's stops before any change, with `the box is on release ...; this keelbox carries the older release ...`. Run `keelbox self-update` on that computer.

To move the box back on purpose:

```sh
keelbox apply acme --allow-downgrade
```

The guard compares release ids, not keelbox versions.

## Rollback

```sh
keelbox rollback acme
```

This applies the box again with the keelbox it ran before.

A rollback leaves the agent CLIs as they are, and projects, containers, volumes and sign-ins too.

The older keelbox refuses a config key it does not know. Remove the key, apply, then roll back.

The next `keelbox apply` from this computer moves the box forward again. To stay on the older release, install that keelbox here:

```sh
curl -fsSL https://get.keelbox.dev | sh -s -- --version vX.Y.Z
```

## The agent CLIs

keelbox installs Claude Code and Codex with each vendor's own installer, as the agent, into its home. From then on they update as they would on your laptop: Claude Code updates itself in the background, and Codex offers an update when it starts. To update either now, on the box:

```sh
claude update
codex update
```

To stop Claude Code updating itself, set `DISABLE_AUTOUPDATER` to `1` in the `env` of the agent's `~/.claude/settings.json`; `claude install VERSION` installs a particular version. Codex asks before it updates, and `check_for_update_on_startup = false` in the agent's `~/.codex/config.toml` stops it asking. apply checks that each CLI is installed and runs, and never changes its version; one that does not run is installed again at the vendor's current release.
