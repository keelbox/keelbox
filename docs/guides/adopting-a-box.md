# A server you already have

```sh
keelbox up
```

Two answers to the first question set up a server that keelbox does not create:

- **1, An existing server (SSH)**: a server you already log in to.
- **4, A server I create myself (cloud-config)**: a new server at any provider, started from a snippet keelbox prints.

The server must run Ubuntu 24.04 or 26.04 on amd64 or arm64, with internet access. Agents and access, and Sign in, work as for [your first box](../getting-started.md).

## A server you reach over SSH

At step 2, give the address you `ssh` to now and the account you log in as: `root`, or one with sudo such as `ubuntu`. The host key fingerprint is optional (`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the server shows it). Without it, keelbox shows the keys at the first connection and asks `Trust and pin these host keys?`, with No preselected. It refuses the server later if the key changes.

keelbox then connects once, changes nothing, and checks the Ubuntu release and sudo. If the server does not take your SSH key, it offers `ssh-copy-id` or a line to paste in the provider's console. If sudo needs a password, sudo asks on your terminal at step 4 and again at Set up; keelbox never reads it.

The box name defaults to the hostname. A different name asks whether to rename the machine too, with No preselected.

At step 4 keelbox reads the server as root, still changing nothing. If it already runs other software, you get more questions: see [a machine already in use](machine-in-use.md).

The Review lists what changes on the server:

- SSH takes keys only, so password and root logins stop.
- The UFW firewall turns on and admits SSH and Tailscale only.
- The accounts `admin` and `agent` are created, and Docker, Node, Tailscale and the agent CLIs are installed.
- The image's default account is locked once `admin` logs in with your key.

`n` at `Go ahead?` stops before any change, and `keelbox up` continues later. Set up applies as the account you named, then again as `admin`, to confirm `admin` logs in with your key and has sudo. From then on keelbox connects as `admin`.

## A server you create yourself

Step 2 asks `Include the host key?` (Yes preselected), so keelbox recognises the server at the first connection. Without it, you compare fingerprints then.

A yes at Review creates nothing. keelbox prints a cloud-config, saves it in `~/.config/keelbox/boxes/acme.cloud-config`, and tells you to create an Ubuntu 24.04 or newer server with it in the provider's "Cloud config" or "User data" field, and no SSH key. The cloud-config creates `admin` with your key and turns off SSH passwords and root login.

Then type the address at `The server's IP address, once it is running`. Ctrl-C there keeps the box as `waiting for the server`; `keelbox up acme` asks again. At the first connection keelbox replaces the snippet's host key with keys made on the server and deletes the saved file.

## The image's default account

Most cloud images have a default account, usually `ubuntu`, with your SSH key and sudo without a password. keelbox locks it once `admin` has logged in with your key: SSH and sudo refuse it, and its files stay. Only accounts cloud-init gave passwordless sudo are locked, never `admin`, `agent`, `root` or one you made. The [security model](../security-model.md) says why.

To keep the account, set this in `~/.config/keelbox/boxes/acme.yaml` and run `keelbox apply acme`:

```yaml
users:
  lock_cloud_user: false
```

To undo a lock, set the key above, then:

```sh
keelbox ssh acme --admin
sudo usermod --unlock ubuntu && sudo chage -E -1 ubuntu
```

Without the key, the next apply locks it again. `usermod` warns that the account has no password; that is expected, as it logs in with keys.
