#!/bin/sh
# keelbox installer, served at https://get.keelbox.dev
#
# Read it before you run it:
#   curl -fsSL https://get.keelbox.dev -o install.sh
#   less install.sh
#   sh install.sh
# or in one go: curl -fsSL https://get.keelbox.dev | sh
# On the Ubuntu machine to set up, install and start keelbox up there:
#   curl -fsSL https://get.keelbox.dev | sh -s -- up
#
# What it does, without sudo: downloads the keelbox binary for this OS
# (Linux, macOS) and CPU (amd64, arm64), checks its signature and checksums
# (below), copies it to ~/.local/bin, writes shell completion for bash, zsh
# and fish where they are installed, and offers to put ~/.local/bin on your
# PATH. Where Claude Code or Codex is set up, it asks to add keelbox's skill
# for them (keelbox agent-skill install; keelbox agent-skill remove takes it
# out). Nothing else on the machine changes. Rerunning it updates keelbox.
#
#   sh install.sh [--version vX.Y.Z] [--prefix DIR | --dir DIR]
#                 [--no-modify-path] [up [keelbox up flags]]
#   --prefix DIR      binary in DIR/bin, completion under DIR/share
#   --dir DIR         binary in DIR, completion where your shells look
#   --no-modify-path  print the PATH line instead of offering to add it
#   up ...            run keelbox up when installed, on this terminal
#
# Remove keelbox: keelbox agent-skill remove, if you added the skill, then
# delete the binary and the completion files it lists at the end. Your
# boxes stay in ~/.config/keelbox.
#
# Checks before installing anything (https://keelbox.dev/docs/reference/release#installing-and-updating-keelbox):
#   - SHA256SUMS is signed by a keelbox release key carried below, checked
#     with OpenSSL 3 or, where openssl lacks Ed25519 (macOS), ssh-keygen;
#     with neither the install stops;
#   - the binary matches SHA256SUMS, and SHA256SUMS agrees with the checksum
#     the channel index (get.keelbox.dev) lists;
#   - downloads come only from keelbox's release hosts on GitHub;
#   - with cosign installed, the Sigstore signature is checked as well.
# This script is in each release's signed SHA256SUMS as install.sh; to
# check it before running it, see https://keelbox.dev/docs/guides/installing.
#
# Environment: KEELBOX_VERSION and KEELBOX_INSTALL_DIR are defaults for
# --version and --dir. KEELBOX_INDEX_URL replaces the channel index with
# another HTTPS one (a mirror); the release host must still be keelbox's.
# --test is for keelbox's own installer tests (scripts/test-install.sh) and
# alone honours KEELBOX_RELEASE_URL (another release host),
# KEELBOX_TEST_RELEASE_KEYS (keys to trust instead of keelbox's) and a plain
# HTTP KEELBOX_INDEX_URL; without it, setting them is an error.
set -eu

release_hosts='https://github.com/keelbox/releases/releases/download
https://github.com/keelbox/keelbox/releases/download'
signer=https://github.com/keelbox/keelbox/.github/workflows/release.yml@refs/heads/main
issuer=https://token.actions.githubusercontent.com

# keelbox's release keys, the same as internal/release/keys. A signature by
# any one is accepted, so a release signed by the current key can add the
# next one before the old key is retired.
release_keys_pem='-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEA3PhBOMM7kClF7joRVvEFZNVmTPqa03lhgdgKDG/oTxg=
-----END PUBLIC KEY-----'
release_keys_ssh='ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINz4QTjDO5ApRe46EVbxBWTVZkz6mtN5YYHYCgxv6E8Y'

usage='usage: install.sh [--version vX.Y.Z] [--prefix DIR | --dir DIR] [--no-modify-path] [up [flags]]'

say() { printf 'keelbox: %s\n' "$*"; }
die() { printf 'keelbox: %s\n' "$*" >&2; exit 1; }

fetch() { # url dest
  # HTTPS URLs may only redirect to HTTPS. Plain HTTP is for a test server
  # given explicitly with --test.
  case $1 in
    https://*) proto='=https' ;;
    *) proto='=http' ;;
  esac
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --proto "$proto" --retry 3 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget_fetch "$1" "$2"
  else
    die "need curl or wget"
  fi
}

