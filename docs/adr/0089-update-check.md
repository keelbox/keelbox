# 0089: keelbox on your computer checks for updates once a day

Date: 2026-11-19. Status: accepted.

## Context
keelbox on your computer reads the channel index only when asked: at `keelbox self-update`, at `keelbox update`, and when it downloads a box binary. Someone who never runs those does not learn that a newer keelbox is out, or that the one they run has a security problem. keelbox has no way to tell them.

A box could check too, but it has nothing to fetch: keelbox on a box changes only at an apply from your computer, and since ADR 0088 the agent CLIs update from their vendors. A box that contacts keelbox's hosts on a timer is also what someone reading the security model before giving an agent a server is looking for.

The maintainers also have no count of how many people use keelbox, or which versions. ADR 0062 says keelbox has no telemetry, and that stays true.

## Decision
- **At most once a day, keelbox on your computer reads `stable.json` while a command** typed at a terminal runs, in the background, and waits at most a second for it when the command ends. It keeps what it read in the user cache directory, and records the attempt before it starts, so a failed or slow read waits a day too.
- **The first check on a computer says what it sends** and how to turn it off.
- **It says when a newer release is out**, once a day, with the command that updates this install (`keelbox self-update`, or `brew upgrade keelbox`).
- **The index may carry security notices** (`security`: since, fixed, a summary, a URL), maintained in `get/security.json`. A notice for the running release is shown after every command. `keelbox status BOX` also names a box that this computer last applied an affected release to, from the cached index, without reaching anything.
- **The index is not signed, so a notice is shown as data**: one printable line of at most 200 characters, quoted as the notice's words and never styled as a command, and a link whose path is plain (no `..`, encoding, query or user part) on keelbox.dev or under github.com/keelbox, with keelbox's own update command. A notice that breaks any of that is ignored. `scripts/publish.sh` refuses an index that keelbox could not read or that carries such a notice (`internal/tools/indexcheck`).
- **It does not run** on a box, in CI (`CI` set), for a development build, for `ssh`, `attach` and `login`, or with `KEELBOX_NO_UPDATE_CHECK` or `DO_NOT_TRACK` set. It never downloads or installs a release.
- **Every request to the release hosts sends `keelbox/<version> (<os>/<arch>)`** as its user agent. It carries no identifier.
- **A box makes no request to keelbox's hosts.**

## Consequences
- The maintainers can count requests for `stable.json` by version and platform from the host's logs. That approximates daily active installs, with no identifier and nothing about boxes. The security model says so, and that the count is the only use made of those logs.
- Whoever could change `stable.json` could show a false notice or a false "newer release" line, but could not make keelbox download or run anything: self-update verifies the release key signature and refuses a downgrade.
