# Work on the box: git, packages, root

```sh
keelbox status acme         # what is not committed or pushed
keelbox requests acme       # packages the agent asked for
keelbox ssh acme --admin    # a shell with sudo
```

Agents write code in `~/projects`, as the agent: the account without sudo that the agent CLIs run as. Until that code is pushed or copied off, the box holds the only copy.

## Add an agent or a tool

```sh
keelbox add acme          # what acme has, and what can be added
keelbox add acme codex    # install Codex, then sign in
```

`keelbox add` sets it in the box's config, applies the config and runs the sign-in. It adds `claude-code`, `codex`, `gh`, `docker` and `node`: [reference](../reference/cli.md#add).

## Git identity and GitHub

git refuses to commit without a name and an email. `keelbox up` asks for both. On a box that is already set up, add them to its config and run `keelbox apply acme`.

```yaml
users:
  agent:
    git:
      name: Ada Lovelace
      email: ada@example.com
```

Apply sets the two in the agent's global git config, and `init.defaultBranch` to `main` when nothing sets one. Without them, `keelbox doctor acme` warns that commits will fail.

The identity is a name on commits. To push, the agent needs a GitHub sign-in: [`keelbox login acme gh`](signing-in.md).

## What is not committed or pushed

`keelbox status acme` lists each directory directly under `~/projects`.

```text
projects   4 in ~/projects, 3 with work that is only on this box
           shop: not a git repository
           api: 3 uncommitted changes, 2 commits not pushed
           notes: no remote
           site: clean and pushed
```

keelbox reads this with git, as the agent, and never fetches. "Not pushed" compares against what the last fetch or push on the box saw.

`keelbox doctor acme` warns, and never fails, while any project holds work that is only on the box. `keelbox destroy acme` reads the same list and asks before it deletes: [remove a box](removing-a-box.md).

To get work off the box, push it, or copy it from your computer with `scp -r agent@acme:projects/shop .`. [Backups](backups.md) keep copies of the agent's home on a schedule, once set up.

## What the agent may change

In its home the agent changes what it likes: its projects, its tools from mise, its own rootless Docker. `users.agent.permissions` in the config sets what it may change on the rest of the box.

| Level | Ubuntu packages | sudo |
|---|---|---|
| `manual` | `keelbox install` records a request, and nothing is installed until you approve it | none |
| `auto` | `keelbox install` has them installed at once, after root's checks | none |
| `full` | the agent installs them with sudo | passwordless, for anything |

`keelbox up` preselects `auto`, and a config without the key is `auto`. To change the level, set the key and run `keelbox apply acme`.

With `full`, none of keelbox's limits on the agent hold.

## Packages the agent asks for

On the box, the agent asks for an Ubuntu package without sudo:

```sh
keelbox install libpq-dev --why "psycopg builds against it"
```

A service running as root checks every request, at every level, and refuses one outside these bounds with the reason:

- package names only, from the apt sources the box already has
- no package removed
- no package on keelbox's deny-list touched: those that would replace or reconfigure what keelbox manages, or what reaching the box depends on

With `manual` the request waits, and your [notification channels](notifications.md) get an `install-request` message. From your computer:

```sh
keelbox requests acme            # what waits, with the agent's reason
keelbox approve acme 3f2a9c1e    # install it; --all approves every request
keelbox deny acme 3f2a9c1e
```

The package names passed root's checks. The reason is the agent's own text, and nothing checks it.

With `auto` the agent, or whatever instructed it, chooses the packages. Each one adds programs and services to the box, and its maintainer scripts run as root. A package with a flawed privileged program is a route to root that was not there before. The [security model](../security-model.md) has the rest.

## Packages that stay

`keelbox status acme` lists the packages installed for the agent, with the YAML that keeps them. A box rebuilt from the config lacks them until `host.packages` names them.

```yaml
host:
  packages:
    - libpq-dev
```

`keelbox apply acme` installs what the list names.

## Root

`keelbox ssh acme --admin` opens a shell as the admin, the box's account with sudo. keelbox removes no package; the admin does, with `sudo apt-get remove NAME`.
