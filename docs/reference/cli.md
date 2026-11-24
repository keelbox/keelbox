# Commands and flags

```sh
keelbox help
keelbox COMMAND --help
```

Each command below shows its help text as the binary prints it, then what the help leaves out. The examples use a box named `acme`.

<!-- keelbox help: begin -->
```text
keelbox: sets up Ubuntu machines for AI coding agents.

Usage: keelbox COMMAND [box] [flags]

Set up:
  up [box]                  set up a box step by step; run it again to continue
  ls                        list your boxes and how far each is set up (also: boxes)
  agent-skill install|remove
      teach Claude Code and Codex on this computer to use keelbox
  init [path]               write a commented config to edit by hand
  create [box]              create the Hetzner, AWS, DigitalOcean or Google Cloud server, then apply
  resize [box]              change the size of the box's cloud server, up or down
  park [box]                stop paying for the server while the box is not needed; the disk is kept
  resume [box]              bring a parked box back on a new server, from its snapshot
  destroy [box]             delete the Hetzner, AWS, DigitalOcean or Google Cloud server keelbox
                            created
  forget <box>              stop managing a box from here; the machine is not touched

Use:
  attach [box]              open the agent's tmux session
  ssh [box] [-- command]    open a shell on the box as the agent, or run one command
  ssh-config                let ssh, scp and editors reach each box as BOX.keelbox
  sessions [box]            list the agent's tmux sessions
  skills [box]              copy skills from this computer to the agent on the box
  login [box] [sign-in]     sign in to Tailscale, the agent CLIs and GitHub
  expose <port>             on the box: publish a dev server on the tailnet
  report <title>            on the box: save a keelbox problem as a draft for the owner
  install <package>...      on the box: get Ubuntu packages installed, without sudo

Maintain:
  plan [box]                show what apply would change; changes nothing
  apply [box]               converge the box to its config; repeat any time
  doctor [box]              check the box works; exit 1 on any failure
  status [box]              short summary of the box
  reports [box]             read the keelbox reports the agent saved, and send one yourself
  requests [box]            list the packages the agent asked for that wait for you
  approve [box] [id]        let a package request of the agent's be installed
  deny [box] <id>           turn down a package request of the agent's
  notify test [box]         send a test message on each notification channel
  update [box]              update keelbox on this computer, then the box, in one command
  add [box] [what]          add an agent CLI or a tool to a box that is set up
  rollback [box]            re-apply with the keelbox the box ran before
  reboot [box]              restart the box now and wait until it is back
  backup [box]              back up the box now with restic
  restore [snapshot] [box]  restore a snapshot into a new directory

This computer:
  forget-token [box]        remove the Hetzner token from the credential store
  completion <shell>        print shell completion for bash, zsh or fish
  self-update               update keelbox to the current stable release
  version                   print versions (also: -v, --version)

[box] is a box name from ~/.config/keelbox/boxes/ or a config file path. Left out:
/etc/keelbox/keelbox.yaml on a box, else ./keelbox.yaml, else the only box in
~/.config/keelbox/boxes/; with several, keelbox asks which. Flags go after the command.

Exit codes: 0 done; 1 failed; 2 usage, config, or an answer missing without a terminal, with nothing
changed; 3 a step only you can do; 4 an apply carries on on the box; 130 Ctrl-C. ssh BOX -- CMD
exits with the command's code, or 255 when ssh did not log in.

keelbox COMMAND --help shows a command's flags and examples.
Docs: https://keelbox.dev/docs
```
<!-- keelbox help: end -->

A flag the command does not take is an error, exit 2, and the message suggests the nearest flag. `--yes` and `--verbose` are accepted by every command.

## Conventions

- With several boxes and none named, a command asks `Which box?`. Enter takes the box last used, else the first one set up. Without a terminal it lists the boxes and exits 2. A mistyped name gets a suggestion.
- The config is checked before anything runs. Every problem is listed with its key path, and the command exits 2.
- The agent account on a box may run `expose`, `install`, `report`, `status`, `version`, `init` and `completion`. Any other command tells the agent who runs it.

Where a command runs depends on `target.provider` in the box's [config](config.md#target):

| `target.provider` | Where the command runs |
|---|---|
| `local` | On this machine. A command that needs root runs itself again with `sudo`. |
| `ssh` | On `target.ssh.host`. keelbox uploads itself and the config, and runs the same command there with `sudo -n`. |
| `hetzner` | On the server named after the box, found through the Hetzner Cloud API, then as for `ssh`. |
| `aws`, `digitalocean`, `gcp` | On the box's server, found with your AWS CLI, doctl or gcloud, then as for `ssh`. The command checks that CLI's sign-in first. |

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success. `plan` exits 0 with or without changes. |
| 1 | The command ran and failed, or you answered no to a question that deletes or stops something. |
| 2 | A usage or config error, or an answer missing without a terminal. Nothing was changed. |
| 3 | `up` stopped at a step only you can do, after printing the command that continues. `install`, on the box: the request waits for `keelbox approve`. |
| 4 | keelbox stopped following an apply that carries on on the box, after printing the command that picks it up. From `apply`, `up`, `create`, `resize`, `resume`, and `status` on a terminal. |
| 130 | Ctrl-C. At a question nothing has changed. During `apply` or `up` the current step finishes first. |

A command that runs on the box exits with the box's exit code. When ssh itself fails, keelbox exits 1 and names what to check. `attach` and `ssh` pass on the exit status of the session; 255 becomes 1. `ssh BOX -- CMD` passes on the command's own code, and exits 255 when ssh did not log in.

