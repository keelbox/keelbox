# Sign in

```sh
keelbox login acme          # every sign-in not done yet
keelbox login acme codex    # one of tailscale, claude-code, codex, gh
```

`keelbox up` runs these sign-ins as its last step; `keelbox login` does any you skipped. Each is the vendor's own sign-in on your terminal, and keelbox never sees the credentials. For each, choose "Sign in now", "Skip for now" or "I have signed in", which checks again.

## What each sign-in asks

| Sign-in | What you do |
|---|---|
| Tailscale | Open the link and approve the machine. |
| Claude Code | Open the link, sign in, and paste the code it gives you into the terminal. |
| Codex | Open the link and type the code Codex shows into the browser. A code from an earlier attempt fails. |
| GitHub | Paste a fine-grained token (preselected), or sign in in the browser with the code gh shows. |

Tailscale signs in as the admin, your account with sudo; the others sign in as the agent.

With `access.tailscale.auth_key_file` in the config there is no Tailscale sign-in: [a Tailscale auth key file](automation.md#a-tailscale-auth-key-file). Once the box is on the tailnet, `keelbox apply acme` from a computer on the same tailnet [closes public SSH](public-ssh.md).

## With an API key

```sh
keelbox login acme codex --api         # an OpenAI API key
keelbox login acme claude-code --api   # an Anthropic Console account
```

Both bill by use, with no plan. For Codex, paste the key; Codex keeps it in `~/.codex/auth.json`, which the agent can read. For Claude Code, sign in to the Console in the browser. [The Claude app](remote-control.md) needs a Claude plan.

## Skipping and coming back

A skipped sign-in does not stop `keelbox up`: it is listed under "Left to do" with its command, such as `keelbox login acme gh`.

A sign-in that fails is reported, the rest go on, and keelbox exits 1. `--force` runs every sign-in again. `keelbox login` reports whether each CLI is signed in, never which account.

## What to give the agent

The agent can read every token it is signed in with, and so can any code it runs. keelbox does not limit the agent's connections to the internet, so anything it can read can leave the box: [security model](../security-model.md).

For GitHub, use a fine-grained token, made at `https://github.com/settings/personal-access-tokens/new` for only the repositories the agent needs, with Contents, Pull requests and Workflows set to read and write. The browser sign-in reaches every repository your account can.

The agent pushes over HTTPS through gh. sshd refuses SSH agent forwarding for the agent account.

## Signing out

For an agent CLI or GitHub, run `keelbox ssh acme`, then `claude auth logout`, `codex logout` or `gh auth logout`.

For Tailscale, run `keelbox ssh acme --admin`, then `sudo tailscale logout`, and remove the machine in the Tailscale admin console.
