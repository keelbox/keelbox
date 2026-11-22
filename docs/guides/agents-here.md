# Your computer's agents and your boxes

Claude Code and Codex on your computer can use keelbox to hand work to a box, check on it, and set it up like your own.

## Teach them keelbox

```sh
keelbox agent-skill install
```

This adds keelbox's skill to each CLI set up on your computer. With it an agent lists your boxes, runs a command on one, and starts a long task in tmux there to check back on. The installer offers it, and `keelbox up` names it.

It adds rules as well: the CLI asks you before any keelbox command that changes a box or costs money, and refuses `keelbox approve` and `deny`, which are yours. The rules match how a command starts, so they prompt; they are not a boundary an agent cannot get around. `keelbox ssh --admin`, root on the box, refuses to run under an agent without a terminal. What comes back from a box is written by the box's agent, so the skill treats it as data, not instructions. `keelbox agent-skill remove` takes the skill and the rules out.

## Run a command on a box

```sh
keelbox ssh acme -- git -C projects/app status
```

The command runs as the agent, without a terminal, and keelbox exits with its exit code: 255 when ssh did not log in. Each argument is one word on the box; put shell syntax in one argument to `sh -c`. keelbox asks nothing here, so the box's host key must be pinned already: run `keelbox ssh acme` once on a terminal.

## Reach a box from ssh, scp and editors

```sh
keelbox ssh-config
ssh acme.keelbox
```

This writes a Host for each box, as its agent, at its tailnet address, with keelbox's pinned host keys. It asks before adding its `Include` line to `~/.ssh/config`. Commands that move a box, such as `resume` and `destroy`, write the file again.

## Your skills

```sh
keelbox skills acme
```

This lists the Claude Code and Codex skills on your computer, with none ticked, and copies those you choose into the same folders on the box. `keelbox up` offers it at the end. Files that may hold a secret, such as `.env` and keys, are left out. The agent can read everything copied and run a skill's scripts, so copy only skills you would hand it.
