# Where a box can run

```sh
keelbox up
```

The first question of `keelbox up` is where the box runs: a new cloud server, a server you already have, or the machine you are on. A box is Ubuntu 24.04 or 26.04 with internet access, on amd64 or ARM (arm64); ARM was tested once, on an arm64 Google Cloud VM set up over SSH; on Google Cloud keelbox creates only x86 servers. One or two agents need 2 vCPU and 4 GB.

## A new cloud server

keelbox creates the server, and can later resize it, park it and delete it.

| | Hetzner | AWS (Lightsail) | DigitalOcean | Google Cloud |
|---|---|---|---|---|
| Needed on this computer | nothing: you paste an API token | the AWS CLI, signed in | doctl, signed in | gcloud, signed in |
| Needed in the account | a project | nothing more | nothing more | a project with billing, the Compute Engine API and the `default` network |
| What the sign-in reaches | every server in the token's project | all the profile may do, in the whole account | all the token's scopes allow, in the whole team | all the account may do in the project |
| Narrowest sign-in | a project used only for boxes | a policy for Lightsail alone | 18 custom scopes | Compute Admin on a project used only for boxes |
| What keelbox stores | the token, in your OS credential store or a mode 600 file | nothing | nothing | nothing |
| Preselected location | `fsn1` | the AWS CLI's region | the first region listed | gcloud's zone |
| Price shown before you confirm | EUR with VAT | USD before tax | USD before tax | none: gcloud has no price list |
| Resize | up and down | up only; the disk grows | up and down | up and down |
| Snapshot of a parked box, billed | per GB stored | per GB of the disk's size | per GB stored | per GB stored |
| Cloud firewall takes effect | with the server | seconds after it | seconds after it | with the server |
| Limits | Arm64 sizes need arm64 builds of your own images | Lightsail, not EC2; no rescue console | root password emailed unless your SSH key is in the account | E2 types in the menu; park refuses a second disk |

On all four:

- The preselected size is the cheapest with 2 vCPU and 4 GB.
- A server that is powered off is still billed. `keelbox park` deletes the server and keeps its disk as a snapshot; `keelbox destroy` deletes both.
- A parked box gives up its public address and comes back at a new one.
- keelbox changes only what carries its `keelbox` label or tag. The cloud enforces that only as far as the sign-in is narrowed.
- The end-to-end test has created, set up, checked and destroyed a box on a live account.

The steps for [getting back in](public-ssh.md#getting-back-in) to a locked-out box have been run on a live account on each cloud.

What to install and the sign-in command for each cloud are on [cloud sign-ins and permissions](credentials.md).

How each cloud resizes, parks and resumes is in the [clouds reference](../reference/clouds.md).

## A server or machine you already have

| | A server you reach over SSH | A server you create yourself | This machine |
|---|---|---|---|
| What it is | a VPS, a VM or a home server | a new server at any provider that takes a cloud-config | the Ubuntu machine you run keelbox on |
| Needed | SSH as root, or as an account with sudo | to paste a snippet keelbox prints into the provider's form | sudo |

None of the three needs a token or a cloud sign-in, and keelbox shows no price for them. The firewall is UFW on the box, with no cloud firewall in front unless you add one.

keelbox does not create, resize, park or delete these machines. `keelbox forget` stops managing one and leaves it as it was set up.

On a machine that runs other software, `keelbox up` first lists what is listening and what it would replace: [a machine already in use](machine-in-use.md).

A machine at home is reachable only while it is awake. `host.power.sleep: never` in the config stops it suspending.

## What differs

Hetzner takes one token, which keelbox stores, and needs no other tool. AWS, DigitalOcean and Google Cloud use the sign-in of a CLI you install, and keelbox stores nothing for them.

Google Cloud is the one cloud with no price on the screen before you confirm. AWS is the one cloud that cannot resize down.

A machine you own adds no bill from keelbox's side, and keelbox cannot resize, park or delete it.
