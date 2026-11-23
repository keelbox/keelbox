# Update keelbox and the agent CLIs

```sh
keelbox update acme    # keelbox on this computer, then the box, in one command
```

To look before the box changes, do it in steps:

```sh
keelbox self-update    # keelbox on this computer; lists the boxes still on an older one
keelbox plan acme      # what the new release would change on the box
keelbox apply acme     # move the box to it
```

Each keelbox carries a [release manifest](../reference/release.md#release-manifest) of the Docker, Node, pnpm and tmux plugin versions it was tested with. Apply makes the box match its config and that manifest, installs the new keelbox on the box, and keeps the old one for a rollback. Between applies, only these update on their own:

| What | How | To change it |
|---|---|---|
| Ubuntu security updates | unattended-upgrades, daily | `host.unattended_upgrades: false` |
| A reboot those updates need | the box, once idle: [reboots](#reboots) | `host.auto_reboot` |
| Claude Code and Codex | themselves | [the agent CLIs](#the-agent-clis) |

Everything else moves only when you apply.

## Reboots

Some updates take effect only after a reboot. `keelbox up` asks when the box may reboot for them: nightly (the default), weekly on Sunday night, or manually. Nightly and weekly reboots wait until the box has been [idle](../reference/config.md#auto_reboot) for 30 minutes. Programs in tmux stop; Claude Code and Codex reopen their conversations. To reboot now, run `keelbox reboot acme`.

## The update check

At most once a day, a command you type at a terminal reads the [channel index](../reference/release.md#the-stable-channel) and says when a newer keelbox is out:

```text
keelbox: keelbox v0.1.1 is out; this is v0.1.0. Update: keelbox self-update
```

If the release you run has a security notice, keelbox says so after every command until you update. `keelbox status acme` also says when this computer last applied a release with a notice to acme, and names `keelbox update acme`.

The check never downloads or installs anything and adds at most a second. It does not run on a box, in CI, for a development build, or for `ssh`, `attach` and `login`. `KEELBOX_NO_UPDATE_CHECK=1` or `DO_NOT_TRACK=1` turns it off. What it sends is in the [security model](../security-model.md#what-keelboxs-hosts-learn).

## The downgrade guard

An apply from a keelbox older than the box's release stops before any change, with `the box is on release ...; this keelbox carries the older release ...`. Run `keelbox self-update` on that computer, or move the box back on purpose:

```sh
keelbox apply acme --allow-downgrade
```

The guard compares release ids, not keelbox versions.

## Rollback

```sh
keelbox rollback acme
```

This applies the box again with the keelbox it ran before. The agent CLIs, projects, containers, volumes and sign-ins stay as they are. If the older keelbox refuses a config key it does not know, remove the key, apply, then roll back.

The next `keelbox apply` from this computer moves the box forward again. To stay on the older release, install it here:

```sh
curl -fsSL https://get.keelbox.dev | sh -s -- --version vX.Y.Z
```

## The agent CLIs

keelbox installs Claude Code and Codex with each vendor's installer, into the agent's home. They then update as on your laptop: Claude Code in the background, Codex by offering when it starts. To update now, on the box as the agent, run `claude update` or `codex update`.

- To stop Claude Code updating, set `DISABLE_AUTOUPDATER` to `1` in the `env` of the agent's `~/.claude/settings.json`. `claude install VERSION` installs a given version.
- To stop Codex asking, set `check_for_update_on_startup = false` in the agent's `~/.codex/config.toml`.

Apply never changes a CLI's version. It reinstalls one that does not run, at the vendor's current release.
