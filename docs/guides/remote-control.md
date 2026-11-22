# The Claude app

Claude Code's Remote Control lets the Claude app, or claude.ai/code, start and continue Claude Code sessions on the box. keelbox runs it as a service of the agent account. It is off until you turn it on, and needs a claude.ai plan (not an API key) and `claude-code` in the config's `agents`.

Turning it on means:

- anyone signed in to your Claude account can run commands on the box as the agent;
- session content passes through Anthropic's servers;
- the box opens no port: Claude Code connects out over HTTPS.

## Turn it on

`keelbox up` offers it after the Claude Code sign-in. On a box that is already set up, add this to its config:

```yaml
sessions:
  remote_control:
    enabled: true
```

```sh
keelbox apply acme
keelbox login acme claude-code
```

Claude Code asks on your terminal whether to trust `~/projects` and whether to enable Remote Control. Answer `y` to both; keelbox answers neither for you. At `Connected`, keelbox hands over to a background service and gives your terminal back (press `Ctrl-C` if it does not).

If you decline, Remote Control stays off and `keelbox login` exits 1. Run the login again to retry.

Sessions start in `~/projects`; `directory` and `spawn` change that: [config](../reference/config.md#sessions).

## Use it

Open the Code tab in the Claude app, or claude.ai/code, and pick the box.

## Check it

`keelbox status acme` says whether the service runs. Claude Code's errors are in the agent's journal:

```sh
keelbox ssh acme
journalctl --user -u keelbox-claude-remote-control.service
```

systemd restarts the service up to six times an hour, and the box resets it up to four times a day after that. Then it stays stopped until the next `keelbox apply` or `keelbox login`.

## Turn it off

`enabled: false` only makes keelbox stop managing the service: it keeps running. Stop it on the box:

```sh
keelbox ssh acme
systemctl --user disable --now keelbox-claude-remote-control.service
```
