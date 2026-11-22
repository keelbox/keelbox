# Security policy

## Reporting a vulnerability

Do not open a public issue. Report privately, by either:

- GitHub private vulnerability reporting: [report a vulnerability](https://github.com/keelbox/keelbox/security/advisories/new);
- email to [security@keelbox.dev](mailto:security@keelbox.dev).

Include the output of `keelbox version`, the Ubuntu release, the config with secrets removed, and the steps to reproduce. We aim to acknowledge within three working days, agree a disclosure date with you, and credit you in the advisory unless you prefer otherwise.

## Supported versions

Only the latest release on the stable channel receives fixes. `keelbox self-update` moves to it.

## Scope

In scope: the keelbox CLI, what it configures on a box, the installer at get.keelbox.dev, and the release pipeline (signatures, checksums, the channel index). A way past a control the [security model](docs/security-model.md) says keelbox enforces is in scope.

Out of scope: the agent CLIs keelbox installs (report those to their vendors), and what the security model lists under [what keelbox does not defend against](docs/security-model.md#what-keelbox-does-not-defend-against).

## Verifying releases

Every release's `SHA256SUMS` is signed by the release key, in `SHA256SUMS.sig` and, in OpenSSH's format, `SHA256SUMS.sshsig`. The installer, `keelbox self-update` and the box binary download all check it; [verifying a download by hand](docs/reference/release.md#verifying-a-download-by-hand) shows how to check it yourself with OpenSSL or `ssh-keygen`.

The release workflow also signs `SHA256SUMS` keylessly with cosign. The installer checks that signature when cosign is installed. To check it by hand:

```sh
cosign verify-blob --bundle SHA256SUMS.sigstore.json \
  --certificate-identity https://github.com/keelbox/keelbox/.github/workflows/release.yml@refs/heads/main \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS
```
