# mailbridge-mcp

MCP server for email (IMAP + SMTP) via stdio, used by Claude Code.

## Build & Install

```bash
cd /Users/edouard/Code/go/mcp-mail

# Build only (binaire local ./mailbridge-mcp)
make build

# Build + copie dans /usr/local/bin (là où Claude Code le charge)
make install
```

**IMPORTANT :** `make build` seul ne suffit PAS. Le binaire chargé par Claude Code est `/usr/local/bin/mailbridge-mcp`. Après chaque modification, **toujours faire `make install`** pour copier le binaire au bon endroit.

## Après chaque mise à jour

1. `make install` — build + copie dans /usr/local/bin
2. Dans Claude Code : `/mcp` → reconnecter mailbridge
3. Tester avec un appel simple (ex: `list_accounts`)

Si `/mcp` échoue avec "Failed to reconnect" :
- Vérifier que le binaire tourne : `echo '{"jsonrpc":"2.0","method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{}},"id":1}' | /usr/local/bin/mailbridge-mcp serve`
- Doit retourner un JSON avec `serverInfo`
- Si crash silencieux : vérifier `go vet ./...` et `go build ./cmd/...`

## Architecture

- `cmd/mailbridge-mcp/` — Point d'entrée, sous-commandes `serve` (MCP stdio), `setup` (config interactive) et `signature` (import de la signature Apple Mail d'un compte)
- `internal/imap/` — Client IMAP (pool de connexions)
- `internal/smtp/` — Envoi SMTP (StartTLS/TLS)
- `internal/tools/` — Outils MCP (search, read, reply, send, etc.)
- `internal/config/` — Config JSON (`~/.config/mailbridge/accounts.json`), voir `config.go:73`
- `internal/auth/` — Mots de passe via macOS Keychain
- `internal/signature/` — Signatures : import ponctuel depuis Apple Mail (setup), lecture de la copie `~/.config/mailbridge/signatures/` au runtime

## Config MCP Claude Code

Définie dans `~/.claude.json` :
```json
"mailbridge": {
  "type": "stdio",
  "command": "/usr/local/bin/mailbridge-mcp",
  "args": ["serve"]
}
```

## Lecture des mails — corps vide ?

Un corps vide n'est presque jamais un problème IMAP : c'est un mail **HTML seul** ou
**transféré plusieurs fois** (`message/rfc822` imbriqués). `internal/imap/fetch.go`
parcourt tout l'arbre MIME et convertit le HTML en texte ; `internal/imap/html.go`
fait la conversion et extrait les liens actionnables (`mailto:`, `tel:`, URLs).

`read_email` accepte `body_format` : `auto` (défaut, texte puis HTML converti),
`text`, `html` (source brute), `both`. Passer `max_body_chars: 0` pour ne pas tronquer.

## SMTP — Gotcha adresses

Les adresses email récupérées via IMAP peuvent contenir des display names (ex: `'Edouard CLAUDE' <edouard@squirrel.fr>`). La fonction `extractEmail()` dans `internal/smtp/sender.go` parse ces adresses avant de les passer au `RCPT TO` SMTP qui n'accepte que des adresses nues.

## Conventions

- Go standard library, pas de frameworks HTTP
- `net/mail.ParseAddress()` pour parser les adresses email
- Credentials via macOS Keychain (jamais en dur)
- Tests : `go test ./...` (couverture : parsing MIME/HTML dans `internal/imap`, construction MIME dans `internal/smtp`, réponses dans `internal/tools`, signatures dans `internal/signature`)
