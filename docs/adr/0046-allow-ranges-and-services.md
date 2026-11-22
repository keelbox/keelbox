# 0046: Port ranges and service presets in host.firewall.allow

Date: 2026-11-07. Status: accepted. Extends ADR 0038.

## Context
A home server running Portainer, Jellyfin and a UniFi controller in system Docker needs about a dozen single-port `host.firewall.allow` entries to stay reachable on the LAN: UniFi alone lists eight ports, TCP and UDP, one of them a range (hotspot portal redirection, 8880 to 8882), and Plex lists another range for discovery. Writing each by hand means reading every vendor's port page, and missing a discovery port shows up only when a new access point cannot be adopted.

## Decision
- **Ranges.** `port` takes one port (`32400`) or a range (`8000-8010`), lower port first, with `proto`. One entry is one UFW rule per source (`port 8000:8010 proto tcp`) and one guard exception (`--ctorigdstport 8000:8010`, which the conntrack match supports). A range may not include SSH (22 or `target.ssh.port`). A range is written back as the string `"8000-8010"` in YAML and JSON, a single port as a number.
- **Services.** An entry is a port or a `service`, never both. `{service: unifi}` stands for every port the vendor lists for UniFi, each with its protocol, from the entry's `from` (default the LAN), its rules commented `keelbox allow unifi` (or the entry's `name`). The presets live in `internal/services` with the vendor page each was read from: `unifi`, `jellyfin`, `plex`, `home-assistant`, `portainer`, `samba`, `syncthing`, `pihole`, `adguard`. A preset takes the ports the vendor says the LAN needs, discovery included, and leaves out what is loopback-only (UniFi's database), for the internet only, off by default and rarely used (syslog, packet capture), or not matchable by a LAN rule (DHCP, whose requests come from `0.0.0.0`); `docs/reference/services.md` lists both, and a test keeps it in step with the code.
- Rules are expanded before UFW and the guard see them, and deduplicated by source, port and protocol, so two presets that share a port (1900/udp for UniFi and Plex) make one rule; the first entry names it.
- Unknown service names are refused with the list of known ones.

| Preset | Ports | Source |
|---|---|---|
| `unifi` | 8080, 8443, 6789, 8880-8882, 8843/tcp; 3478, 10001, 1900/udp | <https://help.ui.com/hc/en-us/articles/218506997> |
| `jellyfin` | 8096, 8920/tcp; 7359/udp | <https://jellyfin.org/docs/general/post-install/networking/> |
| `plex` | 32400, 8324, 32469/tcp; 1900, 5353, 32410, 32412-32414/udp | <https://support.plex.tv/articles/201543147-what-network-ports-do-i-need-to-allow-through-my-firewall/> |
| `home-assistant` | 8123/tcp | <https://www.home-assistant.io/integrations/http/> |
| `portainer` | 9443, 8000/tcp | <https://docs.portainer.io/start/requirements-and-prerequisites> |
| `samba` | 445, 139/tcp; 137, 138/udp | smb.conf(5) `smb ports = 445 139`, nmbd(8) |
| `syncthing` | 22000/tcp; 22000, 21027/udp | <https://docs.syncthing.net/users/firewall.html> |
| `pihole` | 53, 80, 443/tcp; 53/udp | <https://docs.pi-hole.net/main/prerequisites/> |
| `adguard` | 53, 80, 443, 853, 3000/tcp; 53, 443, 853/udp | <https://github.com/AdguardTeam/AdGuardHome/wiki/Docker> |

## Consequences
- One entry can open several ports. The security model says so; the presets never include SSH, and the rules still admit only the LAN or the network named.
- A vendor that changes its defaults needs a keelbox release to follow; a box keeps the ports of the keelbox that applied it. A port a user moved is an entry of its own.
- Docker does not forward broadcasts to containers on a bridge network, so a preset's discovery ports reach a container only on the host network. The reference says so.
- Home Assistant OS defaults to port 80 from 2026.8; the preset follows the Container and Core default (8123), the installs that run on Ubuntu.
