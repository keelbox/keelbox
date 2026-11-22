# 0051: Previews skip ports kept open to the LAN

Date: 2026-11-07. Status: accepted. Amends ADR 0046 and ADR 0019 (previews).

## Context
Previews are served on `previews.ports`, default 8401-8499, on the tailnet only, and doctor fails when a UFW rule admits any of those ports elsewhere. ADR 0046 added service presets: `{service: unifi}` opens 8443/tcp to the LAN, inside the default range. Without more, a box set up by `keelbox up` with UniFi kept fails doctor right after Ready, and a preview given 8443 would be reachable from the LAN through the allow rule, or clash with UniFi itself.

There are two fixes: move the default range to one no preset uses, or leave out of the range the ports the config opens. Moving the default would strand every box's previews (their file names ports in 8401-8499) and only fixes the presets keelbox ships, not a port the owner allows by number.

## Decision
- `Config.PreviewSkipped` is every port in `previews.ports` that an entry of `host.firewall.allow`, presets expanded, admits for TCP. UDP-only entries do not count: previews are HTTPS.
- `keelbox expose` never gives a skipped port to a new preview; a preview already on one gets a new port the next time it is exposed. The previews service refuses an entry on a skipped port with a problem that says why and how to move it, and records the skipped ports in its status file, which `expose` reads.
- Doctor's `previews/firewall` check ignores UFW rules whose ports in the range are all skipped, and UDP rules, and its ok line names the skipped ports and the entries that keep them. `keelbox up` says so in the summary when the LAN answers skip any.
- A range left with no port is a config error that says to choose another range.
- The default stays 8401-8499.

## Consequences
- Previews on a box that keeps UniFi's 8443 open use 98 ports, not 99.
- A preview URL on a port that becomes skipped changes when it is exposed again; until then the service refuses it and doctor warns with the fix.
