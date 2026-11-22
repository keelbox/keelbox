# 0037: The tap's formula commits are signed by GitHub

Date: 2026-11-07. Status: accepted. Amends ADR 0029.

## Context
ADR 0029 has `publish.sh` write `Formula/keelbox.rb` through the REST contents API with the maintainer's `gh` login. A commit made that way with an OAuth token is not signed and shows as unverified in `keelbox/homebrew-tap`, while every other commit keelbox publishes is signed. The tap's trust is the maintainer's GitHub account (ADR 0029), so an unsigned commit there is the one link a reader cannot check.

## Decision
- `publish.sh` commits the formula with the GraphQL `createCommitOnBranch` mutation (`gh api graphql`), same login, no new token. GitHub signs commits made through it, and shows them as verified.
- The mutation names `expectedHeadOid`, the tap branch's head read just before; a push in between makes the call fail rather than overwrite it.
- The formula at that head is read through the contents API and, when it is identical, nothing is committed.

## Consequences
- Every formula commit `publish.sh` makes is signed by GitHub and shows as verified.
- A second run with the same formula commits nothing.
