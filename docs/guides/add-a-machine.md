# Add a machine

Back up and watch another server with the hub you already run.

The examples add a machine called `apps`. Use each new machine's own name.

## Prerequisites

1. A running hub with a destination, and your API token in `~/.config/tender/token` or `TENDER_TOKEN`. `tender destinations list` shows the destinations.
2. Root on the new machine, with Linux on x86_64 and systemd.
3. The new machine can reach the hub, such as over your tailnet.

## Add it

1. From any machine that can reach the hub, add the machine, and copy the token it prints:

   ```sh
   tender machines add apps
   ```

   ```text
   Added machine apps.

   Its hub token, shown only now. Save it before you go on:
     <token>
   ```

2. Set its schedule and retention:

   ```sh
   tender backups set apps --destination r2 --schedule "0 4 * * *" --retention-days 14
   ```

   ```text
   apps backs up to r2 at "0 4 * * *" (UTC) and keeps 14 days.
   ```

3. On the new machine, install the daemon:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- daemon \
     --hub https://apps.your-tailnet.ts.net --machine apps
   ```

4. Paste the token when it asks. Typing is hidden.

   ```text
   The daemon is running. It gets its destination, schedule and keys from the hub. Next:
   ```

5. Check that the machine reports:

   ```sh
   tender machines list
   ```

   ```text
   NAME   LAST SEEN   MEMORY   DISK
   apps   just now    15%      28%
   ```

The first backup starts within a minute. The next ones follow the schedule.

## Next steps

1. [Install](../install.md#the-daemon-runs-as-root) explains why the daemon runs as root.
2. [Backups](../backups.md) explains what a backup covers.
