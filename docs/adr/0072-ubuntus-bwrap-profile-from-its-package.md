# 0072: Ubuntu's bwrap profile comes from Ubuntu's package, not from keelbox

Date: 2026-11-14. Status: accepted. Supersedes the first point of ADR 0059's decision (where the profile's text comes from); the rest of ADR 0059 stands.

## Context
ADR 0059 has keelbox write an AppArmor profile for `/usr/bin/bwrap` on Ubuntu 24.04, so the Codex and Claude Code sandboxes can start under `kernel.apparmor_restrict_unprivileged_userns = 1`. The text was Ubuntu 26.04's `bwrap-userns-restrict`, adapted for the 4.0 policy ABI and held in the keelbox binary.

Two things were wrong with that:

- The profile is part of the AppArmor project, which is under the GNU General Public License, version 2. keelbox is Apache-2.0. A binary that carries the profile carries GPL text without the licence and its terms.
- ADR 0059 said 24.04 does not ship the profile. It does not load one, but its `apparmor-profiles` package (in `noble-updates`, 4.0.1) ships the file as `/usr/share/apparmor/extra-profiles/bwrap-userns-restrict`, already written for the 4.0 ABI. keelbox was maintaining its own adaptation of a file Ubuntu maintains for that release.

Options: keep the text and ship it as a separate GPL file with the licence (the binary still has to carry or fetch it, and keelbox still maintains an adaptation); install `apparmor-profiles` (it also puts profiles for ping, syslogd, klogd, dnsmasq, nscd, avahi, Samba, php-fpm, traceroute and others in `/etc/apparmor.d` and loads them, on a box that asked for none of them); or take the one file from Ubuntu's package on the box.

## Decision
- **The profile's text is Ubuntu's, read on the box.** Where the restriction is on and `/etc/apparmor.d/bwrap-userns-restrict` is absent, the host module reads `/usr/share/apparmor/extra-profiles/bwrap-userns-restrict` if the `apparmor-profiles` package is installed. Otherwise it runs `apt-get download apparmor-profiles` in a private temporary directory, with apt's own signature check, unpacks the package there with `dpkg-deb -x`, reads the one file and removes the directory. The package is never installed.
- **Written under a header, loaded as in ADR 0059.** The file is `/etc/apparmor.d/keelbox-bwrap`: a "Managed by keelbox" header that names the package and its version, then Ubuntu's text unchanged. It is loaded with `apparmor_parser --replace --write-cache` and rolled back if the parser refuses it. It yields to Ubuntu's own file after a release upgrade.
- **A file without the header is replaced** with Ubuntu's text at the next apply.
- **The step is soft.** It needs the box's Ubuntu mirror. If the download fails, or the package is from before Ubuntu added the profile, the step fails with what to do and the run carries on: sshd, the firewall and the agent's network rules do not wait for it. Apply exits 1 and doctor's `sandbox` and `codex-sandbox` checks fail until it passes.
- **keelbox carries no AppArmor profile text**, so NOTICE does not list one.

## Consequences
- The policy on a 24.04 box is the one Ubuntu publishes for 24.04. It differs slightly from 26.04's (`px` where 26.04 has `pix`, no `mediate_deleted`); the end-to-end test checks the same properties on both.
- keelbox's file does not follow later Ubuntu updates to the profile. Removing `/etc/apparmor.d/keelbox-bwrap` and applying takes the current one.
- A 24.04 box whose apt sources leave out `noble-updates` gets no profile and is told to turn the source on.
- Applying to a 24.04 box downloads one small package (about 40 kB) the first time, and nothing afterwards.
