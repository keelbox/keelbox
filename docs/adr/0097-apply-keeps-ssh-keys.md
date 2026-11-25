# 0097: Apply keeps the SSH keys its last apply put on the box

Date: 2026-11-25. Status: accepted.

## Context
`keelbox up` wrote `ssh_authorized_key_files: [~/.ssh/id_ed25519.pub]` for both accounts. The file is read on the computer that runs keelbox, and apply replaces keelbox's marked block of each account's `authorized_keys` with what it reads. Copy the config to a second computer, add that computer's key, and apply from it: the block holds only the second computer's key, and the first computer is locked out of the admin and the agent, without a word. A simulated box showed it.

## Decision
- **Apply stops before taking a key off.** Before any change, apply compares the keys for the admin and the agent with root's record of the keys applies put there, `/etc/keelbox/ssh-keys.yaml`; on a box set up before the record, with the key lists of the config the last apply left in `/etc/keelbox/keelbox.yaml`, read on their own so a change elsewhere in the schema cannot make the check pass by default. If a key would come off, it stops, exit 1, names each key by type, fingerprint and comment, names `keelbox apply BOX --remove-keys` for a computer no longer used, and ends with the YAML that keeps them and `keelbox apply BOX`: the last line is the safe one to copy. It runs ahead of the lock and of a detached run, as the container check does, so the refusal reaches the laptop at once.
- **The record is written before the steps and after them.** Before any step runs, apply writes the union of the record and its own keys; once it has passed, its own keys alone. An apply that stops partway, after `authorized_keys` changed, leaves every key it may have put there in the record, so the next apply from another computer stops rather than take one off. With `--remove-keys` only this config's keys are recorded before the steps, since the user asked for the others to come off, so a step that fails later does not make every next apply stop. The check runs once before the lock, for a quick refusal, and again under it, as another apply may have recorded a key in between.
- **Rollback goes back to the last config applied in full, keys included.** It removes the record before it runs the older keelbox, which may not know `--remove-keys`; keys a failed apply added since are not kept for it.
- **The record is root's.** Only root can change it; the accounts' own `authorized_keys` is not read. The agent can edit its own, so a key it adds there cannot make apply stop.
- **A key is its type and data.** The same key with another comment is the same key.
- **`up` writes the key itself.** The config `keelbox up` writes lists the public key line under `ssh_authorized_keys`, so the config means the same on every computer. `ssh_authorized_key_files` still works and is read where keelbox runs.
- **Taking a key off is said aloud.** `--remove-keys` takes off keys the config no longer lists, for a lost laptop or a key taken out on purpose. Without it, a key taken out of the config also stops apply, every time until it is given.

## Consequences
- A second computer's apply can no longer lock the first one out. Taking a key off needs a flag.
- A box set up before this change has no record; its last applied config holds the keys read from the files of the computer that applied it, so the first apply from another computer stops and prints them as lines to keep.
- Not done: `keelbox export` and `import` of a box's config and host keys, and commands that add or remove a computer's key. The guide's steps are by hand for now.
