# Install keelbox

```sh
curl -fsSL https://get.keelbox.dev | sh
```

keelbox is one binary for macOS and Linux, on amd64 and arm64. The installer puts it in `~/.local/bin`, without sudo. Install it on the computer you manage boxes from, or on the Ubuntu machine that becomes the box.

## Read the installer first

```sh
curl -fsSL https://get.keelbox.dev -o install.sh
less install.sh
sh install.sh
```

The script is POSIX `sh`. Its first lines say what it does and which options it takes.

## On the machine to set up

```sh
curl -fsSL https://get.keelbox.dev | sh -s -- up
```

This installs keelbox, then starts `keelbox up` on your terminal. Flags after `up` go to `keelbox up`.

## With Homebrew

```sh
brew install keelbox/tap/keelbox
```

The formula installs the same release binary and its shell completion. Homebrew checks the binary against the SHA-256 in the formula, which comes from the release's signed `SHA256SUMS`. Update with `brew upgrade keelbox`; `keelbox self-update` stops on a Homebrew install and names that command.

## What the installer writes

| What | Where |
|---|---|
| The binary | `~/.local/bin/keelbox` |
| Completion for bash | `~/.local/share/bash-completion/completions/keelbox` |
| Completion for zsh | `~/.local/share/zsh/site-functions/_keelbox` |
| Completion for fish | `~/.config/fish/completions/keelbox.fish` |
| A `PATH` line, when `~/.local/bin` is not on your `PATH` | your login shell's file, under a comment that starts `# keelbox` |
| keelbox's skill for Claude Code and Codex, after a yes | `~/.claude/skills/keelbox`, rules in `~/.claude/settings.json`, `~/.codex/skills/keelbox`, `~/.codex/rules/keelbox.rules` |

Completion is written for each of the three shells that is installed. When zsh is your login shell, the installer also prints the `fpath` line to add before `compinit` in `~/.zshrc`.

The installer prints the `PATH` line and, on a terminal, asks before adding it. Open a new terminal afterwards.

Before the binary is put in place, the installer checks the release signature and the checksums: [what the installer verifies](../reference/release.md#installing-and-updating-keelbox).

Running the installer again updates keelbox and its completion. `keelbox self-update` updates the binary alone.

## Options

```sh
sh install.sh --version v0.1.0          # a given release
sh install.sh --prefix /opt/keelbox      # binary in /opt/keelbox/bin, completion under /opt/keelbox/share
sudo sh install.sh --prefix /usr/local   # a prefix your account cannot write to
sh install.sh --dir ~/bin                # binary in ~/bin, completion where your shells look
sh install.sh --no-modify-path           # print the PATH line, never offer to add it
```

`KEELBOX_VERSION` and `KEELBOX_INSTALL_DIR` in the environment are the defaults for `--version` and `--dir`.

## Check the installer before it runs

Each release's signed `SHA256SUMS` lists `install.sh`. get.keelbox.dev serves that file and its signature beside the script.

```sh
g=https://get.keelbox.dev
curl -fsSL -O $g/install.sh -O $g/SHA256SUMS -O $g/SHA256SUMS.sshsig
v=$(curl -fsSL $g/stable.json | sed -n 's/.*"version": "\(v[^"]*\)".*/\1/p')
echo 'keelbox-release namespaces="keelbox-release" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINz4QTjDO5ApRe46EVbxBWTVZkz6mtN5YYHYCgxv6E8Y' >allowed_signers
{ printf 'keelbox-release %s\n' "$v"; cat SHA256SUMS; } |
  ssh-keygen -Y verify -f allowed_signers -I keelbox-release -n keelbox-release -s SHA256SUMS.sshsig
grep ' install.sh$' SHA256SUMS | sha256sum --check    # macOS: shasum -a 256 --check
```

The first check prints a line that starts `Good "keelbox-release" signature`, the second `install.sh: OK`. The key in `allowed_signers` is keelbox's release key, which `install.sh` also carries.

## Remove keelbox

```sh
keelbox agent-skill remove    # first, if you added the skill
rm ~/.local/bin/keelbox ~/.local/share/bash-completion/completions/keelbox \
  ~/.local/share/zsh/site-functions/_keelbox ~/.config/fish/completions/keelbox.fish
```

The installer's last line lists the files it wrote. Delete the `PATH` line from your shell's file too, if the installer added one.

Your boxes' configs stay in `~/.config/keelbox`. To remove the boxes themselves, see [remove a box](removing-a-box.md).
