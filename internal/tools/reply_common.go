package tools

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/edouard-claude/mailbridge-mcp/internal/config"
	imappool "github.com/edouard-claude/mailbridge-mcp/internal/imap"
	"github.com/edouard-claude/mailbridge-mcp/internal/signature"
	smtpsender "github.com/edouard-claude/mailbridge-mcp/internal/smtp"
)

// replyParts holds everything needed to build a reply message, in both text and
// HTML representations, shared by reply_email (send) and reply_draft (save).
type replyParts struct {
	to         []string
	cc         []string
	subject    string
	inReplyTo  string
	references string
	text       string
	html       string
}

// buildReplyParts assembles recipients, threading headers, quoted original and
// (optionally) the signature for a reply. sig may be nil.
func buildReplyParts(acc *config.Account, original *imappool.ParsedEmail, body string, replyAll bool, sig *signature.Signature) replyParts {
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

	// Threading headers (RFC 5322 §3.6.4)
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

	// Plain-text body: reply, signature, then quoted original.
	var text strings.Builder
	text.WriteString(body)
	if sig != nil && strings.TrimSpace(sig.Text) != "" {
		text.WriteString("\n\n")
		text.WriteString(sig.Text)
	}
	text.WriteString("\n\n")
	text.WriteString(fmt.Sprintf("On %s, %s wrote:\n", original.Date, original.From))
	for _, line := range strings.Split(original.Body, "\n") {
		text.WriteString("> " + line + "\n")
	}

	// HTML body: reply, signature, then quoted original in a blockquote.
	var html strings.Builder
	html.WriteString(textToHTML(body))
	if sig != nil && strings.TrimSpace(sig.HTML) != "" {
		html.WriteString("<br><br>\n")
		html.WriteString(sig.HTML)
	}
	quote := htmlBodyContent(original.HTMLBody)
	if strings.TrimSpace(quote) == "" {
		quote = textToHTML(original.Body)
	}
	html.WriteString(fmt.Sprintf(
		"<br><br>\n<div>On %s, %s wrote:</div>\n<blockquote type=\"cite\" style=\"margin:0 0 0 0.8ex; border-left:2px solid #cccccc; padding-left:1ex;\">\n%s\n</blockquote>\n",
		escapeHTML(original.Date), escapeHTML(original.From), quote,
	))

	return replyParts{
		to:         to,
		cc:         cc,
		subject:    subject,
		inReplyTo:  parentID,
		references: strings.Join(refs, " "),
		text:       text.String(),
		html:       html.String(),
	}
}

// resolveSignature returns the account signature when include is true. It is
// best-effort: a resolution error is returned as a human-readable warning
// string instead of blocking the send, and sig is nil in that case.
func resolveSignature(acc *config.Account, include bool) (*signature.Signature, string) {
	if !include {
		return nil, ""
	}
	sig, err := signature.Resolve(acc.Signature)
	if err != nil {
		return nil, fmt.Sprintf(" (signature ignored: %v)", err)
	}
	if sig.Empty() {
		return nil, ""
	}
	return sig, ""
}

// composeBody builds the text and HTML bodies for a fresh (non-reply) message,
// appending the signature when present. When there is no signature, htmlBody is
// empty so the message stays a plain text/plain message (current behaviour).
func composeBody(body string, sig *signature.Signature) (textBody, htmlBody string) {
	if sig == nil || sig.Empty() {
		return body, ""
	}
	textBody = body
	if strings.TrimSpace(sig.Text) != "" {
		textBody = body + "\n\n" + sig.Text
	}
	htmlBody = textToHTML(body)
	if strings.TrimSpace(sig.HTML) != "" {
		htmlBody += "<br><br>\n" + sig.HTML
	}
	return textBody, htmlBody
}

// textToHTML escapes plain text and converts newlines to <br> for inclusion in
// an HTML part.
func textToHTML(s string) string {
	s = escapeHTML(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "<br>\n")
}

// escapeHTML escapes the HTML metacharacters in s.
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

var (
	reQuoteBody    = regexp.MustCompile(`(?is)<body[^>]*>(.*)</body>`)
	reQuoteDocTags = regexp.MustCompile(`(?is)<!doctype[^>]*>|<head\b.*?</head>|</?html\b[^>]*>|</?body\b[^>]*>`)
)

// htmlBodyContent returns the inner content of an HTML document so it can be
// nested inside the reply's blockquote without duplicate <html>/<body> tags.
func htmlBodyContent(doc string) string {
	if m := reQuoteBody.FindStringSubmatch(doc); m != nil {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSpace(reQuoteDocTags.ReplaceAllString(doc, ""))
}

// inlineOf returns the inline images (e.g. logo) of a signature, if any.
func inlineOf(sig *signature.Signature) []smtpsender.InlinePart {
	if sig == nil {
		return nil
	}
	return sig.Inline
}
