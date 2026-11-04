package tools

import (
	"github.com/edouard-claude/mailbridge-mcp/internal/config"
	imappool "github.com/edouard-claude/mailbridge-mcp/internal/imap"
	"github.com/mark3labs/mcp-go/server"
)

// RegisterAll registers all MCP tools on the server.
func RegisterAll(s *server.MCPServer, cfg *config.Config, pool *imappool.Pool) {
	// Read operations
	registerListAccounts(s, cfg)
	registerListMailboxes(s, cfg, pool)
	registerSearchEmails(s, cfg, pool)
	registerReadEmail(s, cfg, pool)
	registerSaveAttachment(s, cfg, pool)
	registerMailboxStatus(s, cfg, pool)

	// Write operations - email
	registerSendEmail(s, cfg, pool)
	registerReplyEmail(s, cfg, pool)
	registerReplyDraft(s, cfg, pool)
	registerSaveDraft(s, cfg, pool)
	registerSendDraft(s, cfg, pool)

	// Write operations - mailbox
	registerMoveEmail(s, cfg, pool)
	registerCopyEmail(s, cfg, pool)
	registerMarkEmail(s, cfg, pool)
	registerDeleteEmail(s, cfg, pool)

	// Write operations - folder management
	registerCreateMailbox(s, cfg, pool)
	registerRenameMailbox(s, cfg, pool)
	registerDeleteMailbox(s, cfg, pool)
}