wget_fetch() { # url dest
  # wget's --https-only binds only recursive downloads, not redirects, so
  # redirects are followed here, one at a time, each checked like curl's
  # --proto: from HTTPS only to HTTPS.
  wget --help 2>&1 | grep -q -- --max-redirect ||
    die "this wget (BusyBox?) cannot be limited to HTTPS redirects; install curl or GNU wget"
  url=$1 hops=0
  while :; do
    case $url in
      https://*) tls=--https-only ;;
      *) tls= ;;
    esac
    # shellcheck disable=SC2086 # $tls is one flag or none
    if hdrs=$(wget -q -S $tls --max-redirect=0 -O "$2" "$url" 2>&1); then
      return 0
    fi
    next=$(printf '%s\n' "$hdrs" | sed -n 's/^ *[Ll]ocation: *//p' | tail -n 1 | tr -d '\r')
    [ -n "$next" ] || die "download failed: $url"
    case $next in
      /*) next=$(printf '%s\n' "$url" | sed 's|^\([a-z]*://[^/]*\).*|\1|')$next ;;
    esac
    case $url in
      https://*)
        case $next in
          https://*) ;;
          *) die "refusing redirect from $url to $next: not HTTPS" ;;
        esac
        ;;
    esac
    hops=$((hops + 1))
    [ "$hops" -le 10 ] || die "too many redirects from $1"
    url=$next
  done
}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | cut -d' ' -f1
  else
    die "need sha256sum or shasum to verify the download"
  fi
}

# verify_signature SHA256SUMS: refuse unless a trusted release key signed it
# for this version. The signature covers a "keelbox-release VERSION" line,
# then the file, so one version's SHA256SUMS cannot pass as another's.
verify_signature() {
  { printf 'keelbox-release %s\n' "$version"; cat "$1"; } >"$tmp/signed"
  if command -v openssl >/dev/null 2>&1 &&
    openssl version 2>/dev/null | grep -Eq '^OpenSSL ([3-9]|[1-9][0-9])\.'; then
    fetch "$base/SHA256SUMS.sig" "$tmp/SHA256SUMS.sig"
    printf '%s\n' "$keys_pem" | awk -v d="$tmp" '/BEGIN PUBLIC KEY/ { n++ } n { print > (d "/release-key-" n ".pem") }'
    for key in "$tmp"/release-key-*.pem; do
      if openssl pkeyutl -verify -pubin -inkey "$key" -rawin -in "$tmp/signed" -sigfile "$tmp/SHA256SUMS.sig" >/dev/null 2>&1; then
        say "release key signature verified (openssl)"
        return 0
      fi
    done
    die "SHA256SUMS for $version is not signed by a keelbox release key; refusing to install"
  fi
  if command -v ssh-keygen >/dev/null 2>&1; then
    fetch "$base/SHA256SUMS.sshsig" "$tmp/SHA256SUMS.sshsig"
    printf '%s\n' "$keys_ssh" | sed 's/^/keelbox-release namespaces="keelbox-release" /' >"$tmp/allowed_signers"
    if ssh-keygen -Y verify -f "$tmp/allowed_signers" -I keelbox-release -n keelbox-release \
      -s "$tmp/SHA256SUMS.sshsig" <"$tmp/signed" >/dev/null 2>&1; then
      say "release key signature verified (ssh-keygen)"
      return 0
    fi
    die "SHA256SUMS for $version is not signed by a keelbox release key (checked with ssh-keygen, which needs OpenSSH 8.1 or later); refusing to install"
  fi
  die "cannot check the release signature: install OpenSSL 3 or OpenSSH 8.1+ (ssh-keygen) and rerun"
}

platform() {
  case $(uname -s) in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) die "unsupported OS $(uname -s); keelbox runs on Linux and macOS" ;;
  esac
  case $(uname -m) in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) die "unsupported architecture $(uname -m)" ;;
  esac
  # A shell under Rosetta reports x86_64 on Apple silicon; install native.
  if [ "$os" = darwin ] && [ "$arch" = amd64 ] &&
    [ "$(sysctl -n sysctl.proc_translated 2>/dev/null)" = 1 ]; then
    arch=arm64
  fi
  echo "$os-$arch"
}

main() {
  version=${KEELBOX_VERSION:-}
  dir=${KEELBOX_INSTALL_DIR:-}
  prefix='' test_mode=no modify_path=ask run_up=no skill_added=no
  while [ $# -gt 0 ]; do
    case $1 in
      --version) [ $# -ge 2 ] || die "--version needs a value"; version=$2; shift 2 ;;
      --dir) [ $# -ge 2 ] || die "--dir needs a value"; dir=$2; shift 2 ;;
      --prefix) [ $# -ge 2 ] || die "--prefix needs a value"; prefix=${2%/}; shift 2 ;;
      --no-modify-path) modify_path=no; shift ;;
      --test) test_mode=yes; shift ;;
      up) run_up=yes; shift; break ;; # the rest is for keelbox up
      -h | --help) echo "$usage"; exit 0 ;;
      *) die "unknown option $1; $usage" ;;
    esac
  done
  [ -z "$prefix" ] || [ -z "$dir" ] || die "--prefix and --dir both name where keelbox goes; give one"
  if [ -n "$prefix" ]; then
    dir=$prefix/bin
  elif [ -z "$dir" ]; then
    dir=$HOME/.local/bin
  fi

  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT INT TERM
  target=$dir/keelbox
  install_binary
  install_completion
  check_path
  offer_agent_skill

  echo
  if [ "$run_up" = yes ]; then
    say "starting keelbox up"
    # Piped from curl, stdin is this script: keelbox up asks on the terminal.
    # exec replaces this shell, so its EXIT trap would not run.
    rm -rf "$tmp"
    trap - EXIT INT TERM
    if (: </dev/tty) 2>/dev/null; then
      exec "$target" up "$@" </dev/tty
    fi
    exec "$target" up "$@"
  fi
  printf 'Next:\n  keelbox up    # set up a box: a server you reach over SSH, this machine, or a new one\n'
  remove="rm $target$completion_files"
  [ "$skill_added" = yes ] && remove="keelbox agent-skill remove; $remove"
  printf 'Update: keelbox self-update. Remove: %s\n' "$remove"
}

# install_binary downloads, checks and installs keelbox as $target, unless
# that version is there already.
install_binary() {
  index_url=${KEELBOX_INDEX_URL:-https://get.keelbox.dev/stable.json}
  if [ "$test_mode" = no ]; then
    for v in KEELBOX_RELEASE_URL KEELBOX_TEST_RELEASE_KEYS; do
      eval "val=\${$v:-}"
      # shellcheck disable=SC2154 # val is set by eval
      [ -z "$val" ] || die "$v is honoured only with --test (keelbox's installer tests); unset it"
    done
    case $index_url in
      https://*) ;;
      *) die "KEELBOX_INDEX_URL must be an https:// URL" ;;
    esac
  else
    say "warning: --test: honouring KEELBOX_RELEASE_URL, KEELBOX_TEST_RELEASE_KEYS and HTTP URLs"
  fi

  keys_pem=$release_keys_pem keys_ssh=$release_keys_ssh
  if [ -n "${KEELBOX_TEST_RELEASE_KEYS:-}" ]; then
    keys_pem=$(cat "$KEELBOX_TEST_RELEASE_KEYS/keys.pem")
    keys_ssh=$(cat "$KEELBOX_TEST_RELEASE_KEYS/keys.ssh")
    say "warning: trusting test release keys from $KEELBOX_TEST_RELEASE_KEYS, not keelbox's"
  fi

  fetch "$index_url" "$tmp/index.json"
  field() { sed -n "s/^ *\"$1\": *\"\([^\"]*\)\".*/\1/p" "$tmp/index.json" | head -n 1; }
  if [ -n "${KEELBOX_RELEASE_URL:-}" ]; then
    releases=${KEELBOX_RELEASE_URL%/}
    say "downloading from $releases (KEELBOX_RELEASE_URL)"
  else
    releases=$(field releases)
    releases=${releases%/}
    [ -n "$releases" ] || die "no releases URL in $index_url"
    known=no
    for h in $release_hosts; do
      [ "$releases" = "$h" ] && known=yes
    done
    [ "$known" = yes ] || die "$index_url names release host $releases, which is not one keelbox downloads from; refusing"
  fi
  if [ -z "$version" ]; then
    version=$(field version)
    [ -n "$version" ] || die "no version in $index_url"
  fi
  case $version in
    v*) ;;
    *) version=v$version ;;
  esac
  printf '%s\n' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$' ||
    die "invalid version $version"

  plat=$(platform)
  if [ -x "$target" ] && "$target" version 2>/dev/null | grep -q "^keelbox $version "; then
    say "keelbox $version is already installed at $target"
    return 0
  fi

  say "downloading keelbox $version for $plat"
  base=$releases/$version
  fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS"
  verify_signature "$tmp/SHA256SUMS"
  # The Sigstore signature is a second, optional check for a machine that
  # has cosign. Without cosign there is nothing to report.
  if ! command -v cosign >/dev/null 2>&1; then
    :
  elif ! cosign version >/dev/null 2>&1; then
    # A version manager's shim with no version selected, for one.
    say "warning: cosign is on PATH but does not run: the Sigstore signature was not checked (the release key signature was)"
  else
    fetch "$base/SHA256SUMS.sigstore.json" "$tmp/SHA256SUMS.sigstore.json"
    cosign verify-blob --bundle "$tmp/SHA256SUMS.sigstore.json" \
      --certificate-identity "$signer" --certificate-oidc-issuer "$issuer" \
      "$tmp/SHA256SUMS" >/dev/null 2>&1 ||
      die "SHA256SUMS for $version is not signed by keelbox's release workflow (cosign)"
    say "Sigstore signature verified (cosign)"
  fi
  want=$(awk -v f="keelbox-$plat" '$2 == f { print $1 }' "$tmp/SHA256SUMS")
  [ -n "$want" ] || die "release $version has no keelbox-$plat binary"
  key=$(printf '%s/%s' "$version" "$plat" | sed 's/\./\\./g')
  listed=$(sed -n "s|^ *\"$key\": *\"\([0-9a-f]*\)\".*|\1|p" "$tmp/index.json" | head -n 1)
  [ -n "$listed" ] || die "$index_url lists no checksum for keelbox $version $plat; refusing to install"
  [ "$listed" = "$want" ] || die "SHA256SUMS and $index_url disagree on keelbox-$plat; refusing to install"
  fetch "$base/keelbox-$plat" "$tmp/keelbox"
  got=$(sha256 "$tmp/keelbox")
  [ "$got" = "$want" ] || die "checksum mismatch for keelbox-$plat: got $got, want $want"
  say "checksum verified against SHA256SUMS and $index_url"

  chmod 0755 "$tmp/keelbox"
  mkdir -p "$dir"
  # Copy beside the target, then rename: a running keelbox is never half-written.
  cp "$tmp/keelbox" "$dir/.keelbox.new"
  mv -f "$dir/.keelbox.new" "$target"
  say "installed $("$target" version) to $target"
}

