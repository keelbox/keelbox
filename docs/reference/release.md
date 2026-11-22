# Releases and the release manifest

```sh
keelbox version
```

```text
keelbox v0.1.0 6ba3787cc462 2026-11-21 (release 2026.11.4, go1.27.3, linux/amd64)
```

The line carries two versions. The CLI version, `v0.1.0`, names the keelbox binary. The release manifest id, `2026.11.4`, names the tested set of tools that binary installs on a box.

## The stable channel

`https://get.keelbox.dev/stable.json` is the channel index. It names the current stable version, the release host that serves it, and the checksums of every published release:

```json
{
  "channel": "stable",
  "version": "v0.1.0",
  "releases": "https://github.com/keelbox/releases/releases/download",
  "binaries": {
    "linux-amd64": {
      "url": "https://github.com/keelbox/releases/releases/download/v0.1.0/keelbox-linux-amd64",
      "sha256": "..."
    }
  },
  "checksums": {
    "v0.1.0/linux-amd64": "..."
  }
}
```

`checksums` keeps every release's entries, so an older keelbox can check its own version's box binary against the index.

`security`, when there is one, lists security problems and the release that fixes each:

```json
"security": [
  {"since": "v0.1.0", "fixed": "v0.1.1", "summary": "One line on the problem.", "url": "https://keelbox.dev/security/..."}
]
```

`since` is the first release affected, and without it every release before `fixed` is. keelbox shows a notice whose versions are releases, whose summary is one printable line of at most 200 characters, and whose URL is on `https://keelbox.dev/` or `https://github.com/keelbox/`, and ignores any other.

`releases` may name one of two release hosts, and clients refuse any other:

- `https://github.com/keelbox/releases/releases/download`
- `https://github.com/keelbox/keelbox/releases/download`

Each release at `<releases>/<version>/` has:

| Asset | Content |
|---|---|
| `keelbox-{linux,darwin}-{amd64,arm64}` | Static binaries, no archive. |
| `keelbox-*.spdx.json` | An SPDX SBOM per binary. |
| `install.sh` | The installer, the same file get.keelbox.dev serves. |
| `SHA256SUMS` | Checksums of every binary, SBOM and `install.sh`. |
| `SHA256SUMS.sig` | A raw Ed25519 signature by the keelbox release key. It covers the line `keelbox-release <version>` followed by `SHA256SUMS`, so one release's file does not pass as another's. |
| `SHA256SUMS.sshsig` | The same key's signature in `ssh-keygen -Y` form, namespace `keelbox-release`, for machines whose openssl lacks Ed25519. |
| `SHA256SUMS.sigstore.json` | A keyless cosign bundle for `SHA256SUMS`, signed by the GitHub Actions identity `https://github.com/keelbox/keelbox/.github/workflows/release.yml@refs/heads/main`. No client requires it. |
| `stable.json` | The channel index for this release. |

The release public keys are embedded in keelbox, in `internal/release/keys/`, and written into `get/install.sh`. A signature by any one of them is accepted.

