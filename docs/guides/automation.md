# Without the wizard: flags, CI, config by hand

```sh
keelbox up acme --provider ssh --host 203.0.113.10 --ssh-user root --host-key SHA256:... \
  --git-name "Ada Lovelace" --git-email ada@example.com --yes
```

Without a terminal, `keelbox up` asks nothing: each answer is a flag or its default. It always needs `--provider`, the box name and `--yes`, and also:

- `--host` and `--host-key` with `--provider ssh` (`--host-key` only for a server keelbox has not connected to; the error lists its fingerprints to compare with the provider's console);
- `--token-file` with `hetzner`, unless a token is stored or `HCLOUD_TOKEN` is set;
- `--location` with `digitalocean`, and with `aws` or `gcp` when their CLI has no default;
- `--lan-ports` or `--adopt` with `ssh` or `local`, when the firewall would block a listening service or the first apply would cut off or replace something.

Without `--git-name` and `--git-email`, `git commit` on the box fails. On a private address SSH stays open to the LAN unless you pass `--public-ssh auto`. Every flag: [command reference](../reference/cli.md#up).

## Exit codes

| Code | When |
|---|---|
| 2 | An answer is missing. Nothing has changed; the message ends with an example command. |
| 3 | A step only you can do. The message names the command that continues. |

Exit 3 comes when the server refuses your SSH key or its sudo asks for a password, when the cloud's CLI is missing or signed out, when the cloud-config is printed for you to create the server, or when only the sign-ins are left (add `--skip-sign-in` to finish without them). Other codes: [command reference](../reference/cli.md#exit-codes).

## A command per cloud

| Cloud | Command | The job needs |
|---|---|---|
| Hetzner | `keelbox up acme --provider hetzner --token-file /run/secrets/hetzner --yes` | the API token in a mode 600 file |
| AWS | `keelbox up acme --provider aws --location eu-central-1 --yes` | the AWS CLI and its credentials |
| DigitalOcean | `keelbox up acme --provider digitalocean --location fra1 --yes` | doctl, with its token in `DIGITALOCEAN_ACCESS_TOKEN` |
| Google Cloud | `keelbox up acme --provider gcp --gcp-project my-project --location europe-west3-a --yes` | gcloud, signed in |

Permissions each sign-in needs: [cloud sign-ins and permissions](credentials.md).

## Config by hand

```sh
keelbox init acme.yaml --box acme --provider ssh    # write a commented config
keelbox plan acme.yaml --ssh-user ubuntu            # what apply would change; changes nothing
keelbox apply acme.yaml --ssh-user ubuntu           # first apply, as the account that logs in now
keelbox login acme.yaml                             # the sign-ins, on your terminal
keelbox apply acme.yaml                             # closes SSH from the internet once the tailnet works
```

These are the commands `keelbox up` runs. Each takes a config path or the name of a box in `~/.config/keelbox/boxes/`. After the first apply keelbox logs in as `admin`, the account it creates, so drop `--ssh-user`. For a cloud provider, `keelbox create acme.yaml` creates the server and applies in place of the first apply. With `provider: local`, `plan` and `apply` run themselves with sudo.

## A Tailscale auth key file

With an auth key in a file, `keelbox apply` joins the box to your tailnet without approval in a browser. Generate the key in the Tailscale admin console, where its tags and expiry are set, and save it to a file only you can read:

```sh
mkdir -p ~/.config/keelbox
(umask 077; cat > ~/.config/keelbox/tailscale.authkey)   # paste the key, Enter, Ctrl-D
```

```yaml
access:
  tailscale:
    auth_key_file: ~/.config/keelbox/tailscale.authkey
```

keelbox refuses a file others can read. The key never appears on a command line, and the copy on the box is deleted after the run.
