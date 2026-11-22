# 0056: One sanitiser for text the agent can influence

Date: 2026-11-07. Status: accepted.

## Context
A PATH shim can put `ESC[2J`, an OSC title and colour codes into tmux session names, and `keelbox status` would print them on the owner's laptop as they are. Real tmux escapes names itself, but the snapshot, doctor details, version strings, sign-in output, error output and notification text all carry text an agent process wrote. Escaping in each printer leaves gaps.

## Decision
- `internal/plain` is the one sanitiser. `plain.Line` makes one line: control characters and every other character Go's `unicode.IsPrint` rejects (bidirectional overrides and zero-width marks among them) become `\xNN` or `\uXXXX`, and invalid UTF-8 becomes `\xNN`. `plain.Text` keeps newlines and tabs and escapes the rest.
- It is applied where text leaves keelbox: status and `status --all` cells, session names, plan, apply and doctor details, error output in `ExitError`, the doctor run's findings, notification titles and texts, and the watchdog's details in the logbook.
- The status snapshot is cleaned on write and again on read, so a snapshot written by another keelbox, or edited by hand, prints plain too.
- With `notify.agent_text`, the agent CLI's own text is quoted after `claude-code wrote:` or `codex wrote:`, so it does not read as keelbox's wording.

## Consequences
- A name with odd characters shows escaped, not as the terminal would draw it.
- New output paths must use `plain`; `TestStatusPrintsAgentOutputPlain`, `TestStatusAllPrintsSessionNamesPlain` and `TestSnapshotStringsArePlain` use a session name full of escape sequences.
