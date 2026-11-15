# Backups

Each daemon backs up its whole machine to your own storage on a schedule, keeps snapshots for as many days as you choose, and reads part of the data back every week to prove it restores.

This page explains what a backup covers and how the runs fit together. To restore, see [Restore files](guides/restore-files.md).

## Destinations, settings and runs

A destination is a place backups go, such as an S3 bucket, an SFTP server or a folder on a disk, with a repository password. Many machines can share one destination. Each gets its own folder in it, such as `my-backups/tender/apps`. [Destination types](#destination-types) lists the places.

A machine's backup settings name its destination, a cron schedule in UTC and a retention in days. The daemon gets them from the hub, with the keys. When you remove a machine's backup settings, its daemon stops backing up and deletes its saved copy of the settings and password. The backups at the destination stay there.

The daemon does three kinds of runs. Each run is recorded in the machine's history with the status `in_progress`, then `completed` or `failed`.

| Type | When | What it does |
| --- | --- | --- |
| `backup` | On the schedule. | Takes a snapshot of the machine's backup paths. |
| `cleanup` | After each completed backup. | Forgets this machine's snapshots older than the retention and frees their space. |
| `check` | After a completed backup, once a week. | Reads back a random 5% of the stored data and checks it. |

A plain `restic check` reads only the repository's structure, so it misses damaged data. The weekly check reads real data, a different random 5% each time.

The daemon runs at normal priority, so its reports keep time during a run. It starts restic at nice 19, with the `SCHED_IDLE` CPU policy and the idle IO class, so a run uses only what the machine's other work leaves. The `tenderd` service's system call filter allows `setpriority`, `sched_setscheduler` and `ioprio_set` for this. On a machine that doesn't let the daemon lower a priority, restic starts at normal priority.

## Destination types

Each destination has a type. The hub keeps its config and the type's secrets, encrypted, and sends them to each daemon with the settings. The SFTP key and the rclone remotes stay on each machine instead, so they never pass through Tender.

| Type | Where backups go | What each machine needs |
| --- | --- | --- |
| `s3` | Any S3-compatible bucket: AWS S3, Cloudflare R2, Backblaze B2, Wasabi, MinIO, or Google Cloud Storage through its S3 interoperability keys. | Nothing. |
| `local` | A folder on each machine, such as a mounted disk. | The folder, writable by the daemon. |
| `sftp` | A folder on an SSH server. | An SSH key and the server's host key in `/etc/tender/ssh`. |
| `rest` | restic's [rest-server](https://github.com/restic/rest-server). | Nothing. |
| `rclone` | Any storage [rclone](https://rclone.org) reaches, such as Google Drive or OneDrive. | rclone, and the remote in `/etc/tender/rclone.conf`. |

After you add a destination, [test it](#test-a-destination) from a machine before you point the machine's backups at it.

### S3

1. Make a bucket and an access key that can read and write it.
2. Add the destination. It asks for the two keys with typing hidden:

   ```sh
   tender destinations add r2 --endpoint https://<account-id>.r2.cloudflarestorage.com --bucket my-backups --path tender
   ```

For Google Cloud Storage, use the endpoint `https://storage.googleapis.com` and an HMAC key from the bucket's interoperability settings.

### Local

A `local` destination is a folder on each machine, such as a USB disk or a mounted NAS share. It protects against deleted files, not against losing the machine, so pair it with another destination for anything you can't lose.

1. On each machine, make the folder for root, which the daemon runs as:

   ```sh
   sudo install -d -m 0700 -o root -g root /mnt/backup/tender
   ```

   The daemon has no right to write another user's files, so root must own the folder.

2. The daemon may write only its own folders, so allow this one too, then restart it:

   ```sh
   sudo systemctl edit tenderd
   ```

   ```ini
   [Service]
   ReadWritePaths=/mnt/backup/tender
   ```

   ```sh
   sudo systemctl restart tenderd
   ```

3. Add the destination:

   ```sh
   tender destinations add usb --type local --path /mnt/backup/tender
   ```

### SFTP

An `sftp` destination is a folder on any SSH server. Each machine logs in with its own key, which never leaves the machine.

1. On each machine, make a key for the daemon and save the server's host key. Replace `nas.example.com` and the port with your server's:

   ```sh
   sudo install -d -m 0700 -o root -g root /etc/tender/ssh
   sudo ssh-keygen -t ed25519 -N '' -C "tender@$(hostname)" -f /etc/tender/ssh/id_ed25519
   ssh-keyscan -p 22 nas.example.com | sudo tee /etc/tender/ssh/known_hosts
   ```

   `ssh-keygen` saves the key for root alone, at mode 0600, which is how `ssh` wants it. The daemon runs as root, so it reads the key there. It refuses a key that others can read, as `ssh` does.

   Check the host key that `ssh-keyscan` printed against the server's own, with `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the server. The daemon refuses a server whose key isn't in `known_hosts`.

2. On the server, add each machine's public key, from `sudo cat /etc/tender/ssh/id_ed25519.pub`, to the backup user's `~/.ssh/authorized_keys`. The user needs to write the backup folder.
3. Add the destination. `--path` is absolute, or relative to the user's home:

   ```sh
   tender destinations add nas --type sftp --host nas.example.com --user tender --path /srv/restic
   ```

The daemon runs `ssh` with this key only, in batch mode, so a missing key or an unknown host key fails the run instead of waiting for an answer.

### REST server

A `rest` destination is restic's rest-server, which serves repositories over HTTP. Run it with logins and over HTTPS, or inside your tailnet.

1. Add the destination. With `--username`, it asks for the password with typing hidden, and the hub stores it encrypted:

   ```sh
   tender destinations add vault --type rest --url https://backup.example.com:8000/tender --username tender
   ```

With `--private-repos`, rest-server lets each user reach only the folder named after the user, so end the URL with `/<username>`, as above. Each machine's repository is a folder under the URL. A server started with `--append-only` refuses deletes, so every cleanup run there fails and old snapshots stay. Use it only if you clean up on the server yourself.

### rclone

An `rclone` destination reaches any storage that rclone does, through a remote you set up on each machine. The remote's login stays in that machine's rclone config and never passes through Tender.

1. On each machine, install rclone from [rclone.org](https://rclone.org/install/) into `/usr/local/bin`.
2. Make the remote in the daemon's rclone config. The daemon runs as root, so it reads the file as root made it:

   ```sh
   sudo rclone config --config /etc/tender/rclone.conf
   ```

3. Add the destination, with the remote's name and a folder in it:

   ```sh
   tender destinations add drive --type rclone --remote gdrive:backups
   ```

The daemon can't write `/etc/tender`, so rclone can't save a token it refreshes there. A remote with fixed keys, such as B2 or S3, works as it is. A remote that signs in through a browser, such as Google Drive, keeps working only as long as its saved refresh token does. Run `sudo rclone config reconnect gdrive: --config /etc/tender/rclone.conf` when the `backup.failed` notification says the token expired.

### Test a destination

Test a destination from each machine that will back up to it. The machine's daemon opens its repository there with the destination's settings, and makes the repository when there is none yet, as the first backup would.

1. Run the test:

   ```sh
   tender destinations test nas apps
   ```

   ```text
   Reached sftp://tender@nas.example.com:22//srv/restic/apps from apps. It had no repository for apps yet, so the test made one with the destination's password.
   ```

2. Point the machine's backups at the destination:

   ```sh
   tender backups set apps --destination nas --schedule "30 3 * * *" --retention-days 30
   ```

A failed test says why, such as `Could not reach the destination nas from apps: the sftp destination needs /etc/tender/ssh/id_ed25519 on this machine, and it isn't there.` Fix what it names and run it again.

## What a backup covers

The installer sets the backup paths to every local filesystem it finds on the machine: ext2, ext3, ext4, xfs, btrfs, vfat and f2fs. Other types, such as ZFS, network and memory filesystems, stay out unless you add their paths. Every backup leaves out:

```text
/var/lib/docker  /var/lib/containerd  /var/cache  /tmp  /var/tmp
/home/*/.cache  /home/*/.local/share/docker  **/node_modules
```

Folders with a `CACHEDIR.TAG` file are left out too.

Docker volumes live under `/var/lib/docker`, so backups leave them out. A database's files copied while it runs may not restore cleanly anyway. Dump each application database with its own tool, such as `pg_dump`, into a folder that Tender backs up, on a schedule that ends before the backup starts.

You can change the paths and excludes in `/etc/tender/daemon.toml` on each machine. An `exclude` list replaces the default list above, so repeat the defaults you want to keep:

```toml
[backups]
paths   = ["/", "/srv"]
exclude = ["/var/lib/docker", "/tmp", "/srv/scratch"]
```

The daemon reads the file when it starts. After editing it, run `sudo systemctl restart tenderd`.

## Encryption and the repository password

Tender uses [restic](https://restic.net). Snapshots are deduplicated and encrypted on the machine with the destination's repository password before they leave it. The storage provider sees only encrypted data.

Save the repository password in a password manager when you add a destination. Without it nobody can restore the backups, including you. It can't change later, because the repositories are encrypted with it.

Because the backups are plain restic repositories, you can read and restore them with restic alone, without Tender.

## When the hub is down

Backups don't need the hub to be up. Each daemon keeps the last settings it got and goes on backing up on schedule. It saves its reports and sends them when the hub is back.

A hub that answers but refuses the machine's token is different. After `tender machines rotate-token` or `tender machines remove`, the daemon stops backing up until you install a new token.

## Next steps

1. [Restore files](guides/restore-files.md) from a snapshot.
2. [Add a machine](guides/add-a-machine.md) to back up another server.
3. [Notifications](notifications.md#events) explains `backup.failed` and `backup.stale`.
