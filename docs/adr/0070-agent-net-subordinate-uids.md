# 0070: The agent's network limits match its subordinate uids

Date: 2026-11-13. Status: accepted. Amends [ADR 0011](0011-agent-cannot-reach-admin.md).

## Context
The `inet keelbox_agent` table ([ADR 0011](0011-agent-cannot-reach-admin.md), [ADR 0069](0069-agent-ssh-to-outside-addresses.md)) matches by socket owner. When the table began with `meta skuid != "agent" accept`, its rejects applied only to sockets the agent's uid owns, and every other socket passed.

The agent's uid is not the only uid the agent can run as. `/etc/subuid` gives the account a range of subordinate uids (rootless Docker needs one, [ADR 0012](0012-rootless-docker.md), and `useradd` on Ubuntu gives every new account one), and `newuidmap`, setuid root in the `uidmap` package, maps that range into a user namespace the account creates. A user namespace is not a network namespace. A process that enters one and switches to a mapped uid still uses the box's own network, and the kernel records its sockets as owned by the subordinate uid:

```sh
rootlesskit --net=host sh -c 'setpriv --reuid=1 --regid=1 --clear-groups CMD'
unshare --user --map-root-user --map-auto sh -c 'setpriv --reuid=1 --regid=1 --clear-groups CMD'
```

With the uid map `0 1001 1` / `1 165536 65536`, `CMD` runs as host uid 165536. A first rule that matches the agent's uid alone accepts it, so from there the agent reaches sshd on 127.0.0.1 and the metadata address, the two things the table exists to refuse. On an Ubuntu 24.04 userland (nftables 1.0.9, rootlesskit 1.1.1) both commands get the `SSH-2.0-OpenSSH` banner and a connection to 169.254.169.254 past such a table, and neither with the rule below.

Containers of the agent's rootless Docker as keelbox sets it up are not affected: they have a network namespace of their own, and their traffic leaves it through slirp4netns or pasta, which run as the agent's own uid. A rootless daemon the agent starts without a network namespace is the same case as the commands above.

## Decision
- **The table matches every uid the agent can take.** Its first rule is `meta skuid != { UID, START-END, ... } accept`: the agent's own uid and each range `/etc/subuid` gives the account, named there by login name or by uid. Every reject after it therefore applies to all of them, and sockets of any other uid still pass untouched. Ranges are sorted and merged, so the rule is written one way whatever the file's order, and uids are written as numbers.
- **Read at apply, from `/etc/subuid`.** The `agent-net-rules` step reads the account's uid and the file each time it is checked. An account with no range gets a rule for its own uid alone. When the runtime module gives an account its first range, later in the same apply than the table's step, that step rewrites and reloads the table at once, so no apply ends with a range the table does not match.
- **A changed range is drift.** A range added or removed after the last apply (`usermod --add-subuids`) makes the step pending and doctor fail: `verifyAgentNet` reads the uids back from the loaded table and compares them, as ranges, with the account's uid and its ranges in the file. The next apply loads the table again.
- **The gid is not matched.** The rules look at the socket's uid only, and no uid outside the agent's own and its subordinate ranges is one the agent can take, whatever its gid; `/etc/subgid` adds nothing to match.
- **The ranges are taken as the file states them.** A range the file gives the agent is by definition uids the agent can use, so the limits belong on all of it. keelbox's own ranges start at 100000 and do not overlap accounts.

## Rejected
- **Matching the agent's cgroup** (`socket cgroupv2` under `user-UID.slice`). It would follow the process tree instead of the uid, but nftables resolves the cgroup when the rule is loaded: the table loads at boot before the network, when the agent's slice need not exist, and a slice that is removed and made again is no longer the one the rule names. The uids are a closed set that exists whether or not the agent is running.
- **Removing `newuidmap` or the range.** Rootless Docker needs both.
- **Refusing by default and accepting known system uids.** It would put every other account and service under the agent's limits.

## Consequences
- A process of the agent's under any of its subordinate uids is refused SSH to the box and link-local addresses like the agent itself. The end-to-end test runs the `rootlesskit` command above against 127.0.0.1:22 and 169.254.169.254:80 and expects no connection; it is skipped where `rootlesskit` or `setpriv` is missing.
- Between a change to `/etc/subuid` by root and the next apply the new range is not matched; doctor fails in that time.
- Subordinate uids that come from somewhere other than `/etc/subuid` (an NSS `subid` module) are not read. Ubuntu does not configure one.
