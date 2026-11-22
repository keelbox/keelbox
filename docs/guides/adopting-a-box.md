# A server you already have

```sh
keelbox up
```

Two answers to its first question set up a server that keelbox does not create. Option 1, "An existing server (SSH)", is a server you already log in to. Option 4, "A server I create myself (cloud-config)", is a new server at any provider, started from a snippet keelbox prints.

Either becomes a box: an Ubuntu machine for AI coding agents, with an `admin` account for you and an `agent` account for the agents.

## Before you start

- The server runs Ubuntu 24.04 or 26.04, on amd64 or arm64, and has internet access.
- For option 1, you log in to it over SSH as `root`, or as an account with sudo.

## A server you reach over SSH

The steps are: Where the box runs, Server, Agents and access, What runs there, Review, Set up, Sign in. Agents and access and Sign in are the same as for [your first box](../getting-started.md).

### Step 2: Server

| Question | Answer |
|---|---|
| Server's IP address or DNS name | The address you `ssh` to now. |
| Which account do you log in to the server as now? | `root`, `ubuntu`, or another account with sudo. |
| Host key fingerprint | Optional. The provider's console shows it, and so does `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the server. |

Without a fingerprint, keelbox shows the server's host keys at the first connection and asks `Trust and pin these host keys?`, with No preselected. It keeps the keys in `~/.config/keelbox/known_hosts` and refuses a server whose key differs later.

keelbox then connects once and changes nothing. It checks the Ubuntu release and that the account can use sudo.

| The check finds | What happens |
|---|---|
| The server does not take your SSH key | A menu offers to copy the key with `ssh-copy-id`, or prints one line to paste in the provider's console. |
| The server refuses `root` and names the image's account | keelbox checks again as that account. |
| sudo asks for a password | sudo asks on your terminal twice: at step 4 and at the start of Set up. keelbox does not read the password. |

The last question is the box name. Enter keeps the server's hostname. With another name, keelbox asks whether to rename the machine as well, with No preselected. The server keeps its timezone and locale.

### Step 4: What runs there

keelbox looks at the server again, as root, and changes nothing.

With Tailscale, a server at a private address gets the question `Keep SSH open to your LAN as well?`, with Yes preselected. [Closing public SSH](public-ssh.md) says what each answer leaves open.

A server that runs other software gets more questions here, and a longer Review. It says that accounts other than root lose link-local addresses and the server's own SSH port: [a machine already in use](machine-in-use.md).

### Step 5: Review

The first card lists your answers. The second, "On ADDRESS, keelbox changes", lists what happens to the server:

- SSH takes keys only: password and root logins stop.
- The UFW firewall turns on, and lets in SSH and Tailscale only.
- The accounts `admin` and `agent` are created. Docker, Node, Tailscale and the agent CLIs are installed.
- The image's default account is locked once `admin` logs in with your key.

Under them: `Go ahead?`, with Yes preselected. `n` stops before keelbox changes the server, and `keelbox up` continues later with your answers.

### Step 6: Set up

keelbox applies the config: it changes the server until the two match. It applies as the account you named, then again as `admin`. The second run shows that `admin` logs in with your key and can use sudo.

keelbox asks nothing in either run. From then on it connects as `admin`.

## A server you create yourself

The steps are: Where the box runs, Cloud-config, Agents and access, Review, Create the server, Set up, Sign in.

Step 2 asks one question, `Include the host key?`, with Yes preselected. With it, keelbox recognises the server at the first connection by a host key in the snippet. Without it, you compare fingerprints at that connection.

At Review, a yes to `Go ahead?` creates nothing. It prints a card and, under it, the cloud-config:

```text
╭─ Create the server for acme ────────────────────────────────────────────────────────╮
│ 1.        Create an Ubuntu 24.04 (or newer) server with your provider.              │
│ 2.        Paste the cloud-config below into its "Cloud config" or "User data" field │
│           (Hetzner: Cloud config; DigitalOcean, Vultr, AWS: User data).             │
│ 3.        Add no SSH key there: the cloud-config installs yours.                    │
│                                                                                     │
│ saved in  ~/.config/keelbox/boxes/acme.cloud-config, until keelbox has replaced its │
│           host key                                                                  │
│ copy it   cat ~/.config/keelbox/boxes/acme.cloud-config                             │
╰─────────────────────────────────────────────────────────────────────────────────────╯
```

Create the server, then type its address at `The server's IP address, once it is running`. Ctrl-C at that question keeps the box. `keelbox ls` then shows `waiting for the server`, and `keelbox up acme` asks for the address again.

The cloud-config creates the `admin` account with your SSH public key, and turns off SSH passwords and root login. Its host key is used once. At the first connection keelbox replaces it with keys made on the server, then deletes the saved file.

Set up and Sign in then run as for any other box.

## The image's default account

Most cloud images come with a default account, usually `ubuntu`, that has your SSH key and sudo without a password. keelbox locks that account once `admin` has logged in with your key. SSH and sudo then refuse it; its home directory and files stay. The reason is in the [security model](../security-model.md).

The accounts locked are the ones cloud-init gave sudo without a password, other than `admin`, `agent` and `root`. An account you made yourself is not locked.

To keep the account, set this key in `~/.config/keelbox/boxes/acme.yaml` and run `keelbox apply acme`:

```yaml
users:
  lock_cloud_user: false
```

The key stops the next lock. To undo a lock that has happened:

```sh
keelbox ssh acme --admin
sudo usermod --unlock ubuntu && sudo chage -E -1 ubuntu
```

Without the key in the config, the next apply locks the account again.