Stable is the only channel. A box never reads it: keelbox on a box changes only at an apply or a `keelbox rollback`. keelbox on your computer reads it at `self-update`, `update`, when it downloads a box binary, and for the [update check](../guides/upgrades.md#the-update-check).

## Installing and updating keelbox

```sh
curl -fsSL https://get.keelbox.dev -o install.sh && less install.sh && sh install.sh
curl -fsSL https://get.keelbox.dev | sh                      # latest stable, to ~/.local/bin
curl -fsSL https://get.keelbox.dev | sudo sh -s -- --version vX.Y.Z --prefix /usr/local
curl -fsSL https://get.keelbox.dev | sh -s -- up             # on the machine to set up
```

The installer is a POSIX `sh` script, commented at the top. Its options, what it writes and how to remove keelbox are on [Install keelbox](../guides/installing.md).

### What the installer verifies

Before it installs anything, the installer checks four things:

| Check | How |
|---|---|
| The release host | The channel index must name one of keelbox's two release hosts. Downloads come from that host only. |
| The release key signature | `SHA256SUMS` must carry a signature by a release key the installer holds. It checks `SHA256SUMS.sig` with OpenSSL 3, or `SHA256SUMS.sshsig` with `ssh-keygen` from OpenSSH 8.1 or later. With neither tool it stops and names what to install. |
| The checksums | The binary's SHA-256 must match `SHA256SUMS`, and `SHA256SUMS` must agree with the checksum the channel index lists for that version and platform. |
| HTTPS | An HTTPS download may redirect only to HTTPS, with curl or wget. |

When `cosign` is installed, the installer also verifies the Sigstore bundle against the signer identity above, and stops if it does not match. Without `cosign` it prints nothing about it. A `cosign` that is on `PATH` but does not run gets a warning that the Sigstore signature was not checked.

A failed check stops the install before the binary is put in place.

### What the installer does

1. It reads `stable.json` for the version and the release host.
2. Unless that version is already installed, it downloads `SHA256SUMS`, its signature and this platform's binary, and runs the checks above. Under Rosetta it takes the native arm64 binary.
3. It installs the binary by rename, without sudo.
4. It writes shell completion for bash, zsh and fish where they are installed.
5. It prints the line that puts the install directory on `PATH` when it is not there. On a terminal it offers to add the line to the login shell's file.
6. Without `up`, it prints what to delete to remove keelbox.
7. With `up`, it runs `keelbox up` with the arguments after it, reading from the terminal even when the script came through a pipe.

| Option or variable | Effect |
|---|---|
| `--prefix DIR` | The binary in `DIR/bin`, completion under `DIR/share`. |
| `--dir DIR` | The binary in `DIR`. |
| `KEELBOX_VERSION`, `KEELBOX_INSTALL_DIR` | Defaults for `--version` and `--dir`. |
| `KEELBOX_INDEX_URL` | Another HTTPS channel index, for a mirror of the index. The release host it names must still be one of keelbox's. |
| `--test` | For the installer's own tests. Only with it are `KEELBOX_RELEASE_URL`, `KEELBOX_TEST_RELEASE_KEYS` and a plain HTTP index honoured, with a warning. Without it, setting them stops the install. |

The binaries have no mirror option.

### Verifying a download by hand

Save the current public key, [`keelbox-release-1.pem`](../../internal/release/keys/keelbox-release-1.pem), as `keelbox-release.pem`:

```sh
v=vX.Y.Z; b=https://github.com/keelbox/releases/releases/download/$v
curl -fsSLO $b/SHA256SUMS -O $b/SHA256SUMS.sig -O $b/keelbox-linux-amd64
{ printf 'keelbox-release %s\n' "$v"; cat SHA256SUMS; } > signed
openssl pkeyutl -verify -pubin -inkey keelbox-release.pem -rawin -in signed -sigfile SHA256SUMS.sig
sha256sum --check --ignore-missing SHA256SUMS
```

Then compare the binary's checksum with `checksums` in `https://get.keelbox.dev/stable.json`.

Without OpenSSL 3, download `SHA256SUMS.sshsig` and use `ssh-keygen`:

```sh
ssh-keygen -Y verify -n keelbox-release -I keelbox-release -f allowed_signers -s SHA256SUMS.sshsig < signed
```

`allowed_signers` holds one line: `keelbox-release namespaces="keelbox-release" ssh-ed25519 AAAA...`. The key is `release_keys_ssh` in `get/install.sh`. OpenSSL prints `Signature Verified Successfully`. `ssh-keygen` prints a line that starts `Good "keelbox-release" signature`.

### keelbox self-update

`keelbox self-update` replaces the running binary with the version `stable.json` names. The download passes the same checks as the box binary below. The new binary must also run `version` before it is renamed into place. Self-update does not check the cosign bundle.

It refuses a version older than the running one unless `--allow-downgrade` is given. Older releases carry valid signatures, so the signature alone does not stop a move back.

## The binary uploaded to the box

For every target but `local`, keelbox uploads a linux binary of its own version to the box. It takes the first of:

1. `--binary PATH`;
2. itself, when it is linux on the box's architecture;
3. `keelbox-linux-<arch>` beside itself, as `make build` leaves it;
4. the cache, `<user cache dir>/keelbox/X.Y.Z/keelbox-linux-<arch>`;
5. a download of exactly this version from an allowlisted release host, which is then cached.

The download is accepted only if `SHA256SUMS` carries a release key signature and the binary matches both `SHA256SUMS` and the index's `checksums`. The user cache directory is `~/Library/Caches` on macOS, and `$XDG_CACHE_HOME` or `~/.cache` on Linux.

A development build, whose `version` reports a git description or `dev`, never downloads: pass `--binary` or run `make build`. The box always runs the version of the keelbox that applied it, so the manifest `plan` showed is the one `apply` uses.

## Release manifest

[`internal/manifest/stable.yaml`](../../internal/manifest/stable.yaml) is embedded in the binary. It is the one place tool versions, checksums and key fingerprints live.

| Key | Pinning | What apply does |
|---|---|---|
| `release` | | Recorded in the logbook at every apply, and compared by the downgrade guard. |
| `os.ubuntu`, `os.arch` | | The supported platforms: Ubuntu 24.04 and 26.04, on amd64 and arm64. apply refuses any other. |
| `docker.series`, `docker.gpg_fingerprint` | series | Installs the newest Docker Engine in the major series, Engine and CLI at the same version. The apt key must match the fingerprint. |
| `tailscale.gpg_fingerprint`, `github_cli.gpg_fingerprint` | key | The apt keys must match. The packages are the repository's current ones. |
| `mise.version`, `mise.sha256` | exact | Installs that mise, checked against the SHA-256 for the box's architecture. |
| `node`, `pnpm` | exact | Installed through mise for the agent. |
| `caddy.version`, `caddy.sha512` | exact | Installs that Caddy for previews from the release tarball, checked against the SHA-512. |
| `restic.version`, `restic.sha256` | exact | Installs that restic for backups, checked against the SHA-256. |
| `tmux.resurrect`, `tmux.continuum` | exact | The plugins' git commits. |

An exact pin converges up or down: apply installs that version whichever one the box has. The manifest has no pins for Claude Code and Codex: their vendors' installers put the current release in the agent's home, and the CLIs [update themselves](../guides/upgrades.md#the-agent-clis).

The checksums and key fingerprints were taken from the vendors when each pin was set. [Tampered downloads](../security-model.md#tampered-downloads) names the source of the Caddy and restic checksums.
