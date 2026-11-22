# 0065: AWS through the user's AWS CLI, on Lightsail

Date: 2026-11-12. Status: accepted. Supersedes the AWS line of [ADR 0027](0027-delegated-auth.md).

## Context
keelbox creates a box's server on AWS, as it does on Hetzner, so nobody has to create it by hand. Two things make AWS different from Hetzner:

- **Credentials.** An AWS sign-in is not one token. It is access keys, IAM Identity Center (SSO), `aws login` with console credentials, assumed roles, MFA, a credential process, or several chained, and requests are signed (SigV4). Supporting that inside keelbox means an SDK (keelbox has one dependency) or a second implementation of the credential chain, and it would put AWS credentials in keelbox's hands. ADR 0027 proposed Identity Center's device flow; that covers only accounts with Identity Center enabled.
- **Which service.** EC2 needs a VPC, a subnet, a security group, a key pair and an AMI lookup before the first instance. Lightsail is one call for a server with a public address, a firewall and a fixed monthly price, which is what a box is.

Facts the design relies on, read 2026-11-12 in the AWS CLI v2 reference, the Lightsail API reference and the Lightsail user guide:
- `aws lightsail create-instances` takes `--instance-names`, `--availability-zone`, `--blueprint-id`, `--bundle-id`, `--user-data`, `--tags key=...,value=...`; any string parameter can be read from `file://path`. User data is "commands ... run as root"; AWS's launch-script article says the first line must be `#!/bin/bash`. Lightsail documents no cloud-config support.
- Ubuntu blueprints log in as `ubuntu`. A new Linux instance admits TCP 22 and 80 from anywhere. `put-instance-public-ports` "closes all currently open ports that are not included in the request".
- Static IPs: free while attached, USD 0.005 an hour when unattached for more than an hour, 5 per region by default, and they "remain in Lightsail, and incur additional charges" after their instance is deleted. `allocate-static-ip` takes no tags and `get-static-ip` returns none.
- `get-instances`, `get-bundles` and `get-blueprints` are paginated by the CLI itself. A missing resource is `NotFoundException`; the CLI exits 254 for a service error and 253 without credentials. `sts get-caller-identity` needs no permission.
- Tag conditions (`aws:RequestTag`, `aws:ResourceTag`) apply to `CreateInstances`, `DeleteInstance` and `PutInstancePublicPorts`; the `Get*` calls and `AllocateStaticIp` take neither a resource nor a condition.

## Decision
- **Provider `aws` runs the user's installed AWS CLI.** `internal/provider/aws` executes `aws ... --output json` and reads the JSON. keelbox holds no AWS credential, signs no request and adds no SDK. The config names a profile (`target.aws.profile`, passed as `--profile`) and a region; the sign-in is whatever that profile already has. There is nothing for `keelbox forget-token` to remove.
- **Lightsail, not EC2.** A box is one Lightsail instance named after the box, tagged `keelbox=true` and `keelbox-box=<box>`, with a static IP named `keelbox-<box>` so the address survives a stop and start.
- **Only tagged instances are touched.** `Find` and `Destroy` refuse an instance without both tags. Static IPs cannot carry tags, so keelbox knows its own by name and by attachment: it releases `keelbox-<box>` only when it is attached to nothing or to the box's tagged instance, and refuses otherwise. `create` fails when an instance or a static IP of the box's names exists.
- **First contact is verified.** The host key is generated on the laptop and pinned before the instance exists, as on Hetzner (ADR 0007). Lightsail takes a shell script, not cloud-config, so the user data is a script that replaces the image's host keys with the injected one and restarts sshd. Until it has run the instance presents keys keelbox has not pinned; keelbox refuses them and retries. The user data goes to the CLI as `file://` from a mode 600 temporary file, removed afterwards, never as an argument. After the first verified connection `RotateHostKeys` replaces the key.
- **The first login needs no key pair in the account.** The same script appends the admin's public keys to `ubuntu`'s `authorized_keys`. keelbox imports no Lightsail key pair, so there is none to delete. The first apply runs as `ubuntu` and locks that account once the admin is proven (`users.lock_cloud_user`).
- **The instance firewall follows the box**, as the Hetzner Cloud Firewall does (ADR 0022): UDP 41641 always, TCP 22 until the box has closed public SSH, nothing else. It is set as soon as the instance runs, which closes port 80.
- **Preflight names the fix.** No `aws` on PATH: the install page. `sts get-caller-identity` fails: `aws login`, `aws sso login` or `aws configure`, with the profile filled in. A region without Lightsail: the regions that have it. An unknown bundle or blueprint: some that exist.
- `keelbox up` offers AWS, checks the sign-in, and lists regions and bundles with the monthly prices `get-bundles` returns. It offers only active Linux bundles with a public IPv4 address.

## What the credential can reach, and what is checked
- The AWS CLI's sign-in can do whatever its profile is allowed, in the whole account; keelbox cannot narrow it. keelbox itself calls only `sts get-caller-identity` and Lightsail operations, and changes only the box's tagged instance and its static IP. This is enforced by keelbox's own code and covered by tests against a fake CLI (`TestNeverTouchesUntaggedInstances`), not by AWS.
- To have AWS enforce it, use a profile whose policy allows only Lightsail, with tag conditions on create and delete. `docs/guides/credentials.md` has the policy. The read calls and `AllocateStaticIp` cannot be limited by tag.
- Short-lived sign-ins (`aws login`, Identity Center) are preferred over access keys, which never expire. keelbox works with either and cannot tell which is in use.
- The host key never reaches a command line: a test fake fails if it appears in an argument, and checks the file's mode.

## Consequences
- AWS needs the AWS CLI on the laptop, version 2. Without it `keelbox up` says how to install it and stops (exit 3 without a terminal).
- A command that reaches AWS costs one `sts` call more than it strictly needs, so that an expired sign-in is reported with its fix instead of as a failed Lightsail call.
- Between boot and the first firewall call, ports 22 and 80 are open as Lightsail sets them; nothing listens on 80 on the Ubuntu image.
- Lightsail's default key pair for the region is installed for `ubuntu` as well, and its browser SSH connects as `ubuntu`; both end when keelbox locks that account, and the browser client also needs port 22.
- Prices are in US dollars before tax. IPv6-only bundles are left out: keelbox connects over IPv4 and a static IP needs one.
- keelbox does not create EC2 instances, so other instance families and VPCs are not available.
