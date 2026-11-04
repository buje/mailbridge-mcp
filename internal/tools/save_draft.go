package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/edouard-claude/mailbridge-mcp/internal/config"
	imappool "github.com/edouard-claude/mailbridge-mcp/internal/imap"
	smtpsender "github.com/edouard-claude/mailbridge-mcp/internal/smtp"
	goimap "github.com/emersion/go-imap/v2"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func registerSaveDraft(s *server.MCPServer, cfg *config.Config, pool *imappool.Pool) {
	tool := mcp.NewTool("save_draft",
		mcp.WithDescription("Save a draft email to the Drafts folder. Does NOT send the email."),
		mcp.WithString("account_id",
			mcp.Description("Account identifier"),
			mcp.Required(),
		),
		mcp.WithString("to",
			mcp.Description("Comma-separated recipient email addresses"),
		),
		mcp.WithString("cc",
			mcp.Description("Comma-separated CC recipients"),
		),
		mcp.WithString("subject",
			mcp.Description("Email subject"),
			mcp.Required(),
		),
		mcp.WithString("body",
			mcp.Description("Plain text email body"),
			mcp.Required(),
		),
		mcp.WithString("attachments",
			mcp.Description("JSON array of attachments: [{\"filename\":\"...\", \"content_base64\":\"...\", \"mime_type\":\"...\"}]"),
		),
		mcp.WithString("mailbox",
			mcp.Description("Drafts mailbox name (default: 'Drafts')"),
		),
		mcp.WithBoolean("include_signature",
			mcp.Description("Append the account's email signature (from Apple Mail) to the draft. Default: true."),
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

		subject := req.GetString("subject", "")
		if subject == "" {
			return mcp.NewToolResultError("subject is required"), nil
		}
		body := req.GetString("body", "")
		if body == "" {
			return mcp.NewToolResultError("body is required"), nil
		}

		to := splitAndTrim(req.GetString("to", ""))
		cc := splitAndTrim(req.GetString("cc", ""))
		draftsMailbox := req.GetString("mailbox", "Drafts")
		includeSignature := req.GetBool("include_signature", true)

		attachments, err := parseAttachments(req.GetString("attachments", ""))
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid attachments: %v", err)), nil
		}

		sig, sigWarn := resolveSignature(acc, includeSignature)
		textBody, htmlBody := composeBody(body, sig)

		msg := smtpsender.BuildMessageMIME(smtpsender.Message{
			From:        acc.Email,
			To:          to,
			Cc:          cc,
			Subject:     subject,
			TextBody:    textBody,
			HTMLBody:    htmlBody,
			Inline:      inlineOf(sig),
			Attachments: attachments,
		})

		client, err := pool.Get(accountID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("IMAP connection failed: %v", err)), nil
		}

		if err := imappool.AppendMessage(client, draftsMailbox, []goimap.Flag{goimap.FlagDraft}, msg); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("save draft failed: %v", err)), nil
		}

		recipients := strings.Join(to, ", ")
		if recipients == "" {
			recipients = "(no recipients yet)"
		}
		return mcp.NewToolResultText(fmt.Sprintf("Draft saved to %s: \"%s\" → %s%s", draftsMailbox, subject, recipients, sigWarn)), nil
	})
}
