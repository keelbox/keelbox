# Releases and the release manifest

```sh
keelbox version
```

```text
keelbox v0.1.0 6ba3787cc462 2026-11-21 (release 2026.11.4, go1.27.3, linux/amd64)
```

The line has two versions. `v0.1.0` is the keelbox binary. `2026.11.4` is the release manifest: the tested set of tools that binary installs on a box.

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

`checksums` keeps every release, so an older keelbox can still check its own box binary against the index.

`security`, when present, lists security problems and the release that fixes each:

```json
"security": [
  {"since": "v0.1.0", "fixed": "v0.1.1", "summary": "One line on the problem.", "url": "https://keelbox.dev/security/..."}
]
```

`since` is the first release affected; without it, every release before `fixed` is. keelbox shows a notice only when its versions are releases, its summary is one printable line of at most 200 characters, and its URL is on `https://keelbox.dev/` or `https://github.com/keelbox/`.

`releases` must name one of two release hosts; clients refuse any other:

- `https://github.com/keelbox/releases/releases/download`
- `https://github.com/keelbox/keelbox/releases/download`

Each release at `<releases>/<version>/` has:

| Asset | Content |
|---|---|
| `keelbox-{linux,darwin}-{amd64,arm64}` | Static binaries, no archive. |
| `keelbox-*.spdx.json` | An SPDX SBOM per binary. |
| `install.sh` | The installer, the same file get.keelbox.dev serves. |
| `SHA256SUMS` | Checksums of every binary, SBOM and `install.sh`. |
| `SHA256SUMS.sig` | A raw Ed25519 signature by the keelbox release key over the line `keelbox-release <version>` followed by `SHA256SUMS`, so one release's file cannot pass as another's. |
| `SHA256SUMS.sshsig` | The same key's signature in `ssh-keygen -Y` form, namespace `keelbox-release`, for machines whose openssl lacks Ed25519. |
| `SHA256SUMS.sigstore.json` | A keyless cosign bundle for `SHA256SUMS`, signed by the GitHub Actions identity `https://github.com/keelbox/keelbox/.github/workflows/release.yml@refs/heads/main`. Optional for clients. |
| `stable.json` | The channel index for this release. |

The release public keys are built into keelbox and `install.sh`. A signature by any one of them is accepted.

