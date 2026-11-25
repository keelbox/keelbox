# 0097: Apply keeps the SSH keys its last apply put on the box

Date: 2026-11-25. Status: accepted.

## Context
`keelbox up` wrote `ssh_authorized_key_files: [~/.ssh/id_ed25519.pub]` for both accounts. The file is read on the computer that runs keelbox, and apply replaces keelbox's marked block of each account's `authorized_keys` with what it reads. Copy the config to a second computer, add that computer's key, and apply from it: the block holds only the second computer's key, and the first computer is locked out of the admin and the agent, without a word. A simulated box showed it.

## Decision
- **Apply stops before taking a key off.** Before any change, apply compares the keys for the admin and the agent with those in the config the last apply left in `/etc/keelbox/keelbox.yaml`. If a key would come off, it stops, exit 1, names each key by type, fingerprint and comment, prints the YAML that keeps them and ends with `keelbox apply BOX --remove-keys`. It runs ahead of the lock and of a detached run, as the container check does, so the refusal reaches the laptop at once.
- **The record is root's.** The comparison reads the last applied config, which only root can change, not the accounts' own `authorized_keys`. The agent can edit its own, so a key it adds there cannot make apply stop.
- **A key is its type and data.** The same key with another comment is the same key.
- **`up` writes the key itself.** The config `keelbox up` writes lists the public key line under `ssh_authorized_keys`, so the config means the same on every computer. `ssh_authorized_key_files` still works and is read where keelbox runs.
- **Taking a key off is said aloud.** `--remove-keys` takes off keys the config no longer lists, for a lost laptop or a key taken out on purpose. Without it, a key taken out of the config also stops apply, once.

## Consequences
- A second computer's apply can no longer lock the first one out. Taking a key off needs a flag.
- A box set up before this change has keys from files in its last config; the first apply from another computer stops and prints them as lines to keep, which is the fix.
- Not done: `keelbox export` and `import` of a box's config and host keys, and commands that add or remove a computer's key. The guide's steps are by hand for now.
