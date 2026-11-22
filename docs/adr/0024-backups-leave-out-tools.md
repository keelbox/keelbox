# 0024: Backups leave out reinstallable agent CLIs by default

Date: 2026-11-06. Status: accepted. Amends the default list in ADR 0020.

## Context
On a fresh box the agent's home is mostly the agent CLIs' own release directories: `~/.codex/packages/standalone/releases` (about 516 MiB) and `~/.local/share/claude/versions` (about 325 MiB). Every backup carried them, though apply reinstalls them: `~/.local/bin/claude` and `codex` are symlinks into those directories, so with the target gone `command -v` fails and apply runs the installer again.

## Decision
`backup.exclude` defaults add `.local/share/claude/versions` and `.codex/packages` to the existing list (`node_modules`, caches, the mise and pnpm stores, rootless container storage). Sign-ins and settings (`~/.claude`, `~/.codex/auth.json`, `~/.config`) stay in. Setting `backup.exclude` still replaces the whole list, so a user who wants the CLIs backed up repeats the list without those two entries.

## Consequences
A restored home needs one `keelbox apply` to bring the CLIs back. Backups shrink by about 800 MiB on a fresh box.
