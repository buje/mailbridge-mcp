package imap

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"

	goimap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	message "github.com/emersion/go-message"
	"github.com/emersion/go-message/charset" // also registers non-UTF-8 charsets
	"github.com/emersion/go-message/mail"
)

// Body formats accepted by FetchEmail / read_email.
const (
	BodyFormatAuto = "auto" // plain text if present, otherwise HTML converted to text
	BodyFormatText = "text" // plain text only (or HTML converted to text)
	BodyFormatHTML = "html" // raw HTML source
	BodyFormatBoth = "both" // text rendering + raw HTML
)

type ParsedEmail struct {
	From        string
	To          []string
	Cc          []string
	Date        string
	Subject     string
	MessageID   string
	InReplyTo   []string
	References  []string
	Flags       []goimap.Flag
	Body        string   // rendering selected by the requested format
	TextBody    string   // text/plain parts, concatenated
	HTMLBody    string   // text/html parts, concatenated
	Links       []string // actionable links found in the HTML parts
	Attachments []Attachment
}

type Attachment struct {
	Filename string
	Size     int64
	MimeType string
}

// maxMIMEDepth bounds the recursion on nested forwarded messages.
const maxMIMEDepth = 12

// FetchEmail fetches and parses a single email by UID. format selects the body
// rendering (see BodyFormat* constants); an empty value means BodyFormatAuto.
func FetchEmail(c *imapclient.Client, mailbox string, uid uint32, maxBodyChars int, format string) (*ParsedEmail, error) {
	if _, err := c.Select(mailbox, nil).Wait(); err != nil {
		return nil, fmt.Errorf("select %s: %w", mailbox, err)
	}

	uidSet := goimap.UIDSet{}
	uidSet.AddNum(goimap.UID(uid))

	bodySection := &goimap.FetchItemBodySection{}
	fetchOptions := &goimap.FetchOptions{
		Envelope:    true,
		Flags:       true,
		UID:         true,
		BodySection: []*goimap.FetchItemBodySection{bodySection},
	}

	messages, err := c.Fetch(uidSet, fetchOptions).Collect()
	if err != nil {
		return nil, fmt.Errorf("IMAP fetch UID %d: %w", uid, err)
	}

	if len(messages) == 0 {
		return nil, fmt.Errorf("email UID %d not found in %s", uid, mailbox)
	}

	msg := messages[0]
	parsed := &ParsedEmail{}

	// Extract envelope and flags
	parsed.Flags = msg.Flags
	if msg.Envelope != nil {
		parsed.Subject = msg.Envelope.Subject
		parsed.MessageID = msg.Envelope.MessageID
		parsed.Date = msg.Envelope.Date.Format(time.RFC3339)

		if len(msg.Envelope.From) > 0 {
			parsed.From = formatAddress(msg.Envelope.From[0])
		}
		for _, a := range msg.Envelope.To {
			parsed.To = append(parsed.To, formatAddress(a))
		}
		for _, a := range msg.Envelope.Cc {
			parsed.Cc = append(parsed.Cc, formatAddress(a))
		}
	}

	// Parse MIME body
	bodyBytes := msg.FindBodySection(bodySection)
	if bodyBytes != nil {
		parsed.parseBody(bodyBytes, maxBodyChars, format)
	}

	return parsed, nil
}

func (p *ParsedEmail) parseBody(body []byte, maxChars int, format string) {
	// Header-level metadata (In-Reply-To / References) comes from the
	// top-level message only.
	if mr, err := mail.CreateReader(bytes.NewReader(body)); err == nil {
		if ids, err := mr.Header.MsgIDList("In-Reply-To"); err == nil {
			p.InReplyTo = ids
		}
		if ids, err := mr.Header.MsgIDList("References"); err == nil {
			p.References = ids
		}
		mr.Close()
	}

	entity, err := message.Read(bytes.NewReader(body))
	if err != nil && message.IsUnknownCharset(err) {
		// Unknown charset is recoverable: entity is still usable.
		err = nil
	}
	if err != nil || entity == nil {
		// Fallback: raw body, better than nothing.
		p.Body = truncate(string(body), maxChars)
		return
	}

	var text, htm strings.Builder
	p.walk(entity, &text, &htm, 0)

	p.TextBody = strings.TrimSpace(text.String())
	p.HTMLBody = strings.TrimSpace(htm.String())
	p.Links = ExtractLinks(p.HTMLBody)
	p.Body = truncate(p.render(format), maxChars)
}

