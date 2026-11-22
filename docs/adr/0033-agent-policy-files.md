# 0033: One builder writes the agent CLIs' policy files

Date: 2026-11-07. Status: accepted.

## Context
Some features need settings in the agent CLIs that the agent cannot change: notifications install hooks that report events (ADR 0032). Both CLIs have an administrator layer that ranks above the user's own settings: Claude Code reads `/etc/claude-code/managed-settings.json` and every `*.json` in `/etc/claude-code/managed-settings.d/`; Codex reads `/etc/codex/requirements.toml`. Both are root files the agent cannot write. If each feature wrote its own files, two features could fight over one file, and a mistake in one would be hard to see.

Verified on Ubuntu 26.04 with Claude Code 2.1.311 and Codex 0.166.0:
- Claude Code merges the drop-in files in name order after `managed-settings.json`, `env` key by key, a later file winning. A managed `env` value beats the user's `settings.json` and the shell.
- Managed hooks run even when the user's settings say `disableAllHooks: true`, and see the managed `env`.
- One drop-in file that is not valid JSON makes `claude -p` exit 1 ("Managed settings document could not be parsed; none of its settings are in effect"): no session starts and no hook fires. Unknown keys and wrong value types in valid JSON were ignored.
- A managed `DISABLE_UPDATES` also turns the official installer into a no-op that exits 0 and prints "Installation complete!". A feature that sets it must install Claude Code another way.
- Codex ignores unknown keys in `requirements.toml`, but a value of the wrong type stops it starting ("invalid type: string \"no\", expected a boolean"). `check_for_update_on_startup = false` there beats `true` in the user's `config.toml`. Managed `[[hooks.EVENT]]` hooks run without the trust prompt the user's own hooks get.

## Decision
- `internal/modules/agentpolicy.go` holds the builder, `AgentPolicy`. A feature adds one `policySource` to `policySources`, which calls `AddEnv`, `SetClaude`, `SetCodex` or `AddHook`. It writes no policy file itself.
- The `agent-policy` module renders the builder and runs before `agents`, so a CLI is never installed or upgraded without its policy. It is enabled while any source exists; notifications' hooks (ADR 0043) are one.
- Claude Code gets two keelbox-owned files: `50-keelbox.json` (env and settings) and `51-keelbox-hooks.json` (hooks). Both are rendered with `encoding/json` from typed values, so they always parse. Other files in the directory are left alone; an organisation's policy can come before or after keelbox's by name.
- Codex has one file. keelbox writes `requirements.toml` only when it is absent or starts with keelbox's marker. A foreign file is left alone and doctor warns, naming the requirements keelbox could not set. Codex values are booleans, integers and quoted strings from a small typed renderer with TOML escaping, so a value can never have the wrong type.
- Every input is checked: env names, one-line printable values, camelCase setting names other than `env` and `hooks`, snake_case Codex keys at most one table deep, event names, and hook commands that start with an absolute path. A bad input is keelbox's bug: apply stops before writing any policy file.
- Settings for a CLI not in `agents` are dropped. A file left with nothing to set is removed. Files are 0644 root, since the CLIs read them as the agent.
- Doctor fails when any `*.json` in the drop-in directory (or `managed-settings.json`) is not a JSON object, whoever wrote it, because then Claude Code starts no session.

## Consequences
- The agent cannot change or skip what these files set: the files are root's, and both CLIs rank them above the user's settings. The ADR of each feature says what it sets.
- keelbox never writes `managed-settings.json` or an organisation's `requirements.toml`. On a box with its own Codex requirements, keelbox's Codex settings are absent until the owner merges them by hand.
- A setting a source drops stays until the next apply renders without it. With no source the module is disabled and leaves its files where they are.
