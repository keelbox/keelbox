# The Claude app

```sh
keelbox apply acme                # after turning it on in the config
keelbox login acme claude-code    # the first run: two questions, once
```

Claude Code's Remote Control lets the Claude app, or claude.ai/code in a browser, start and continue Claude Code sessions on the box. keelbox runs it as a service of the agent, the account the agent CLIs run as. It is off until you turn it on.

## What turning it on allows

- Anyone signed in to your Claude account can run commands on the box as the agent.
- Session content passes through Anthropic's servers.
- The box opens no port for it: Claude Code connects out over HTTPS.

It needs a claude.ai plan, not an API key, and `claude-code` in the config's `agents`.

## Turn it on

`keelbox up` asks after the Claude Code sign-in, with No preselected. A yes does the rest of this section for you.

On a box that is already set up, add this to its config and run the two commands at the top of the page.

```yaml
sessions:
  remote_control:
    enabled: true
```

Sessions start in `~/projects`, as the agent. `directory` and `spawn` under the same key change where and how: [config](../reference/config.md#sessions).

Until Claude Code is signed in, the service waits, and `keelbox doctor acme` warns that it is waiting.

## The first run

The first time, Claude Code asks two questions on your terminal: whether to trust `~/projects`, and whether to enable Remote Control. Answer `y` to both.

Claude Code's screen then goes on to `Connected` and `Press Ctrl+C to stop`. A few seconds after your second answer keelbox ends that copy, and your terminal comes back. keelbox then starts the service, which runs without a terminal.

If the terminal does not come back, press Ctrl-C when the screen says `Connected`.

keelbox answers neither question. While Claude Code has the terminal, a check on the box tells keelbox, by its exit code, whether both answers are recorded.

After a no, or when the service does not stay up, keelbox says so and asks `Try again now?`. Declining leaves Remote Control not running: `keelbox login` then exits 1, and nothing else fails. `keelbox login acme claude-code` runs it again.

## Use it

Open the Code tab in the Claude app, or claude.ai/code, and pick the box. Sessions are named `acme-` and a few words until you rename them.

## Check it

`keelbox sessions acme` and `keelbox status acme` say whether the service runs. Claude Code's own errors are in the agent's journal:

```sh
keelbox ssh acme
journalctl --user -u keelbox-claude-remote-control.service
```

systemd starts the service again when it exits, up to six times an hour. After that the box resets it 15 minutes later, at most four times a day.

## Turn it off

Set `enabled: false` and run `keelbox apply acme`: keelbox then leaves the service alone, still installed and running. Stop it on the box, as the agent:

```sh
keelbox ssh acme
systemctl --user disable --now keelbox-claude-remote-control.service
```
