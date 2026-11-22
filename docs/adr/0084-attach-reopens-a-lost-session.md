# 0084: attach opens a lost session again

Date: 2026-11-16. Status: accepted. Supersedes the last consequence of [ADR 0080](0080-terminal-restored-after-a-cut-session.md).

## Context
ADR 0080 restores the terminal after a session that was cut off and gives the command to open it again. With only that, the user would still have to notice and run it. A laptop that sleeps or changes network loses the connection every time, and the session on the box is still there.

ssh's exit code is 255 for a connection it lost and for one it could not make, such as a key the box refuses. Opening again after the second would loop.

## Decision
- **`keelbox attach` opens the session again after a connection that was lost.** It says so, waits for the box's SSH port to answer, looking every three seconds, and runs the same attach. Ctrl-C stops the wait: exit 130, with the command to come back.
- **Lost means the session had been open.** ssh is given a `LocalCommand`, which it runs on the user's computer once it has logged in: it makes an empty file in a temporary directory of keelbox's. A session that ends with 255 and left that file was open. keelbox reads nothing of the session.
- **Only with a terminal on standard input and standard error**, and only for attach. `keelbox ssh` is a shell that is gone with the connection, and a sign-in is the vendor's flow.
- **Three sessions in a row that each ended within ten seconds end it**, with ADR 0080's line: a box that takes the connection and drops it is not waited for again.

## Consequences
- There is no limit on the wait. A terminal left open looks at one port every three seconds until Ctrl-C.
- After a reboot of the box the session it opens is a new one, or the one tmux-resurrect restored.
- `PermitLocalCommand` is on for that one ssh, with a command keelbox wrote; ssh runs it through the user's shell.
