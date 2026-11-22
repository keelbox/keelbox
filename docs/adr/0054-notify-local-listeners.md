# 0054: Notifications to this box only to a listener of root, a system account or the admin

Date: 2026-11-07. Status: accepted. Amends ADR 0043.

## Context
ADR 0043 allows plain `http://` to loopback and the tailnet, for a self-hosted ntfy or webhook receiver. Without a check, an agent that binds the configured loopback ports while the owner's service is down would get the webhook header, the ntfy token and the topic within a second of an event it raises. A port number says nothing about who listens on it.

Options: refuse loopback http; allow only ports below `ip_unprivileged_port_start`; pin a certificate; or check the listener's owner.

## Decision
- keelbox connects, then finds the server-side socket of that exact connection (local and remote address and port swapped) in `/proc/net/tcp` and `/proc/net/tcp6`, retrying for up to 100 ms while the kernel moves it from the backlog. The `uid` column of that row is the owner of the listening socket.
- It sends only when that uid is root, a system account (below 1000, or `nobody`), or the box's admin. Otherwise it closes the connection without writing a byte and the send fails with "the listener on this machine is not root's, a system account's or the admin's": no URL, header or topic in the error.
- The check applies when the address is loopback or one of the box's own addresses (tailnet included). A connection whose socket is not in the tables (another machine, or a port root's NAT forwards elsewhere, such as a system Docker container) is sent to as configured: that listener is not a local account's process.
- The HTTP client for these channels uses no proxy from the environment, so the address checked is the address connected to.

## Consequences
- A self-hosted ntfy or receiver on the box must run as root, a system account or the admin; one run by another login account is refused. A system Docker container publishing the port counts as root's.
- Refusing loopback http would have broken the self-hosted case the config allows; a port floor would still admit an agent with `CAP_NET_BIND_SERVICE` in a namespace mapping and says nothing about the tailnet address; a pinned certificate needs setup the owner would get wrong.
- Checked by a unit test with a real listener on loopback (refused when its uid is not allowed, no bytes received; sent when allowed), a parser test on `/proc/net/tcp` rows, and the end-to-end test, which binds both channels' ports as the agent.
