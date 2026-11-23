# Known services

```yaml
host:
  firewall:
    allow:
      - {service: unifi}
      - {service: jellyfin, from: 192.168.1.0/24}
```

A `service` entry in [`host.firewall.allow`](config.md#allow) opens every port its section below lists, TCP and UDP. `from` defaults to `lan`, and `name` renames the rules, which are commented `keelbox allow unifi`. Published ports of system Docker containers get the same exceptions.

- An entry names a service or a port, not both. For a port a section does not list, add a port entry: `{name: jellyfin-dlna, port: 1900, proto: udp}`.
- No preset opens SSH or a port its vendor marks as local-only. Ports are as each vendor's page listed them on 2026-11-07.
- On [a machine already in use](../guides/machine-in-use.md), `keelbox up` and `keelbox plan --impact` recognise these services by process name, systemd unit or container image. For each one the firewall would cut off, `up` asks once and `plan --impact` prints the entry that keeps it.

Ports marked "discovery" carry the broadcasts or multicasts devices and apps use to find a service on the LAN. The entry admits them from the LAN like any other port. Docker does not forward broadcasts to a container on a bridge network, so discovery reaches a container only with `network_mode: host`. Without discovery, enter the address in the app or device by hand; for UniFi, `set-inform http://ADDRESS:8080/inform` on the device.

## adguard

AdGuard Home. Source: <https://github.com/AdguardTeam/AdGuardHome/wiki/Docker>.

| Port | Protocol | For |
|---|---|---|
| 53 | tcp | plain DNS |
| 53 | udp | plain DNS |
| 80 | tcp | admin panel |
| 443 | tcp | HTTPS and DNS-over-HTTPS |
| 443 | udp | DNS-over-HTTPS over HTTP/3 |
| 853 | tcp | DNS-over-TLS |
| 853 | udp | DNS-over-QUIC |
| 3000 | tcp | admin web interface during setup |

Recognised by process `AdGuardHome`, unit `AdGuardHome.service` or image `adguardhome`.

Not opened:

- 67-68/udp, its DHCP server. Requests come from 0.0.0.0, which no LAN rule matches.
- 5443, DNSCrypt.
- 6060/tcp, debug profiles.

## home-assistant

Home Assistant. Source: <https://www.home-assistant.io/integrations/http/>.

| Port | Protocol | For |
|---|---|---|
| 8123 | tcp | web interface and apps, the default for Home Assistant Container and Core |

Recognised by process `hass`, unit `home-assistant@*`, `homeassistant.service` or `home-assistant.service`, or image `home-assistant` or `homeassistant`.

Not opened: 21063/tcp and up, which HomeKit Bridge uses once set up. Add its port as an entry. UFW already admits mDNS, 5353/udp multicast.

## jellyfin

Jellyfin. Source: <https://jellyfin.org/docs/general/post-install/networking/>.

| Port | Protocol | For |
|---|---|---|
| 8096 | tcp | web interface and apps (HTTP) |
| 8920 | tcp | HTTPS, when enabled |
| 7359 | udp | client discovery on the local network; discovery |

Recognised by process `jellyfin`, unit `jellyfin.service` or image `jellyfin`.

Not opened: 1900/udp, which the DLNA plugin uses. Its entry is `{port: 1900, proto: udp}`.

## pihole

Pi-hole. Source: <https://docs.pi-hole.net/main/prerequisites/>.

| Port | Protocol | For |
|---|---|---|
| 53 | tcp | DNS |
| 53 | udp | DNS |
| 80 | tcp | web interface (HTTP) |
| 443 | tcp | web interface (HTTPS) |

Recognised by process `pihole-FTL`, unit `pihole-FTL.service` or image `pihole`.

Not opened:

- 67/udp and 547/udp, its DHCP server. Requests come from 0.0.0.0, which no LAN rule matches.
- 123/udp, its optional NTP server.

## plex

Plex Media Server. Source: <https://support.plex.tv/articles/201543147-what-network-ports-do-i-need-to-allow-through-my-firewall/>.

| Port | Protocol | For |
|---|---|---|
| 32400 | tcp | access to the Plex Media Server (required) |
| 1900 | udp | the Plex DLNA Server; discovery |
| 5353 | udp | older Bonjour/Avahi network discovery; discovery |
| 8324 | tcp | controlling Plex for Roku via Plex Companion |
| 32410 | udp | GDM network discovery; discovery |
| 32412-32414 | udp | GDM network discovery; discovery |
| 32469 | tcp | the Plex DLNA Server |

Recognised by process `Plex Media Serv` or `Plex DLNA Serve`, unit `plexmediaserver.service`, or image `plex` or `pms-docker`.

## portainer

Portainer. Source: <https://docs.portainer.io/start/requirements-and-prerequisites>.

| Port | Protocol | For |
|---|---|---|
| 9443 | tcp | the UI and API |
| 8000 | tcp | the TCP tunnel server for Edge Agents (only with Edge Compute) |

Recognised by process `portainer`, or image `portainer`, `portainer-ce` or `portainer-ee`.

Not opened: 9000/tcp, the UI over plain HTTP in older installs. Its entry is `{port: 9000}`.

## samba

Samba file sharing. Source: <https://manpages.ubuntu.com/manpages/noble/man5/smb.conf.5.html>.

| Port | Protocol | For |
|---|---|---|
| 445 | tcp | SMB over TCP |
| 139 | tcp | NetBIOS session (SMB for older clients) |
| 137 | udp | NetBIOS name service (nmbd); discovery |
| 138 | udp | NetBIOS datagrams and browsing (nmbd); discovery |

Recognised by process `smbd` or `nmbd`, unit `smbd.service`, `nmbd.service` or `samba.service`, or image `samba`.

Not opened: 3702/udp and 5357/tcp, WS-Discovery. The separate wsdd package uses them so that Windows lists the share.

## syncthing

Syncthing. Source: <https://docs.syncthing.net/users/firewall.html>.

| Port | Protocol | For |
|---|---|---|
| 22000 | tcp | TCP based sync protocol traffic |
| 22000 | udp | QUIC based sync protocol traffic |
| 21027 | udp | local discovery broadcasts (IPv4) and multicasts (IPv6); discovery |

Recognised by process `syncthing`, unit `syncthing@*` or `syncthing.service`, or image `syncthing`.

Not opened: 8384/tcp, the web GUI. It listens on 127.0.0.1 unless you move it, and the tailnet reaches it if you do.

## unifi

UniFi Network Application. Source: <https://help.ui.com/hc/en-us/articles/218506997>.

| Port | Protocol | For |
|---|---|---|
| 8080 | tcp | device and application communication (devices inform here) |
| 8443 | tcp | application GUI and API |
| 3478 | udp | STUN, for device adoption and communication |
| 10001 | udp | device discovery during adoption; discovery |
| 1900 | udp | L2 discovery, the setting "Make application discoverable on L2 network"; discovery |
| 6789 | tcp | UniFi mobile speed test |
| 8880-8882 | tcp | hotspot portal redirection (HTTP) |
| 8843 | tcp | hotspot portal redirection (HTTPS) |

Recognised by unit `unifi.service`, or image `unifi`, `unifi-network-application` or `unifi-controller`.

Not opened:

- 27117/tcp, the local database, on loopback only.
- 5514/udp, remote syslog.
- 10101/udp, client fingerprinting.
- 5671/tcp, UXG traffic flow logs.
- 8444/tcp, the hotspot portal UniFi calls secure.
- 28082/tcp, packet capture.
- 20100-22100/udp, Site Magic.
