# 0001: Go, one static binary

Date: 2026-11-06. Status: accepted.

## Context
A shell script that sets up a box runs to well over a thousand lines, and bash past that size is brittle: no types, weak testing, idempotency by convention. The product must run on fresh Ubuntu machines with nothing installed, and also from a laptop (macOS or Linux) to provision remote boxes.

## Decision
One Go binary, statically linked (`CGO_ENABLED=0`), for linux/amd64 and linux/arm64 (plus whatever the laptop needs). The same binary is the laptop CLI and the on-box agent: remote providers copy it to the box and run `keelbox apply --local` there. Stdlib only apart from a YAML parser; system `ssh`/`scp` rather than an SSH library, so users' agents, configs and hardware keys work unchanged.

## Consequences
- Nothing to install on the box first; no interpreter or runtime drift.
- Modules are testable with a fake executor, without root or a VM.
- Cross-architecture runs need the matching binary (`scripts/build.sh`, `--binary`).
- Go toolchain via project mise; never a global install.