# install_completion writes completion for each shell found here, from the
# binary itself, so it always matches its commands and flags.
install_completion() {
  completion_files='' zsh_hint=''
  if [ -n "$prefix" ]; then
    share=$prefix/share
    bash_dir=$share/bash-completion/completions
    zsh_dir=$share/zsh/site-functions
    fish_dir=$share/fish/vendor_completions.d
  else
    share=${XDG_DATA_HOME:-$HOME/.local/share}
    bash_dir=$share/bash-completion/completions
    zsh_dir=$share/zsh/site-functions
    fish_dir=${XDG_CONFIG_HOME:-$HOME/.config}/fish/completions
    zsh_hint=yes # zsh does not look there unless told
  fi
  for sh in bash zsh fish; do
    command -v "$sh" >/dev/null 2>&1 || continue
    case $sh in
      bash) f=$bash_dir/keelbox ;;
      zsh) f=$zsh_dir/_keelbox ;;
      fish) f=$fish_dir/keelbox.fish ;;
    esac
    if mkdir -p "$(dirname "$f")" 2>/dev/null && "$target" completion "$sh" >"$f.new" 2>/dev/null && mv -f "$f.new" "$f"; then
      completion_files="$completion_files $f"
    else
      rm -f "$f.new" 2>/dev/null || true
      say "warning: could not write $sh completion to $f (keelbox completion $sh prints it)"
    fi
  done
  [ -z "$completion_files" ] || say "shell completion:$completion_files"
  case $completion_files in
    *_keelbox*)
      if [ -n "$zsh_hint" ] && [ "$(basename "${SHELL:-}")" = zsh ]; then
        # shellcheck disable=SC2016 # for the user's ~/.zshrc
        printf 'For zsh completion, add before compinit in ~/.zshrc:\n  fpath=(%s $fpath)\n' "$(home_path "$zsh_dir")"
      fi
      ;;
  esac
}

