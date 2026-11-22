# Cloud sign-ins and permissions

```sh
keelbox up
```

Step 2 of `keelbox up` checks that this computer can reach the cloud you chose. Hetzner takes an API token you paste there. AWS, DigitalOcean and Google Cloud take the sign-in of a CLI you install first: keelbox runs that CLI and reads its output.

## Hetzner: an API token

There is nothing to install. A card at step 2 gives three steps: create a project in the Hetzner Console, generate a Read & Write API token, and paste it. keelbox checks the token against the API and refuses a read-only one.

The token reaches every server, volume and firewall in its project, and nothing outside it. The narrowest set-up is a project that holds only boxes.

keelbox stores the token on this computer:

| Computer | Where |
|---|---|
| macOS | The login Keychain. |
| Linux with a desktop session | The Secret Service, through `secret-tool`. |
| Linux without one | `~/.config/keelbox/credentials/hetzner`, mode 600. keelbox warns that any program running as you can read it. |

Back to [your first box](../getting-started.md).

### Removing or rotating the token

```sh
keelbox forget-token
```

This removes keelbox's copy. The token works until you delete it in the Hetzner Console, in the project under Security, API tokens.

To rotate the token, create a new one and run `keelbox forget-token`. The next command that needs a token asks for it. Then delete the old one in the console.

### Using the hcloud CLI's token

When Hetzner's `hcloud` CLI is installed and signed in, step 2 first asks `Use the token of the hcloud context "work"?`, with Yes preselected. A yes writes the context's name to the box's config:

```yaml
target:
  hetzner:
    hcloud_context: work
```

keelbox then asks hcloud for the token each time it needs the API, and stores no copy. This needs hcloud 1.44.0 or newer.

To stop, remove `hcloud_context` from the config and run `keelbox up acme`, which asks for a token to store. `hcloud context delete work` removes the token from hcloud.

## AWS: nothing kept

Install the [AWS CLI](https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html) and sign in one of these ways:

```sh
aws login
aws sso login --profile keelbox    # after: aws configure sso --profile keelbox
aws configure --profile keelbox    # access keys
```

Step 2 asks `AWS CLI profile`; Enter takes the CLI's default. The box's config names the profile and the region, and keelbox stores nothing else. When the sign-in has run out, a keelbox command stops and names these commands.

The sign-in reaches everything the profile is allowed, in the whole account. keelbox calls Lightsail only, and `sts get-caller-identity` to say who is signed in. To have AWS hold it to Lightsail, give keelbox a profile with this policy:

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

With it the profile reaches nothing outside Lightsail. The first statement has no tag condition: the read calls and the static IPs are allowed for all of Lightsail. The policy follows AWS's service authorization reference and has not been run with a restricted profile on a live account.

Back to [your first box](../getting-started.md).

## DigitalOcean: nothing kept

Install [doctl](https://docs.digitalocean.com/reference/doctl/how-to/install/) and sign in through the browser, with the scopes keelbox needs:

```sh
doctl auth login --scopes "account:read regions:read sizes:read image:read droplet:create droplet:read droplet:delete firewall:create firewall:read firewall:update firewall:delete tag:create tag:read tag:delete droplet:update image:create image:update image:delete"
```

Without `--scopes` you choose the permissions on DigitalOcean's page. A sign-in that may read the account but not create passes step 2, and is refused at the first thing keelbox creates. keelbox then names this command again.

A doctl without `auth login` takes a token instead. Create one with the same scopes in the [control panel](https://cloud.digitalocean.com/account/api/tokens) and save it with `doctl auth init`.

```text
account:read  regions:read  sizes:read  image:read
droplet:create  droplet:read  droplet:delete
firewall:create  firewall:read  firewall:update  firewall:delete
tag:create  tag:read  tag:delete
droplet:update  image:create  image:update  image:delete
ssh_key:read
```

These are the narrowest scopes that work. The fifth line is needed by `keelbox resize`, `park` and `resume` only. `ssh_key:read` is optional: with it keelbox finds your SSH key in the account, and DigitalOcean then sets no root password.

The sign-in reaches everything its scopes allow, in the whole team. Scopes do not limit by tag, so it reaches every Droplet, firewall, snapshot and custom image of the team. The box's config names the doctl context, and keelbox stores nothing else.

Back to [your first box](../getting-started.md).

## Google Cloud: nothing kept

Install [gcloud](https://cloud.google.com/sdk/docs/install), sign in, and turn on the Compute Engine API in a project that has billing:

```sh
gcloud auth login
gcloud config set project PROJECT-ID
gcloud services enable compute.googleapis.com --project PROJECT-ID
```

Step 2 asks `Google Cloud project` and offers gcloud's own. The box's config names the project, and keelbox stores nothing else.

The sign-in reaches everything its account is allowed in the project. Google's IAM does not limit by label or name. The narrowest set-up is a project that holds only boxes, with the role Compute Admin, `roles/compute.admin`, and no other.

keelbox creates the VM without a service account, so nothing on the box gets a token for the project.

Back to [your first box](../getting-started.md).

## Other secrets

A Tailscale auth key and the backup credentials are mode 600 files you create and name in the config by path. keelbox reads them by path and copies them to the box for the run; none goes into the credential store.
