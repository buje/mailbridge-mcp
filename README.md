# mailbridge-mcp

MCP server for multi-account IMAP/SMTP email access, designed for Claude Desktop.

## Features

- **Multi-account** — manage multiple email accounts from a single server
- **IMAP** — list mailboxes, search, read, download attachments, move, copy, delete, and flag emails
- **Drafts** — save drafts, prepare replies as drafts (threaded, with the quoted history), and send them later
- **Folders** — create, rename, and delete mailbox folders
- **SMTP** — send new emails and reply to existing ones with proper threading headers
- **macOS Keychain** — passwords are stored securely, never in config files
- **MCP stdio** — runs as a subprocess launched by Claude Desktop

## Tools

| Tool | Description |
|------|-------------|
| `list_accounts` | List all configured email accounts |
| `list_mailboxes` | List mailbox folders for an account |
| `mailbox_status` | Get message/unseen count for a mailbox |
| `search_emails` | Search emails by sender, subject, date, read/unread, flagged/starred |
| `read_email` | Read full email content by UID |
| `save_attachment` | Download an attachment to a local directory |
| `send_email` | Send a new email |
| `reply_email` | Reply to an email (with proper In-Reply-To/References) |
| `reply_draft` | Prepare a reply as a draft instead of sending it (same threading and quoting as `reply_email`) |
| `save_draft` | Save a draft email to the Drafts folder |
| `send_draft` | Send a draft email by UID |
| `move_email` | Move an email to another folder |
| `copy_email` | Copy an email to another folder |
| `mark_email` | Mark as read/unread/flagged/unflagged |
| `delete_email` | Permanently delete an email |
| `create_mailbox` | Create a new mailbox folder |
| `rename_mailbox` | Rename a mailbox folder |
| `delete_mailbox` | Delete an empty mailbox folder |

## Reply drafts

`reply_draft` works like `reply_email` but saves the reply in the Drafts folder instead of sending it, so it can be reviewed (and edited) in any mail client before sending:

- **Threaded**: `In-Reply-To` / `References` point to the original message, so mail clients group the draft with the conversation.
- **Subject**: prefixed with `Re:` when needed.
- **History**: the original message is quoted below the reply.
- **Reply all**: `reply_all: true` copies the original To/Cc recipients, except your own address.
- **Drafts folder**: detected through the IMAP `\Drafts` special-use attribute (RFC 6154), with a fallback on common names (`Drafts`, `Brouillons`). Override with `drafts_mailbox`.

## Installation

```bash
# Build
make build

# Or install to /usr/local/bin
make install
```

## Setup

### 1. Add an email account

```bash
./mailbridge-mcp setup
```

This will interactively ask for:
- Account ID (e.g. `work`, `personal`)
- Email address
- IMAP host (e.g. `ssl0.ovh.net`, `imap.gmail.com`)
- SMTP host (defaults to IMAP host, or `smtp.*` if IMAP starts with `imap.*`)
- Password (stored in macOS Keychain)

The config file is automatically created at `~/.config/mailbridge/accounts.json`.

Run `setup` again to add more accounts — existing accounts with the same ID are updated in place.

### 2. Configure Claude Desktop

Add to `~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "mailbridge": {
      "command": "/path/to/mailbridge-mcp",
      "args": ["serve"]
    }
  }
}
```

### 3. Restart Claude Desktop

The email tools will be available immediately.

## Configuration

The config file at `~/.config/mailbridge/accounts.json` is managed by the `setup` command. Example:

```json
{
  "accounts": [
    {
      "id": "work",
      "label": "work",
      "email": "user@example.com",
      "imap": {
        "host": "imap.example.com",
        "port": 993,
        "tls": true
      },
      "smtp": {
        "host": "smtp.example.com",
        "port": 587,
        "starttls": true
      },
      "auth": {
        "type": "keychain",
        "keychain_service": "mailbridge-mcp",
        "keychain_account": "user@example.com"
      }
    }
  ],
  "defaults": {
    "max_fetch": 50,
    "body_max_chars": 10000,
    "timeout_seconds": 30
  }
}
```

## Security

- Passwords are stored in the macOS Keychain via [go-keyring](https://github.com/zalando/go-keyring), never in plain text
- IMAP connections use TLS (port 993) or STARTTLS
- SMTP connections use STARTTLS (port 587) or TLS (port 465)
- Plain text connections are never allowed

## Tech Stack

- [mcp-go](https://github.com/mark3labs/mcp-go) — MCP server framework
- [go-imap/v2](https://github.com/emersion/go-imap) — IMAP client
- [go-message](https://github.com/emersion/go-message) — MIME parsing
- [go-keyring](https://github.com/zalando/go-keyring) — macOS Keychain access

## License

MIT
