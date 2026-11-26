# Attach, leave and come back

```sh
keelbox attach acme                     # open session main, creating it if missing
keelbox attach acme --session review    # open or create another session
keelbox sessions acme                   # list the sessions
keelbox ssh acme                        # a shell as the agent, outside tmux
```

Agents work in tmux sessions on the box, as the agent: the account without sudo that the agent CLIs run as. Type `claude` or `codex` in a session to start one.

## Leave

Press `Ctrl-b`, let go, then press `d`. The session keeps running on the box, as it does when you close the terminal or lose the connection. After a dropped connection, `keelbox attach` waits for the box and reopens the session.

`exit` or `Ctrl-d` ends the shell, and the session with it if that was its last window.

To come back, run `keelbox attach acme` from any computer that has the box's config: [from a second computer](#from-a-second-computer).

| Keys | What they do |
|---|---|
| `Ctrl-b c` | A new window |
| `Ctrl-b "` / `Ctrl-b %` | Split below / beside |
| `Ctrl-b w` | Every session and window, to pick one |
| `Ctrl-b r` | Reload `~/.tmux.conf` |

The mouse works for windows, pane borders and scrolling. The status line lists only this session's windows. Click the box name at its left, or press `Ctrl-b w`, to see every session and window and pick one with the mouse or the arrow keys and Enter.

## The welcome

A new session prints a few lines on how to start an agent, leave, come back, get a package and reach root. It does not print again for a session restored after a reboot. Turn it off with `sessions.welcome: false` and `keelbox apply acme`.

The welcome and status line come from files the agent can change, so they are not a check of the box. `keelbox doctor acme` is.

## From a phone

1. Put the phone on the box's tailnet, the private Tailscale network between your devices.
2. Generate an SSH key in the phone's SSH app, add the public key to the config and run `keelbox apply acme`:

   ```yaml
   users:
     agent:
       ssh_authorized_keys:
         - ssh-ed25519 AAAA... phone
   ```

3. Connect to `agent@acme` and run `tmux new -A -s main`.

To start Claude Code sessions without SSH, use [the Claude app](remote-control.md).

## After a reboot

keelbox saves each session's windows, panes and directories every 15 minutes and restores them at boot. Programs in them do not restart, except Claude Code and Codex after a reboot keelbox made ([auto_reboot](../reference/config.md#auto_reboot)). Otherwise run `claude --continue` or `codex resume`.

Every two minutes the box restarts the agent's tmux server if it has stopped. `keelbox status acme` shows the last restart. The [`sessions`](../reference/config.md#sessions) keys turn each part off.

## Your own tmux settings

Put your settings in `~/.tmux.conf` after its `source-file` line, which loads keelbox's. Add hooks with `set-hook -ga`: a plain `set-hook -g` on `session-created` or `client-attached` replaces keelbox's hook and the welcome with it.

## From a second computer

A second computer needs the box's config, its own SSH key on the box, and a way to reach the box.

1. On the first computer, list both computers' public keys, the line in each one's `~/.ssh/id_ed25519.pub`, under `ssh_authorized_keys` for both accounts in `~/.config/keelbox/boxes/acme.yaml`, and take out `ssh_authorized_key_files`: a file path is read on each computer, so it names another key there. Then run `keelbox apply acme`.
2. Copy that config to the same path on the second computer. If it sets `target.ssh.identity_file`, point that at the second computer's private key.
3. Join the second computer to your tailnet if public SSH has closed. The first connection shows the box's host key; compare it with `ssh-keygen -lf ~/.config/keelbox/known_hosts` on the first computer.

If an apply from either computer would take off a key the last apply put there, it stops and prints the lines that keep it: [apply would take SSH keys off](troubleshooting.md#apply-would-take-ssh-keys-off).
