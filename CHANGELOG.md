# Changelog

## 0.1.0 (2026-11-21)

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
