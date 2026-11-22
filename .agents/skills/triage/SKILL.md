---
name: triage
description: Triage keelbox issues and pull requests: label, deduplicate, ask for missing details, reproduce on a VM. Use when asked to triage, sort or review the issue queue or incoming pull requests.
---

# Triage keelbox

Follow the Triage runbook in `docs/maintaining.md`. Labels are defined in `scripts/labels.sh`.

1. List the queue: `gh issue list --label needs-triage --json number,title,labels,body`.
2. Per issue, in this order: security content (stop, tell the maintainer, do not comment publicly), duplicate, missing information, reproduction, labels, then remove `needs-triage`.
3. Reproduce only in a fresh LXD VM named `kb-<something>`, deleted afterwards. Never on the host.
4. Comment briefly and factually: what you checked, what you need. One question round per issue.
5. Pull requests: check the title is a Conventional Commit, `make check` passed, steps have sim tests, AI assistance is disclosed. Leave a review; **do not merge** without the maintainer.
6. Report a table: issue, action taken, labels, anything needing the maintainer.

Closing as `wontfix` or out of scope needs the maintainer's agreement.
