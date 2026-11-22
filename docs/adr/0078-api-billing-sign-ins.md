# 0078: Sign-ins for API billing

Date: 2026-11-16. Status: accepted. Builds on [ADR 0014](0014-login-and-sessions.md).

## Context
`keelbox login` runs one sign-in for each agent CLI: `claude auth login`, which is a Claude plan, and `codex login --device-auth`, which is a ChatGPT account. Both vendors have a second way to pay, by use through their API: an Anthropic Console account, and an OpenAI API key. Someone with API credits and no plan needs a route in keelbox too, or they skip the sign-in and nothing says what to run on the box.

Codex takes a key with `codex login --with-api-key`, which reads its input up to the end. Typed at a terminal the key shows on the screen, stays in the scrollback, and needs Ctrl-D after it.

## Decision
- **A fourth choice.** Where keelbox asks about the Claude Code or the Codex sign-in, the menu has "Use a Console account" or "Use an API key" after the other three, which keep their numbers. Tailscale and GitHub have three.
- **`--api` with a named sign-in.** `keelbox login BOX codex --api` and `keelbox login BOX claude-code --api` run it without the question. They run it where already signed in too, since naming it is asking for it. `--api` with no sign-in, or with one that has no such route, exits 2 with the command to run.
- **Claude Code runs `claude auth login --console`**, the vendor's flow in a browser.
- **Codex's key is asked for on the box with the echo off.** The line keelbox runs over SSH prints a prompt, turns the terminal's echo off, reads one line into a shell variable, turns echo back on, and pipes the variable to `codex login --with-api-key` with the shell's own `printf`. The key is in no command's arguments and no environment. Ctrl-C at the prompt restores the echo and ends the step.
- **keelbox on the user's computer reads nothing.** It hands SSH the terminal, as ADR 0014 has it, and uses the exit code of `codex login status` after. The flag takes no value, so no form of the command carries a key.
- **Where the key ends up is Codex's choice**: `~/.codex/auth.json`, mode 600, the agent's. The agent can read it, like every sign-in of its own. The line before the prompt says so.

## Consequences
- The shell line that takes the key is keelbox's, between the user and the vendor's CLI. It is the one sign-in where that is so. It is in `internal/cli/interactive.go`, with a test that Codex receives exactly what was typed.
- A provider other than the vendor's own, reached with a key in the agent's environment and the CLI's own configuration, is set up by hand on the box. Codex reports such a setup as not signed in, so keelbox lists that sign-in as left to do.
