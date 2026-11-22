# 0022: A Hetzner Cloud Firewall per box

Date: 2026-11-06. Status: accepted.

## Context
UFW filters on the box. A provider firewall drops traffic before it reaches the box, so it still holds if UFW is stopped or misconfigured. Cloud targets should have one.

## Decision
- `keelbox create` creates `keelbox-<box>`, labelled `keelbox=true`, `keelbox-box=<box>` and the config's labels, before the server, and attaches it in the server's create request, so it applies from first boot.
- Rules, inbound only:
  - TCP 22 from anywhere, until the box records that public SSH is closed (ADR 0021);
  - UDP 41641 from anywhere. Tailscale needs no inbound port: "Most of the time, you don't need to open any firewall ports", but "permitting connections to the UDP port that one of them listens on may help Tailscale make a peer-to-peer connection, rather than falling back to a relay", and 41641 is its default ([Tailscale KB 1082](https://tailscale.com/kb/1082/firewall-ports)). `tailscaled` opens the same port in its own `ts-input` chain. Only `tailscaled` listens there, for WireGuard and Tailscale's own path discovery.
  - No outbound rules: Hetzner permits all outbound traffic when there are none ([Hetzner Cloud Firewalls](https://docs.hetzner.com/cloud/firewalls/overview): "If you do not set any rule, all inbound traffic will automatically be blocked", and outbound is "automatically permitted"). Agents need open egress (ADR 0011).
- `keelbox apply` on a Hetzner box first makes sure the firewall exists and is attached (keeping its SSH rule), then applies, then reads `/var/lib/keelbox/public-ssh.json` over the connection it used and sets the rules: 22 goes only when the box says it is closed. If the state cannot be read, the rules stay as they are. Rules edited in the console are put back.
- `keelbox doctor` checks from the laptop that the firewall exists, is attached and matches the box's SSH state.
- `keelbox destroy` deletes the server, then the firewall (detaching it and retrying while Hetzner still reports it in use), and also deletes the firewall when the server is already gone.
- keelbox finds its firewall only by the labels `keelbox=true,keelbox-box=<box>`. A firewall with the name but without the labels is an error, never changed or deleted. Firewalls keelbox did not create, attached to the same server, are left alone (Hetzner allows five per server; the most permissive rule wins).

## Consequences
- A Hetzner box without the firewall gains it on its next apply.
- Opening another inbound port means a second firewall of your own; keelbox's would be reset on apply.
- Tested against a fake API (create with the server, reconcile after tampering, label refusal, delete retries) and on Hetzner by the end-to-end test (`make e2e-hetzner`): create, tamper and reconcile, SSH kept without proof, 22 removed after proof, destroy.
