# Security policy

keelbox hardens machines that run AI agents with access to code and credentials, so we treat security reports as the highest priority.

## Reporting a vulnerability

Please do not open a public issue. Report privately through either:

- GitHub private vulnerability reporting: **Security > Report a vulnerability** on [keelbox/keelbox](https://github.com/keelbox/keelbox/security/advisories/new), or
- email to [security@keelbox.dev](mailto:security@keelbox.dev).

Include the keelbox version (`keelbox version`), the Ubuntu release, the config (with secrets removed) and steps to reproduce. We aim to acknowledge within three working days, agree a disclosure date with you, and credit you in the advisory unless you prefer otherwise.

## Supported versions

keelbox is pre-1.0. Only the latest release on the stable channel (`https://get.keelbox.dev/stable.json`) receives fixes; `keelbox self-update` moves to it.

| Version | Supported |
|---|---|
| latest `0.x` release | yes |
| older releases | no |

## Scope

In scope: the keelbox CLI, the configuration it applies to a box, the installer at get.keelbox.dev and the release pipeline (signatures, checksums, the channel index). Out of scope: the agent CLIs keelbox installs (report those to their vendors) and Docker's documented bypass of UFW for published ports.

## Verifying releases

Every release's `SHA256SUMS` carries two signatures. The one every client checks (the installer, `keelbox self-update`, the download of a box binary) is the release key's, in `SHA256SUMS.sig` and, in OpenSSH's format, `SHA256SUMS.sshsig`: [docs/reference/release.md](docs/reference/release.md#installing-and-updating-keelbox) shows how to check it by hand with OpenSSL or `ssh-keygen`. The release workflow also signs `SHA256SUMS` keylessly with cosign, which the installer checks only when cosign is installed. To verify that one by hand:

```sh
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity https://github.com/keelbox/keelbox/.github/workflows/release.yml@refs/heads/main \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
```
