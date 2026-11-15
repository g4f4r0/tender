# Set up email alerts

Send alert emails through your SMTP server to everyone who should hear when a backup fails or a service stops.

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

The installer keeps the hub's key and every other setting, writes the SMTP settings to `/etc/tender/hub.env` and restarts the hub. To read the password from a file instead, add `--smtp-password-file PATH`.

## Add recipients

1. Add each address that should get alerts:

   ```sh
   tender recipients add ops@example.com
   ```

   ```text
   Added ops@example.com. It gets every alert. Check that the email arrives with: tender email test
   ```

2. Send a test email to every recipient:

   ```sh
   tender email test
   ```

   ```text
   Sent a test email to ops@example.com.
   ```

3. Check that the email with the subject `Tender test email` arrived. Look in the spam folder too.

Every recipient gets every alert. Remove one with `tender recipients remove ops@example.com`.

## Next steps

1. [Alerts](../alerts.md) explains each alert and when it opens.
2. [Control a service](control-a-service.md) to turn one service's alerts off.
