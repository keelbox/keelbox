---
name: add-module
description: Add or extend a keelbox module or step (a new tool, file, service or check on the box), with its config key, sim test, e2e check and docs. Use when asked to make keelbox install, configure or verify something new on a box.
---

# Add a module or step

Follow `docs/contributing/add-a-module.md`; it has a worked example. The engine and the Executor seam are in `docs/contributing/architecture.md`.

1. Decide: a step in an existing module (a tool that belongs with others there) or a new module (its own concern, toggle and doctor checks). Prefer a step.
2. Config: a tri-state `*bool` or field in `internal/config/config.go`, a helper that applies the default, validation with the key path in the message. Add the row to `docs/reference/config.md` (`TestConfigReferenceListsEveryKey` enforces it).
3. Steps: use the helpers in `internal/engine/steps.go` and `internal/modules/util.go`. Check has no side effects; Apply makes Check pass; all machine access through the Executor, and files in a home through `e.FS(owner)` (ADR 0015); no secrets in commands; versions and checksums only in `internal/manifest/stable.yaml`.
4. New module: register it in `modules.All()` in order (host and access first, agents last), and add `Verify` findings for doctor.
5. Teach `internal/modules/sim_test.go` any new command, and add a sim test. `make check` must pass, including `TestApplyConvergesAndIsIdempotent`.
6. Docker, sshd, tmux environment or `agentnet.go`: add a check to `test/e2e/features_test.go` and run `make e2e` (LXD, `kb-e2e-*`, one at a time). Never run `keelbox apply` on your own workstation; use a `kb-*` VM.
7. Same commit: `docs/reference/box-files.md`, the modules table in `docs/concepts/how-it-works.md`, the primer if agents need to know, and `docs/security-model.md` if enforcement changes. An ADR if you chose between alternatives.
8. Commit as `feat(modules): ...` or `feat(<module>): ...` per `CONTRIBUTING.md`.
