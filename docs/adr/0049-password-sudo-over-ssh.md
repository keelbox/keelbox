# 0049: Password sudo over SSH, and no bootstrap sudoers left behind

Date: 2026-11-07. Status: accepted. Extends ADRs 0015 and 0038.

## Context
On most home servers the owner's account has sudo that asks for a password. keelbox's SSH route ran every remote command under `sudo -n`, which such an account refuses. A passwordless rule added only so keelbox can set the machine up (`/etc/sudoers.d/90-keelbox-bootstrap` with `NOPASSWD:ALL`), to remove by hand afterwards, tends to stay: after setup the owner's account still has passwordless sudo, a second way to root beside the admin account, on a machine the owner believes has kept its password.

## Decision
- **sudo asks on the terminal.** When the account keelbox first logs in as has password sudo, and `keelbox up` runs on a terminal, the runs that need root as that account go over `ssh -t` with `exec sudo env SSH_CONNECTION=... sh -c SCRIPT` as the remote command. sudo reads the password from the pty itself, with its own prompt; keelbox hands ssh the terminal and reads nothing that passes through. The password is never on argv, in the environment, in a file, in the logbook or in keelbox's output; keelbox never sees or keeps it.
- **It says so first.** After the read-only check, `up` says sudo will ask for the account's password, when, and that keelbox never sees it; the summary reads `sudo with sam's password`. The impact report does not flag SSH password login for an account that logs in with a key now (its authorized keys files hold one, or keelbox itself logged in as it with one).
- **Twice, once per root run.** sudo's timestamp is per terminal session, and every ssh is a session of its own, so sudo asks once for the read-only look (`plan --impact`, which needs root to see listeners and accounts) and once for the first apply. After that keelbox uses its own admin account, with key login and passwordless sudo of its own (ADR 0015).
- **Output of a look.** A look's output goes to a mode-600 file in the box's staging directory and is read back over a second plain SSH call, since a pty mixes stdout and stderr. An apply streams straight to the terminal; the box-side progress view is not used for it.
- **Without a terminal.** `keelbox up` exits 3 (needs you) and says to run it on one. An account with no sudo at all exits 2 with the `usermod -aG sudo` fix. keelbox never advises a `NOPASSWD` rule.
- **Bootstrap files go.** A host step, `bootstrap-sudoers`, removes `/etc/sudoers.d/90-keelbox-bootstrap`. Run over SSH as the account the file may serve, it defers to the admin's apply that follows, since that session still uses it; on the machine itself, or as admin or root, it removes the file at once. Apply says what it removed and that sudo asks for the password again. Doctor fails (`bootstrap-sudo`) while such a file is there.

## Consequences
- Two password prompts in one `keelbox up`. Fewer would need keelbox to hold the password, or a temporary passwordless rule, and both are what this decision removes.
- Ctrl-C during a sudo-run apply ends it as an SSH hang-up does; the next apply converges from where it stopped.
- Only that file name is known. A passwordless rule the owner wrote under another name is theirs and stays.
