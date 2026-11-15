# Connect an AI client

Give an AI client such as Claude Code the hub's MCP tools, so it can check on your machines and manage them for you.

## Prerequisites

1. A running hub, reachable from the machine where the client runs.
2. An MCP client that supports streamable HTTP, such as Claude Code.

## Connect Claude Code over HTTP

The hub serves MCP at `/mcp`.

1. Add the hub to Claude Code for every project:

   ```sh
   claude mcp add --transport http tender https://apps.your-tailnet.ts.net/mcp --scope user
   ```

2. Check that Claude Code connects to it:

   ```sh
   claude mcp list
   ```

   ```text
   tender: https://apps.your-tailnet.ts.net/mcp (HTTP) - ✓ Connected
   ```

To connect for one session only, save the server as `tender-mcp.json`:

```json
{
  "mcpServers": {
    "tender": {
      "type": "http",
      "url": "https://apps.your-tailnet.ts.net/mcp"
    }
  }
}
```

Then pass the file when you start Claude Code:

```sh
claude --mcp-config tender-mcp.json
```

## Connect over stdio on the hub machine

A client on the hub machine can run the tools as a local process with `tender mcp --stdio`. It reads and writes the hub's database directly, so the hub must have run once on the same version. Run it as the `tender` user, which owns `/var/lib/tender/hub`; other users can't open the database:

```json
{
  "mcpServers": {
    "tender": {
      "command": "sudo",
      "args": ["-u", "tender", "tender", "mcp", "--stdio"]
    }
  }
}
```

The client starts the command without a terminal, so `sudo` must not ask for a password there. Don't run it as root while the hub is stopped, because files root creates next to the database lock the hub out.

The tools that store destination keys need `TENDER_ENCRYPTION_KEY`, and `send_test_email` needs the `TENDER_SMTP_` variables, all from `/etc/tender/hub.env`. `sudo` clears the environment by default, so with the command above those tools say what is missing. Every other tool works.

## Ask it something

Try questions like these:

1. "Did last night's backups work?"
2. "Which machine is running out of disk?"
3. "Is anything using too much memory on apps?"
4. "Restart web on apps and show me its last 50 log lines."
5. "Back up apps every night at 4 and keep two weeks."

Each tool tells the client whether it only reads, changes something or removes something, so the client can ask you before a removal or a stop.

> [!WARNING]
> Logs and anything else a tool returns go to the client's model provider. Keys you give `add_destination` pass through the conversation too. For real bucket keys, use `tender destinations add`, which asks for them with typing hidden.

## Next steps

1. [MCP reference](../mcp.md) lists every tool with its inputs, results and limits.
2. [Security](../security.md) explains what the hub protects.
