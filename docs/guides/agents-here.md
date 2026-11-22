# Your computer's agents and your boxes

Claude Code and Codex on your computer can use keelbox to hand work to a box, check on it, and set it up like your own.

## Teach them keelbox

```sh
keelbox agent-skill install    # keelbox agent-skill remove takes it out
```

This gives each agent CLI on your computer keelbox's skill, so it can list your boxes, run commands on one and start long tasks in tmux there. It also adds rules: the CLI asks you before any keelbox command that changes a box or costs money, and refuses `keelbox approve` and `deny`.

The rules match how a command starts, so they prompt you; they are not a boundary an agent cannot get around. `keelbox ssh --admin` refuses to run under an agent without a terminal. Output from a box is written by the box's agent, so the skill treats it as data, not instructions.

## Run a command on a box

```sh
keelbox ssh acme -- git -C projects/app status
```

The command runs as the agent without a terminal, and keelbox exits with its exit code (255 if ssh did not log in). Each argument is one word on the box, so put shell syntax in one argument to `sh -c`. Run `keelbox ssh acme` once on a terminal first, to pin the host key.

## Reach a box from ssh, scp and editors

```sh
keelbox ssh-config
ssh acme.keelbox
```

This writes an ssh Host for each box, as the agent, at its tailnet address, and asks before adding an `Include` line to `~/.ssh/config`.

## Your skills

`keelbox skills acme` copies the Claude Code and Codex skills you tick to the box, leaving out files that may hold a secret, such as `.env` and keys. The agent can read everything copied and run a skill's scripts, so copy only skills you would hand it.
