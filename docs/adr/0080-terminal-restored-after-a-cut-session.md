# 0080: The terminal is restored after a session that was cut off

Date: 2026-11-16. Status: accepted. Builds on [ADR 0014](0014-login-and-sessions.md).

## Context
`keelbox attach` and `keelbox ssh` hand ssh the terminal. tmux and the agent CLIs on the box turn on modes in that terminal: the alternate screen, mouse reports, bracketed paste, focus reports, extended keys, a hidden cursor. They turn them off when they exit or the user detaches.

When the connection is lost, nothing on the box can reach the terminal to turn them off. The user is back at their own shell on the alternate screen, with no scrollback, and every movement of the mouse types `35;22;51M` into the prompt. A command typed there is run with those characters in it. ssh's one line about why it ended was drawn on that screen.

A message that the address did not answer, with `ssh agent@ADDRESS` as the fix, would say neither that the session on the box is still there nor the command that opens it again.

## Decision
- **After a session ssh ended with exit code 255, or that a signal ended, keelbox writes the sequences that turn those modes off**, when standard error is a terminal. Each one does nothing where the mode is off. The cursor is saved before the alternate screen is left, because leaving it restores the saved cursor: a terminal that was not on the alternate screen keeps its cursor where it was.
- **keelbox reads nothing.** It writes to the terminal after ssh has exited, and uses ssh's exit code.
- **The line after it stands without ssh's.** It names the box, says that a session left open there keeps running, and ends with the command that opens it again. Where the port no longer answers it says to check that this computer is online, and connected to Tailscale when the address is a tailnet one, and that the box is on.
- **A session's own exit code is left alone**, and so is Ctrl-C.

## Consequences
- keelbox cannot tell a connection that was lost from one that was never made, nor from a shell that itself exited with 255. The line gives the exit code and what ssh uses it for, and holds for all three.
- keelbox does not reconnect. The user runs the command it gives.
