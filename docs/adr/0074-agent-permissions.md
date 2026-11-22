# 0074: The agent may have Ubuntu packages installed, through a root service that checks each request

Date: 2026-11-16. Status: accepted. Amends ADR 0005 (the agent account) and ADR 0030 (what a day-two run may do). Builds on ADR 0015 (files read as their owner), ADR 0019 and ADR 0032 (a root service started by a path unit on a file of the agent's).

## Context
The agent account has no sudo. When its work needs a system package, a library's headers for a build or a tool Ubuntu ships, it has to stop and hand the owner a manual step: log in as the admin, install it, come back. People expect an agent to get on with the common thing itself, and a box whose agent stops for `libpq-dev` is not doing its job. The only other way is passwordless sudo, with which none of the limits on the agent bind.

What must not be lost is what those limits protect: the agent gets no root shell, not the system Docker socket, no path to the admin, and cannot change keelbox's own files.

## Decision

### One setting, three modes
`users.agent.permissions` is `manual`, `auto` or `full`, after the manual, auto and bypass modes agent CLIs have for their own permissions.

- `manual`: the agent cannot change the system. `keelbox install` records a request; nothing is installed until the owner runs `keelbox approve BOX` from their computer.
- `auto`, the default: `keelbox install` has the packages installed straight away, with no root shell, inside the bounds below.
- `full`: passwordless sudo. `users.agent.sudo: true` is another way to write it; both keys given with values that disagree is a config error. `AgentSudo()` is true only for `full`.

A config without the key is `auto`. `keelbox up` asks, with Auto preselected, and writes the key only when it is not the default.

### The route
`keelbox install PACKAGE...` runs as the agent, without sudo. It writes one request (an ID, a time, names, an optional reason) to `~/.local/state/keelbox/install-request.json`, replaced atomically, and waits for root's record of it. `keelbox-install.path` starts `keelbox-install.service`, a root oneshot in the shape of ADR 0030, which reads the file through `Env.FS(agent)` with `system.ReadData`: as the agent, not following a final link, a regular file only, at most 16 KiB, decoded with no unknown field. The units are the same in every mode; the service reads the mode from the applied config at each run.

Only that service runs apt-get for a request. `keelbox approve` marks a request approved under a lock and starts the service, so an install is not tied to the SSH session of whoever approved it, and there is one place where the checks and the install happen.

### The bounds, enforced by the root side
- **Names only.** Lower-case letters, digits, `+`, `.` and `-`, starting with a letter or digit, at most 20. A version, release, architecture, path, URL or option fails the pattern, and the names reach apt after `--`.
- **Exactly a package, with a candidate.** `apt-cache policy` must print a stanza under each name with a candidate. apt reads a name it does not know as a pattern (`lib.` selects every package that matches) and a trailing `-` as "remove", in `apt-cache policy` and in `apt-get install` alike, so a stanza missing under a name, or present under any other, is a refusal. No source can be added and no local `.deb` is taken.
- **The transaction, not the request.** `apt-get install -s` is read first. A request is refused when it would remove a package, when it would install or upgrade a package on the deny-list (also as a dependency or a recommended package of what was asked for), or when it would touch more than 300 packages.
- **The deny-list** is one table, `internal/installs/names.go`, by family and with the reason for each: remote login servers, firewall tools, sudo and other tools that run commands as another account, Docker's engine and containerd, Tailscale, AppArmor and other security modules, apt and unattended-upgrades, systemd and the system bus, PAM and NSS modules, cloud guest agents, network managers, kernels, boot loaders and kernel modules, schedulers other than cron and at, and proxies that take requests from the box itself. Each would replace or reconfigure what keelbox manages, or what reaching the box depends on.
- **Installs only.** `apt-get install -y --no-remove` with keelbox's usual apt options and lock timeout, so apt fails rather than remove a package if anything changed since the simulated run. A named package that is already installed is left alone. `NEEDRESTART_SUSPEND` is set: services are not restarted for a library an agent's request upgraded.
- **Paced.** One run at a time (systemd), at most every 2 seconds, at most 30 requests an hour and 20 waiting. The service does not hold the apply lock while apt-get runs: an apply that starts meanwhile waits for dpkg's lock, and is not turned away by something the agent started.

A dependency that is not on the deny-list may be upgraded when the new package needs a newer version of it. Refusing that would break the most common request: a `-dev` package depends on the exact version of its library, and a box that takes security updates only is often one version behind the archive.

The service runs with a lower CPU and I/O weight than system services and without `NoNewPrivileges`. Its work is the agent's and runs outside the agent's slice, so it should yield to the box's own services; and maintainer scripts are written for apt-get as root runs it by hand.

### The record
Every request and its outcome is a logbook record of kind `install` (ADR 0031): checked package names, the account, and for a refusal a reason in fixed words. The agent's stated reason and apt's output are not in it. Root keeps the list of packages installed for the agent in `/var/lib/keelbox/installs/installed.json`; apply never removes them, `keelbox status` lists them, and status and, once, apply print the `host.packages` YAML that keeps them for a rebuilt box.

With `manual`, a request that starts to wait raises the event `install-request` (ADR 0032), whose text is root's: the names and the command. `keelbox requests` shows the agent's stated reason as the agent's own text, with everything that does not print removed.

## Alternatives
- **Leave it manual.** No new route from the agent to root, and the agent keeps stopping for the owner. That is `manual`, kept as a mode; it is not the default because the default should let the common thing happen.
- **Give sudo.** One line of sudoers, and none of the limits on the agent then bind: the agent can read the admin's keys, reach the system Docker socket and change keelbox's own files. A sudoers rule for `apt-get install` alone is the same thing in effect: `apt-get` takes options that run commands (`-o`), installs local files, and removes packages. That is `full`, for a box handed to the agent whole.
- **A package manager in the agent's home** (Nix in single-user mode, Homebrew, conda, static binaries). No root at all. It does not give a build the headers and libraries of the system it links against, duplicates what apt already has, and puts a second supply chain on the box. mise already covers tools and runtimes in the home, and the primer keeps pointing there for those.

## Consequences
- **A day-two run adds software.** ADR 0030 said such runs only bring back the configured state or report. This one installs packages the config does not name, at the agent's request, within the bounds above, and records each in the logbook.
- **What `auto` gives up.** Every installed package widens what is on the box. A package with a flawed privileged program is a way up for a process of the agent's. Maintainer scripts run as root, from the sources the box has: Ubuntu's signed archive, the repositories keelbox adds, and any the owner added. Services start when their package is installed. Unattended security updates are the mitigation, not a guarantee. The security model says all of it.
- **The deny-list is names.** It cannot say what a package does, and a package outside it can still change how the box behaves, including one Ubuntu adds later that touches what keelbox manages.
- **An install outside the agent's limits.** apt-get runs in the system slice, not the agent's, so `users.agent.limits` does not count it; the service's weights are the bound, and a large install still fills the disk.
- **A cut-off install.** A restart or the service's one-hour limit can end apt-get part-way. The next run closes the request as failed and says how to finish dpkg's work (`sudo dpkg --configure -a`, as the admin); keelbox does not run that by itself.
- **What is tested.** The route is covered by unit tests and tests against the simulated box, and its checks were run against apt's own simulated output on Ubuntu 26.04. The end-to-end test runs the path unit, the service under systemd and a real `apt-get install` on a local test VM, in `auto` and in `manual` mode.
