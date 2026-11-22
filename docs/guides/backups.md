# Back up and restore

```sh
keelbox backup acme           # back up now
keelbox backup acme --list    # list snapshots
keelbox restore acme          # the latest snapshot, into a new directory
```

keelbox backs the box up with restic to S3-compatible storage you choose: Backblaze B2, Cloudflare R2, AWS S3 or MinIO. Snapshots are encrypted with a password that you and the box hold. Backups are off until the config has `backup.repository`.

## Set it up

1. At your storage provider, make a bucket and a key for it. restic lists, reads, writes and deletes objects.
2. On your computer, write the key and a new password into two files, mode 600.

   ```sh
   umask 077
   cat > ~/.config/keelbox/backup-s3.credentials <<'EOF'
   [default]
   aws_access_key_id = <key id>
   aws_secret_access_key = <secret>
   EOF
   openssl rand -base64 32 > ~/.config/keelbox/backup.password
   ```

3. Add the block to the box's config. The repository is `s3:https://HOST/BUCKET/PREFIX`.

   ```yaml
   backup:
     repository: s3:https://s3.eu-central-003.backblazeb2.com/acme-backups/acme
     credentials_file: ~/.config/keelbox/backup-s3.credentials
     password_file: ~/.config/keelbox/backup.password
   ```

4. Run `keelbox apply acme`, then `keelbox backup acme`.

Without the password no snapshot can be read. The box holds the only other copy of it.

Apply installs restic, keeps copies of both files that only root can read, and starts a daily timer. It does not contact the bucket; the first backup does, and creates the repository.

The schedule, how many snapshots are kept, and which paths are read or left out are in the [config reference](../reference/config.md#backup).

## What is backed up

The home of the agent, the account the agent CLIs run as: its projects, its settings and the CLIs' sign-ins.

Left out are package stores, caches and the agent CLIs' own releases, which apply reinstalls. The agent's rootless Docker storage is left out too, so container images and volumes are not backed up.

## Restore

| Command | What it does |
|---|---|
| `keelbox restore acme` | The latest snapshot, into `~/keelbox-restore/SNAPSHOT` in the agent's home |
| `keelbox restore 4f3a9c1e acme` | That snapshot |
| `keelbox restore acme --target /home/agent/old` | Into that directory, which must be new or empty |
| `keelbox restore acme --overwrite` | Over the original path |

With `--overwrite`, files in the snapshot replace the ones on the box. Files the snapshot does not have stay.

On a new box, apply a config with the same `box`, `repository` and password, then restore. The box name selects the snapshots.

## Health

`keelbox doctor acme` fails when:

- the timer is not running
- root's copy of either file is missing, or not mode 600 and root's
- the last success is older than `backup.stale_after`, 48 hours unless set

Doctor warns until the first backup succeeds, and when the latest attempt failed.

`keelbox status acme` shows the last success. The log is `sudo journalctl -u keelbox-backup.service`, in `keelbox ssh acme --admin`.

## Who can read a backup

The bucket holds only encrypted data. Whoever has the bucket or its key, without the password, can delete or withhold snapshots and cannot read them.

Root on the box holds the key and the password, so root can read and delete backups. Both files are mode 600 and root's, and the agent cannot read them.

tar reads the agent's home as the agent, so a backup holds only files the agent could read.
