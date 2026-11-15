# Backups

Each daemon backs up its whole machine to your S3 bucket on a schedule, keeps snapshots for as many days as you choose, and reads part of the data back every week to prove it restores.

This page explains what a backup covers and how the runs fit together. To restore, see [Restore files](guides/restore-files.md).

## Destinations, settings and runs

A destination is an S3 bucket and a path in it, with the access keys and a repository password. Many machines can share one destination. Each gets its own folder, such as `my-backups/tender/apps`.

A machine's backup settings name its destination, a cron schedule in UTC and a retention in days. The daemon gets them from the hub, with the keys, so there is nothing to copy to the machine. When you remove a machine's backup settings, its daemon stops backing up and deletes its saved copy of the settings and password. The backups in the bucket stay there.

The daemon does three kinds of runs. Each run is recorded in the machine's history with the status `in_progress`, then `completed` or `failed`.

| Type | When | What it does |
| --- | --- | --- |
| `backup` | On the schedule. | Takes a snapshot of the machine's backup paths. |
| `cleanup` | After each completed backup. | Forgets this machine's snapshots older than the retention and frees their space. |
| `check` | After a completed backup, once a week. | Reads back a random 5% of the stored data and checks it. |

A plain `restic check` reads only the repository's structure, so it misses damaged data. The weekly check reads real data, a different random 5% each time.

The daemon runs at normal priority, so its reports keep time during a run. It starts restic at nice 19, with the `SCHED_IDLE` CPU policy and the idle IO class, so a run uses only what the machine's other work leaves. The `tenderd` service's system call filter allows `setpriority`, `sched_setscheduler` and `ioprio_set` for this. On a machine that doesn't let the daemon lower a priority, restic starts at normal priority.

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

Tender uses [restic](https://restic.net). Snapshots are deduplicated and encrypted on the machine with the destination's repository password before they leave it. The bucket provider sees only encrypted data.

Save the repository password in a password manager when you add a destination. Without it nobody can restore the backups, including you. It can't change later, because the repositories are encrypted with it.

Because the backups are plain restic repositories, you can read and restore them with restic alone, without Tender.

## When the hub is down

Backups don't need the hub to be up. Each daemon keeps the last settings it got and goes on backing up on schedule. It saves its reports and sends them when the hub is back.

A hub that answers but refuses the machine's token is different. After `tender machines rotate-token` or `tender machines remove`, the daemon stops backing up until you install a new token.

## Next steps

1. [Restore files](guides/restore-files.md) from a snapshot.
2. [Add a machine](guides/add-a-machine.md) to back up another server.
3. [Notifications](notifications.md#events) explains `backup.failed` and `backup.stale`.
