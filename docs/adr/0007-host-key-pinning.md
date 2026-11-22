# 0007: SSH host keys are pinned, never accepted silently

Date: 2026-11-06. Status: accepted. Supersedes the "Remote access" row of ADR 0005.

## Context
With `StrictHostKeyChecking=accept-new`, the first connection to a box trusts whatever key answers. A man in the middle on that first connection receives the config and runs keelbox as root.

## Decision
keelbox keeps its own `known_hosts` (`~/.config/keelbox/known_hosts`, or `target.ssh.known_hosts`) and always connects with `StrictHostKeyChecking=yes`. A host gets into that file in one of three ways:

| Path | How the key is trusted |
|---|---|
| `keelbox create` (Hetzner) | keelbox generates an ed25519 host key on the laptop, injects it through cloud-init (`ssh_keys`, `ssh_deletekeys`, no other generated types) and pins it before the server exists. The first connection is verified. |
| `target.ssh.host_key: SHA256:...` | `ssh-keyscan` fetches the offered keys; only the key with that fingerprint is pinned. Any other answer is refused. |
| Interactive first connection | The fingerprints are shown with a hint to compare them with the provider console; nothing is pinned unless the user types yes. Without a terminal and without `host_key`, keelbox refuses. |

Cloud-init user data is not secret: Hetzner's metadata service serves it to every process on the server. The injected key is therefore single-use. Over the first verified connection keelbox deletes all host keys, generates fresh ones on the box (`ssh-keygen -A`), reads the new public keys back over the same authenticated session and pins them. A stolen copy of the injected key is worthless after a few seconds of the server's life.

## Consequences
- A private key in user data would on its own contradict ADR 0005. Rotating it at the first connection answers that, where trusting on first use would not.
- `ssh-keyscan` does not read `~/.ssh/config`, so `target.ssh.host` must be a real address or name, not a `Host` alias.
- `keelbox destroy` and `create` forget the address's entries, since cloud addresses are reused.
