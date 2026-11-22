# Without the wizard: flags, CI, config by hand

```sh
keelbox up acme --provider ssh --host 203.0.113.10 --ssh-user root --host-key SHA256:... \
  --git-name "Ada Lovelace" --git-email ada@example.com --yes
```

Without a terminal, `keelbox up` sets up a box, an Ubuntu machine for AI coding agents, and asks nothing. Each answer is a flag or its default. `--provider`, the box name and `--yes` are always needed. The [command reference](../reference/cli.md#up) lists every flag.

| Flag | Needed |
|---|---|
| `--host` | With `--provider ssh`. |
| `--host-key` | With `--provider ssh`, unless keelbox has connected to the server before. |
| `--token-file` | With `--provider hetzner`, unless a token is stored or `HCLOUD_TOKEN` is set. |
| `--location` | With `--provider digitalocean`. For `aws` and `gcp`, when the CLI has no region or zone of its own. |
| `--lan-ports` | With `ssh` or `local`, when something listens that the firewall would block. |
| `--adopt` | With `ssh` or `local`, when the first apply would cut off or replace something. |

For a missing `--host-key` the message lists the fingerprints the server offers, to compare with the provider's console.

Without `--git-name` and `--git-email` no name and email are set for the agent's commits, and `git commit` on the box fails.

A server at a private address keeps SSH open to the LAN; `--public-ssh auto` closes it there too.

## Exit codes

| Code | When |
|---|---|
| 2 | An answer is missing. Nothing has changed, and the message ends with an example command. |
| 3 | A step only you can do. The message names the command that continues. |

The steps that end with 3:

- The server does not take your SSH key, or its sudo asks for a password.
- The cloud's CLI is not installed or not signed in.
- The cloud-config is printed. Create the server, then run `keelbox up acme --host ADDRESS`.
- The sign-ins are left. Run `keelbox up acme` in a terminal, or add `--skip-sign-in` to finish without them.

The other codes are in the [command reference](../reference/cli.md#exit-codes).

## A command per cloud

| Cloud | Command | The job needs |
|---|---|---|
| Hetzner | `keelbox up acme --provider hetzner --token-file /run/secrets/hetzner --yes` | the API token in a mode 600 file |
| AWS | `keelbox up acme --provider aws --location eu-central-1 --yes` | the AWS CLI and its credentials |
| DigitalOcean | `keelbox up acme --provider digitalocean --location fra1 --yes` | doctl, and its token in `DIGITALOCEAN_ACCESS_TOKEN` |
| Google Cloud | `keelbox up acme --provider gcp --gcp-project my-project --location europe-west3-a --yes` | gcloud, signed in |

The permissions each sign-in needs are on [cloud sign-ins and permissions](credentials.md).

## Config by hand

```sh
keelbox init acme.yaml --box acme --provider ssh
keelbox plan acme.yaml --ssh-user ubuntu
keelbox apply acme.yaml --ssh-user ubuntu
keelbox login acme.yaml
keelbox apply acme.yaml
```

`keelbox up` runs the same commands. Each takes a config's path, or the name of a box in `~/.config/keelbox/boxes/`.

| Command | What it does |
|---|---|
| `init` | Writes a commented [config](../reference/config.md), and prints which keys to set and the next command. |
| `plan` | Lists what `apply` would change to make the box match the config. Changes nothing. |
| `apply --ssh-user` | Applies as the account that logs in now, then as `admin`, the account keelbox creates for you. Later applies leave the flag out. |
| `login` | Runs each sign-in on your terminal. |
| `apply` | Closes SSH from the internet, once it reaches the box over your tailnet, the private Tailscale network. |

For a cloud provider, `keelbox create acme.yaml` takes the place of the first apply: it creates the server, then applies. With `provider: local`, on the machine itself, `plan` and `apply` run themselves with sudo.

## A Tailscale auth key file

With an auth key in a file, `keelbox apply` joins the box to your tailnet with no approval in a browser. Generate the key in the Tailscale admin console; its tags and expiry are set there. Save it to a file only you can read:

```sh
mkdir -p ~/.config/keelbox
(umask 077; cat > ~/.config/keelbox/tailscale.authkey)   # paste the key, Enter, Ctrl-D
```

```yaml
access:
  tailscale:
    auth_key_file: ~/.config/keelbox/tailscale.authkey
```

keelbox refuses a file that others can read. It gives `tailscale up` the file's path, so the key is in no command line, and deletes the copy on the box after the run.
