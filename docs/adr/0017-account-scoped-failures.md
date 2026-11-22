# 0017: A failure in an account's files stops only that account

Date: 2026-11-06. Status: accepted. Amends ADR 0008.

## Context
Apply stops at the first step that fails (ADR 0008). Since ADR 0015, a link an agent plants in its home makes apply refuse the step that touches it. The earliest such step, `host/agent-ssh-dir`, runs before sshd hardening, UFW, the agent network table and the default account's lock. So an agent could stop a later release's security fix, or a config change such as closing public SSH, from ever converging, until a human repaired the home. Moving every home step after the system steps would split modules and still leave the runtime and sessions modules exposed to whatever follows them.

## Decision
- A step that acts for a non-root account sets `Step.User`. `FileStep`, `DirStep`, `LineStep` and `BlockStep` set it from the owner; module steps that run as the account or touch its home (user units, tmux plugins, the codex link, the agent's Docker config, mise tools, agent CLIs) set it explicitly.
- When such a step fails, apply records the failure and skips that account's later steps, each recorded as skipped. Steps for root and other accounts carry on. A hard failure in a root step still stops the run.
- The run exits 1 and the logbook says `partial`. Doctor reports a check that errors in an account's files as a failure, not a warning, so a broken home is never reported as fine.

## Consequences
- An agent can stop its own setup converging, but not the box's security configuration.
- Skipping the rest of the account, rather than only the failed step, keeps steps that depend on earlier ones in the same home from running against a half-applied state.
- The output lists every skipped step; the first failure is the one to fix.
