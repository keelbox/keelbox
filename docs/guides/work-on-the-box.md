# Work on the box: git, packages, root

Agents write code in `~/projects`, as the agent: the account without sudo that the agent CLIs run as. Until that code is pushed or copied off, the box holds the only copy.

## Add an agent or a tool

Run `keelbox add acme` to see what the box has and what can be added (`claude-code`, `codex`, `gh`, `docker`, `node`). `keelbox add acme codex` updates the config, applies it and runs the sign-in.

## Git identity and GitHub

git refuses to commit without a name and email. `keelbox up` asks for them; on a box already set up, add them and run `keelbox apply acme`:

```yaml
users:
  agent:
    git:
      name: Ada Lovelace
      email: ada@example.com
```

To push, the agent also needs a GitHub sign-in: [`keelbox login acme gh`](signing-in.md).

## What is not committed or pushed

`keelbox status acme` lists each project in `~/projects` and whether it has uncommitted changes, unpushed commits or no remote. keelbox never fetches, so "not pushed" means since the box's last fetch or push. `keelbox destroy` shows the same list and asks before deleting.

To get work off the box, push it, or copy it with `scp -r agent@acme:projects/shop .`. [Backups](backups.md) copy the agent's home on a schedule.

## What the agent may change

The agent changes what it likes in its own home. `users.agent.permissions` sets what it may change on the rest of the box:

| Level | Ubuntu packages | sudo |
|---|---|---|
| `manual` | `keelbox install` asks you; nothing installs until you approve | none |
| `auto` (default) | `keelbox install` installs at once, after root's checks | none |
| `full` | the agent installs them with sudo | passwordless, for anything |

Set the key and run `keelbox apply acme` to change it. With `full`, none of keelbox's limits on the agent hold.

## Packages the agent asks for

On the box, the agent runs `keelbox install libpq-dev --why "psycopg builds against it"`. A root service refuses, with the reason, anything that is not a package name from the box's apt sources, would remove a package, or is on keelbox's deny-list (packages that would replace what keelbox manages or what reaching the box depends on).

With `manual`, the request waits and your [notification channels](notifications.md) get an `install-request`. From your computer:

```sh
keelbox requests acme            # what waits, with the agent's reason
keelbox approve acme 3f2a9c1e    # install it; --all approves every request
keelbox deny acme 3f2a9c1e
```

The reason is the agent's own text and is not checked.

With `auto`, the agent, or whatever instructed it, chooses the packages. Their maintainer scripts run as root, and a package with a flawed privileged program is a new route to root. See the [security model](../security-model.md).

`keelbox status acme` lists the packages installed for the agent. A box rebuilt from the config lacks them unless you add them to `host.packages`.

## Root

`keelbox ssh acme --admin` opens a shell as the admin, the box's account with sudo. To remove a package the agent asked for, run `sudo apt-get remove NAME` there.
