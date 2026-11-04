package tools

import (
	"context"
	"fmt"
	"net/mail"
	"strings"

	"github.com/edouard-claude/mailbridge-mcp/internal/auth"
	"github.com/edouard-claude/mailbridge-mcp/internal/config"
	imappool "github.com/edouard-claude/mailbridge-mcp/internal/imap"
	smtpsender "github.com/edouard-claude/mailbridge-mcp/internal/smtp"
	goimap "github.com/emersion/go-imap/v2"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerReplyEmail(s *server.MCPServer, cfg *config.Config, pool *imappool.Pool) {
	tool := mcp.NewTool("reply_email",
		mcp.WithDescription("Reply to an existing email. Sets In-Reply-To and References headers correctly. Prefixes subject with 'Re:' if not already present. Quotes the original message body. Supports attachments."),
		mcp.WithString("account_id",
			mcp.Description("Account to reply from"),
			mcp.Required(),
		),
		mcp.WithString("mailbox",
			mcp.Description("Mailbox containing the original email"),
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
		mcp.WithBoolean("include_signature",
			mcp.Description("Append the account's email signature (from Apple Mail) to the reply. Default: true."),
		),
		mcp.WithReadOnlyHintAnnotation(false),
		mcp.WithDestructiveHintAnnotation(true),
		mcp.WithIdempotentHintAnnotation(false),
		mcp.WithOpenWorldHintAnnotation(true),
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
		includeSignature := req.GetBool("include_signature", true)

		// Fetch original email (both renderings so we can quote in text and HTML).
		client, err := pool.Get(accountID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("IMAP connection failed: %v", err)), nil
		}

		original, err := imappool.FetchEmail(client, mailbox, uint32(uid), 0, imappool.BodyFormatBoth)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("fetch original email failed: %v", err)), nil
		}

		sig, sigWarn := resolveSignature(acc, includeSignature)
		parts := buildReplyParts(acc, original, body, replyAll, sig)

		password, err := auth.GetPassword(acc.Auth)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("get password: %v", err)), nil
		}

		attachments, err := parseAttachments(req.GetString("attachments", ""))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid attachments: %v", err)), nil
		}

		msg := smtpsender.BuildMessageMIME(smtpsender.Message{
			From:        acc.Email,
			To:          parts.to,
			Cc:          parts.cc,
			Subject:     parts.subject,
			TextBody:    parts.text,
			HTMLBody:    parts.html,
			Inline:      inlineOf(sig),
			Attachments: attachments,
			ExtraHeaders: map[string]string{
				"In-Reply-To": parts.inReplyTo,
				"References":  parts.references,
			},
		})

		recipients := append(append([]string{}, parts.to...), parts.cc...)
		if _, err := smtpsender.SendRaw(acc, password, recipients, msg); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("reply failed: %v", err)), nil
		}

		// Copy to Sent folder via IMAP APPEND
		if sentMailbox, err := imappool.FindSentMailbox(client); err == nil {
			imappool.AppendMessage(client, sentMailbox, []goimap.Flag{goimap.FlagSeen}, msg)
		}

		return mcp.NewToolResultText(fmt.Sprintf("Reply sent successfully from %s to %s.%s", acc.Email, original.From, sigWarn)), nil
	})
}

// ensureAngleBrackets normalizes a Message-ID value to the RFC 5322 form
// "<id@host>". IMAP envelopes return Message-IDs with or without brackets
// depending on the server, so we always wrap them before emitting headers.
func ensureAngleBrackets(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if !strings.HasPrefix(id, "<") {
		id = "<" + id
	}
	if !strings.HasSuffix(id, ">") {
		id = id + ">"
	}
	return id
}

// isSameEmail compares a potentially formatted address (e.g. "'Name' <user@host>")
// against a plain email address, ignoring the display name.
func isSameEmail(addr, email string) bool {
	parsed, err := mail.ParseAddress(addr)
	if err == nil {
		return strings.EqualFold(parsed.Address, email)
	}
	// Fallback: try to extract email from angle brackets
	addr = strings.TrimSpace(addr)
	if i := strings.LastIndex(addr, "<"); i >= 0 {
		if j := strings.LastIndex(addr, ">"); j > i {
			return strings.EqualFold(addr[i+1:j], email)
		}
	}
	return strings.EqualFold(addr, email)
}
