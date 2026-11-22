# Your first box

```sh
curl -fsSL https://get.keelbox.dev | sh
keelbox up
```

The first line installs keelbox. The second asks its questions in seven steps, creates a server and sets it up as a box: an Ubuntu machine for AI coding agents. This page follows those steps for a new Hetzner server.

## Before you start

- A computer running macOS or Linux.
- A Hetzner Cloud account. For AWS, DigitalOcean or Google Cloud, install that cloud's CLI and sign in first: [cloud sign-ins](guides/credentials.md).
- A Claude account for Claude Code and a ChatGPT account for Codex, or [API billing](guides/signing-in.md#with-an-api-key) for either. Either agent can be left out.
- A GitHub account, if the agent is to push.
- A Tailscale account, with Tailscale running on this computer. Optional; the wizard preselects it.

A cloud server is billed from the moment it exists. [Where a box can run](guides/where-to-run.md) sets the four clouds beside a server or a machine you already have.

## Install

The installer puts one binary in `~/.local/bin`, without sudo. If that directory is not on your `PATH`, it offers to add it; open a new terminal afterwards. Other ways: [install keelbox](guides/installing.md).

## Run keelbox up

```sh
keelbox up
```

Ctrl-C stops at any question. No server is created before the yes at step 5.

### Step 1: Where the box runs

```text
? Where should the box run?
  › 1  An existing server (SSH)
       a home server, mini PC, VM or VPS you reach over SSH
    2  This machine
       set up the Ubuntu machine you are on, with sudo
    3  A new cloud server
       keelbox creates it at Hetzner, AWS (Lightsail), DigitalOcean or Google Cloud
    4  A server I create myself (cloud-config)
       any provider: paste a snippet keelbox prints, no token
  ↑↓ move · 1-4 pick · Enter choose
```

The highlight starts on option 1, or 2 on a headless Ubuntu. Choose 3, then Hetzner, then name the box; this page uses `acme`. The name is also the server's hostname.

- Options 1 and 4: [a server you already have](guides/adopting-a-box.md).
- Option 2: [a machine already in use](guides/machine-in-use.md).

### Step 2: Hetzner token

A card gives three steps: create a project in the Hetzner Console, generate an API token with Read & Write, and paste it. It is not shown as you paste.

The token reaches every server in its project and nothing outside it. keelbox keeps it in your OS credential store, or a mode 600 file where there is none; `keelbox forget-token` removes it.

### Step 3: Server

Choose a location, then a size. The wizard preselects `fsn1` and the cheapest size with 2 vCPU and 4 GB, enough for one or two agents. Each size shows Hetzner's price, with VAT.

keelbox logs in to the box with an SSH key. If `~/.ssh` has none, it offers to make one with `ssh-keygen`, which asks for a passphrase.

### Step 4: Agents and access

| Question | Preselected |
|---|---|
| Which agents? | Claude Code and Codex |
| Install the GitHub CLI? | Yes |
| Commit as your name and email? | Yes, when git on this computer has them. Otherwise type them, or press Enter to skip. |
| How much may the agent change this box? | Auto: the agent installs Ubuntu packages and has no sudo. |
| Use Tailscale to reach the box? | Yes |
| Restart for updates? | Nightly, once idle |

Tailscale is a private network between your devices, called your tailnet. With it, SSH from the internet closes at the end of setup, once keelbox has reached the box over the tailnet. For that, this computer must be on the same tailnet; if it is not, SSH stays open.

The box's Tailscale key expires after 180 days, unless you turn key expiry off for it in the Tailscale admin console. A box with an expired key and SSH closed is recovered from the cloud account: [getting back in](guides/public-ssh.md#getting-back-in).

Without Tailscale the box is reached over public SSH, with keys only.

### Step 5: Review

A card lists the server, an estimate of its price, the agents and the access. Under it: `Create the server (about EUR ... a month until you destroy it)?`

Yes is preselected, so Enter creates the server and Hetzner bills it by the hour from then. `n` stops with no server created.

### Step 6: Set up

keelbox uploads your SSH public key to the Hetzner project, creates the server and a firewall in front of it, and sets the box up.

### Step 7: Sign in

For each sign-in the wizard offers "Sign in now", "Skip for now" and "I have signed in".

| Sign-in | What you do |
|---|---|
| Tailscale | Open the link and approve the machine in the browser. |
| Claude Code | Open the link, sign in, and paste the code it gives you into the terminal. |
| Codex | Open the link and type the code Codex shows into the browser. |
| GitHub | Open the link gh shows and type its code in the browser. gh finishes once you approve. |

A skipped sign-in is listed at the end with its command, such as `keelbox login acme codex`.

After the Claude Code sign-in the wizard asks whether to turn on Remote Control, with No preselected. A yes lets anyone signed in to your Claude account start sessions on the box from [the Claude app](guides/remote-control.md).

keelbox then applies once more, over the tailnet, and closes SSH from the internet.

## The box is set up

The last screen says what is installed, whether SSH from the internet is closed, and what is left.

```text
╭─ Start working ─────────────────────────────────────────────────────────╮
│ This computer   keelbox attach acme                                     │
│                 opens the agent's terminal on the box. Close it and the │
│                 work carries on; run it again to come back.             │
│                                                                         │
│ Claude app      Remote Control: the Code tab in the Claude app, or      │
│                 claude.ai/code. Sessions run on acme.                   │
│                                                                         │
│ Phone over SSH  add your phone's key to the box first:                  │
│                 https://keelbox.dev/docs/guides/sessions                │
╰─────────────────────────────────────────────────────────────────────────╯
```

```sh
keelbox attach acme
```

Type `claude` or `codex` to start an agent. `Ctrl-b` then `d` leaves: the session and everything in it keeps running on the box, and `keelbox attach acme` returns to it.

## If it stops

Run `keelbox up acme`. It continues where it stopped, with your answers. Setup on a server carries on while this computer is closed.

## Next

- Work [from a phone](guides/sessions.md#from-a-phone).
- `keelbox doctor acme` [checks the box](guides/troubleshooting.md).
- `keelbox park acme` deletes the server and [keeps its disk](guides/resize-park-resume.md).
- `keelbox destroy acme` [deletes the server and its disk](guides/removing-a-box.md).