# home_path shows a path under $HOME as $HOME/..., for a line in a shell rc.
# shellcheck disable=SC2016 # a literal $HOME, for the rc file
home_path() {
  case $1 in
    "$HOME"/*) printf '$HOME%s' "${1#"$HOME"}" ;;
    *) printf '%s' "$1" ;;
  esac
}

# check_path makes sure the shell finds keelbox: it says which line puts
# $dir on PATH, in the rc file of the user's login shell, and offers to
# add it there.
check_path() {
  case :$PATH: in
    *:"$dir":*) return 0 ;;
  esac
  d=$(home_path "$dir")
  case $(basename "${SHELL:-sh}") in
    zsh) rc=${ZDOTDIR:-$HOME}/.zshrc line="export PATH=\"$d:\$PATH\"" ;;
    bash)
      rc=$HOME/.bashrc
      [ "$(uname -s)" = Darwin ] && rc=$HOME/.bash_profile # Terminal opens login shells
      line="export PATH=\"$d:\$PATH\""
      ;;
    fish) rc=${XDG_CONFIG_HOME:-$HOME/.config}/fish/config.fish line="fish_add_path $d" ;;
    *) rc=$HOME/.profile line="export PATH=\"$d:\$PATH\"" ;;
  esac
  echo
  if [ -f "$rc" ] && grep -qF "$line" "$rc"; then
    say "$rc puts $dir on PATH; open a new terminal, or run: $line"
    return 0
  fi
  say "$dir is not on your PATH. Add this line to $rc:"
  printf '  %s\n' "$line"
  [ "$modify_path" = ask ] && (: </dev/tty) 2>/dev/null || return 0
  printf 'Add it now? [Y/n] ' >/dev/tty
  read -r answer </dev/tty || answer=n
  case $answer in
    '' | [Yy] | [Yy][Ee][Ss]) ;;
    *) return 0 ;;
  esac
  mkdir -p "$(dirname "$rc")"
  printf '\n# keelbox (https://get.keelbox.dev)\n%s\n' "$line" >>"$rc"
  say "added to $rc; open a new terminal, or run: $line"
}

# offer_agent_skill asks to add keelbox's skill for Claude Code and Codex
# on this computer, where either is set up; without a terminal, or with
# --no-modify-path, it names the command instead.
offer_agent_skill() {
  [ -d "$HOME/.claude" ] || [ -d "$HOME/.codex" ] || return 0
  echo
  if [ "$modify_path" = ask ] && (: </dev/tty) 2>/dev/null; then
    printf "Add keelbox's skill, so Claude Code and Codex on this computer can use keelbox? [Y/n] " >/dev/tty
    read -r answer </dev/tty || answer=n
    case $answer in
      '' | [Yy] | [Yy][Ee][Ss])
        if "$target" agent-skill install; then skill_added=yes; else say "later: keelbox agent-skill install"; fi ;;
      *) say "later: keelbox agent-skill install" ;;
    esac
    return 0
  fi
  say "For Claude Code and Codex on this computer to use keelbox: keelbox agent-skill install"
}

main "$@"
