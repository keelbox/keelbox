# 0086: keelbox update, and which boxes are behind

Date: 2026-11-16. Status: accepted.

## Context
Without `keelbox update`, moving a box to a new release would take two commands that belong together: `keelbox self-update`, then `keelbox apply BOX`. After the first, nothing would say that boxes are still on the old release, or which.

## Decision
- **`keelbox update BOX` is `self-update` then `apply`.** It reads the stable channel as self-update does. With a newer release it replaces keelbox on this computer by the same verified download, then hands over to the new binary for the apply: the process that began is the old keelbox and has the old release's steps. With the current release already here, it applies it.
- **It stops before the box is touched** when the channel cannot be read, the binary cannot be replaced, or the binary is Homebrew's. Each message says the box was not touched and ends with the command to run.
- **This computer records the keelbox it last applied to each box with**, in `boxes/<box>.applied.json`, after an apply that passed. It reaches no box to learn it.
- **`self-update` ends by listing the boxes that release has not been applied to**, with the command for each: `keelbox update BOX`, or `keelbox resume BOX` for a parked box, which applies as it comes back. A box with no record is listed as not recorded.
- **`keelbox add BOX codex --api`** carries `--api` to the sign-in that follows, and `keelbox login BOX codex --api` on a box without Codex says to add it.

## Consequences
- The record is this computer's view. A box applied from another computer is listed here until this one applies it, which then changes nothing.
- A development build updates nothing: `update` says so and applies what it is.