// walk traverses the MIME tree, descending into multipart/* containers and
// into message/rfc822 parts (forwarded messages), collecting every text/plain
// and text/html leaf.
func (p *ParsedEmail) walk(e *message.Entity, text, htm *strings.Builder, depth int) {
	if e == nil || depth > maxMIMEDepth {
		return
	}

	mediaType, _, err := e.Header.ContentType()
	if err != nil {
		mediaType = "text/plain"
	}
	mediaType = strings.ToLower(mediaType)

	// Container: recurse into each child part.
	if mr := e.MultipartReader(); mr != nil {
		if mediaType == "multipart/alternative" {
			p.walkAlternative(mr, text, htm, depth)
			return
		}
		eachPart(mr, func(part *message.Entity) {
			p.walk(part, text, htm, depth+1)
		})
		return
	}

	// Embedded message (forwarded mail): parse it as a full message.
	if mediaType == "message/rfc822" || mediaType == "message/global" {
		inner, err := message.Read(e.Body)
		if err != nil && message.IsUnknownCharset(err) {
			err = nil
		}
		if err == nil && inner != nil {
			appendSection(text, forwardedHeaderSummary(inner))
			p.walk(inner, text, htm, depth+1)
		}
		return
	}

	// Leaf part.
	disp, dispParams, _ := e.Header.ContentDisposition()
	isAttachment := strings.EqualFold(disp, "attachment")

	if isAttachment || !strings.HasPrefix(mediaType, "text/") {
		filename := partFilename(e, dispParams)
		if filename == "" && !isAttachment {
			return // inline image without a name: not worth listing
		}
		// Read the part so the listing reports its decoded size; the part
		// is never needed again afterwards.
		var size int64
		if data, err := io.ReadAll(e.Body); err == nil {
			size = int64(len(data))
		}
		p.Attachments = append(p.Attachments, Attachment{
			Filename: filename,
			Size:     size,
			MimeType: mediaType,
		})
		return
	}

	data, err := io.ReadAll(e.Body)
	if err != nil {
		return
	}
	switch {
	case strings.HasPrefix(mediaType, "text/html"):
		appendSection(htm, string(data))
		appendSection(text, HTMLToText(string(data)))
	default:
		appendSection(text, string(data))
	}
}

// eachPart calls fn for every child of a multipart entity, skipping past
// parts with an unknown charset (still usable) and stopping on other errors.
func eachPart(mr message.MultipartReader, fn func(*message.Entity)) {
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return
		}
		if err != nil {
			if message.IsUnknownCharset(err) && part != nil {
				fn(part)
				continue
			}
			return
		}
		fn(part)
	}
}

// walkAlternative keeps one version of a multipart/alternative: the children
// are the same content in different formats (RFC 2046 §5.1.4), so collecting
// all of them would repeat the message (text/plain, then the HTML converted to
// text). The text comes from the text/plain child, or from the HTML when there
// is none; the HTML from the last HTML child, the richest one.
func (p *ParsedEmail) walkAlternative(mr message.MultipartReader, text, htm *strings.Builder, depth int) {
	var plain, fromHTML, richHTML string
	eachPart(mr, func(part *message.Entity) {
		var t, h strings.Builder
		p.walk(part, &t, &h, depth+1)
		switch {
		case h.Len() > 0:
			richHTML = h.String()
			fromHTML = t.String()
		case t.Len() > 0 && plain == "":
			plain = t.String()
		}
	})
	if plain == "" {
		plain = fromHTML
	}
	appendSection(text, plain)
	appendSection(htm, richHTML)
}

// render picks the body representation matching the requested format.
func (p *ParsedEmail) render(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case BodyFormatHTML:
		if p.HTMLBody != "" {
			return p.HTMLBody
		}
		return p.TextBody
	case BodyFormatBoth:
		if p.HTMLBody == "" {
			return p.TextBody
		}
		if p.TextBody == "" {
			return p.HTMLBody
		}
		return p.TextBody + "\n\n--- HTML source ---\n\n" + p.HTMLBody
	default: // auto / text
		if p.TextBody != "" {
			return p.TextBody
		}
		return HTMLToText(p.HTMLBody)
	}
}

// forwardedHeaderSummary renders the headers of an embedded message so the
// forwarded envelope (real sender, reply-to, subject) is not lost.
func forwardedHeaderSummary(e *message.Entity) string {
	var sb strings.Builder
	sb.WriteString("--- Message transféré ---")
	for _, k := range []string{"From", "Reply-To", "To", "Date", "Subject"} {
		if v := strings.TrimSpace(e.Header.Get(k)); v != "" {
			fmt.Fprintf(&sb, "\n%s: %s", k, decodeHeader(v))
		}
	}
	return sb.String()
}

