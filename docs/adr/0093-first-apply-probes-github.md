# 0093: The first apply checks the box can reach GitHub

Date: 2026-11-24. Status: accepted.

## Context
Caddy, mise and restic come from GitHub's releases, and the tmux plugins from github.com. github.com, its release-asset hosts and get.keelbox.dev have no IPv6 address. On an IPv6-only server, or one with a private IPv4 address and no NAT, the first apply hardened sshd, enabled UFW and installed Tailscale and Docker, then stopped at `runtime/mise`. That left a box half set up, with an error about a download rather than about the network.

## Decision
- **Before the first apply changes anything, the box requests `https://github.com/` with the downloads' curl.** If it gets no HTTP answer at all, apply stops with exit 1, says nothing has changed and names the fix. An HTTP error status, a 429 say, counts as an answer.
- **The probe decides; the route only picks the message.** With no IPv4 default route, the message says the box reaches the internet over IPv6 only. Otherwise it says the box cannot reach github.com. A box behind NAT64 with DNS64, 464XLAT or a transparent proxy reaches GitHub and passes, so a check on routes alone would refuse working boxes.
- **Only the first apply probes**, as with `firstApplyGate`. A box that was set up has already reached GitHub, and a GitHub outage must not stop an apply that may need nothing from it.
- Without curl there is nothing to probe with, and apply goes on. Ubuntu's server and cloud images have curl.

## Consequences
- An IPv6-only or NAT-less box stops with a network error instead of half an apply.
- Every first apply makes one HTTPS request to GitHub, which takes up to about 30 seconds when GitHub cannot be reached.
- A box that loses its route to GitHub after setup is caught by the failing download step, as before.
