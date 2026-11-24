# Maintaining keelbox

<!-- site: exclude -->

Runbooks for releases, triage and repository settings. They are written so an agent can follow them step by step; the matching skills are `.agents/skills/release` and `.agents/skills/triage`. A human maintainer (@nickmartink) approves every merge, release and promotion.

## Release

Pipeline (ADR 0013, verification ADR 0016): Conventional Commits on `main` -> release-please keeps a release PR open -> merging it tags `vX.Y.Z` and creates the GitHub release -> `release.yml` builds with goreleaser, signs `SHA256SUMS` with the release key (`KEELBOX_RELEASE_SIGNING_KEY`) and with cosign (keyless), attaches binaries, SBOMs, signatures and `stable.json` -> `scripts/publish.sh` promotes to `keelbox/releases` and `get.keelbox.dev`.

1. `main` is green: `gh run list --branch main --limit 5`.
2. Find the release PR: `gh pr list --label "autorelease: pending"`. Check the proposed version and the generated `CHANGELOG.md` entry. Edit the PR body or reword commits only through follow-up commits, never by editing `CHANGELOG.md` on `main`.
3. Release PRs opened by `GITHUB_TOKEN` do not trigger CI. CI already ran on the commits it contains; merge with `gh pr merge <n> --squash` (an admin bypass of the ruleset).
4. Watch the build: `gh run watch $(gh run list --workflow release.yml --limit 1 --json databaseId --jq '.[0].databaseId')`. The release `vX.Y.Z` on keelbox/keelbox must list `keelbox-{linux,darwin}-{amd64,arm64}`, `*.spdx.json`, `SHA256SUMS`, `SHA256SUMS.sig`, `SHA256SUMS.sshsig`, `SHA256SUMS.sigstore.json` and `stable.json`.
5. Promote: `KEELBOX_VERCEL_SCOPE=<team> scripts/publish.sh vX.Y.Z` (Linux, logged in to `gh` and `vercel`; the team is the maintainer's Vercel team, which owns the get.keelbox.dev project; `KEELBOX_VERCEL_PROJECT` overrides the project name). It refuses unless the signature and every checksum verify, unless the new `stable.json` passes `internal/tools/indexcheck`, and unless the release's `install.sh` is the tag's `get/install.sh`; it serves that file with `SHA256SUMS` and its signatures, points `Formula/keelbox.rb` in `keelbox/homebrew-tap` at the release through the same `gh` login in a commit GitHub signs (ADR 0029, ADR 0037), and exits 0 only once `https://get.keelbox.dev/stable.json` names the version.
6. Verify as a user, in a scratch home: `HOME=$(mktemp -d) sh -c 'curl -fsSL https://get.keelbox.dev | sh && ~/.local/bin/keelbox version'`. From the previous version: `keelbox self-update`.
7. Update keelbox.dev for the release, candidates included: it names the version the installer delivers and serves that version's docs. Then `scripts/site-check.sh vX.Y.Z` must exit 0, which it does only once keelbox.dev names the version and its changelog page has the release.

Versions: pre-1.0, `feat` bumps the minor and `fix` the patch. After an `-rc.N`, release-please proposes the final version (`X.Y.Z`); for another candidate use the manual path below with `X.Y.Z-rc.N+1`. To enter a prerelease or force a version, set `"release-as": "0.1.1"` (or `"0.1.1-rc.1"`) under `packages["."]` in `release-please-config.json`, merge the release PR, then remove it.

Without the release PR (release candidates, or when a maintainer-signed release commit and tag are wanted): commit the new version to `.release-please-manifest.json` and its `CHANGELOG.md` section as `chore(main): release X.Y.Z`, then `git tag -a vX.Y.Z -m "keelbox vX.Y.Z" && git push origin main vX.Y.Z`, `gh release create vX.Y.Z --verify-tag --notes-file <section>` (add `--prerelease` for `-rc`), `gh workflow run release.yml -f tag=vX.Y.Z`, and continue at step 4 through to the end, the site included. Pushing the release commit also makes release-please open a release PR for the final version; close it if the final release is not next.

Security notices: a fixed security problem goes in `get/security.json` with the fix, as `{"since": "vA.B.C", "fixed": "vX.Y.Z", "summary": "one line", "url": "https://keelbox.dev/security/..."}` (`since` optional; format in `docs/reference/release.md`). `scripts/channel-index.sh` puts the file in `stable.json`, so the notice reaches users with the next publish, normally the fixed release's; `TestPublishedNotices` fails on a notice clients would not show. Keep the notice while affected releases are in use. Publish the advisory page before the notice links to it.

Rebuild an existing tag (failed build, new signature): `gh workflow run release.yml -f tag=vX.Y.Z`, then publish again.

Roll the stable channel back: `scripts/publish.sh <previous tag>`. The channel is authoritative, so `keelbox self-update` moves users back too.

## Triage

New issues arrive with `needs-triage` from the issue forms. For each: `gh issue list --label needs-triage`.

1. Security content in public: hide it (`gh issue edit` or close as spam), ask the reporter to use a private advisory (SECURITY.md), and open a draft advisory.
2. Duplicate: comment with the original, label `duplicate`, close.
3. Missing version, provider or output: ask once, label `needs-info`. Close after 14 days without a reply.
4. Reproduce bugs on a VM (`make e2e` or an LXD VM named `kb-*`, deleted after), never on a workstation. Note the result in a comment.
5. Label the area (`area: cli`, `area: modules`, `area: providers`, `area: release`) and, where it fits, `good first issue` or `help wanted`. Out of scope per `CONTEXT.md`: explain, label `wontfix`, close.
6. Remove `needs-triage`.

Pull requests: `pull-request.yml` checks the title and labels areas. Review against CONTRIBUTING.md (tests, disclosure of AI assistance, one logical change, and for an agent-written PR a filled Review section from a separate agent, per AGENTS.md), then squash-merge. Dependabot PRs (`dependencies`): merge when CI is green; major action bumps need a look at the changelog.

Labels are defined in `scripts/labels.sh`; rerun it after changing them.

## Automation

- `e2e.yml`, on demand (`gh workflow run e2e.yml`): runs `make e2e` in an LXD VM, with the vendors' current Claude Code and Codex. The manifest's pins (mise, Node, pnpm, Docker series, tmux plugins, Caddy, restic) are bumped by hand with vendor-verified checksums.
- Dependabot: Go modules and actions, weekly.

## Repository settings

Settings belong to the GitHub repository, so a new or recreated `keelbox/keelbox` has none of them. Set them in this order:

1. Before the first push, while the repository is empty:
   - the release key, from the maintainer's offline copy of the private key whose public half is `internal/release/keys/keelbox-release-1.pem`: `gh secret set KEELBOX_RELEASE_SIGNING_KEY --repo keelbox/keelbox < key.pem`. GitHub cannot export a secret, and `releasesign` refuses any other key, so without that copy the release build fails at its signing step. Keep the private key in two places off the build machine, for example a password manager and an encrypted drive, and check a copy without printing it: `openssl pkey -in COPY -pubout | diff - internal/release/keys/keelbox-release-1.pem`;
   - squash merge only, PR title and body as the commit, branches deleted on merge, "update branch" offered;
   - Actions must be pinned to a full SHA; workflow tokens default to read-only;
   - GitHub Actions may create pull requests, which release-please needs (security-sensitive, so a maintainer sets it by hand):

     ```sh
     gh api -X PUT repos/keelbox/keelbox/actions/permissions/workflow \
       -f default_workflow_permissions=read -F can_approve_pull_request_reviews=true
     ```

   - labels: `scripts/labels.sh`;
   - description, homepage and topics; Dependabot alerts and security updates.
2. Push `main` and the release tag together (`git push --atomic origin main vX.Y.Z`), so release-please finds the release at once and does not propose one for the whole history. Push no other tags or branches.
3. Once the repository is public (the organisation's plan offers these on public repositories only), rulesets, private vulnerability reporting and secret scanning. `SECURITY.md` and the issue chooser link to the advisory form, so do this at once:

```sh
gh api -X POST repos/keelbox/keelbox/rulesets --input .github/rulesets/main.json  # PR, checks, linear history, no force push or deletion
gh api -X POST repos/keelbox/keelbox/rulesets --input .github/rulesets/tags.json  # release tags immutable
gh api -X PUT repos/keelbox/keelbox/private-vulnerability-reporting
gh api -X PATCH repos/keelbox/keelbox \
  -f 'security_and_analysis[secret_scanning][status]=enabled' \
  -f 'security_and_analysis[secret_scanning_push_protection][status]=enabled'
```

Admins bypass both rulesets, so the maintainer can still push release commits and tags.

`scripts/publish.sh` needs `keelbox/releases` and `keelbox/homebrew-tap` to exist, public, each with a first commit on `main` (`gh repo create keelbox/releases --public --add-readme`). To start `stable.json` without the checksums of earlier releases, as after recreating the releases, run that one publish with `KEELBOX_FRESH_INDEX=1`.

## Workflows that do not run on every change

- The macOS smoke test in `ci.yml` (about 10 minutes) runs only when a change can affect the darwin binary or the installer.
- `e2e.yml` (up to about 3 hours, the suite one test at a time in an LXD VM) runs by hand: `gh workflow run e2e.yml`. It has no schedule.
