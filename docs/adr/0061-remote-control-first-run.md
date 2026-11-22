# 0061: Remote Control's first run is the owner's step, not a failure

Date: 2026-11-10. Status: accepted. Amends ADR 0023 (Remote Control).

## Context
Claude Code's own "Trust /home/agent/projects?" question appears during `keelbox login` with no keelbox context, and is easily answered no. The server then exits at once each time systemd starts it, so the next apply would fail (`claude-remote-control-service: still not converged`, a soft step, so the run is `partial`) and `keelbox up` would stop before Ready. A check that looks during a restart reports "does not stay running" a second before the service is active. And without a terminal the server redraws its status line every second into the journal, about 400,000 lines a day.

## Decision
- **Say what Claude Code will ask, before it asks.** Before the terminal run: "Enable Remote Control?" answer y; "Trust ~/projects?" answer y, keelbox made this folder for Remote Control sessions; when it shows it is connected, press Ctrl-C and keelbox runs it as a service that keeps working without the terminal.
- **Keep Ctrl-C.** keelbox hands the vendor's run the terminal and reads nothing it prints (ADR 0014), so it cannot see "connected" and end the run itself. Reading Claude Code's state files instead would depend on undocumented keys we cannot check without an account.
- **Wait for the service, then confirm.** After the terminal run keelbox resets the unit's failures, starts it, waits up to 45 seconds for `active (running)`, then checks it is still running, with no restart in between, 6 seconds later.
- **Declining is a choice, offered again in place.** When it does not run, keelbox says what is missing and asks `Try again now? [Y/n]`; no leaves `keelbox login BOX claude-code` for later. Apply defers the service step ("needs your answers on a terminal once") instead of failing it, so the run is `ok`; doctor warns `needs you` instead of failing.
- **Ask after the Claude Code sign-in, in context.** `keelbox up` does not ask about Remote Control with the other choices, where it read as unrelated to setting up a box. Unless `--remote-control` answers it, `up` asks once Claude Code is signed in, as one more way to reach the agents: what it gives (sessions from the Claude app or claude.ai/code without SSH or a terminal), what it means (a service of the agent account; anyone signed in to the Claude account can run commands as the agent) and how to turn it on later. A yes sets `sessions.remote_control.enabled: true` in the box config, changing only that line; the last apply of `up` installs the service and `up` then starts it as above. With Claude Code not signed in, `up` does not ask.
- **No stdout in the journal.** The unit sets `StandardOutput=null` and keeps `StandardError=journal`.

## Consequences
- A wrong answer to an optional feature's question does not fail apply or doctor.
- A server that fails for another reason (no Remote Control on the account) shows the same `needs you` warning, with the unit's state and the journal command; its errors are still in the journal.
- What the server prints when connected, and its exit code after a declined trust question, are not documented. The flow depends on neither.