Stable is the only channel. A box never reads it: keelbox on a box changes only at an apply or `keelbox rollback`. keelbox on your computer reads it at `self-update`, `update`, when it downloads a box binary, and for the [update check](../guides/upgrades.md#the-update-check).

## Installing and updating keelbox

```sh
curl -fsSL https://get.keelbox.dev -o install.sh && less install.sh && sh install.sh
curl -fsSL https://get.keelbox.dev | sh                      # latest stable, to ~/.local/bin
curl -fsSL https://get.keelbox.dev | sudo sh -s -- --version vX.Y.Z --prefix /usr/local
curl -fsSL https://get.keelbox.dev | sh -s -- up             # on the machine to set up
```

The installer is a POSIX `sh` script, commented at the top. What it writes and how to remove keelbox: [install keelbox](../guides/installing.md).

### What the installer verifies

Before it installs anything, the installer checks:

| Check | How |
|---|---|
| The release host | The channel index must name one of keelbox's two release hosts. Downloads come from that host only. |
| The release key signature | `SHA256SUMS` must be signed by a release key the installer carries. It checks `SHA256SUMS.sig` with OpenSSL 3, or `SHA256SUMS.sshsig` with `ssh-keygen` from OpenSSH 8.1 or later. With neither tool it stops and names what to install. |
| The checksums | The binary's SHA-256 must match `SHA256SUMS`, and `SHA256SUMS` must agree with the index's checksum for that version and platform. |
| HTTPS | An HTTPS download may redirect only to HTTPS, with curl or wget. |

With `cosign` installed, the installer also verifies the Sigstore bundle against the signer identity above and stops on a mismatch. Without `cosign` it says nothing about it; a `cosign` on `PATH` that does not run gets a warning that the Sigstore signature was not checked.

A failed check stops the install before the binary is put in place.

### What the installer does

1. Reads `stable.json` for the version and release host.
2. Unless that version is already installed, downloads `SHA256SUMS`, its signature and this platform's binary, and runs the checks above. Under Rosetta it takes the native arm64 binary.
3. Installs the binary, without sudo.
4. Writes shell completion for bash, zsh and fish where they are installed.
5. Prints the line that puts the install directory on `PATH` when it is not there, and on a terminal offers to add it to the login shell's file.
6. Where Claude Code or Codex is set up, asks whether to add keelbox's skill for them. Without a terminal, or with `--no-modify-path`, it prints `keelbox agent-skill install` instead.
7. Without `up`, prints what to delete to remove keelbox. With `up`, runs `keelbox up` with the arguments after it, reading from the terminal even when the script came through a pipe.

| Option or variable | Effect |
|---|---|
| `--version vX.Y.Z` | Install that release. |
| `--prefix DIR` | The binary in `DIR/bin`, completion under `DIR/share`. |
| `--dir DIR` | The binary in `DIR`. Not with `--prefix`. |
| `--no-modify-path` | Print the `PATH` line; never offer to add it. |
| `up [flags]` | Run `keelbox up` once installed. |
| `KEELBOX_VERSION`, `KEELBOX_INSTALL_DIR` | Defaults for `--version` and `--dir`. |
| `KEELBOX_INDEX_URL` | Another HTTPS channel index, for a mirror. The release host it names must still be one of keelbox's. The binaries have no mirror option. |
| `--test` | For keelbox's own development only. Only with it are `KEELBOX_RELEASE_URL`, `KEELBOX_TEST_RELEASE_KEYS` and a plain HTTP index honoured, with a warning; without it, setting them stops the install. |

### Verifying a download by hand

Save the current public key, [`keelbox-release-1.pem`](../../internal/release/keys/keelbox-release-1.pem), as `keelbox-release.pem`:

```sh
v=vX.Y.Z; b=https://github.com/keelbox/releases/releases/download/$v
curl -fsSLO $b/SHA256SUMS -O $b/SHA256SUMS.sig -O $b/keelbox-linux-amd64
{ printf 'keelbox-release %s\n' "$v"; cat SHA256SUMS; } > signed
openssl pkeyutl -verify -pubin -inkey keelbox-release.pem -rawin -in signed -sigfile SHA256SUMS.sig
sha256sum --check --ignore-missing SHA256SUMS
```

OpenSSL prints `Signature Verified Successfully`. Then compare the binary's checksum with `checksums` in `https://get.keelbox.dev/stable.json`.

Without OpenSSL 3, download `SHA256SUMS.sshsig` and use `ssh-keygen`:

```sh
ssh-keygen -Y verify -n keelbox-release -I keelbox-release -f allowed_signers -s SHA256SUMS.sshsig < signed
```

`allowed_signers` holds one line, `keelbox-release namespaces="keelbox-release" ssh-ed25519 AAAA...`, with the key from `release_keys_ssh` in `get/install.sh`. `ssh-keygen` prints a line starting `Good "keelbox-release" signature`.

### keelbox self-update

`keelbox self-update` replaces the running binary with the version `stable.json` names. The download passes the same checks as the box binary below, and the new binary must run `version` before it is renamed into place. Self-update does not check the cosign bundle.

It refuses an older version without `--allow-downgrade`: older releases carry valid signatures, so the signature alone does not stop a downgrade.

## The binary uploaded to the box

For every target but `local`, keelbox uploads a Linux binary of its own version to the box, taking the first of:

1. `--binary PATH`;
2. itself, when it is Linux on the box's architecture;
3. `keelbox-linux-<arch>` beside itself;
4. the cache, `<user cache dir>/keelbox/X.Y.Z/keelbox-linux-<arch>`;
5. a download of exactly this version from an allowed release host, then cached.

A download is accepted only if `SHA256SUMS` carries a release key signature and the binary matches both `SHA256SUMS` and the index's `checksums`. The user cache directory is `~/Library/Caches` on macOS, and `$XDG_CACHE_HOME` or `~/.cache` on Linux.

A development build, whose `version` shows a git description or `dev`, never downloads: pass `--binary`. The box always runs the keelbox version that applied it, so the manifest `plan` showed is the one `apply` uses.

## Release manifest

[`internal/manifest/stable.yaml`](../../internal/manifest/stable.yaml), built into the binary, holds every tool version, checksum and key fingerprint keelbox installs from.

| Key | Pinning | What apply does |
|---|---|---|
| `release` | | Recorded in the logbook at every apply, and compared by the downgrade guard. |
| `os.ubuntu`, `os.arch` | | The supported platforms: Ubuntu 24.04 and 26.04, amd64 and arm64. apply refuses any other. |
| `docker.series`, `docker.gpg_fingerprint` | series | Installs the newest Docker Engine in the major series, Engine and CLI at the same version. The apt key must match the fingerprint. |
| `tailscale.gpg_fingerprint`, `github_cli.gpg_fingerprint` | key | The apt keys must match. The packages are the repository's current ones. |
| `mise.version`, `mise.sha256` | exact | Installs that mise, checked against the SHA-256 for the box's architecture. |
| `node`, `pnpm` | exact | Installed through mise for the agent. |
| `caddy.version`, `caddy.sha512` | exact | Installs that Caddy for previews from the release tarball, checked against the SHA-512. |
| `restic.version`, `restic.sha256` | exact | Installs that restic for backups, checked against the SHA-256. |
| `tmux.resurrect`, `tmux.continuum` | exact | The plugins' git commits. |

An exact pin converges up or down: apply installs that version whatever the box has. Claude Code and Codex are not pinned: their vendors' installers put the current release in the agent's home, and the CLIs [update themselves](../guides/upgrades.md#the-agent-clis).

Checksums and key fingerprints come from the vendors. The source of the Caddy and restic checksums: [tampered downloads](../security-model.md#tampered-downloads).
