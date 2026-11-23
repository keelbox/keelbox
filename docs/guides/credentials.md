# Cloud sign-ins and permissions

```sh
keelbox up
```

Step 2 of `keelbox up` checks that this computer can reach the cloud you chose. For Hetzner you paste an API token. For AWS, DigitalOcean and Google Cloud you install and sign in to the cloud's own CLI first; keelbox runs it and stores nothing.

## Hetzner: an API token

There is nothing to install. At step 2, create a project in the Hetzner Console, generate a Read & Write API token, and paste it. keelbox checks the token and refuses a read-only one.

The token reaches every server, volume and firewall in its project, and nothing outside it. For the narrowest access, use a project that holds only boxes.

keelbox stores the token on this computer:

| Computer | Where |
|---|---|
| macOS | the login Keychain |
| Linux with a desktop session | the Secret Service, through `secret-tool` |
| Linux without one | `~/.config/keelbox/credentials/hetzner`, mode 600. keelbox warns that any program running as you can read it. |

Back to [your first box](../getting-started.md).

### Removing or rotating the token

```sh
keelbox forget-token
```

This removes keelbox's copy only. The token works until you delete it in the Hetzner Console, under the project's Security, API tokens.

To rotate, create a new token and run `keelbox forget-token`; the next command that needs a token asks for it. Then delete the old one in the console.

### Using the hcloud CLI's token

If Hetzner's `hcloud` CLI (1.44.0 or newer) is installed and signed in, step 2 first asks `Use the token of the hcloud context "work"?`, with Yes preselected. A yes writes the context to the config:

```yaml
target:
  hetzner:
    hcloud_context: work
```

keelbox then asks hcloud for the token each time and stores no copy. To stop, remove `hcloud_context` and run `keelbox up acme`, which asks for a token to store. `hcloud context delete work` removes the token from hcloud.

## AWS: nothing kept

Install the [AWS CLI](https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html) and sign in one of these ways:

```sh
aws login
aws sso login --profile keelbox    # after: aws configure sso --profile keelbox
aws configure --profile keelbox    # access keys
```

Step 2 asks `AWS CLI profile`; Enter takes the CLI's default. The config names the profile and region. When the sign-in runs out, keelbox stops and names these commands.

The sign-in reaches everything the profile may do in the whole account. keelbox calls only Lightsail, plus `sts get-caller-identity` to show who is signed in. To have AWS limit it to Lightsail, give keelbox a profile with this policy:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ReadAndAddress",
      "Effect": "Allow",
      "Action": [
        "lightsail:GetRegions", "lightsail:GetBundles", "lightsail:GetBlueprints",
        "lightsail:GetInstance", "lightsail:GetInstances", "lightsail:GetInstancePortStates",
        "lightsail:GetInstanceSnapshot", "lightsail:GetInstanceSnapshots",
        "lightsail:GetStaticIp", "lightsail:GetStaticIps",
        "lightsail:AllocateStaticIp", "lightsail:AttachStaticIp", "lightsail:DetachStaticIp",
        "lightsail:ReleaseStaticIp"
      ],
      "Resource": "*"
    },
    {
      "Sid": "CreateOnlyTagged",
      "Effect": "Allow",
      "Action": [
        "lightsail:CreateInstances", "lightsail:CreateInstanceSnapshot",
        "lightsail:CreateInstancesFromSnapshot", "lightsail:TagResource"
      ],
      "Resource": "*",
      "Condition": {"StringEquals": {"aws:RequestTag/keelbox": "true"}}
    },
    {
      "Sid": "ChangeOnlyTagged",
      "Effect": "Allow",
      "Action": [
        "lightsail:DeleteInstance", "lightsail:PutInstancePublicPorts",
        "lightsail:StopInstance", "lightsail:StartInstance",
        "lightsail:DeleteInstanceSnapshot", "lightsail:TagResource"
      ],
      "Resource": "*",
      "Condition": {"StringEquals": {"aws:ResourceTag/keelbox": "true"}}
    }
  ]
}
```

With it, the profile reaches nothing outside Lightsail. The read calls and static IPs in the first statement have no tag condition, so they cover all of Lightsail. The policy follows AWS's service authorization reference and has not been tried with a restricted profile on a live account.

Back to [your first box](../getting-started.md).

## DigitalOcean: nothing kept

Install [doctl](https://docs.digitalocean.com/reference/doctl/how-to/install/) and sign in through the browser with the scopes keelbox needs:

```sh
doctl auth login --scopes "account:read regions:read sizes:read image:read droplet:create droplet:read droplet:delete firewall:create firewall:read firewall:update firewall:delete tag:create tag:read tag:delete droplet:update image:create image:update image:delete"
```

Without `--scopes` you pick permissions on DigitalOcean's page. A sign-in that can read but not create passes step 2 and fails at the first thing keelbox creates; keelbox then names this command again.

If your doctl has no `auth login`, create a token with the same scopes in the [control panel](https://cloud.digitalocean.com/account/api/tokens) and save it with `doctl auth init`.

These are the narrowest scopes that work. `droplet:update` and the `image:create`, `image:update` and `image:delete` scopes are needed only by `keelbox resize`, `park` and `resume`. You can add `ssh_key:read`: keelbox then finds your SSH key in the account, and DigitalOcean sets no root password.

Scopes do not limit by tag, so the sign-in reaches every Droplet, firewall, snapshot and custom image in the team. The config names the doctl context.

Back to [your first box](../getting-started.md).

## Google Cloud: nothing kept

Install [gcloud](https://cloud.google.com/sdk/docs/install), sign in, and turn on the Compute Engine API in a project with billing:

```sh
gcloud auth login
gcloud config set project PROJECT-ID
gcloud services enable compute.googleapis.com --project PROJECT-ID
```

Step 2 asks `Google Cloud project` and offers gcloud's own. The config names the project.

The sign-in reaches everything its account may do in the project; Google's IAM does not limit by label or name. For the narrowest access, use a project that holds only boxes, with the role Compute Admin (`roles/compute.admin`) and no other. keelbox creates the VM without a service account, so nothing on the box gets a token for the project.

Back to [your first box](../getting-started.md).

## Other secrets

A Tailscale auth key and backup credentials are mode 600 files you create and name in the config by path. keelbox copies them to the box for the run; none goes into the credential store.
