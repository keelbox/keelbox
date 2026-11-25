# Where a box can run

```sh
keelbox up
```

The first question of `keelbox up` is where the box runs: a new cloud server, or a machine you already have. Either needs Ubuntu 24.04 or 26.04 with internet access over IPv4, on amd64 or arm64. An IPv6-only server needs NAT64 with DNS64, because GitHub has no IPv6 address. One or two agents need 2 vCPU and 4 GB of memory.

## A new cloud server

keelbox creates the server, and can later resize, park and delete it.

| | Hetzner | AWS (Lightsail) | DigitalOcean | Google Cloud |
|---|---|---|---|---|
| On this computer | nothing: you paste an API token | the AWS CLI, signed in | doctl, signed in | gcloud, signed in |
| In the account | a project | nothing more | nothing more | a project with billing, the Compute Engine API and the `default` network |
| What keelbox can reach | every server in the token's project | all the AWS profile may do, account-wide | all the token's scopes allow, team-wide | all the account may do in the project |
| What keelbox stores | the token, in your OS credential store or a mode 600 file | nothing | nothing | nothing |
| Price shown before you confirm | EUR with VAT | USD before tax | USD before tax | none: gcloud has no price list |
| Resize | up and down | up only; the disk grows | up and down | up and down |
| Limits | arm64 sizes need arm64 builds of your own images | Lightsail, not EC2; no rescue console | root password emailed unless your SSH key is in the account | E2 types in the menu, x86 only; park refuses a second disk |

On every cloud:

- The preselected size is the cheapest with 2 vCPU and 4 GB.
- A powered-off server is still billed. `keelbox park` deletes the server and keeps its disk as a snapshot, billed per GB stored (on AWS, per GB of the disk's size); `keelbox destroy` deletes both.
- A parked box comes back at a new public address.
- keelbox changes only resources that carry its `keelbox` label or tag. The cloud enforces that only as far as you narrow the sign-in.

What to install and how to narrow each sign-in: [cloud sign-ins and permissions](credentials.md). How each cloud resizes, parks and resumes: [the four clouds](../reference/clouds.md).

## A server or machine you already have

| | A server you reach over SSH | A server you create yourself | This machine |
|---|---|---|---|
| What it is | a VPS, a VM or a home server | a new server at any provider that accepts a cloud-config | the Ubuntu machine you run keelbox on |
| What you need | SSH as root, or as an account with sudo | to paste a snippet keelbox prints into the provider's form | sudo |
| Guide | [a server you already have](adopting-a-box.md) | [a server you already have](adopting-a-box.md) | [a machine already in use](machine-in-use.md) |

None of these needs a token or a cloud sign-in. The firewall is UFW on the box, with no cloud firewall in front unless you add one.

An LXC container (Proxmox, LXD or Incus) works with nesting on and the TUN device for Tailscale. Apply checks both first and refuses a container without them, so a container plan whose host you cannot change will not work: [an LXC container](troubleshooting.md#an-lxc-container).

keelbox does not create, resize, park or delete these machines. `keelbox forget` stops managing one and leaves it as it was set up.

On a machine that already runs other software, `keelbox up` first lists what it would replace and asks. A machine at home is reachable only while awake; `host.power.sleep: never` in the config stops it suspending.
