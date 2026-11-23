# Back up and restore

```sh
keelbox backup acme           # back up now
keelbox backup acme --list    # list snapshots
keelbox restore acme          # restore the latest snapshot into a new directory
```

keelbox backs the box up with restic, daily unless you set a schedule, to S3-compatible storage you choose, such as Backblaze B2, Cloudflare R2, AWS S3 or MinIO. Snapshots are encrypted with a password that you and the box hold. Backups are off until the config has `backup.repository`.

## Set it up

1. At your storage provider, make a bucket and a key that can list, read, write and delete its objects.
2. On your computer, save the key and a new password in two mode 600 files:

   ```sh
   umask 077
   cat > ~/.config/keelbox/backup-s3.credentials <<'EOF'
   [default]
   aws_access_key_id = <key id>
   aws_secret_access_key = <secret>
   EOF
   openssl rand -base64 32 > ~/.config/keelbox/backup.password
   ```

3. Add this to the box's config. The repository is `s3:https://HOST/BUCKET/PREFIX`.

   ```yaml
   backup:
     repository: s3:https://s3.eu-central-003.backblazeb2.com/acme-backups/acme
     credentials_file: ~/.config/keelbox/backup-s3.credentials
     password_file: ~/.config/keelbox/backup.password
   ```

4. Run `keelbox apply acme`, then `keelbox backup acme` to make the first snapshot.

Without the password no snapshot can be read, and the box holds the only other copy, so keep one more somewhere you trust.

## What is backed up

The agent's home: its projects, settings and the agent CLIs' sign-ins. Package stores, caches and the agent CLIs' releases are left out, since apply reinstalls them, and so is the agent's Docker storage: container images and volumes are not backed up. The schedule, retention and paths are in the [config reference](../reference/config.md#backup).

## Restore

```sh
keelbox restore acme                             # the latest, into ~/keelbox-restore/SNAPSHOT
keelbox restore 4f3a9c1e acme                    # a given snapshot
keelbox restore acme --target /home/agent/old    # into a new or empty directory
keelbox restore acme --overwrite                 # over the original path
```

`--overwrite` replaces the files the snapshot has and leaves the others. To restore onto a new box, apply a config with the same `box`, `repository` and password first: the box name selects the snapshots.

## Health

`keelbox status acme` shows the last successful backup. `keelbox doctor acme` warns until the first backup succeeds or when the latest failed, and fails when the timer is not running, the box's copy of either file is missing or not root's with mode 600, or no backup has succeeded within `backup.stale_after` (48 hours by default). The log is `sudo journalctl -u keelbox-backup.service`, in `keelbox ssh acme --admin`.

## Who can read a backup

The bucket holds only encrypted data. Anyone with the bucket's key but not the password can delete snapshots but not read them.

Root on the box holds both the key and the password, so root can read and delete backups. The agent cannot read either file. The agent's home is read as the agent, so a backup holds only files the agent could read.
