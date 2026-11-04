package tools

import (
	"fmt"
	"strings"

	"github.com/edouard-claude/mailbridge-mcp/internal/config"
	imappool "github.com/edouard-claude/mailbridge-mcp/internal/imap"
)

// replyParts holds everything needed to build a reply message, shared by
// reply_email (send) and reply_draft (save as draft).
type replyParts struct {
	to         []string
	cc         []string
	subject    string
	inReplyTo  string
	references string
	text       string
}

// buildReplyParts assembles recipients, threading headers (RFC 5322 §3.6.4)
// and the quoted original for a reply.
func buildReplyParts(acc *config.Account, original *imappool.ParsedEmail, body string, replyAll bool) replyParts {
	// Recipients
	to := []string{original.From}
	var cc []string
	if replyAll {
		for _, addr := range original.To {
			if !isSameEmail(addr, acc.Email) {
				cc = append(cc, addr)
			}
		}
		for _, addr := range original.Cc {
			if !isSameEmail(addr, acc.Email) {
				cc = append(cc, addr)
			}
		}
	}

	// Threading headers:
	//   In-Reply-To: <parent-id>
	//   References:  <ref-1> <ref-2> ... <parent-id>
	parentID := ensureAngleBrackets(original.MessageID)
	var refs []string
	for _, id := range original.References {
		refs = append(refs, ensureAngleBrackets(id))
	}
	if parentID != "" {
		refs = append(refs, parentID)
	}

	subject := original.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}

	// Reply body followed by the quoted original.
	var text strings.Builder
	text.WriteString(body)
	text.WriteString("\n\n")
	text.WriteString(fmt.Sprintf("On %s, %s wrote:\n", original.Date, original.From))
	for _, line := range strings.Split(original.Body, "\n") {
		text.WriteString("> " + line + "\n")
	}

	return replyParts{
		to:         to,
		cc:         cc,
		subject:    subject,
		inReplyTo:  parentID,
		references: strings.Join(refs, " "),
		text:       text.String(),
	}
}
