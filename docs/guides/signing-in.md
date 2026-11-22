# Sign in

```sh
keelbox login acme          # every sign-in not done yet
keelbox login acme codex    # one of tailscale, claude-code, codex, gh
```

keelbox runs each vendor's sign-in on your terminal and uses only its exit code. `keelbox up` runs the same sign-ins as its last step.

For each sign-in that is not done, keelbox says what the account is for. It offers "Sign in now", "Skip for now" and "I have signed in"; the last checks again. A sign-in named on the command line starts without the question.

## What each sign-in asks

| Sign-in | Account | What you do |
|---|---|---|
| Tailscale | your tailnet, the private network between your devices | Open the link and approve the machine in the browser. |
| Claude Code | your Claude account | Open the link, sign in, and paste the code it gives you into the terminal. |
| Codex | a ChatGPT account | Open the link and type the code Codex shows into the browser. A code from an earlier attempt fails. |
| GitHub | a GitHub account for the agent | Open the link gh shows and type its code in the browser. gh finishes once you approve. |

Tailscale signs in as the admin, the box's account with sudo. The other three sign in as the agent, the account the agent CLIs run as.

With `access.tailscale.auth_key_file` in the config, apply joins the tailnet and there is no Tailscale sign-in: [a Tailscale auth key file](automation.md#a-tailscale-auth-key-file).

After the Tailscale sign-in, `keelbox apply acme` from a computer on the same tailnet [closes public SSH](public-ssh.md).

With Remote Control turned on, the Claude Code sign-in is followed by its first run: [the Claude app](remote-control.md).

## With an API key

```sh
keelbox login acme codex --api         # an OpenAI API key
keelbox login acme claude-code --api   # an Anthropic Console account
```

Both bill by use, with no plan. Each is also the fourth choice when keelbox asks about that sign-in.

For Codex, paste the key and press Enter. It is not shown. Codex keeps it in `~/.codex/auth.json`, which the agent can read. For Claude Code, sign in to the Console in the browser.

`--api` also runs where already signed in. [The Claude app](remote-control.md) needs a Claude plan.

## Skipping and coming back

```text
╭─ Sign-ins on devbox ────────────────────────────────────────────────────────╮
│ ✓  Tailscale       signed in                                                │
│ ✓  Claude Code     signed in                                                │
│ ✓  Codex           signed in                                                │
│ ○  GitHub          skipped; later: keelbox login devbox gh                  │
│ !  Remote Control  not running yet; later: keelbox login devbox claude-code │
╰─────────────────────────────────────────────────────────────────────────────╯
```

A skipped sign-in is not a failure: `keelbox up` still finishes and lists it under "Left to do".

A sign-in that ends without signing in is reported, the rest go on, and keelbox exits 1. Ctrl-C stops signing in and prints the command that continues. `--force` runs every sign-in again, also where already signed in.

`keelbox login` reports signed in or not, never which account.

## What to give the agent

The agent can read every token it is signed in with, and so can any code it runs. keelbox does not limit the agent's connections to the internet, so what it can read can leave the box: [security model](../security-model.md).

For GitHub, `keelbox login` offers a fine-grained token first: only the repositories you choose, typed on the box, unseen by keelbox. GitHub's browser sign-in reaches every repository your account can, unless the agent has a GitHub account of its own.

sshd on the box refuses agent forwarding for the agent account. The agent pushes over HTTPS through gh.

## Signing out

Each CLI has its own command. Run `keelbox ssh acme`, then `claude auth logout`, `codex logout` or `gh auth logout`.

For Tailscale, run `keelbox ssh acme --admin`, then `sudo tailscale logout`, and remove the machine in the Tailscale admin console.
