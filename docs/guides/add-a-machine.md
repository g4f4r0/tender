# Add a machine

Back up and watch another server with the hub you already run.

This guide adds a machine called `db`. Replace it with your machine's name.

## Prerequisites

1. A running hub with a destination. `tender destinations list` shows yours.
2. Root on the new machine, with Linux on x86_64 and systemd.
3. The new machine can reach the hub, such as over your tailnet.

## Add it

1. From any machine that can reach the hub, add the machine:

   ```sh
   tender machines add db
   ```

   ```text
   Added machine db.

   Its hub token, shown only now. Save it before you go on:
     <token>
   ```

2. Copy the token.

3. Set its schedule and retention:

   ```sh
   tender backups set db --destination r2 --schedule "0 4 * * *" --retention-days 14
   ```

   ```text
   db backs up to r2 at "0 4 * * *" (UTC) and keeps 14 days.
   ```

4. On the new machine, install the daemon:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- daemon \
     --hub https://apps.your-tailnet.ts.net --machine db
   ```

5. Paste the token when it asks. Typing is hidden.

6. Check that the machine reports:

   ```sh
   tender machines list
   ```

   ```text
   NAME   LAST SEEN   MEMORY   DISK
   apps   just now    15%      28%
   db     just now    9%       41%
   ```

The first backup starts within a minute. The next ones follow the schedule.

## Next steps

1. [Install](../install.md#docker-and-the-journal) explains the groups the daemon joins.
2. [Backups](../backups.md) explains what a backup covers.
