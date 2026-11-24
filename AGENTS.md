# keelbox

Open-source CLI that sets up an Ubuntu machine as a box for AI coding agents. One YAML config per box, idempotent `apply`, a release manifest of tested versions. Purpose, glossary and scope in `CONTEXT.md`. Bugs and accepted requests: GitHub issues.

## Communication
Direct, no fluff. Concise reports. No em dashes. British English.

## Stack
Go (version pinned in `mise.toml`, run everything through `mise exec --`), single static binary, one dependency (`go.yaml.in/yaml/v3`). golangci-lint via mise. No cobra; stdlib `flag`.

## Commands
- `make check` (gofmt, vet, vet e2e, golangci-lint, `go test -race`). Run before every commit.
- `make build`: static `dist/keelbox-linux-{amd64,arm64}`.
- `make e2e`: full run on a fresh local LXD VM (`kb-e2e-*`, deleted after). `make e2e-hetzner` needs `~/.config/keelbox/hetzner.token`; `make e2e-aws` needs the AWS CLI, signed in.
- Never run `keelbox apply` on your own workstation: it hardens sshd, enables UFW and rewrites Docker's daemon.json. Use a `kb-*` VM.

## Layout
- `cmd/keelbox`: entry point. `internal/cli`: commands and output.
- `internal/config`: schema, defaults, validation, `example.yaml.tmpl` (the commented config `keelbox init` and `keelbox up` write).
- `internal/manifest`: `stable.yaml`, the release manifest embedded in the binary.
- `internal/engine`: Env, Step, Module, Plan/Apply/Doctor, step helpers (File, Block, Line, Packages, Service).
- `internal/modules`: host, access, runtime, sessions, primer, logbook, previews, backup, notify, doctor, watch, permissions, agent-policy, agents, in apply order (`All()`). Tailscale comes early so a box is reachable even if later steps fail; agents come last and are soft steps. `sim_test.go` is a stateful fake Ubuntu box.
- `internal/installs`: the agent's package requests (`keelbox install`, `requests`, `approve`, `deny`): the name check, the deny-list and root's install service (ADR 0074).
- `internal/system`: the Executor seam (Real, Fake). Modules never call os/exec or the filesystem directly.
- `internal/provider`: ssh (system ssh/scp, host-key pinning in `hostkey.go`), `cloud.go` (labels and user data every cloud shares), `hetzner/` (API client, create/find/destroy; stop, resize, snapshot and restore in `park.go`), `aws/` (Lightsail through the user's AWS CLI; keelbox holds no AWS credential, ADR 0065), `digitalocean/` (through doctl, ADR 0067) and `gcp/` (through gcloud, ADR 0068), each with the same calls in its own `park.go` (ADR 0071).
- `internal/release`: channel index, release keys (`keys/`) and the one verified download path for keelbox's own binaries. `internal/selfupdate`: `keelbox self-update`. `internal/tools/releasesign`: signs `SHA256SUMS` in the release workflow.
- `get/`: get.keelbox.dev (`install.sh`, `vercel.json`). Release build: `.goreleaser.yaml`, `.github/workflows/release.yml`.
- `test/e2e`: real-machine harness, build tag `e2e`.

## Rules
- Every step: Check has no side effects; Apply must make Check pass. Add a sim test for every new step and keep `TestApplyConvergesAndIsIdempotent` green.
- Never put secrets in command arguments, env, output or the logbook. Pass by file path (`tailscale up --auth-key=file:`).
- keelbox never automates or stores agent sign-in (Claude, OpenAI ToS). Users run each CLI's login themselves.
- Managed files say "Managed by keelbox". Files users also edit get a marked block (`engine.Block`), never a rewrite.
- Destructive provider calls only touch resources labelled `keelbox=true`. On a box, never remove software keelbox did not install without `--adopt`.
- Files under a non-root account's home (or any path it can change) go through `Env.FS(owner)`, never root's file calls; root's refuse such paths (ADR 0015).
- A step that acts for a non-root account sets `Step.User` (the file steps do it from the owner), so its failure skips only that account and not the box's security steps (ADR 0017).
- Remote commands run the uploaded binary from `/var/lib/keelbox/stage/` (`/run` is noexec); only apply installs `/usr/local/bin/keelbox`, keeping the old one for `keelbox rollback`.
- The agent never gets the system Docker socket, sudo, or a path to the admin: changes that touch Docker, sshd, tmux environment or `agentnet.go` need a sim test and an e2e check in `test/e2e/features_test.go`.
- Security wording: say what is enforced and how it is checked. Never "secure", "safe", "sandboxed" or "cannot escape". Keep the primer and `docs/security-model.md` true in the same commit as the code.
- A user-facing error ends with the fix: the exact command or YAML that resolves it, with the box name filled in. `keelbox up` without a terminal stops before any change when an answer is missing (exit 2) and with exit 3 at steps only the user can do (ADR 0025).
- Interactive commands (`login`, `attach`, `ssh`) hand ssh the terminal's file descriptors and read nothing that passes through. Status checks use exit codes only.
- Pins change only in `internal/manifest/stable.yaml` with a `release` bump; checksums and key fingerprints from the vendor, verified.

## Where to look
- Code map, a command end to end, the engine, the sim: `docs/contributing/architecture.md`.
- New module or step: `docs/contributing/add-a-module.md` (skill `add-module`). New provider: `docs/contributing/add-a-provider.md`.
- Test layers, costs, e2e knobs: `docs/contributing/testing.md`.
- User-facing behaviour: `docs/reference/` (commands, config, release manifest, files on the box) and `docs/guides/`. Index: `docs/README.md`.
- Threat model: `docs/security-model.md`.

## Docs
- Keep `docs/` true in the same commit as the code. `docs/reference/cli.md` must match `keelbox help` and `docs/reference/config.md` must list every config key; tests in `internal/cli` and `internal/config` fail otherwise. `internal/docscheck` holds every user page to a prose budget, a phrase lint and working links, and checks that every docs URL keelbox prints exists. New files, packages or services on the box go in `docs/reference/box-files.md`.
- `CONTEXT.md`: rewrite when purpose or scope changes.
- `docs/adr/NNNN-slug.md`: one per decision; append, supersede, never edit.

## Git
Conventional Commits: `type(scope): summary`, types and scopes in `CONTRIBUTING.md`. Pull requests are squash-merged with the title as the commit. Small logical commits. `CHANGELOG.md`: release-please writes final-release entries from the commits, and the maintainer cuts release candidates by hand (`docs/maintaining.md`); contributors never edit it.

## Pull requests
Before an agent-written pull request is merged (Dependabot and release-please PRs excepted):
- The pages in Docs above are updated in the same PR. If none needed changing, the PR body says so.
- A separate agent reviews it: a new session with none of the author's context, not a fork or subagent that inherits it. It is given the PR number and this file, reads whatever else it needs, and reviews the change for correctness, tests, the rules here and the docs. Each finding is fixed or answered in the PR, and the PR's Review section says in a line or two what was found and what changed.

A contributor without an agent leaves Review empty; the maintainer's review covers it.

## Releases
`docs/maintaining.md` has the release and triage runbooks; `.agents/skills/` the matching skills (Claude links in `.claude/skills/`). `make test-install` smoke-tests the installer against a snapshot build. Distribution design: ADR 0013, superseded for integrity by ADR 0016 (release key, allowlisted hosts, index checksums).
