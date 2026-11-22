# 0011: Close the agent-to-admin paths over SSH

Date: 2026-11-06. Status: accepted.

## Context
The example config gives the admin and the agent the same laptop key, and the managed tmux config re-imported `SSH_AUTH_SOCK` on every attach. A human who attaches with `ssh -A` therefore handed the agent a socket that can authenticate as the admin, and `ssh admin@localhost sudo -n` turned an agent into root. A separate key per account helps only if the human never forwards an agent that holds the admin key.

## Decision
Three independent layers, each verified by doctor:

1. **No forwarded agent for the agent account.** The sshd drop-in ends with `Match User <agent>` / `AllowAgentForwarding no`. sshd closes a Match block at the end of an included file, so it cannot capture `sshd_config`'s own settings. Doctor reads the effective value with `sshd -T -C user=<agent>`.
2. **tmux never carries a forwarded agent into sessions.** `SSH_AUTH_SOCK` is out of `update-environment` and unset in the global environment.
3. **The agent cannot SSH into the box at all.** An nftables table (`inet keelbox_agent`, loaded by `keelbox-agent-net.service` before the network comes up) rejects TCP port 22 to any local address (`fib daddr type local`) for sockets the agent user owns. It matches the socket owner, so it also covers the agent's rootless containers, whose traffic leaves through the agent's own network helper. Outbound SSH to other hosts (`git@github.com`) is unaffected.

The same table rejects link-local destinations, which include cloud metadata services (`169.254.169.254`, `fd00:ec2::254`): they serve instance data, including cloud-init user data, to any process that asks.

Sharing one public key between the accounts stays allowed: the agent holds no private key, cannot borrow the human's agent, and cannot connect to the box's sshd.

## Consequences
- Agents that need SSH agent forwarding for git must use a deploy key or token of their own; the primer says so.
- The table is a separate nftables table, independent of UFW's and Docker's; neither reloads it away.
