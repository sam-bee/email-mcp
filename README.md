# Email MCP

A Go MCP server that reads the Nightwatch agent mailbox over Stalwart JMAP.
The Streamable HTTP endpoint is `/mcp`.

Use any unassigned address at `nightwatch-research.co.uk` for a signup or a
test. Stalwart delivers it to the shared agent mailbox. Use the full address
to find matching messages, then pass a message ID to `get_message_body`.

| Tool | Arguments | Result |
| --- | --- | --- |
| `list_recent_messages` | None | Messages received in the last hour, newest first, with IDs, sender and recipient headers, subjects and timestamps |
| `list_messages_to_address` | `address`, optional `all` (defaults to `false`) | Exact To or Cc matches, newest first. The default returns five and `all: true` searches the full mailbox history |
| `get_message_body` | `id` | Plain-text and HTML body parts for the message, with truncation or encoding flags when reported by JMAP |

The list covers the entire agent mailbox, including Junk. Body parts are
returned as data. Attachments are excluded and each text part is capped at
256 KiB.

Set `JMAP_SESSION_URL` to the Stalwart JMAP discovery URL and `JMAP_TOKEN`
to a mailbox API key with `authenticate`, `jmapEmailQuery` and
`jmapEmailGet`. The server listens on port 8000.

Run `go test ./...` to check the tools against a local JMAP fixture. Pushes
to `master` publish `ghcr.io/sam-bee/email-mcp:latest` through GitHub
Actions. The containernode repository manages k3s, Tailscale Serve, and the
encrypted deployment credential.
