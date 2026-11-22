# 0047: Recognising known services on a machine in use

Date: 2026-11-07. Status: accepted. Extends ADR 0038 and ADR 0046.

## Context
On the home server of ADR 0046, a list of what listens has about a dozen ports, `8080/tcp`, `8443/tcp`, `3478/udp`, `10001/udp` and so on. Listed as ports, each would be a question under "Ports to keep open on the LAN" in `keelbox up` and a line in `plan --impact`. The owner thinks of UniFi, Jellyfin and Portainer, not of ports, and the ports that matter most for UniFi (10001/udp, how a new access point finds the controller) are the ones that listen only part of the time or not at all, so a list of what listens now misses them.

## Decision
- **Recognition.** The inventory (ADR 0038) names a listener's known service (ADR 0046) from, in turn: the process name `ss` reports (`jellyfin`, `smbd`, `Plex Media Serv`, `pihole-FTL`); the systemd service it runs in, read from `/proc/PID/cgroup` as root (`unifi.service`, `home-assistant@homeassistant.service`, `syncthing@sam.service`); or the image of its Docker container, by the last part of the repository (`jellyfin/jellyfin`, `lscr.io/linuxserver/unifi-network-application`, `jacobalberty/unifi`). A container on the host network is found from its `docker-ID.scope` cgroup and treated as a host listener, which UFW filters, not the Docker guard. A port is counted towards a service only when the service's preset lists it.
- **One item per service.** `plan --impact` and the first-apply gate report a known service as one item, subject the preset name: "UniFi Network Application (container unifi, host network) listens on 8080/tcp, 8443/tcp, 3478/udp and 10001/udp now; the firewall would block them from the LAN", with the keep entry `{service: unifi}`, the ports it opens beyond those listening, and which are for discovery. The JSON item carries `ports`. A port the service uses beyond its preset (UniFi's syslog on 5514/udp) stays an item of its own.
- **One question per service.** `keelbox up` asks "Keep UniFi Network Application reachable on your LAN?" once, default yes, after a line saying what `{service: unifi}` opens and why the discovery ports are there; the other ports are one question. `--lan-ports` takes preset names as well as ports (a port of a service picks the service). Each yes becomes `{service: NAME}` in the config.

## Consequences
- Recognition is a hint the owner confirms, never an action: a wrong match costs one question with the wrong name. A process called `java` or `python3` is recognised only through its unit or container.
- Opening a preset opens ports that are not listening now (discovery, hotspot portal). That is the point for discovery; the question and the report say which ports, before anything changes.
- The fallback without the root report (`up`'s own probe) recognises by process name only; the gate's report, run as root, recognises all three ways.
