# 0023: Claude Code Remote Control as the agent's user service

Date: 2026-11-06. Status: accepted.

## Context
Users want to start and steer Claude Code sessions on the box from the Claude app on a phone, without SSH. Claude Code's Remote Control does this. Facts from [Anthropic's Remote Control docs](https://code.claude.com/docs/en/remote-control), read 2026-11-06, and checked on a VM:
- `claude remote-control` is server mode: it stays running and starts sessions on demand. Flags used here: `--spawn same-dir|worktree` (how sessions start) and `--remote-control-session-name-prefix` (default the hostname). Others: `--name`, `--capacity` (default 32), `--permission-mode`, `--continue`, `--session-id`, `--debug-file`.
- It needs a claude.ai sign-in on a Pro, Max, Ultra, Team or Enterprise plan (Team and Enterprise need an owner to allow it); API keys do not work. Without a sign-in it exits 1: "You must be logged in to use Remote Control." `claude auth status` exits 1 when not signed in.
- First run asks on the terminal to enable Remote Control and to trust the directory; without a terminal it exits ("Workspace not trusted"). Trust is never saved for the home directory.
- Outbound HTTPS only; nothing listens. After about ten minutes without network it exits. Sessions are listed in claude.ai/code and the Claude app's Code tab; transcripts are kept by Anthropic.
- Unavailable with `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`, Bedrock, Vertex, a custom `ANTHROPIC_BASE_URL`, or zero-data-retention organisations.

## Decision
- Opt in: `sessions.remote_control.enabled: true`, with `directory` (default `projects` in the agent's home) and `spawn` (default `same-dir`). Needs `claude-code` in `agents`.
- Apply writes `~/.config/systemd/user/keelbox-claude-remote-control.service` for the agent (as the agent, ADR 0015; its steps are the agent's, ADR 0017), creates the directory and enables the unit. The unit runs `claude remote-control --spawn <spawn> --remote-control-session-name-prefix <box>` in the directory with `Restart=on-failure`, `RestartSec=30`, at most six starts an hour.
- `ExecCondition` runs `claude auth status` and uses only its exit code. Until the user signs in, systemd skips the unit (inactive, not failed, no restart loop). keelbox never signs in, never reads Claude Code's credential files and never answers its questions.
- `keelbox login`, after the user's own Claude Code sign-in, starts the service; if it does not stay up, it runs the same command on the user's terminal so they answer the two first-run questions themselves, then hands over to the service.
- `keelbox doctor`: a warning "waiting for your Claude Code sign-in" before sign-in; ok when running; a failure with the next step when signed in but not running. `keelbox sessions` and `keelbox status` show the state.

## Consequences
- Anyone signed in to the user's claude.ai account can run commands as the agent on the box, in the directory and wherever the agent's permissions reach. That is the same reach as the agent's own Claude Code; the box gains no listening port.
- Session content goes through Anthropic. Leave this off where that is not acceptable.
- A live run needs a real claude.ai sign-in, so it is not in the end-to-end test: sim tests cover the unit, its states and doctor, and the e2e checks the unit is installed and waits, with doctor saying so.
