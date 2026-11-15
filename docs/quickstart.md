# Quickstart

Set up a hub, back up one machine to your S3 bucket and list its Docker services, in about 10 minutes.

This guide puts the hub and the daemon on the same machine, called `apps`, and backs it up to a bucket called `my-backups` on Cloudflare R2. It skips email alerts; [set up email alerts](guides/set-up-email-alerts.md) adds them later.

## Prerequisites

1. A Linux machine on x86_64 with systemd, and `sudo`.
2. [Tailscale](https://tailscale.com) on the machine, so the installer can publish the hub privately on your tailnet.
3. An S3 bucket and an access key that can read and write it. Any S3-compatible storage works; only the endpoint changes. [Backups](backups.md#destination-types) covers SFTP, rest-server, rclone and local folders instead.

## 1. Install the hub

1. Run the installer:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- hub
   ```

   It prints the hub's address on your tailnet:

   ```text
   The hub is running at https://apps.your-tailnet.ts.net.
   The tender command on this machine finds it by itself.
   Anywhere else, set TENDER_HUB=https://apps.your-tailnet.ts.net or pass --hub https://apps.your-tailnet.ts.net.
   ```

2. Print the hub's settings file:

   ```sh
   sudo cat /etc/tender/hub.env
   ```

   ```text
   # Settings of the Tender hub, read by systemd when the service starts.
   # Keep this file private: the encryption key opens every secret the hub stores.
   # After a change, run: sudo systemctl restart tenderhub
   TENDER_ENCRYPTION_KEY=<key>
   ```

3. Save the file in your password manager. Its key decrypts the secrets the hub stores, and the hub won't start without it.

4. Make your API token, which every `tender` command sends:

   ```sh
   sudo tender tokens add laptop
   ```

   It prints the token once, with the line to use it:

   ```text
   Use it from the tender command:
     export TENDER_TOKEN=tender_<43 characters>
   ```

5. Run that `export` line, and save the token in your password manager.

## 2. Add a destination

1. Tell the hub where backups go:

   ```sh
   tender destinations add r2 --endpoint https://<account-id>.r2.cloudflarestorage.com --bucket my-backups --path tender
   ```

2. Paste the access key ID and the secret access key when it asks, then press Enter at the password prompt, so the hub makes a strong password:

   ```text
   Access key ID (typing is hidden):
   Secret access key (typing is hidden):
   Repository password, or empty for the hub to make one (typing is hidden):
   ```

   The hub shows the password once:

   ```text
   Added destination r2.

   The repository password, made by the hub and shown only now:
     <password>

   Save it in a password manager now. Every machine's backups at this destination are encrypted with
   it, and without it they cannot be restored. Tender cannot show it again.
   ```

3. Save the password in your password manager. It can't change later. [Backups](backups.md#encryption-and-the-repository-password) explains why.

## 3. Add the machine and its schedule

1. Add the machine, and keep the token it prints for step 4:

   ```sh
   tender machines add apps
   ```

   ```text
   Added machine apps.

   Its hub token, shown only now. Save it before you go on:
     <token>
   ```

2. Back up every night at 03:30 UTC and keep 30 days:

   ```sh
   tender backups set apps --destination r2 --schedule "30 3 * * *" --retention-days 30
   ```

   ```text
   apps backs up to r2 at "30 3 * * *" (UTC) and keeps 30 days.
   ```

## 4. Install the daemon

1. Run the installer with the hub's address:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- daemon \
     --hub https://apps.your-tailnet.ts.net --machine apps
   ```

2. Paste the token from step 3 when it asks. Typing is hidden.

   ```text
   The daemon is running. It gets its destination, schedule and keys from the hub. Next:
   ```

The first backup starts within a minute, and the next ones follow the schedule.

## 5. Check the backup

1. Wait a minute, then look at every machine at once:

   ```sh
   tender status
   ```

   ```text
   MACHINE   LAST SEEN   LAST BACKUP   STATUS      SERVICES
   apps      just now    just now      completed   1/2 running

   No open problems.
   ```

   A large machine's first backup takes longer. `STATUS` shows `in_progress` until it ends.

2. See the snapshot:

   ```sh
   tender backups status apps
   ```

   ```text
   MACHINE   DESTINATION   SCHEDULE     KEEPS     LAST BACKUP   STATUS      SNAPSHOTS
   apps      r2            30 3 * * *   30 days   just now      completed   1
   ```

## 6. List the services

1. List the machine's Docker containers:

   ```sh
   tender services list apps
   ```

   ```text
   MACHINE   SERVICE   STATE        HEALTH    MEM               CPU     RESTARTS   STARTED      ALERTS
   apps      migrate   exited (0)   -         -                 -       0          -            on
   apps      web       running      healthy   200 MiB / 1 GiB   12.5%   0          1 hour ago   on
   ```

   A machine without Docker has no services. CPU shows `-` until the second report, a minute after the first.

## Next steps

1. [Set up email alerts](guides/set-up-email-alerts.md) so you hear when a backup fails.
2. [Connect an AI client](guides/connect-an-ai-client.md) to ask "did last night's backups work?"
3. [Add a machine](guides/add-a-machine.md) for each server you want backed up.
4. [Restore files](guides/restore-files.md) before you need to, to see how it works.
