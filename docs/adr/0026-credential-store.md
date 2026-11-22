# 0026: The Hetzner token lives in the OS credential store

Date: 2026-11-06. Status: accepted.

## Context
Creating a Hetzner box means creating a project API token in the console. Saving it by hand with `(umask 077; cat > ~/.config/keelbox/hetzner.token)` is error-prone, and a plaintext file is the weakest place a laptop can keep a secret that controls every server in a project. Hetzner offers no delegated sign-in that would avoid the token (ADR 0027).

Facts the design relies on, read 2026-11-06:
- macOS `security(1)` (Apple's `SecurityTool` man page, 2024-09-24): `add-generic-password -w` "Specify password to be added. Put at end of command to be prompted (recommended)"; that prompt is `getpass(3)`, which reads `/dev/tty`, so it cannot be fed by a program. `security -i` reads commands from stdin "until an EOF is encountered", splitting each line with quote handling. `-U` updates an existing item; `find-generic-password -w` prints only the password; a missing item exits 44.
- libsecret `secret-tool(1)`: "A password to store can also be piped in via stdin. The password will be the contents of stdin until EOF", so no trailing newline is sent. `lookup` and `clear` exit 1 when nothing matched. Over SSH without a D-Bus session bus, every call fails.

## Decision
- `internal/credstore`: a `Store` with `Get`, `Set`, `Delete`, implemented for the macOS Keychain (`security`), the Secret Service (`secret-tool`, used only when a session bus is reachable) and a fallback directory `~/.config/keelbox/credentials/` (mode 700, one mode 600 file per entry, written by rename). A chain reads the OS store then the file, writes the OS store and falls back to the file only when the OS store is unavailable, printing a warning that says where the token is and who can read it.
- The secret never appears in a command's arguments or environment. Keychain writes send `add-generic-password -U -s keelbox -a <name> -l "keelbox <name>" -w "<token>"` to `security -i` on stdin, refusing tokens with characters that would need escaping (Hetzner tokens are letters and digits), then read the entry back, since `security -i` reports errors on stdout and exits 0. Secret Service writes pipe the token to `secret-tool store` on stdin. Reads take stdout. A test fake fails if the secret reaches an argument list.
- Entries are named by `target.hetzner.credential` (default `hetzner`), service `keelbox`. The token is looked up in this order: `HCLOUD_TOKEN`, `target.hetzner.token_file`, the credential store, then `~/.config/keelbox/hetzner.token`. With none and a terminal, the command walks the user through creating one, reads it with echo off, checks it against the API (a 401 is a refused token; a write probe that sends an empty SSH key separates a read-only token, 403, from a usable one, 422, and creates nothing) and stores it. Without a terminal, it fails and names `keelbox up` and `token_file`.
- `keelbox forget-token [box]` deletes the entry from both stores and says that revoking the token is done in the Hetzner Console.
- `KEELBOX_CREDENTIAL_STORE=file` uses only the file store, for CI and the end-to-end tests.

## Consequences
- Other programs running as the user can usually read the Keychain item or Secret Service entry too (the Keychain asks only for apps outside the item's access list). The store protects the token at rest and from accidental exposure (backups of dotfiles, a mistaken `cat`), not from malware running as the user. `docs/security-model.md` says so.
- Linux servers and containers used as laptops usually have no Secret Service; the file fallback keeps the token in a mode 600 file, with a warning.
- Windows is not supported by the laptop CLI, so no Credential Manager backend.
