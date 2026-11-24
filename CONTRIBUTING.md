# Contributing to keelbox

Thanks for helping. keelbox changes real machines that hold people's code and credentials, so the bar is: small changes, tests that prove them, and a human who understands every line.

Start with an issue for anything bigger than a fix, so we can agree on the shape first. Issues hold bugs and accepted requests; if what you have in mind is not there, open one. Read `CONTEXT.md` (purpose, glossary, scope) and the decisions in `docs/adr/`. The [architecture tour](docs/contributing/architecture.md), [testing guide](docs/contributing/testing.md) and walkthroughs to [add a module](docs/contributing/add-a-module.md) or [a provider](docs/contributing/add-a-provider.md) are in `docs/contributing/`.

## Set up

Tools are pinned in `mise.toml`; nothing is installed globally.

```sh
mise install         # Go, golangci-lint, goreleaser, cosign, syft, shellcheck, actionlint
make check           # gofmt, vet (all platforms), golangci-lint, shellcheck, actionlint, go test -race
make build           # dist/keelbox-linux-{amd64,arm64}
make test-install    # snapshot release, then the installer smoke test on 127.0.0.1
make e2e             # full acceptance test on a fresh local LXD VM (needs LXD with KVM)
```

Never run `keelbox apply` on your own workstation: it hardens sshd, enables UFW and rewrites Docker's daemon.json. Use a VM (`make e2e` creates and deletes one).

## Changes

- Every step's `Check` has no side effects and its `Apply` makes `Check` pass. A new or changed step needs a test against the simulated box in `internal/modules/sim_test.go`, and `TestApplyConvergesAndIsIdempotent` must stay green.
- Secrets travel by file path only: never in command arguments, environment, output or the logbook.
- keelbox never automates or stores agent sign-in.
- Tool versions change only in `internal/manifest/stable.yaml`, with a `release` bump and vendor-verified checksums.
- A decision with alternatives gets an ADR: a new `docs/adr/NNNN-slug.md`. ADRs are appended or superseded, never edited.

## Commits and pull requests

[Conventional Commits](https://www.conventionalcommits.org/): `type(scope): summary`, imperative, lower case after the colon. Types: `feat`, `fix`, `perf`, `refactor`, `docs`, `test`, `build`, `ci`, `chore`, `style`, `revert`. The scope is optional and names the area: a package (`cli`, `modules`, `provider`, `release`), a module (`host`, `agents`), a command (`up`, `status`), `e2e`, or a docs area (`reference`, `adr`). Release commits are `chore(main)`, as release-please writes them. The body says why. Breaking changes use `!` and a `BREAKING CHANGE:` footer.

Pull requests are squash-merged and the title becomes the commit, so the title must be a Conventional Commit (CI checks it). `feat` and `fix` titles appear in the changelog. release-please writes final-release entries in `CHANGELOG.md` from the commits, and the maintainer cuts release candidates by hand (`docs/maintaining.md`); contributors never edit it.

There is no CLA and no DCO sign-off. Contributions are under the project's Apache-2.0 licence (section 5): by opening a pull request you confirm you have the right to submit it under that licence.

## AI-assisted contributions

Agents are welcome here; keelbox is built with them. The rules are about accountability, not tools:

- **A human is accountable.** You open the pull request, you have read and understood every line, and you answer review questions yourself. Do not submit output you cannot explain.
- **Tests are required.** Agent-written code needs the same tests as any other, and `make check` must pass locally before you push. "The agent said it works" is not a test.
- **Disclose it.** Say in the pull request which agent you used and for what (the template has a section). Agent co-authorship trailers such as `Co-Authored-By:` are fine.
- **Have it reviewed.** If an agent wrote the change, have a fresh agent session review it before you ask for a merge, and note what it found under Review in the template (details in `AGENTS.md`).
- **Keep it small.** One logical change per pull request. Large generated diffs, drive-by reformatting and speculative abstractions will be closed.
- **No secrets in prompts or transcripts.** Never paste tokens, keys or private box details into a hosted agent, and never commit transcripts.

Agents working in this repository should read `AGENTS.md` first. Maintainer runbooks are in `docs/maintaining.md`; agent skills for releases, triage and adding a module are in `.agents/skills/`.

## Reporting

Bugs, features and provider requests: use the issue forms. Security problems: see `SECURITY.md`. Everyone taking part follows the `CODE_OF_CONDUCT.md`.
