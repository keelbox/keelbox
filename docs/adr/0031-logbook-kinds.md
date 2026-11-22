# 0031: The logbook records day-two actions, by kind, beside the applies

Date: 2026-11-07. Status: accepted.

## Context
keelbox acts on the box between applies: a root timer restarts a dead tmux server, a scheduled doctor finds a check newly failing, a box comes back from an unclean shutdown. Those actions belong in the box's history next to the applies, and `keelbox status` and alerts read them from there.

The logbook's readers must keep working with records they were not written for. A reader that knows no kinds takes the newest record whose result is not `failed` as the last apply: a downgrade guard built that way compares that record's `release`, and rollback relies on the guard. Such a reader reports the newest record as the last apply and, for a `partial` one, reads its first failed step. Readers decode leniently, so a field they do not know is ignored.

An append that rewrites the whole file with the new line can lose a record: a day-two run recording an action while an apply writes its own would lose one of the two.

## Decision
- A record has a `kind`: `watch`, `doctor` or `power` (and `install`, ADR 0074). An apply record has no `kind` field, and a missing field reads as `apply`.
- Every day-two record carries the box's current release, copied from the last converged apply record (`logbook.Action` fills it and refuses when there is none). Its result is `ok` or `failed`, never `partial` or `interrupted`. `Append` refuses any other day-two record.
- Only actions are recorded (a restart, a doctor state change, a power event, a package install), never a check that found nothing to do, so the file grows by a handful of lines a day.
- Records are appended with one `O_APPEND` write each (`Executor.AppendFile`, through `CheckRootPath` like every root file call), never by rewriting the file.
- keelbox's readers look for the newest apply record where they mean the last apply: the downgrade guard (`LastApplied`), doctor's `logbook/last-apply` and the `applied` row of `keelbox status`.

## Consequences
- A reader that knows no kinds gets the box's release from whichever record is newest, so a downgrade guard and rollback built that way still behave. A unit test reads a logbook with day-two records through a frozen copy of such a reader.
- Such a reader shows the newest day-two record as the last apply. When that record is a failed action, it reports the last apply as failed. That is wrong in its label, not in what it says: something on the box failed. keelbox's own readers show the apply.
