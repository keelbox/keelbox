# 0008: Apply resilience: module order, soft steps, deferred steps

Date: 2026-11-06. Status: accepted. Amends the module order in ADR 0003.

## Context
Apply stops at the first step that fails, because later steps may depend on it. With the agents module before access, a vendor changing its installer (the most frequent breakage) would stop the run before Tailscale was installed, leaving a box that is hard to reach. Agent detection also assumed `~/.local/bin/<cli>`, which a vendor can change at any release.

## Decision
- **Order**: host, access, runtime, sessions, primer, logbook, agents. Reachability first, vendor installers last.
- **Soft steps** (`Step.Soft`): a failure is recorded in the run (`Applied.Failed`, logbook `failed`, result `partial`) and the run continues. Only steps nothing else depends on are soft: today, one per agent CLI. Apply exits 1 and doctor reports the missing CLI as a failure, so a partial box is never silently "ok".
- **Deferred steps** (`engine.Defer`): a step whose Apply must not run in this session returns a DeferredError. It stays pending, is reported and logged, and does not fail the run. Used by the cloud default-user lockdown (ADR 0009), which defers while keelbox runs as that user.
- Agent CLIs are detected with `command -v` as the agent user, on the agent's PATH.

## Consequences
- A broken Claude Code or Codex installer costs that agent, not the box.
- `engine.Apply` has three outcomes per step (changed, deferred, failed-soft) besides stopping; the idempotency test still requires a second apply to change nothing.
