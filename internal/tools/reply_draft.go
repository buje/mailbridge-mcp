package tools

import (
	"context"
	"fmt"

	"github.com/edouard-claude/mailbridge-mcp/internal/config"
	imappool "github.com/edouard-claude/mailbridge-mcp/internal/imap"
	smtpsender "github.com/edouard-claude/mailbridge-mcp/internal/smtp"
	goimap "github.com/emersion/go-imap/v2"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerReplyDraft adds a tool that prepares a reply as a draft (stored in
// the Drafts folder, threaded and quoting the original) instead of sending it.
func registerReplyDraft(s *server.MCPServer, cfg *config.Config, pool *imappool.Pool) {
	tool := mcp.NewTool("reply_draft",
		mcp.WithDescription("Save a reply as a draft (does NOT send). Keeps the conversation thread (In-Reply-To/References), prefixes the subject with 'Re:' and quotes the original message. The draft appears in the Drafts folder, ready to review and send."),
		mcp.WithString("account_id",
			mcp.Description("Account to reply from"),
			mcp.Required(),
		),
		mcp.WithString("mailbox",
			mcp.Description("Mailbox containing the original email (default: INBOX)"),
		),
		mcp.WithNumber("uid",
			mcp.Description("UID of the email to reply to"),
			mcp.Required(),
		),
		mcp.WithString("body",
			mcp.Description("Reply body (plain text)"),
			mcp.Required(),
		),
		mcp.WithBoolean("reply_all",
			mcp.Description("Reply to all recipients (default: false)"),
		),
		mcp.WithString("attachments",
			mcp.Description("JSON array of attachments: [{\"filename\":\"...\", \"content_base64\":\"...\", \"mime_type\":\"...\"}]"),
		),
		mcp.WithString("drafts_mailbox",
			mcp.Description("Drafts mailbox name. Defaults to the account's \\Drafts folder, falling back to 'Drafts'."),
		),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(false),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		accountID := req.GetString("account_id", "")
		if accountID == "" {
			return mcp.NewToolResultError("account_id is required"), nil
		}

		acc := cfg.Account(accountID)
		if acc == nil {
			return mcp.NewToolResultError(fmt.Sprintf("unknown account: %q", accountID)), nil
		}

		mailbox := req.GetString("mailbox", "INBOX")
		uid := req.GetInt("uid", 0)
		if uid == 0 {
			return mcp.NewToolResultError("uid is required"), nil
		}
		body := req.GetString("body", "")
		if body == "" {
			return mcp.NewToolResultError("body is required"), nil
		}
		replyAll := req.GetBool("reply_all", false)

		client, err := pool.Get(accountID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("IMAP connection failed: %v", err)), nil
		}

		original, err := imappool.FetchEmail(client, mailbox, uint32(uid), 0, imappool.BodyFormatAuto)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("fetch original email failed: %v", err)), nil
		}

		attachments, err := parseAttachments(req.GetString("attachments", ""))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid attachments: %v", err)), nil
		}

		parts := buildReplyParts(acc, original, body, replyAll)
		msg := smtpsender.BuildMessage(acc.Email, parts.to, parts.cc, parts.subject, parts.text, attachments,
			map[string]string{
				"In-Reply-To": parts.inReplyTo,
				"References":  parts.references,
			})

		// Drafts mailbox: explicit override, then the \Drafts special-use
		// folder, then the conventional "Drafts" name.
		draftsMailbox := req.GetString("drafts_mailbox", "")
		if draftsMailbox == "" {
			if detected, err := imappool.FindDraftsMailbox(client); err == nil {
				draftsMailbox = detected
			} else {
				draftsMailbox = "Drafts"
			}
		}

		if err := imappool.AppendMessage(client, draftsMailbox, []goimap.Flag{goimap.FlagDraft}, msg); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("save reply draft failed: %v", err)), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Reply draft saved to %s (in reply to %s).", draftsMailbox, original.From)), nil
	})
}