// partFilename resolves an attachment name from Content-Disposition, falling
// back to the legacy Content-Type "name" parameter.
func partFilename(e *message.Entity, dispParams map[string]string) string {
	if n := strings.TrimSpace(dispParams["filename"]); n != "" {
		return decodeHeader(n)
	}
	if _, ctParams, err := e.Header.ContentType(); err == nil {
		if n := strings.TrimSpace(ctParams["name"]); n != "" {
			return decodeHeader(n)
		}
	}
	return ""
}

func decodeHeader(v string) string {
	dec := new(mime.WordDecoder)
	dec.CharsetReader = charset.Reader
	if out, err := dec.DecodeHeader(v); err == nil {
		return out
	}
	return v
}

func appendSection(sb *strings.Builder, s string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	if sb.Len() > 0 {
		sb.WriteString("\n\n")
	}
	sb.WriteString(s)
}

func FormatEmail(parsed *ParsedEmail) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "From: %s\n", parsed.From)
	fmt.Fprintf(&sb, "To: %s\n", strings.Join(parsed.To, ", "))
	if len(parsed.Cc) > 0 {
		fmt.Fprintf(&sb, "Cc: %s\n", strings.Join(parsed.Cc, ", "))
	}
	fmt.Fprintf(&sb, "Date: %s\n", parsed.Date)
	fmt.Fprintf(&sb, "Subject: %s\n", parsed.Subject)
	fmt.Fprintf(&sb, "Message-ID: %s\n", parsed.MessageID)
	if len(parsed.InReplyTo) > 0 {
		fmt.Fprintf(&sb, "In-Reply-To: %s\n", formatMsgIDList(parsed.InReplyTo))
	}
	if len(parsed.References) > 0 {
		fmt.Fprintf(&sb, "References: %s\n", formatMsgIDList(parsed.References))
	}
	fmt.Fprintf(&sb, "Flags: %s\n", formatFlags(parsed.Flags))
	fmt.Fprintf(&sb, "\n---\n\n%s", parsed.Body)

	if len(parsed.Links) > 0 {
		fmt.Fprintf(&sb, "\n\n---\nLinks:\n")
		for _, l := range parsed.Links {
			fmt.Fprintf(&sb, "- %s\n", l)
		}
	}

	if len(parsed.Attachments) > 0 {
		fmt.Fprintf(&sb, "\n\n---\nAttachments:\n")
		for _, a := range parsed.Attachments {
			if a.Size > 0 {
				fmt.Fprintf(&sb, "- %s (%s, %d bytes)\n", a.Filename, a.MimeType, a.Size)
			} else {
				fmt.Fprintf(&sb, "- %s (%s)\n", a.Filename, a.MimeType)
			}
		}
	}
	return sb.String()
}

func formatMsgIDList(ids []string) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, "<"+id+">")
	}
	return strings.Join(parts, " ")
}

func formatAddress(a goimap.Address) string {
	email := fmt.Sprintf("%s@%s", a.Mailbox, a.Host)
	if a.Name != "" {
		return fmt.Sprintf("%s <%s>", a.Name, email)
	}
	return email
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "\n... [truncated]"
}

// FetchRawBody fetches the raw RFC 5322 body of an email by UID.
// This preserves the original MIME structure including attachments.
func FetchRawBody(c *imapclient.Client, mailbox string, uid uint32) ([]byte, error) {
	if _, err := c.Select(mailbox, nil).Wait(); err != nil {
		return nil, fmt.Errorf("select %s: %w", mailbox, err)
	}

	uidSet := goimap.UIDSet{}
	uidSet.AddNum(goimap.UID(uid))

	bodySection := &goimap.FetchItemBodySection{}
	fetchOptions := &goimap.FetchOptions{
		BodySection: []*goimap.FetchItemBodySection{bodySection},
		UID:         true,
	}

	messages, err := c.Fetch(uidSet, fetchOptions).Collect()
	if err != nil {
		return nil, fmt.Errorf("IMAP fetch UID %d: %w", uid, err)
	}

	if len(messages) == 0 {
		return nil, fmt.Errorf("email UID %d not found in %s", uid, mailbox)
	}

	msg := messages[0]
	bodyBytes := msg.FindBodySection(bodySection)
	if bodyBytes == nil {
		return nil, fmt.Errorf("email UID %d has no body", uid)
	}

	return bodyBytes, nil
}