Scripts and CI: [without the wizard](../guides/automation.md#exit-codes).

## Commands

### up

<!-- keelbox up --help: begin -->
```text
keelbox up [box]: set up a box step by step; run it again to continue.
Asks where the box runs, sets it up, offers each vendor's sign-in on your terminal
and prints how to reach it. Answers are kept until the box's config is written, so
a check that fails asks nothing twice. Without a terminal, pass --provider and --yes; a
server keelbox has not connected to before also needs --host-key, and the agent's commits
need --git-name and --git-email.

Examples:
  keelbox up         ask, then set it up; on this machine, asks for sudo
  keelbox up devbox  continue devbox where it stopped
  keelbox up nas --provider ssh --host 192.168.1.20 --ssh-user ubuntu --host-key SHA256:... --yes

Flags:
  --provider WHERE          ssh (an existing server), local (this machine), hetzner, aws,
                            digitalocean or gcp (a new server) or cloud-init (one you create from
                            the printed cloud-config)
  --name NAME               the box name (or give it as the argument)
  --yes                     go ahead without asking
  --host ADDRESS            ssh, cloud-init: the server's address
  --ssh-user NAME           ssh: the account your key logs in as now (default root)
  --host-key FPR            ssh: the server's SHA256 host key fingerprint, pinned at first
                            connection
  --no-host-key             cloud-init: leave the single-use host key out of the cloud-config
  --size TYPE               hetzner: the server type; aws: the Lightsail bundle; digitalocean: the
                            Droplet size (default: the cheapest with 2 vCPU and 4 GB); gcp: the
                            machine type (default e2-medium)
  --location NAME           hetzner: the location (default fsn1); aws: the region (default: the AWS
                            CLI's own); digitalocean: the region; gcp: the zone (default: gcloud's
                            own)
  --profile NAME            aws: the AWS CLI profile whose sign-in keelbox uses (default: the CLI's
                            own default)
  --doctl-context NAME      digitalocean: the doctl context whose sign-in keelbox uses (default:
                            doctl's current one)
  --gcp-project ID          gcp: the Google Cloud project the server is created in (default:
                            gcloud's own)
  --token-file PATH         hetzner: a mode 600 file with the API token, written to the config
                            instead of storing the token
  --hcloud-context NAME     hetzner: read the API token from this context of the hcloud CLI each
                            time; keelbox keeps no copy
  --ssh-key PATH            your SSH key (default ~/.ssh/id_ed25519, id_ecdsa or id_rsa)
  --agents LIST             claude-code, codex, both comma-separated (default) or none
  --agent-permissions MODE  what the agent may change: auto (default; it installs Ubuntu packages
                            with keelbox install), manual (it asks first) or full (sudo)
  --auto-reboot WHEN        when the box restarts for an update: nightly (default; 01:00 to 06:00 in
                            the box's timezone once idle), weekly (the same on Sunday night) or
                            never (you run keelbox reboot)
  --remote-control          run Claude Code Remote Control for the Claude app
  --gh=false                leave out the GitHub CLI and its sign-in
  --git-name NAME           the name on the agent's git commits; with --git-email (asked on a
                            terminal, offering this computer's)
  --git-email ADDRESS       the email on the agent's git commits
  --tailscale=false         no Tailscale: the box is reached over public SSH only
  --public-ssh MODE         when SSH closes: auto (outside the tailnet, the LAN included), lan (the
                            LAN keeps it; the default on a LAN), open or closed
  --lan-ports LIST          ssh, local: services found listening to keep open to the LAN: all, none,
                            or known services and ports such as unifi,8081
  --limits PCT              ssh, local: cap the agent's CPU and memory at PCT, such as 75%, or none
                            (default 75% where other services run)
  --skip-sign-in            finish without the sign-ins; keelbox login BOX does them later
  --adopt                   ssh, local: accept what keelbox lists as cut off or replaced on a
                            machine in use
  --verbose                 print every step and every command run on the box, instead of the
                            progress view
```
<!-- keelbox up --help: end -->

The steps one by one: [your first box](../getting-started.md).

- `up` writes the config to `~/.config/keelbox/boxes/<box>.yaml`. Until then the answers wait in `~/.config/keelbox/up-draft.json`, and the next `keelbox up` offers to carry on with them.
- `keelbox up acme` continues at the first step not done. On a box that is set up it prints the closing screen again.
- Flags for a box that already has a config are ignored with a warning: edit the config and run `keelbox apply acme`.
- A second `up` on a box while one runs stops at once.
- For Hetzner, `up` uploads your SSH public key to the project as `keelbox-<this computer's hostname>`, or reuses a project key with the same fingerprint.
- Under `sudo keelbox up`, the config goes to the home of the user who ran sudo.

Without a terminal every answer comes from a flag or a default. A missing one stops `up` before any change, exit 2, with an example command. Besides `--provider`, the box name and `--yes`, it needs:

| `--provider` | Also needed without a terminal |
|---|---|
| `ssh` | `--host`, and `--host-key` unless keelbox has connected to the server before. |
| `ssh`, `local` | `--lan-ports` when the firewall would block a service found listening. |
| `local` | sudo that asks for no password, or `up` run with `sudo`. `--ssh-key` when `~/.ssh/authorized_keys` has no key. |
| all but `local` | `--ssh-key` when `~/.ssh` has no `id_ed25519`, `id_ecdsa` or `id_rsa`. |
| `hetzner` | A token: `--token-file`, `--hcloud-context`, `HCLOUD_TOKEN` or one in the credential store. |
| `aws` | `--location` when the AWS CLI has no region set. |
| `digitalocean` | `--location`. |
| `gcp` | `--location` when gcloud has no zone set, and `--gcp-project` when it has no project. |
| `cloud-init` | Nothing more. It prints the cloud-config and exits 3; `keelbox up acme --host ADDRESS` continues. |

`up` exits 3 where the next step is yours: a key the server does not take, the cloud-config, a cloud CLI to install or sign in to, a sudo password over SSH, and the sign-ins. `--skip-sign-in` finishes without the sign-ins. More in [without the wizard](../guides/automation.md).

### agent-skill

<!-- keelbox agent-skill --help: begin -->
```text
keelbox agent-skill install|remove: teach Claude Code and Codex on this computer to use keelbox.
Puts keelbox's skill in ~/.claude/skills/keelbox and ~/.codex/skills/keelbox, for the CLIs
set up here, with rules that ask you before keelbox commands that change a box and refuse
approve and deny: permissions in ~/.claude/settings.json, and ~/.codex/rules/keelbox.rules.

Examples:
  keelbox agent-skill install
  keelbox agent-skill remove
```
<!-- keelbox agent-skill --help: end -->

### init

<!-- keelbox init --help: begin -->
```text
keelbox init [path]: write a commented config to edit by hand.

Examples:
  keelbox init  a config for this machine
  keelbox init acme.yaml --box acme --provider ssh

Flags:
  --box NAME       the box name
  --provider NAME  the target block to write: local (default), ssh, hetzner, aws, digitalocean or
                   gcp
  --force          overwrite an existing file
```
<!-- keelbox init --help: end -->

The default path is `keelbox.yaml`. What to do with the file: [config by hand](../guides/automation.md#config-by-hand).

### ls

<!-- keelbox ls --help: begin -->
```text
keelbox ls: list your boxes and how far each is set up (also: boxes).

Examples:
  keelbox ls

Flags:
  --json  the rows as JSON, for scripts and agents
```
<!-- keelbox ls --help: end -->

`ls` reads files on this computer only. The address shown is the box's tailnet address once keelbox has reached it there, else the public one.

| Status | Means | Command |
|---|---|---|
| `ready` | Set up. | |
| `ready, sign-ins left` | Set up, with sign-ins skipped. | `keelbox login acme` |
| `not created` | A cloud box whose server `up` has not created. | `keelbox up acme` |
| `waiting for the server` | A cloud-config was printed; you create the server. | `keelbox up acme` |
| `not applied` | Configured, or the server exists; the first apply has not run. | `keelbox up acme` |
| `stopped part-way` | The first apply started and did not finish. | `keelbox up acme` |
| `needs sign-in` | Applied; the sign-ins are left. | `keelbox up acme` |
| `signed in, apply pending` | Signed in; the closing apply is left. | `keelbox up acme` |
| `parked since <date>` | No server; its disk is a snapshot. | `keelbox resume acme` |
| `server destroyed <date>` | `keelbox destroy` deleted the server. | `keelbox up acme` or `keelbox forget acme` |
| `config error` | The config does not load. | `keelbox plan acme` |

`Which box?`, `keelbox status` and `keelbox status --all` use the same words.

### plan

<!-- keelbox plan --help: begin -->
```text
keelbox plan [box]: show what apply would change; changes nothing.
--impact lists, for a machine already in use, what the owner may care about: services
cut off from the network, software replaced, restarts, accounts, and what is kept,
with the config that keeps each. The first apply stops for the items marked *.

Examples:
  keelbox plan acme
  keelbox plan acme --impact  on a machine already in use

Flags:
  --local          run on this machine (with sudo), whatever target.provider says
  --ssh-user NAME  connect as NAME instead of target.ssh.user
  --binary PATH    keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or
                   the same release, downloaded and verified)
  --verbose        print every step and every command run on the box, instead of the progress view
  --impact         what apply would change that the machine's owner may care about
  --json           with --impact: the report as JSON
```
<!-- keelbox plan --help: end -->

Each step is one line: `=` unchanged, `+` would change, `!` the check failed. `plan` exits 1 only for a `!`.

Before the first apply, connect as the image's account: `keelbox plan acme --ssh-user ubuntu`.

`--impact` works out what is reachable from the firewall's state and probes nothing from outside. `--json` prints one object with the keys `impact`, `box`, `release`, `first_apply`, `machine`, `items`, `keep`, `steps`, `errors` and `adopted`. How to read a report: [a machine already in use](../guides/machine-in-use.md).

### apply

<!-- keelbox apply --help: begin -->
```text
keelbox apply [box]: converge the box to its config; repeat any time.
On a terminal, one line per module with the current step and elapsed time. Over
SSH the apply runs on the box and finishes if this window closes; Ctrl-C asks
whether to leave it running or stop it after the current step.

Examples:
  keelbox apply acme
  keelbox apply acme --ssh-user root  first apply on a new machine
  keelbox apply --local               on the box itself (runs with sudo)

Flags:
  --local            run on this machine (with sudo), whatever target.provider says
  --ssh-user NAME    connect as NAME instead of target.ssh.user
  --binary PATH      keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or
                     the same release, downloaded and verified)
  --verbose          print every step and every command run on the box, instead of the progress view
  --adopt            accept what plan --impact marks on a machine in use, such as replacing Ubuntu's
                     docker.io
  --allow-downgrade  move the box to an older release
```
<!-- keelbox apply --help: end -->

Each step is checked, changed if needed and checked again. A failed step stops the run, except an agent CLI that fails to install: the run continues and exits 1. Every apply is recorded in the [logbook](box-files.md#logbook).

- With provider `ssh` and an `--ssh-user` other than `target.ssh.user`, apply runs twice: as that account, then as the admin. The second run locks [the image's default account](../guides/adopting-a-box.md#the-images-default-account).
- The first apply stops before any change, exit 1, when `plan --impact` marks an item. `--adopt` accepts them: [a machine already in use](../guides/machine-in-use.md).
- A box last applied with a newer release is refused without `--allow-downgrade`: [updates](../guides/upgrades.md).
- In an LXC container without nesting, or without `/dev/net/tun` while Tailscale is on, apply stops before any change, exit 1, and prints the commands for the container's host: [an LXC container](../guides/troubleshooting.md#an-lxc-container).
- Only `apply` installs `/usr/local/bin/keelbox` on the box, keeping the binary it replaces for `rollback`.
- Over SSH the apply runs on the box as `keelbox-apply-<id>.service`. keelbox retries a lost connection for 3 minutes, then exits 4.
- The next `apply`, `up` or `status` reports once how a run this computer left ended. `apply` then exits with that result and applies nothing: [closing your laptop during setup](../guides/troubleshooting.md#closing-your-laptop-during-setup).
- One apply runs on a box at a time. Over SSH a second one follows the first.
- Ctrl-C lets the current step finish, records the run as `interrupted` and exits 130. Over SSH it asks first, with leaving the apply running preselected: exit 4.
- Without a terminal, with `TERM=dumb` or with `--verbose`, each step is a line: `ok`, `change`, `defer`, `FAILED` or `skip`.

### doctor

<!-- keelbox doctor --help: begin -->
```text
keelbox doctor [box]: check the box works; exit 1 on any failure.

Examples:
  keelbox doctor acme

Flags:
  --local          run on this machine (with sudo), whatever target.provider says
  --ssh-user NAME  connect as NAME instead of target.ssh.user
  --binary PATH    keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or
                   the same release, downloaded and verified)
  --verbose        print every step and every command run on the box, instead of the progress view
```
<!-- keelbox doctor --help: end -->

Each finding is `ok`, `warn` or `FAIL`. A step `apply` would change is a `warn`. Only the box's scheduled doctor records its result and sends notifications. What a finding means: [check the box and fix problems](../guides/troubleshooting.md).

### status

<!-- keelbox status --help: begin -->
```text
keelbox status [box]: short summary of the box.
With --all, one line per box in the registry, read from each box's status snapshot over ssh.

Examples:
  keelbox status acme
  keelbox status --all  every box; exit 1 unless all are reached and healthy

Flags:
  --local          run on this machine (with sudo), whatever target.provider says
  --ssh-user NAME  connect as NAME instead of target.ssh.user
  --binary PATH    keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or
                   the same release, downloaded and verified)
  --all            every box in the registry, read at once
  --json           the box's status snapshot, read now, as JSON; with --all, the rows
```
<!-- keelbox status --help: end -->

`status` exits 0 once it reaches the box. On a terminal it first follows an apply still running there.

`--all` reads the [status snapshot](box-files.md#the-status-snapshot) each box writes every two minutes, over ssh as the admin, with a 12-second limit per box.

| `REACH` | Means |
|---|---|
| `ok` | Reached, and the snapshot is under 10 minutes old. |
| `stale (HH:MM)` | The snapshot is older: the box's watch run has stopped. |
| `unreachable` | ssh did not reach the box. The reason is under the table. |
| `parked`, `server destroyed` | As in [ls](#ls). Neither makes the exit code 1. |
| `no status` | The box gave no snapshot. The reason is under the table. |

`--all` exits 0 when no box is stale, unreachable or without status, and no box's last scheduled doctor found a failure. `--all --json` prints an array of `{box, reach, error, parked, destroyed, status}`.

### create

<!-- keelbox create --help: begin -->
```text
keelbox create [box]: create the Hetzner, AWS, DigitalOcean or Google Cloud server, then apply.

Examples:
  keelbox create acme

Flags:
  --binary PATH  keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or the
                 same release, downloaded and verified)
  --verbose      print every step and every command run on the box, instead of the progress view
  --adopt        replace conflicting software on the box
```
<!-- keelbox create --help: end -->

`create` fails when a server named after the box exists. If the apply fails after the server is created, the server stays and is billed: run `keelbox apply acme` or `keelbox destroy acme`. What each cloud gets: [the four clouds](clouds.md).

### resize

<!-- keelbox resize --help: begin -->
```text
keelbox resize [box]: change the size of the box's cloud server, up or down.
Lists the sizes the server can change to, with estimated prices where the cloud gives them,
and asks. The box restarts, so running sessions stop; everything on its disk stays, then
keelbox applies as usual and rewrites the size in the box's config. On AWS (Lightsail) a
box can only grow: the instance is replaced by a larger one and cannot shrink again.

Examples:
  keelbox resize acme                     list the sizes with prices, then ask
  keelbox resize acme --size cpx32 --yes  without a terminal

Flags:
  --size SIZE    the size to change to, as the cloud names it: the server type, bundle, Droplet size
                 or machine type
  --yes          restart the box without asking
  --binary PATH  keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or the
                 same release, downloaded and verified)
  --verbose      print every step and every command run on the box, instead of the progress view
```
<!-- keelbox resize --help: end -->

- A `--size` not in the list is refused, exit 2, with the sizes that are.
- Without a terminal, resize exits 2 without `--size` (after printing the list) or without `--yes`.
- The size key in the config is rewritten in place, comments kept. A config not laid out as keelbox writes it is left alone, and the line to set is printed.
- On AWS the box needs `/etc/cloud/cloud.cfg.d/99-keelbox.cfg`. Without it resize stops before any change, exit 1; `keelbox apply acme` puts it there.
- On AWS, if both the new plan and the old one fail, the box is left parked: `keelbox resume acme`.

What each cloud does, costs and takes: [resize](../guides/resize-park-resume.md#resize).

### park

<!-- keelbox park --help: begin -->
```text
keelbox park [box]: stop paying for the server while the box is not needed; the disk is kept.
Shuts the box down, saves its disk as a snapshot and, only once the cloud reports the
snapshot complete, deletes the server. Snapshot storage stays billed; the public
address is given up. keelbox resume brings the box back, with its files and sign-ins
as they were. Before it asks, park estimates what the server costs and what the snapshot
will, where the cloud gives prices, and says how long parking and resuming typically take.

Examples:
  keelbox park acme  estimates what it saves and costs, says how long it takes, then asks

Flags:
  --yes      park without asking
  --verbose  print a line for every step, as in a log, instead of the list of steps
```
<!-- keelbox park --help: end -->

- Without a terminal and without `--yes`, park exits 2.
- On Google Cloud a VM with a second disk is refused, exit 1: the snapshot holds the boot disk only.
- On AWS the box needs the cloud-init file named under [resize](#resize), or park exits 1.
- On a parked box, park says so and exits 0.
- If deleting the server fails, the snapshot is complete and the server is off. Run `keelbox park acme` again to finish, or `keelbox resume acme` to start the server again.

What stays billed and who can read the snapshot: [park](../guides/resize-park-resume.md#park).

### resume

<!-- keelbox resume --help: begin -->
```text
keelbox resume [box]: bring a parked box back on a new server, from its snapshot.
Creates the server again from the parked snapshot, with the same SSH host keys and a
new public address, applies as usual, and deletes the snapshot once that apply passed.

Examples:
  keelbox resume acme

Flags:
  --binary PATH  keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or the
                 same release, downloaded and verified)
  --verbose      print every step and every command run on the box, instead of the progress view
```
<!-- keelbox resume --help: end -->

- On a running box that is not parked, resume says so and exits 0. One that is switched off is started.
- The size comes from the config. If it cannot be ordered now, or its disk is too small for the snapshot, resume creates nothing and names sizes that fit.
- This computer pins the box's known host keys under the new address. A computer that never connected to the box asks about the fingerprint, or needs `target.ssh.host_key`.
- A failed resume keeps the snapshot. Run it again to carry on with the server that exists.

The new address and the tailnet: [resume](../guides/resize-park-resume.md#resume).

### destroy

<!-- keelbox destroy --help: begin -->
```text
keelbox destroy [box]: delete the Hetzner, AWS, DigitalOcean or Google Cloud server keelbox created.
First reads the agent's projects on the box. Where one is not a git repository, has
uncommitted changes, commits not pushed or no remote, or the box does not answer,
destroy lists what it found and asks, No by default; without a terminal it refuses.
On a parked box it deletes the snapshot, which holds everything that was on the box.

Examples:
  keelbox destroy acme  checks the projects, then asks for the box name

Flags:
  --yes           delete without typing the box name; work that is only on the box still stops it
  --discard-work  skip the check: delete the server with whatever work is only on it
```
<!-- keelbox destroy --help: end -->

- destroy deletes only a server that carries keelbox's labels, with the firewall and address keelbox created for it.
- A no to either question, or a wrong box name, deletes nothing and exits 1.
- The config stays, so `keelbox up acme` creates the server again. `keelbox ls` shows `server destroyed`.
- A box with both a server and a parked snapshot loses both.
- On provider `ssh` or `local` it changes nothing and names `keelbox forget`.

What the check for work reads, and what else to delete: [remove a box](../guides/removing-a-box.md).

### forget

<!-- keelbox forget --help: begin -->
```text
keelbox forget <box>: stop managing a box from here; the machine is not touched.
Removes the box's config from ~/.config/keelbox/boxes/ with its pinned host keys.
A cloud server is not deleted, and forget removes what keelbox destroy needs to
find it, so destroy comes before forget where the server is to go. forget says so,
and its question keeps the box unless you choose otherwise.

Examples:
  keelbox forget nas  asks first

Flags:
  --yes  forget without asking
```
<!-- keelbox forget --help: end -->

Declining prints `Nothing changed.` and exits 1. Without a terminal and without `--yes`, forget exits 2. A parked box's snapshot stays in the cloud account and is billed. What stays on the machine: [remove a box](../guides/removing-a-box.md).

### notify

<!-- keelbox notify --help: begin -->
```text
keelbox notify test [box]: send a test message on each notification channel.
Runs on the box as root, through the same senders the box uses, with the secret files
the config names. Exit 1 if any channel fails.

Examples:
  keelbox notify test acme
  keelbox notify test acme --channel phone  one channel

Flags:
  --local          run on this machine (with sudo), whatever target.provider says
  --ssh-user NAME  connect as NAME instead of target.ssh.user
  --binary PATH    keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or
                   the same release, downloaded and verified)
  --channel NAME   only the channel called NAME
```
<!-- keelbox notify --help: end -->

The test ignores `notify.mute`, each channel's `level` and quiet hours. It reads the secret files the config names, so you can test a channel before you apply it. It exits 1 when the config has no channel, and 2 for a `--channel` the config lacks. A failure names the HTTP status or network problem, never a URL, header or token. Channels and events: [notifications](../guides/notifications.md).

### update

<!-- keelbox update --help: begin -->
```text
keelbox update [box]: update keelbox on this computer, then the box, in one command.
Installs the current keelbox release here, as self-update does, then applies
it to the box, as apply does: the box gets that keelbox and what it brings.
With the current release already here, it applies it.

Examples:
  keelbox update acme

Flags:
  --local            run on this machine (with sudo), whatever target.provider says
  --ssh-user NAME    connect as NAME instead of target.ssh.user
  --binary PATH      keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or
                     the same release, downloaded and verified)
  --verbose          print every step and every command run on the box, instead of the progress view
  --adopt            accept what plan --impact marks on a machine in use, such as replacing Ubuntu's
                     docker.io
  --allow-downgrade  move this computer and the box to an older release
```
<!-- keelbox update --help: end -->

The new keelbox runs the apply, so the box gets the release just installed. update stops before touching the box if it cannot read the release or replace the binary, or a package manager installed the binary.

### add

<!-- keelbox add --help: begin -->
```text
keelbox add [box] [what]: add an agent CLI or a tool to a box that is set up.
Sets it in the box's config, applies the config, and runs the sign-in it needs.
With nothing named, lists what the box has and what can be added.
Adds: claude-code, codex, gh, docker, node.

Examples:
  keelbox add acme              what acme has, and what can be added
  keelbox add acme codex        install Codex, then sign in
  keelbox add acme codex --api  sign in with an OpenAI API key

Flags:
  --local          run on this machine (with sudo), whatever target.provider says
  --ssh-user NAME  connect as NAME instead of target.ssh.user
  --binary PATH    keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or
                   the same release, downloaded and verified)
  --verbose        print every step and every command run on the box, instead of the progress view
  --api            with claude-code or codex: sign in for API billing once it is installed
```
<!-- keelbox add --help: end -->

| Name | Also typed as | In the config | Sign-in after |
|---|---|---|---|
| `claude-code` | `claude` | in `agents` | `claude-code` |
| `codex` | | in `agents` | `codex` |
| `gh` | `github` | `runtime.gh: true` | `gh` |
| `docker` | | `runtime.docker: true` | none |
| `node` | | `runtime.node: true` | none |

The config keeps its comments and every other line. A file keelbox cannot edit that way is left alone, and the line to set is printed. Without a terminal the sign-in command is printed. No command takes one of these off a box.

### rollback

<!-- keelbox rollback --help: begin -->
```text
keelbox rollback [box]: re-apply with the keelbox the box ran before.

Examples:
  keelbox rollback acme

Flags:
  --local          run on this machine (with sudo), whatever target.provider says
  --ssh-user NAME  connect as NAME instead of target.ssh.user
  --binary PATH    keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or
                   the same release, downloaded and verified)
  --verbose        print every step and every command run on the box, instead of the progress view
```
<!-- keelbox rollback --help: end -->

It applies the last applied config with the previous binary, `/var/lib/keelbox/keelbox.previous`. The binaries swap, so a second rollback returns to the newer release. What a rollback does not undo: [updates](../guides/upgrades.md).

### reboot

<!-- keelbox reboot --help: begin -->
```text
keelbox reboot [box]: restart the box now and wait until it is back.
Saves the agent's tmux sessions and reboots the box. Programs in the sessions stop.

Examples:
  keelbox reboot acme  asks first

Flags:
  --yes            reboot without asking
  --local          run on this machine (with sudo), whatever target.provider says
  --ssh-user NAME  connect as NAME instead of target.ssh.user
  --binary PATH    keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or
                   the same release, downloaded and verified)
```
<!-- keelbox reboot --help: end -->

[When the box reboots itself](../guides/upgrades.md#reboots).

### login

<!-- keelbox login --help: begin -->
```text
keelbox login [box] [sign-in]: sign in to Tailscale, the agent CLIs and GitHub.
Each sign-in is the vendor's own flow on your terminal; keelbox never sees the credentials.
For each: sign in now, skip it, or check again; Ctrl-C stops.
Sign-ins: tailscale, claude-code, codex, gh.
Claude Code and Codex can also sign in for API billing, with no plan.

Examples:
  keelbox login acme
  keelbox login acme codex        only Codex
  keelbox login acme codex --api  Codex with an OpenAI API key

Flags:
  --force  sign in again, even where already signed in
  --api    with claude-code or codex: sign in for API billing (an Anthropic Console account, an
           OpenAI API key)
```
<!-- keelbox login --help: end -->

| Sign-in | Also typed as | Account | Runs |
|---|---|---|---|
| `tailscale` | | admin | `sudo tailscale up --hostname=<tailnet name>` |
| `claude-code` | `claude` | agent | `claude auth login`; with `--api`, `claude auth login --console` |
| `codex` | | agent | `codex login --device-auth`; with `--api`, `codex login --with-api-key` |
| `gh` | `github` | agent | `GH_PROMPT_DISABLED=1 gh auth login --hostname github.com --git-protocol https --web --clipboard=false`, then `gh auth setup-git`. |

- `tailscale` is offered with Tailscale on and no `access.tailscale.auth_key_file`; `gh` with `runtime.gh` on. Naming a sign-in runs it without the question.
- `--api` needs `claude-code` or `codex` named, and runs even where already signed in. For Codex, a prompt on the box reads the key with echo off and passes it to Codex's input: [with an API key](../guides/signing-in.md#with-an-api-key).
- login needs a terminal (exit 2 without one) and an SSH key under `users.agent`.
- It exits 1 when a sign-in did not finish or could not be checked. One that could not be checked shows as `not checked`, never as signed out.
- With `sessions.remote_control`, login also ends Claude Code's first run once its two questions are answered: [the Claude app](../guides/remote-control.md).

What each vendor's sign-in asks of you: [sign in](../guides/signing-in.md).

### attach

<!-- keelbox attach --help: begin -->
```text
keelbox attach [box]: open the agent's tmux session.

Examples:
  keelbox attach
  keelbox attach acme --session review

Flags:
  --session NAME  the tmux session to open or create (default main)
```
<!-- keelbox attach --help: end -->

attach runs `tmux new-session -A -s NAME` as the agent, creating the session if needed. A name has letters, digits, `.`, `_` and `-`, at most 64 characters; any other is exit 2. After a lost connection attach reopens the session once the box answers; Ctrl-C stops it, exit 130. Leaving and coming back: [sessions](../guides/sessions.md).

### ssh-config

<!-- keelbox ssh-config --help: begin -->
```text
keelbox ssh-config: let ssh, scp and editors reach each box as BOX.keelbox.
Writes ~/.config/keelbox/ssh_config: a Host for each box, as its agent, at its tailnet
address, with keelbox's pinned host keys. Commands that move a box write it again.
Asks before adding its Include as the first line of ~/.ssh/config.

Examples:
  keelbox ssh-config
  ssh acme.keelbox  then

Flags:
  --list  print the file, and change nothing in ~/.ssh/config
```
<!-- keelbox ssh-config --help: end -->

A parked box, or a cloud box off the tailnet, gets a comment instead. To stop, delete the `Include` line.

### sessions

<!-- keelbox sessions --help: begin -->
```text
keelbox sessions [box]: list the agent's tmux sessions.

Examples:
  keelbox sessions acme

Flags:
  --json  the sessions as JSON
```
<!-- keelbox sessions --help: end -->

### skills

<!-- keelbox skills --help: begin -->
```text
keelbox skills [box]: copy skills from this computer to the agent on the box.
Lists the skills in ~/.claude/skills, ~/.agents/skills and ~/.codex/skills to tick, none ticked,
and copies those chosen to the same folders in the agent's home on the box.
Files that may hold a secret (.env, keys, credentials) and links are left out.
The agent can read everything copied and run a skill's scripts.

Examples:
  keelbox skills acme
  keelbox skills acme --skill review,deploy  without a terminal
  keelbox skills acme --list                 the skills on this computer

Flags:
  --skill NAME[,NAME]  copy these, without asking which
  --list               list the skills on this computer, and copy nothing
  --overwrite          replace a different skill of the same name on the box, without asking
```
<!-- keelbox skills --help: end -->

A skill is a folder with a `SKILL.md`; plugins' skills are not listed. Skills are copied as the agent, and replace a different skill of the same name only after a yes or with `--overwrite`. Links in a skill are not followed.

### ssh

<!-- keelbox ssh --help: begin -->
```text
keelbox ssh [box] [-- command]: open a shell on the box as the agent, or run one command.
After --, the command runs without a terminal, each argument as one word, and keelbox
exits with its exit code; 255 when ssh did not log in. It asks nothing, so the host key
must be pinned already. Shell syntax goes in one argument: -- sh -c 'cd app && make'.

Examples:
  keelbox ssh acme
  keelbox ssh acme --admin  as the admin, with sudo
  keelbox ssh acme -- git -C projects/app status
      one command

Flags:
  --admin  log in as the admin instead of the agent
```
<!-- keelbox ssh --help: end -->

It uses keelbox's own pinned `known_hosts`. With `--`, a cloud box on the tailnet is reached without asking its cloud.

### expose

<!-- keelbox expose --help: begin -->
```text
keelbox expose <port>: on the box: publish a dev server on the tailnet.
Run by the agent on the box: publishes 127.0.0.1:<port> at an HTTPS URL on the
tailnet only, with the box's Tailscale certificate.

Examples:
  keelbox expose 3000
  keelbox expose 5173 --name web
  keelbox expose --list

Flags:
  --name NAME    the preview's name (default: the port number)
  --list         list previews
  --remove NAME  stop publishing a preview
```
<!-- keelbox expose --help: end -->

- A name has lowercase letters, digits and hyphens, at most 32 characters.
- A new name gets the lowest free port in `previews.ports`. Exposing a name again keeps its HTTPS port and points it at the new local port.
- expose waits up to 30 seconds for the previews service, and up to two minutes for the box's first certificate.
- It refuses when the box is not on a tailnet.

Opening a preview from your devices: [preview a dev server](../guides/previews.md).

### report

<!-- keelbox report --help: begin -->
```text
keelbox report <title>: on the box: save a keelbox problem as a draft for the owner.
Run by the agent on the box, only after the user said yes in the conversation. The draft
stays in the box's inbox; nothing is sent. The owner reads it with keelbox reports.

Examples:
  keelbox report "expose: says nothing listens" --area expose --detail "exact error"
  keelbox report "codex sandbox fails" --area sandbox --detail - < error.txt

Flags:
  --detail TEXT  the exact error and what was run, at most 2000 characters; - reads it from stdin
  --area AREA    the part of keelbox: apply, doctor, status, expose, previews, sandbox, codex,
                 claude-code, remote-control, sessions, tailnet, agents, notifications, backups,
                 docs, security, other (default other)
```
<!-- keelbox report --help: end -->

With `reports.enabled: false` nothing is saved. The box takes at most 5 reports a day and keeps 30. Reading and sending: [reports](../guides/reports.md).

### install

<!-- keelbox install --help: begin -->
```text
keelbox install <package>...: on the box: get Ubuntu packages installed, without sudo.
Run by the agent on the box. It asks root's install service, which checks the request:
package names only, from the apt sources the box already has, nothing on keelbox's
deny-list, nothing removed. With users.agent.permissions: auto the packages are installed
and the result printed (exit 0 when installed). With manual the request is recorded for
the owner's keelbox approve and the command exits 3.

Examples:
  keelbox install libpq-dev --why "psycopg builds against it"
  keelbox install jq sqlite3  several names are one request
  keelbox install --status    what became of the last requests

Flags:
  --why TEXT  what the work needs it for, in a line; the owner sees it as the agent's own words
  --status    list the last requests and what became of each
```
<!-- keelbox install --help: end -->

- A request names at most 20 packages, by name only: no version, architecture, path, URL or option.
- A request that would install or upgrade over 300 packages is refused. The box takes at most 30 requests an hour.
- Exit 1: refused, denied, failed, no answer from the service, or not a box; the reason is printed. Exit 2: no package named, or not run by the agent.
- The command waits up to 90 seconds for the service to take the request and up to 30 minutes for the install. An install under way finishes without it.
- `--status` shows each request as `pending`, `approved`, `installing`, `installed`, `refused`, `failed` or `denied`.
- With `users.agent.permissions: full` it works as with `auto`.

The three levels: [what the agent may change](../guides/work-on-the-box.md#what-the-agent-may-change).

### requests

<!-- keelbox requests --help: begin -->
```text
keelbox requests [box]: list the packages the agent asked for that wait for you.
With users.agent.permissions: manual, keelbox install on the box records a request and
installs nothing. This lists what waits: who asked, when, the package names, which passed
keelbox's checks, and the agent's stated reason, which is the agent's own text.

Examples:
  keelbox requests acme
  keelbox requests acme --all  also the requests already decided

Flags:
  --local          run on this machine (with sudo), whatever target.provider says
  --ssh-user NAME  connect as NAME instead of target.ssh.user
  --binary PATH    keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or
                   the same release, downloaded and verified)
  --all            also the last requests that were installed, refused, failed or denied
```
<!-- keelbox requests --help: end -->

The box keeps at most 20 requests waiting and the last 50 decided. `keelbox status` shows how many wait; `keelbox ls` shows the count this computer last saw.

### approve

<!-- keelbox approve --help: begin -->
```text
keelbox approve [box] [id]: let a package request of the agent's be installed.
Marks the request approved and starts root's install service on the box, which checks
it again, with the same bounds as any request, and installs it with apt-get. The install
carries on if this window closes. Without an ID it approves the only request waiting.

Examples:
  keelbox approve acme 3f2a9c1e
  keelbox approve acme --all  every request that waits

Flags:
  --local          run on this machine (with sudo), whatever target.provider says
  --ssh-user NAME  connect as NAME instead of target.ssh.user
  --binary PATH    keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or
                   the same release, downloaded and verified)
  --all            approve every request that waits
```
<!-- keelbox approve --help: end -->

- A request the box no longer allows is refused at the second check, and approve exits 1 with the reason.
- While an apply runs the service waits: run `keelbox approve acme` again afterwards.
- With several requests waiting and no ID, approve lists them and exits 2.

### deny

<!-- keelbox deny --help: begin -->
```text
keelbox deny [box] <id>: turn down a package request of the agent's.
Nothing is installed; the agent's keelbox install --status says the owner denied it.

Examples:
  keelbox deny acme 3f2a9c1e

Flags:
  --local          run on this machine (with sudo), whatever target.provider says
  --ssh-user NAME  connect as NAME instead of target.ssh.user
  --binary PATH    keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or
                   the same release, downloaded and verified)
```
<!-- keelbox deny --help: end -->

A request you approved can still be denied until the install service starts on it.

### reports

<!-- keelbox reports --help: begin -->
```text
keelbox reports [box]: read the keelbox reports the agent saved, and send one yourself.
Reports never leave your box unless you send them. --send shows the exact text with
tokens, addresses and paths redacted, lets you edit it, asks, then opens a prefilled
GitHub issue in your browser for you to submit; security reports go to the private
advisory form. keelbox posts nothing and uses no token.

Examples:
  keelbox reports acme  list the drafts
  keelbox reports acme --show 1
  keelbox reports acme --send 1

Flags:
  --local          run on this machine (with sudo), whatever target.provider says
  --ssh-user NAME  connect as NAME instead of target.ssh.user
  --binary PATH    keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or
                   the same release, downloaded and verified)
  --show N         print report N in full, with the box facts keelbox added
  --send N         review, redact and edit report N, then open it as a GitHub issue
  --delete N       delete report N from the box
```
<!-- keelbox reports --help: end -->

`--send` runs on this computer and needs a terminal. It redacts tokens, private keys, email addresses, tailnet names and addresses, public IP addresses, the box name, home paths and project names, and asks whether to keep the project names. The editor is `$VISUAL`, `$EDITOR` or `vi`. Without a browser the link is printed. More in [reports](../guides/reports.md).

### backup

<!-- keelbox backup --help: begin -->
```text
keelbox backup [box]: back up the box now with restic.

Examples:
  keelbox backup acme
  keelbox backup acme --list  list snapshots

Flags:
  --local          run on this machine (with sudo), whatever target.provider says
  --ssh-user NAME  connect as NAME instead of target.ssh.user
  --binary PATH    keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or
                   the same release, downloaded and verified)
  --list           list snapshots
```
<!-- keelbox backup --help: end -->

This is the same run `keelbox-backup.timer` starts on `backup.schedule`. It fails when `backup.repository` is not set. Setting up a repository: [back up and restore](../guides/backups.md).

### restore

<!-- keelbox restore --help: begin -->
```text
keelbox restore [snapshot] [box]: restore a snapshot into a new directory.
The latest snapshot unless one is given; over the original path with --overwrite.

Examples:
  keelbox restore acme  the latest, into a new directory
  keelbox restore 4f3a9c1e acme --overwrite

Flags:
  --local          run on this machine (with sudo), whatever target.provider says
  --ssh-user NAME  connect as NAME instead of target.ssh.user
  --binary PATH    keelbox binary to upload (default: this one, keelbox-linux-<arch> beside it, or
                   the same release, downloaded and verified)
  --target PATH    a new or empty directory to restore into
  --overwrite      restore over the original path, replacing files the snapshot has
```
<!-- keelbox restore --help: end -->

- The new directory is `<home>/keelbox-restore/<snapshot>` for a path in the agent's or admin's home, else `/var/lib/keelbox/restore/<snapshot>`.
- `snapshot` may be the first 8 or more characters of an ID.
- `--overwrite` keeps files the snapshot does not have. It cannot be combined with `--target`.

### forget-token

<!-- keelbox forget-token --help: begin -->
```text
keelbox forget-token [box]: remove the Hetzner token from the credential store.

Examples:
  keelbox forget-token       the token keelbox up stored
  keelbox forget-token acme  the one acme uses
```
<!-- keelbox forget-token --help: end -->

Without a box it removes the entry named `hetzner`, which `keelbox up` stores. A `token_file` is left alone. The token stays valid until you delete it in the Hetzner Console. Where keelbox looks for a token: [the Hetzner token](../guides/credentials.md#hetzner-an-api-token).

### completion

<!-- keelbox completion --help: begin -->
```text
keelbox completion <shell>: print shell completion for bash, zsh or fish.

Examples:
  keelbox completion zsh > ~/.zfunc/_keelbox  then fpath+=(~/.zfunc) before compinit
  keelbox completion bash > ~/.local/share/bash-completion/completions/keelbox
  keelbox completion fish > ~/.config/fish/completions/keelbox.fish
```
<!-- keelbox completion --help: end -->

It completes commands, flags and box names. The installer and Homebrew set it up for you.

### version

<!-- keelbox version --help: begin -->
```text
keelbox version: print versions (also: -v, --version).

Examples:
  keelbox version
  keelbox -v  the same
```
<!-- keelbox version --help: end -->

```text
keelbox v0.1.0 a305f511743f 2026-11-21 (release 2026.11.4, go1.27.3, linux/amd64)
```

The CLI's version, commit and build date, then the [release manifest](release.md#release-manifest) it carries.

### self-update

<!-- keelbox self-update --help: begin -->
```text
keelbox self-update: update keelbox to the current stable release.

Examples:
  keelbox self-update

Flags:
  --allow-downgrade  install an older keelbox
```
<!-- keelbox self-update --help: end -->

- It ends by listing the boxes this computer has not applied the new release to. `keelbox apply acme` moves a box; `keelbox update acme` does both steps.
- A development build refuses. So does a copy a package manager installed: Homebrew's names `brew upgrade keelbox`, and one in `/usr/bin`, such as an Arch package, names the package manager's update.
- Where the binary's directory is not writable, run it with `sudo`.

What is verified before the binary is replaced: [installing and updating](release.md#installing-and-updating-keelbox).

## Flags

Every flag except those only `up` takes, which are in [its help](#up).

| Flag | Commands | Effect |
|---|---|---|
| `--local` | all that take a config | Run on this machine, whatever `target.provider` says. |
| `--ssh-user NAME` | `up`, and over SSH `plan`, `apply`, `update`, `doctor`, `status`, `rollback`, `reboot`, `backup`, `restore`, `notify`, `add`, `reports`, `requests`, `approve`, `deny` | `up`: the account your key logs in as now. The others: connect as `NAME`, not `target.ssh.user`. Once a box is set up, root and the image's account no longer log in, so leave it out. |
| `--binary PATH` | remote commands | The Linux keelbox to upload. Default: this executable if it fits the box, `keelbox-linux-<arch>` beside it, the cache, then a verified download of this version ([releases](release.md#the-binary-uploaded-to-the-box)). |
| `--verbose` | `up`, `plan`, `apply`, `update`, `doctor`, `add`, `create`, `resize`, `park`, `resume`, `rollback` | One line for every step and every command run on the box. |
| `--allow-downgrade` | `apply`, `update`, `self-update` | Move the box, or this binary, to an older release. |
| `--adopt` | `apply`, `update`, `up`, `create` | Accept what `plan --impact` marks on a machine in use. It also lets keelbox remove `docker.io`, `docker-compose`, `docker-compose-v2`, `podman-docker`, `containerd` and `runc`, which stops their containers. |
| `--impact` | `plan` | What apply would change on a machine in use. |
| `--json` | `plan --impact`, `status`, `ls`, `sessions` | JSON output. |
| `--all` | `status`, `requests`, `approve` | `status`: every box. `requests`: also the requests decided. `approve`: every request waiting. |
| `--yes` | `up`, `resize`, `park`, `destroy`, `forget`, `reboot` | Go ahead without the question. On `destroy`, work that is only on the box still stops it. |
| `--discard-work` | `destroy` | Skip the check for work that is only on the box. |
| `--size SIZE` | `up`, `resize` | The server's size, as the cloud names it. |
| `--provider NAME` | `up`, `init` | `up`: where the box runs. `init`: the `target` block to write. |
| `--force` | `init`, `login` | `init`: overwrite the file. `login`: sign in again. |
| `--api` | `login`, `add` | With `claude-code` or `codex`: the sign-in for API billing. |
| `--session NAME` | `attach` | The tmux session. |
| `--admin` | `ssh` | Log in as the admin. |
| `--box NAME` | `init` | The box name to write. |
| `--name NAME` | `up`, `expose` | `up`: the box name. `expose`: the preview's name. |
| `--list` | `ssh-config`, `skills`, `expose`, `backup` | `ssh-config`: print the file. `skills`: list the skills on this computer. `expose`: list previews. `backup`: list snapshots. |
| `--skill NAME[,NAME]` | `skills` | The skills to copy, without asking. |
| `--remove NAME` | `expose` | Stop publishing a preview. |
| `--target PATH` | `restore` | The directory to restore into. |
| `--overwrite` | `skills`, `restore` | `skills`: replace a different skill of the same name. `restore`: restore over the original path. |
| `--channel NAME` | `notify test` | Test one channel. |
| `--detail TEXT` | `report` | The error and what was run, at most 2000 characters. |
| `--area AREA` | `report` | The part of keelbox the report is about. |
| `--show N` | `reports` | Print report `N`. |
| `--send N` | `reports` | Review report `N`, then open it as a GitHub issue. |
| `--delete N` | `reports` | Delete report `N` from the box. |
| `--why TEXT` | `install` | The reason, shown to the owner. At most 300 characters are kept. |
| `--status` | `install` | List the last requests. |

## Files on this computer

All are under `~/.config/keelbox/`. Two hold a secret: `credentials/`, and a `.cloud-config` with its single-use host key. `keelbox forget` removes a box's files except `<box>.run.json`.

| Path | What |
|---|---|
| `boxes/<box>.yaml` | The box's config, written by `up`. |
| `up-draft.json` | `up`'s answers, until it writes the config. |
| `boxes/<box>.up.json` | How far `up` got, and the sign-ins left for later. |
| `boxes/<box>.cloud-config` | The cloud-config `up` printed. Deleted once keelbox has replaced its single-use host key. |
| `boxes/<box>.run.json` | An apply this computer started on the box and has not seen end. |
| `boxes/<box>.json` | The tailnet address keelbox last reached the box at. |
| `boxes/<box>.parked.json` | A note that the box is parked: the date, its old address and the snapshot. The snapshot's labels in the cloud are the authority. |
| `boxes/<box>.destroyed.json` | A note that `destroy` deleted the server. Removed when `up` or `create` makes the server again. |
| `boxes/<box>.requests.json` | How many package requests waited when this computer last heard from the box. |
| `boxes/<box>.applied.json` | The keelbox this computer last applied to the box with, and when. |
| `last-box` | The box a command last used. |
| `ssh_config` | From `ssh-config`; `~/.ssh/config` includes it after a yes. |
| `known_hosts` | The host keys keelbox has pinned. |
| `credentials/` | Hetzner tokens, one mode 600 file each, used only where there is no Keychain or Secret Service. |

The [update check](../guides/upgrades.md#the-update-check) keeps its last result in `update-check.json` in the user cache directory: `~/.cache/keelbox/` on Linux, `~/Library/Caches/keelbox/` on macOS.

## Environment

| Variable | Effect |
|---|---|
| `ACCESSIBLE` | Any value: for screen readers. Nothing is redrawn in place, and questions are plain lines, one at a time. |
| `NO_COLOR` | Any value: no colour. Layout stays. |
| `TERM` | `dumb`: plain text, as when the output is not a terminal. |
| `COLORFGBG` | The terminal's background, light or dark, read before the terminal is asked. |
| `FORCE_HYPERLINK` | `1`: links are OSC 8 hyperlinks. `0`: never. |
| `HCLOUD_TOKEN` | The Hetzner API token, used before `target.hetzner.token_file` and the credential store. |
| `CLAUDECODE`, `KEELBOX_AGENT` | Any value: an agent runs keelbox, so a question without a terminal names the command for you to run, not `--yes`. |
| `KEELBOX_CREDENTIAL_STORE` | `file`: tokens go in `~/.config/keelbox/credentials/`, not the Keychain or Secret Service. |
| `KEELBOX_INDEX_URL` | Another channel index, for a mirror. HTTPS only, and the release host it names must be one of keelbox's. |
| `KEELBOX_NO_UPDATE_CHECK`, `DO_NOT_TRACK`, `CI` | Any value: no [update check](../guides/upgrades.md#the-update-check). |
| `KEELBOX_RELEASE_URL` | While it is set, `self-update` and the download of the box's binary refuse to start. |
| `SUDO_USER` | Under sudo, `~` in key file paths is this user's home, not root's. |
