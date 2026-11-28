# Your first box

This page takes you from nothing to an AI coding agent at work on a new Hetzner cloud server. You install keelbox, answer the questions of `keelbox up`, sign in, and attach.

## Before you start

- A computer running macOS or Linux.
- A [Hetzner Cloud](https://console.hetzner.com) account. For AWS, DigitalOcean or Google Cloud, sign in to that cloud's CLI first: [cloud sign-ins](guides/credentials.md).
- A Claude account for Claude Code, a ChatGPT account for Codex, or an [API key](guides/signing-in.md#with-an-api-key) for either.
- A GitHub account, if the agent is to push code.
- Optionally, [Tailscale](https://tailscale.com) running on this computer.

The server is billed from the moment it exists until you delete it. To use a machine you already have instead, see [where a box can run](guides/where-to-run.md).

## Install

```sh
curl -fsSL https://get.keelbox.dev | sh
keelbox version
```

The installer checks the release signature and puts keelbox in `~/.local/bin`, without sudo. If that is not on your `PATH`, it offers to add it: say yes and open a new terminal. Other ways: [install keelbox](guides/installing.md).

## Run keelbox up

```sh
keelbox up
```

It asks its questions in seven steps; Enter takes the highlighted answer. Ctrl-C stops at any question, and `keelbox up` continues later with your answers. No server is created and nothing changes on the box before your yes at step 5.

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

Choose 3, A new cloud server, then Hetzner. On a Mac the menu has no This machine row, so the cloud server is 2. Name the box: Enter takes `devbox`; this page uses `acme`.

### Step 2: Hetzner token

Follow the three steps keelbox shows: create a project in the Hetzner Console, generate an API token with Read & Write, and paste it (it is not shown). The token reaches every server in its project, so use a project for boxes only. keelbox keeps it in your OS credential store; `keelbox forget-token` removes it.

### Step 3: Server

Press Enter for the preselected location, `fsn1`, and size: the cheapest with 2 vCPU and 4 GB, enough for one or two agents.

If `~/.ssh` has no SSH key, keelbox offers to create one, and `ssh-keygen` asks for a passphrase.

### Step 4: Agents and access

| Question | Preselected |
|---|---|
| Which agents? | Claude Code and Codex |
| Install the GitHub CLI? | Yes |
| Commit as your name and email? | Yes, from git on this computer |
| How much may the agent change this box? | Auto: it installs Ubuntu packages, with no sudo. Full gives it sudo, and keelbox's limits on it no longer hold. |
| Use Tailscale to reach the box? | Yes |
| When should the box restart for an update? | Nightly, once idle |

Tailscale is a private network between your devices, your tailnet. With it, SSH from the internet closes at the end of setup, provided this computer is on the same tailnet. Otherwise SSH stays open, with keys only.

The box's Tailscale key expires after 180 days unless you turn key expiry off for it in the Tailscale admin console. If it expires, you [get back in](guides/public-ssh.md#getting-back-in) through Hetzner.

### Step 5: Review

keelbox lists the server, its price with VAT, the agents and the access, then asks `Create the server (about EUR ... a month until you destroy it)?`. Enter says yes, and Hetzner bills by the hour from then. `n` stops with nothing created.

### Step 6: Set up

keelbox creates the server with a cloud firewall in front of it and sets the box up. Setup runs on the server, so it carries on if this computer sleeps.

### Step 7: Sign in

keelbox runs each vendor's own sign-in on your terminal and never sees the credentials. For each, choose "Sign in now" or "Skip for now".

| Sign-in | What you do |
|---|---|
| Tailscale | Open the link and approve the machine. |
| Claude Code | Open the link, sign in, and paste the code it gives you into the terminal. |
| Codex | Open the link and type the code Codex shows into the browser. |
| GitHub | Paste a fine-grained token for the repositories you choose (keelbox says how to make one), or sign in in the browser. |

A skipped sign-in is listed at the end with its command, such as `keelbox login acme codex`.

After Claude Code, keelbox asks whether to turn on Remote Control, with No preselected. Yes lets anyone signed in to your Claude account start sessions on the box from [the Claude app](guides/remote-control.md).

keelbox then applies once more, over the tailnet, and closes SSH from the internet.

## Start working

The last screen says what is installed, what is left to do, and how to reach the box. Open the agent's terminal:

```sh
keelbox attach acme
```

Type `claude` or `codex` to start an agent. To leave, press `Ctrl-b`, let go, then press `d`: the agent keeps working on the box. `keelbox attach acme` takes you back.

## If it stops

`keelbox up acme` continues where it stopped. `keelbox doctor acme` checks the box and says what to fix: [troubleshooting](guides/troubleshooting.md).

## Stop paying for it

A powered-off server is still billed. A parked box's snapshot is billed per GB stored.

```sh
keelbox park acme       # delete the server, keep its disk as a snapshot
keelbox resume acme     # bring a parked box back on a new server
keelbox destroy acme    # delete the server and its disk
```

More: [resize, park and resume](guides/resize-park-resume.md), [remove a box](guides/removing-a-box.md).

## Next

- [Attach, leave and come back](guides/sessions.md), also from a phone.
- [Work on the box](guides/work-on-the-box.md): git, packages and what the agent may change.
- [Security model](security-model.md): read it before you give the agent credentials.
