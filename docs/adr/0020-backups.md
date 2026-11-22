# 0020: Backups with restic to S3-compatible storage, read and restored as each path's owner

Date: 2026-11-06. Status: accepted.

## Context
An agent box holds work that is not yet pushed anywhere, plus the agent's sign-ins and settings. Users need scheduled, encrypted, off-box backups to storage they already pay for (Backblaze B2, Cloudflare R2, AWS S3, a MinIO), a way to run one now, and a restore that cannot destroy what is on the box by accident. Two constraints from earlier decisions: secrets travel by file path only (ADR 0005), and root never reads or writes a path another account can redirect (ADR 0015).

## Decision

### restic, pinned
restic 0.19.1, the official release binary, checked against the SHA-256 in the release's `SHA256SUMS`, whose GPG signature was verified against restic's release key `CF8F18F2844575973F79D4E191A6868BD3F7A907` when the pin was set. Installed root-owned at `/usr/local/bin/restic`. Ubuntu 24.04's package (0.16) predates `--stdin-from-command` (0.17), which this design needs, and a manifest pin is the same on both supported releases.

### S3 only, credentials by file
- `backup.repository` must be `s3:https://host[:port]/bucket[/prefix]` (or `s3:http://` for a store on a trusted network), with no user info in the URL. B2 and R2 are used through their S3 endpoints, so one credential format covers every provider.
- `backup.credentials_file` is an AWS shared credentials file; `backup.password_file` the repository password. Both mode 600, refused otherwise. From a laptop they are staged on the box's tmpfs with each command, like the Tailscale auth key, and removed after; apply keeps root-only copies in `/etc/keelbox/backup/` (directory 0700, files 0600). With provider `local` the source must be in a directory only root can change, since root reads it.
- restic gets only paths in its environment: `RESTIC_PASSWORD_FILE`, `AWS_SHARED_CREDENTIALS_FILE`, `RESTIC_REPOSITORY` (no secret in it). Variables that would override the files (`AWS_ACCESS_KEY_ID`, `RESTIC_PASSWORD` and the like) are set empty. Nothing secret is in an argument, the logbook, keelbox's output or the plan.

### Reading as the owner
restic runs as root because only root can read the credentials. Letting it walk the agent's home would break ADR 0015: a directory the agent swaps for a symlink mid-walk would get root-only files into a backup the agent can later restore. So restic never reads users' files:

- Each backup path is archived by `tar --create` running as the account that owns it (`runuser -u <account> -- env -i ... tar`), and restic stores the stream with `restic backup --stdin-from-command`. restic records a snapshot only if tar succeeds (exit 1, "files changed while being read", is accepted). A path in the agent's or admin's home belongs to that account; any other path is read by root's tar.
- One snapshot per path, tagged `keelbox` and `keelbox-path=<path>`, host `<box>`, holding one file, `/<label>.tar` (`/home-agent.tar`). restic's content-defined chunking deduplicates the stream between runs.
- Restore is the reverse: `restic dump` as root, piped into `tar --extract` as the path's owner. A crafted archive (the agent controls what its tar wrote) can write only where the agent can.
- The cost: every run reads the whole tree, and `restic ls` shows one archive rather than files. Selective restore is tar's (`tar -x` with member names) on a restored archive.

### What is backed up
`backup.paths`, default the agent's home, which holds its projects, sign-ins and settings. `backup.exclude` (default `node_modules`, caches, the package stores mise and pnpm rebuild, rootless container storage, which the agent itself cannot fully read, and `keelbox-restore`) replaces the defaults when set; `CACHEDIR.TAG` directories are always skipped.

### Schedule and retention
`keelbox-backup.timer` (`backup.schedule`, default `daily`, `Persistent=true`, up to 15 minutes' random delay) starts `keelbox-backup.service`, which runs `keelbox backup` against the applied config. Until a run has seen the repository, it first runs `restic init`, which creates the bucket if needed and fails at once with "config file already exists" on an existing repository (probing with `restic cat config` instead retries for about 15 minutes when the bucket is missing); then it backs up, then `restic forget --group-by host,tags --keep-daily/weekly/monthly ... --prune` (default 7, 4, 6), per path. Apply never contacts the repository, so plan and doctor work offline.

### Commands
`keelbox backup` (run now), `keelbox backup --list` (this box's snapshots), `keelbox restore [snapshot] [--target PATH | --overwrite]`. Restore defaults to a new directory: `<home>/keelbox-restore/<snapshot>` for a path in a home, `/var/lib/keelbox/restore/<snapshot>` otherwise. `--target` must be new or empty. Only `--overwrite` restores over the original path, replacing files the snapshot has and keeping the rest. All run as root on the box, from a laptop through the usual SSH path.

### Doctor
Fails when the timer is not active, a credential copy is missing or not `0600 root`, or the last successful backup is older than `backup.stale_after` (default 48h). Warns before the first success and when the latest attempt failed while an earlier success is still fresh. Outcomes are kept in `/var/lib/keelbox/backup/status.json` (root only).

## Consequences
- A compromised bucket (or its credentials alone) can delete, withhold or roll back snapshots, and learn sizes and timing; restic's encryption and authentication stop it reading or altering contents without the password. Object lock or bucket versioning, set at the provider, is the defence against deletion.
- Root on the box holds the credentials and the password, so a root compromise can read and delete every backup.
- The agent cannot read the credentials, but decides what its own tar emits: it can fill the bucket, or make its backups useless. It cannot get another account's files into them.
- Losing the password file loses the backups. The guide says to keep a copy off the box.
