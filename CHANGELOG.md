# Changelog

## [0.2.1](https://github.com/keelbox/keelbox/compare/v0.2.0...v0.2.1) (2026-11-26)


### Bug Fixes

* **cli:** a refused host key names the config file and the command to run ([#21](https://github.com/keelbox/keelbox/issues/21)) ([7334c48](https://github.com/keelbox/keelbox/commit/7334c48141f1056783d738c4bcb151d493ba800e))
* **cli:** an apply from a second computer no longer takes the first one's key off ([#24](https://github.com/keelbox/keelbox/issues/24)) ([126079e](https://github.com/keelbox/keelbox/commit/126079ecbdafac693d5b9a1aa41470c8db7ab662))
* **doctor:** read sshd's config before the first SSH connection after a boot ([#20](https://github.com/keelbox/keelbox/issues/20)) ([7a27d77](https://github.com/keelbox/keelbox/commit/7a27d77504fddbedb8911a5e9790c841416007ea))
* **watch:** reopen conversations after any orderly reboot ([#19](https://github.com/keelbox/keelbox/issues/19)) ([16ec105](https://github.com/keelbox/keelbox/commit/16ec1059d2609840fcafc659ff0b6e9402b031c9))

## [0.2.0](https://github.com/keelbox/keelbox/compare/v0.1.1...v0.2.0) (2026-11-25)


### Features

* **host:** terminfo for Ghostty, cmux and kitty over ssh ([#13](https://github.com/keelbox/keelbox/issues/13)) ([150c0c5](https://github.com/keelbox/keelbox/commit/150c0c5e4266c0ea81557241b8cc52589947b0d6))
* **sessions:** click the box name to pick any session or window ([#11](https://github.com/keelbox/keelbox/issues/11)) ([34dc304](https://github.com/keelbox/keelbox/commit/34dc3048ec3f802b773eb2753e41067f130aedde))


### Bug Fixes

* check an LXC container's nesting and TUN device before apply and in doctor ([#14](https://github.com/keelbox/keelbox/issues/14)) ([0f1c7b5](https://github.com/keelbox/keelbox/commit/0f1c7b54b0a632a3c876131d2ac14aabe6f45256))
* **cli:** stop before any change on a box that reaches the internet over IPv6 only ([#17](https://github.com/keelbox/keelbox/issues/17)) ([2bbdb10](https://github.com/keelbox/keelbox/commit/2bbdb10b3d37f1b81acab467ad3cce277749dbaa))
* **hetzner:** say when Hetzner refuses a server and offer the ways on ([#18](https://github.com/keelbox/keelbox/issues/18)) ([3cc6846](https://github.com/keelbox/keelbox/commit/3cc684672f4e8210b59410727c2e4b06d4aa66fa))
* **provider:** list offered host keys fingerprint first, as host_key takes them ([#16](https://github.com/keelbox/keelbox/issues/16)) ([e603fc1](https://github.com/keelbox/keelbox/commit/e603fc1341db03f523a4e46cc4af46e6d79adb21))

## [0.1.1](https://github.com/keelbox/keelbox/compare/v0.1.0...v0.1.1) (2026-11-24)


### Features

* **installer:** clearer, shorter install output ([#3](https://github.com/keelbox/keelbox/issues/3)) ([b623ecf](https://github.com/keelbox/keelbox/commit/b623ecfb310eb0d71f128d7269df2dd806f04240))


### Bug Fixes

* **cli:** attach falls back to xterm-256color for a terminal the box does not know ([#5](https://github.com/keelbox/keelbox/issues/5)) ([dfe8e9d](https://github.com/keelbox/keelbox/commit/dfe8e9d24a8263803884cee7c0df62fb2fcddeb9))
* **cli:** self-update leaves a distribution package to its package manager ([#6](https://github.com/keelbox/keelbox/issues/6)) ([ef26656](https://github.com/keelbox/keelbox/commit/ef26656a5c2055ca592f8715bbde7c54018bf34e))
* **system:** set up unprivileged LXC, LXD and Proxmox containers ([#7](https://github.com/keelbox/keelbox/issues/7)) ([fba0a88](https://github.com/keelbox/keelbox/commit/fba0a888cad074d5450dacb124e51f98950a22eb))

## 0.1.0 (2026-11-22)

The first public release of keelbox. It turns an Ubuntu machine into a box for AI coding agents such as Claude Code and Codex: a machine at home, a VPS, or a server it creates for you on Hetzner, AWS, DigitalOcean or Google Cloud.

### Setting up a box

* `keelbox up` asks where the box runs, checks the machine, shows what it will change, then sets it up and runs each vendor's sign-in on your terminal. Run it again and it continues where it stopped. The config is one YAML file, and `keelbox apply` changes nothing on a second run.
* The agent works in its own account, without sudo and without the system Docker socket, and gets a rootless Docker daemon of its own. SSH takes keys only, UFW closes inbound ports apart from SSH, the tailnet and the LAN ports you choose, and keelbox pins every host key it connects to.
* Claude Code and Codex come from their vendors' installers and update themselves. keelbox never sees, stores or relays the agents' sign-ins.
* The agent gets Ubuntu packages with `keelbox install`. By default a root service installs them after checking each request against a deny-list. `users.agent.permissions` is `auto` (the default), `manual`, where you approve each request with `keelbox approve`, or `full`, which gives the agent passwordless sudo.

### Working on it

* The agent's tmux sessions survive a lost connection and a reboot. `keelbox attach` opens them again from your computer, and Tailscale and Remote Control reach them from a phone or the Claude app.
* `keelbox ssh BOX -- CMD`, `keelbox agent-skill install` and `keelbox skills` let an agent on your computer work with a box, and ask you before anything that changes it or costs money.
* Previews, backups with restic, notifications, and a primer at `/etc/keelbox/AGENTS.md` that tells the agents the box's rules.

### Looking after it

* Security updates install every day. A box reboots itself for one at night once it is idle, or weekly, or only when you run `keelbox reboot`, and the agents' conversations open again afterwards.
* `keelbox doctor` and `keelbox status` check the box, and a logbook records every apply. `keelbox update` moves your computer and a box to the current release in one command, and keelbox checks for a newer release once a day; `KEELBOX_NO_UPDATE_CHECK=1` turns that off.
* `keelbox park` and `resume` keep a cloud box's disk while it is off, `keelbox resize` changes its size, and `keelbox destroy` deletes it and lists what stays valid elsewhere.

### Known limits

* Laptops: macOS and Linux. Boxes: Ubuntu 24.04 and 26.04. Creating an Arm server on Hetzner has not been tested.
* The commands for agents on your computer were tried from Linux, not macOS.
* Resuming a Claude Code conversation after a reboot reads a file Claude Code keeps for each running process, which is not documented; without it the pane opens the directory's latest conversation.
