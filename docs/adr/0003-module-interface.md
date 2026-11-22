# 0003: Module interface: steps with check and apply, plus verify

Date: 2026-11-06. Status: accepted.

## Decision
```go
type Module interface {
    Name() string
    Enabled(*config.Config) bool
    Steps(*Env) []Step                         // ordered; may depend on earlier steps
    Verify(context.Context, *Env) []Finding    // doctor: does it work, not just exist
}
type Step struct {
    Name  string
    Check func(ctx, *Env) (Status, error)      // no side effects
    Apply func(ctx, *Env) error                // must make Check pass
}
```
- `plan` runs every Check. `apply` runs Check, then Apply when needed, then Check again; a step that does not converge, or fails, stops the run (later steps may depend on it). `doctor` reports drift (pending checks) as warnings and Verify findings as ok/warn/fail.
- All machine access goes through `system.Executor` (Run, ReadFile, Stat, WriteFile, MkdirAll, Symlink, Remove, LookupUser). `Real` for the box, `Fake` for tests; the module tests drive a stateful simulated Ubuntu box to prove apply converges and a second apply changes nothing.
- Shared helpers: File (exact content, mode, owner, optional validate with rollback, on-change hook), Block (marked region in a user-edited file), Line, Dir, Packages (apt), Service (systemd).
- Module order: host, runtime, agents, sessions, access, primer, logbook.
- Every apply appends a logbook record (changes, result, release, config hash), even on failure.

## Consequences
New features are new steps; the idempotency test catches a step whose Check and Apply disagree.
