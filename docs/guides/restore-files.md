# Restore files

Restore files from a machine's backups with restic, on any Linux machine, with or without Tender.

Tender has no restore command yet. You restore with restic directly, because Tender's backups are plain restic repositories.

## Prerequisites

1. [restic](https://restic.net). Every machine with a Tender daemon has restic 0.19.1 at `/usr/local/bin/restic`.
2. The destination's repository password and its keys or login, from your password manager.
3. The destination's location. `tender destinations list` shows it in `LOCATION`.

## Restore a folder

1. Point restic at the machine's repository. It is the destination's location followed by `/` and the machine's name. For an S3 destination:

   ```sh
   export AWS_ACCESS_KEY_ID=<access key ID>
   export AWS_SECRET_ACCESS_KEY=<secret access key>
   export RESTIC_PASSWORD=<repository password>
   export RESTIC_REPOSITORY=s3:https://<account-id>.r2.cloudflarestorage.com/my-backups/tender/apps
   ```

   For the other types, set `RESTIC_REPOSITORY` the same way and give the login as the type needs:

   | Type | `RESTIC_REPOSITORY` | Login |
   | --- | --- | --- |
   | `local` | `local:/mnt/backup/tender/apps` | None. |
   | `sftp` | `sftp://tender@nas.example.com:22//srv/restic/apps` | Add `-o sftp.args="-i /etc/tender/ssh/id_ed25519"` to each command, or use your own key. |
   | `rest` | `rest:https://backup.example.com:8000/tender/apps` | `RESTIC_REST_USERNAME` and `RESTIC_REST_PASSWORD`. |
   | `rclone` | `rclone:gdrive:backups/apps` | `RCLONE_CONFIG=/etc/tender/rclone.conf`, or your own rclone config with the same remote. |

   On a machine whose daemon still runs, root can use the daemon's copy of the password instead: `export RESTIC_PASSWORD_FILE=/var/lib/tender/daemon/repository-password`.

2. List the snapshots:

   ```sh
   restic snapshots --host apps --tag tender --compact
   ```

   ```text
   ID        Time                 Host  Tags    Size
   --------------------------------------------------------
   4f2c9a1e  2026-11-15 03:30:12  apps  tender  12.418 GiB
   --------------------------------------------------------
   Timestamps shown in local time
   1 snapshots
   ```

3. Restore the folder you need from the newest snapshot into an empty folder:

   ```sh
   restic restore latest --host apps --tag tender --target /root/restore --include /etc/nginx
   ```

   ```text
   restoring snapshot 4f2c9a1e of [/] at 2026-11-15 03:30:12.418093312 +0000 UTC by tender@apps to /root/restore
   Summary: Restored 46 files/dirs (88.120 KiB) in 0:00
   ```

4. Compare the restored files with the live ones, then copy back what you need:

   ```sh
   sudo diff -r /root/restore/etc/nginx /etc/nginx
   ```

   `diff` prints nothing when the folders match, and each changed line when they don't.

> [!WARNING]
> Restore into an empty folder, like `/root/restore`, and copy back what you need. Restoring over a running system can break it.

To restore an older snapshot, put its ID from step 2 in place of `latest`.

## Next steps

1. [Backups](../backups.md) explains what each snapshot covers.
2. [Security](../security.md#how-tender-stores-secrets) explains where the keys and password live.
