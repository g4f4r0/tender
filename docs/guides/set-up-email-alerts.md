# Set up email alerts

Send notifications by email through your SMTP server to everyone who should hear when a backup fails or a service stops.

## Prerequisites

1. A running hub, and root on its machine.
2. An SMTP server with implicit TLS, usually on port 465, and its login if it needs one.

## Turn on email

1. On the hub machine, run the hub installer again with your SMTP settings:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/g4f4r0/tender/main/install.sh | sudo sh -s -- hub \
     --smtp-host smtp.example.com --smtp-from "Tender <alerts@example.com>" --smtp-username alerts@example.com
   ```

2. Type the SMTP password when it asks. Typing is hidden.

   ```text
   The hub is running at https://apps.your-tailnet.ts.net.
   ```

The installer keeps the hub's key and every other setting, writes the SMTP settings to `/etc/tender/hub.env` and restarts the hub. To read the password from a file instead, add `--smtp-password-file PATH`.

## Add an email channel

1. Add a channel for each address that should get notifications:

   ```sh
   tender channels add ops --type email --to ops@example.com
   ```

   ```text
   Added channel ops. It gets the notifications of * from warning up. Check that it arrives with: tender channels test ops
   ```

   To get `info` notifications too, add `--min-severity info`. For only the worst, use `--min-severity critical`. To get only some events, add `--events`, such as `--events 'backup.*,machine.silent'`.

2. Send it a test:

   ```sh
   tender channels test ops
   ```

   ```text
   CHANNEL   RESULT
   ops       delivered
   ```

3. Check that the email with the subject `Tender test email` arrived. Look in the spam folder too.

Remove a channel with `tender channels remove ops`. To send notifications to Slack, a webhook or a program of your own as well, see [Adapters](../adapters.md).

## Next steps

1. [Notifications](../notifications.md) explains each event and when it fires and resolves.
2. [Adapters](../adapters.md) adds Slack, webhook and exec channels.
3. [Control a service](control-a-service.md) to turn one service's notifications off.
