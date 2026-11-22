# 0036: public_ssh: lan keeps SSH open to the LAN

Date: 2026-11-07. Status: accepted. Extends ADR 0021.

## Context
`public_ssh: auto` closes every SSH path except the tailnet once a tailnet proof exists (ADR 0021). On a home server that includes the LAN: the user's desktop, which reached the box over SSH before, silently loses it. Without a mode for the LAN, nothing says so, and `status` would call SSH "open to the internet" before it closes.

## Decision
- A fourth mode, `host.firewall.public_ssh: lan`. It closes SSH on the same tailnet proof as `auto`, but only to addresses outside the private ranges (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `fc00::/7`):
  - UFW allows `22/tcp` from each range, labelled `keelbox allow ssh-lan`, through the same allow-list rules as `host.firewall.allow` (`from: lan`);
  - the sshd drop-in's `Match LocalAddress` adds `Address *,!<ranges>`, so `DenyUsers *` applies only to clients outside them.
- `keelbox up` asks, on this machine or a server it reaches at a private address with Tailscale on: it first says that SSH from outside the tailnet, the LAN included, closes once keelbox reaches the box over Tailscale, then asks whether the LAN keeps SSH (default yes). A server at a public address is not asked and keeps `auto`. `--public-ssh` answers without a terminal; its default there is `lan` when the box is on a LAN.
- `status` and `doctor` name what is open: "open (LAN and internet)", "closed to the internet: tailnet and LAN only", "closed: tailnet only".

## Consequences
- Anyone on the box's LAN, or able to reach it from a private address (another VPC host, a VPN), can reach sshd. sshd still takes keys only, and only for keelbox's accounts.
- On a cloud server with a private network, `lan` would keep SSH open to that network too; `up` does not offer it there, and the user who sets it chooses that.
