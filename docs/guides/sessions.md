# Attach, leave and come back

```sh
keelbox attach acme                     # session main, created if missing
keelbox attach acme --session review    # another session
keelbox sessions acme                   # list them
keelbox ssh acme                        # a shell as the agent, outside tmux
```

Agents work in tmux sessions on the box, as the agent: the account without sudo that the agent CLIs run as. In a session, type `claude` or `codex` to start an agent.

A session name is letters, digits, `.`, `_` and `-`.

## Leave

A session's status line has the box's name on the left and the keys that leave on the right: `Ctrl-b d`. Hold `Ctrl` and press `b`, let go, then press `d`.

The session, and every program in it, keeps running on the box. Closing the terminal window or losing the connection leaves it running in the same way.

When the connection is lost, as when a laptop sleeps or changes network, `keelbox attach` waits for the box to answer and opens the session again. `Ctrl-c` stops the wait.

`exit` and `Ctrl-d` end the shell you are in, and the session with it when that was its last window.

`keelbox attach acme` comes back, from any computer that has the box's config.

| Keys | What they do |
|---|---|
| `Ctrl-b c` | A new window, in the current directory |
| `Ctrl-b "` | Split below |
| `Ctrl-b %` | Split beside |
| `Ctrl-b r` | Read `~/.tmux.conf` again |

The mouse is on: click a window's name, drag a pane border, scroll.

## The welcome

A new session starts with a few lines above its first prompt.

```text
  devbox-2  ·  you are the agent, an account without sudo  ·  Ubuntu 24.04
  Start an agent   claude    codex
  Your work        ~/projects
  Leave            Ctrl-b then d: everything here keeps running
  Come back        keelbox attach devbox-2   (from your computer)
  Packages         keelbox install NAME: an Ubuntu package, without sudo
  Need root?       from your computer: keelbox ssh devbox-2 --admin
```

The Packages line follows `users.agent.permissions`. With `manual` it names `keelbox requests`, and with `full` both lines are left out.

It prints once for each new session, in its first window. A session restored after a reboot does not print it again, and a terminal under 36 columns gets none.

`sessions.welcome: false` in the config, then `keelbox apply acme`, turns it off. The status line stays.

The agent's own tmux and shell draw the status line and the welcome, from files the agent can change. Neither is a check of the box; `keelbox doctor acme` reads the box as root.

## From a phone

1. Put the phone on the same tailnet as the box. A tailnet is the private Tailscale network between your devices.
2. Generate an SSH key in the phone's SSH app, add its public key to the box's config, and run `keelbox apply acme`.

   ```yaml
   users:
     agent:
       ssh_authorized_keys:
         - ssh-ed25519 AAAA... phone
   ```

3. Connect to `agent@acme`. The login lists the running sessions.
4. `tmux new -A -s main` attaches to `main`.

Until [public SSH closes](public-ssh.md), the phone also reaches the box at its public address.

[The Claude app](remote-control.md) starts Claude Code sessions on the box without SSH.

## After a reboot

keelbox saves each session's windows, panes and working directories every 15 minutes. At boot the agent's tmux server starts and restores the last save, and a session `main` always exists.

Programs that were running are not started again, except Claude Code and Codex after a reboot by keelbox: [auto_reboot](../reference/config.md#auto_reboot). Otherwise `claude --continue` and `codex resume` are the CLIs' own commands that pick a conversation up.

Every two minutes the box checks that the agent's tmux server runs. If it is gone, the box starts it and restores the last save. `keelbox status acme` shows the last such restart.

The keys are `sessions.recovery`, `sessions.start_on_boot` and `sessions.watchdog` in the [config](../reference/config.md#sessions), each on unless set to `false`.

## Your own tmux settings

`~/.tmux.conf` is yours. Its `source-file` line loads keelbox's settings from `~/.config/tmux/keelbox.conf`, which apply keeps as keelbox wrote it. Settings after that line replace keelbox's, and `Ctrl-b r` loads them.

Add hooks with `set-hook -ga`. `set-hook -g session-created` or `set-hook -g client-attached` replaces keelbox's hook, and the welcome goes with it.
