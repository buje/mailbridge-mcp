package smtp

import (
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/edouard-claude/mailbridge-mcp/internal/config"
)

// extractEmail parses a potentially formatted address like
// "'Name' <user@host>" and returns just "user@host".
func extractEmail(addr string) string {
	parsed, err := mail.ParseAddress(addr)
	if err == nil {
		return parsed.Address
	}
	// Fallback: strip angle brackets manually
	addr = strings.TrimSpace(addr)
	if i := strings.LastIndex(addr, "<"); i >= 0 {
		if j := strings.LastIndex(addr, ">"); j > i {
			return addr[i+1 : j]
		}
	}
	return addr
}

// generateMessageID creates a globally unique Message-ID per RFC 5322 §3.6.4.
func generateMessageID(from string) string {
	domain := "localhost"
	if parsed, err := mail.ParseAddress(from); err == nil {
		if i := strings.IndexByte(parsed.Address, '@'); i >= 0 {
			domain = parsed.Address[i+1:]
		}
	} else if i := strings.IndexByte(from, '@'); i >= 0 {
		domain = from[i+1:]
	}
	b := make([]byte, 16)
	rand.Read(b)
	return fmt.Sprintf("<%s.%s@%s>", hex.EncodeToString(b), strconv.FormatInt(time.Now().UnixNano(), 36), domain)
}

// isASCII reports whether s contains only ASCII characters.
func isASCII(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

// sanitizeHeaderValue strips CR and LF to prevent header injection (RFC 5322 §2.2).
func sanitizeHeaderValue(s string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(s)
}

// foldHeader wraps a long header value at whitespace boundaries to keep lines
// ≤ 78 characters per RFC 5322 §2.2.3. prefixLen is the length of
// "Header-Name: " already on the first line.
func foldHeader(value string, prefixLen int) string {
	const maxLen = 78
	if prefixLen+len(value) <= maxLen {
		return value
	}
	var buf strings.Builder
	lineLen := prefixLen
	for i, token := range strings.Fields(value) {
		if i > 0 {
			if lineLen+1+len(token) > maxLen {
				buf.WriteString("\r\n ")
				lineLen = 1
			} else {
				buf.WriteByte(' ')
				lineLen++
			}
		}
		buf.WriteString(token)
		lineLen += len(token)
	}
	return buf.String()
}

// Attachment represents a file to attach to an email.
type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

// Send sends an email via SMTP. Returns the built message for IMAP Sent copy.
func Send(acc *config.Account, password string, to, cc, bcc []string, subject, body string, attachments []Attachment) ([]byte, error) {
	recipients := make([]string, 0, len(to)+len(cc)+len(bcc))
	recipients = append(recipients, to...)
	recipients = append(recipients, cc...)
	recipients = append(recipients, bcc...)

	msg := BuildMessage(acc.Email, to, cc, subject, body, attachments, nil)

	var err error
	if acc.SMTP.TLS {
		err = sendTLS(acc, password, recipients, msg)
	} else {
		err = sendStartTLS(acc, password, recipients, msg)
	}
	return msg, err
}

// SendReply sends a reply email with proper In-Reply-To and References headers.
// Returns the built message for IMAP Sent copy.
func SendReply(acc *config.Account, password string, to, cc []string, subject, body, inReplyTo, references string, attachments []Attachment) ([]byte, error) {
	recipients := make([]string, 0, len(to)+len(cc))
	recipients = append(recipients, to...)
	recipients = append(recipients, cc...)

	extraHeaders := map[string]string{
		"In-Reply-To": inReplyTo,
		"References":  references,
	}

	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}

	msg := BuildMessage(acc.Email, to, cc, subject, body, attachments, extraHeaders)

	var err error
	if acc.SMTP.TLS {
		err = sendTLS(acc, password, recipients, msg)
	} else {
		err = sendStartTLS(acc, password, recipients, msg)
	}
	return msg, err
}

// Message describes an outgoing message for BuildMessageMIME. HTMLBody is
// optional: when set, the message is built as multipart/alternative so clients
// can show the formatted version (e.g. an HTML signature) while keeping a
// plain-text fallback.
type Message struct {
	From     string
	To       []string
	Cc       []string
	Subject  string
	TextBody string
	HTMLBody string
	// Inline holds images referenced from HTMLBody via "cid:" URLs (e.g. the
	// logo of an Apple Mail signature). They are sent in a multipart/related
	// block together with the HTML part. Ignored when HTMLBody is empty.
	Inline       []InlinePart
	Attachments  []Attachment
	ExtraHeaders map[string]string
}

// InlinePart is an inline resource (typically an image) referenced from the
// HTML body as "cid:<ContentID>".
type InlinePart struct {
	ContentID   string // without angle brackets
	ContentType string
	Filename    string
	Data        []byte
}

// BuildMessage constructs an RFC 5322 compliant text/plain MIME message
// (multipart/mixed when attachments are present). Kept for callers that only
// need a plain-text message; richer messages go through BuildMessageMIME.
func BuildMessage(from string, to, cc []string, subject, body string, attachments []Attachment, extraHeaders map[string]string) []byte {
	return BuildMessageMIME(Message{
		From:         from,
		To:           to,
		Cc:           cc,
		Subject:      subject,
		TextBody:     body,
		Attachments:  attachments,
		ExtraHeaders: extraHeaders,
	})
}

// BuildMessageMIME constructs an RFC 5322 compliant MIME message. The body
// structure depends on what is provided:
//
//	text only                 -> text/plain
//	text + attachments        -> multipart/mixed [ text/plain, attachments ]
//	text + html               -> multipart/alternative [ text/plain, text/html ]
//	text + html + attachments -> multipart/mixed [ multipart/alternative, attachments ]
func BuildMessageMIME(m Message) []byte {
	var msg strings.Builder
	writeTopHeaders(&msg, m)

	hasHTML := strings.TrimSpace(m.HTMLBody) != ""
	hasAtt := len(m.Attachments) > 0

	switch {
	case !hasHTML && !hasAtt:
		writeTextPart(&msg, "text/plain; charset=UTF-8", m.TextBody)
	case !hasHTML && hasAtt:
		boundary := newBoundary()
		fmt.Fprintf(&msg, "Content-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", boundary)
		fmt.Fprintf(&msg, "--%s\r\n", boundary)
		writeTextPart(&msg, "text/plain; charset=UTF-8", m.TextBody)
		for _, att := range m.Attachments {
			fmt.Fprintf(&msg, "\r\n--%s\r\n", boundary)
			writeAttachmentPart(&msg, att)
		}
		fmt.Fprintf(&msg, "--%s--\r\n", boundary)
	case hasHTML && !hasAtt:
		writeAlternative(&msg, m.TextBody, m.HTMLBody, m.Inline)
	default:
		boundary := newBoundary()
		fmt.Fprintf(&msg, "Content-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", boundary)
		fmt.Fprintf(&msg, "--%s\r\n", boundary)
		writeAlternative(&msg, m.TextBody, m.HTMLBody, m.Inline)
		for _, att := range m.Attachments {
			fmt.Fprintf(&msg, "\r\n--%s\r\n", boundary)
			writeAttachmentPart(&msg, att)
		}
		fmt.Fprintf(&msg, "--%s--\r\n", boundary)
	}

	return []byte(msg.String())
}

// writeTopHeaders writes the shared top-level headers (everything up to and
// including MIME-Version) but not the content-type of the body.
func writeTopHeaders(msg *strings.Builder, m Message) {
	fmt.Fprintf(msg, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(msg, "From: %s\r\n", sanitizeHeaderValue(m.From))
	fmt.Fprintf(msg, "To: %s\r\n", sanitizeHeaderValue(strings.Join(m.To, ", ")))
	if len(m.Cc) > 0 {
		fmt.Fprintf(msg, "Cc: %s\r\n", sanitizeHeaderValue(strings.Join(m.Cc, ", ")))
	}
	fmt.Fprintf(msg, "Message-ID: %s\r\n", generateMessageID(m.From))

	subject := sanitizeHeaderValue(m.Subject)
	if isASCII(subject) {
		fmt.Fprintf(msg, "Subject: %s\r\n", subject)
	} else {
		fmt.Fprintf(msg, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	}

	for k, v := range m.ExtraHeaders {
		if v != "" {
			v = sanitizeHeaderValue(v)
			fmt.Fprintf(msg, "%s: %s\r\n", k, foldHeader(v, len(k)+2))
		}
	}

	fmt.Fprintf(msg, "MIME-Version: 1.0\r\n")
}

// writeAlternative writes a multipart/alternative entity with a text/plain part
// and a text/html part. The Content-Type line it emits serves both as a
// top-level body header and as a part header when nested inside multipart/mixed
// (the caller emits the enclosing boundary in the nested case).
func writeAlternative(msg *strings.Builder, text, html string, inline []InlinePart) {
	boundary := newBoundary()
	fmt.Fprintf(msg, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n", boundary)
	fmt.Fprintf(msg, "\r\n")
	fmt.Fprintf(msg, "--%s\r\n", boundary)
	writeTextPart(msg, "text/plain; charset=UTF-8", text)
	fmt.Fprintf(msg, "\r\n--%s\r\n", boundary)
	if len(inline) == 0 {
		writeTextPart(msg, "text/html; charset=UTF-8", html)
	} else {
		// HTML + the inline images it references (cid:), per RFC 2387.
		related := newBoundary()
		fmt.Fprintf(msg, "Content-Type: multipart/related; type=\"text/html\"; boundary=\"%s\"\r\n", related)
		fmt.Fprintf(msg, "\r\n")
		fmt.Fprintf(msg, "--%s\r\n", related)
		writeTextPart(msg, "text/html; charset=UTF-8", html)
		for _, p := range inline {
			fmt.Fprintf(msg, "\r\n--%s\r\n", related)
			writeInlinePart(msg, p)
		}
		fmt.Fprintf(msg, "--%s--\r\n", related)
	}
	fmt.Fprintf(msg, "\r\n--%s--\r\n", boundary)
}

// writeTextPart writes a text part (text/plain or text/html) with its
// Content-Type and body, using quoted-printable when the body is non-ASCII or
// has lines too long for 7bit. Line endings are normalized to CRLF.
func writeTextPart(msg *strings.Builder, contentType, body string) {
	fmt.Fprintf(msg, "Content-Type: %s\r\n", contentType)
	body = strings.ReplaceAll(body, "\r\n", "\n")
	if !isASCII(body) || hasLongLine(body, 998) {
		fmt.Fprintf(msg, "Content-Transfer-Encoding: quoted-printable\r\n")
		fmt.Fprintf(msg, "\r\n")
		var buf bytes.Buffer
		w := quotedprintable.NewWriter(&buf)
		w.Write([]byte(body))
		w.Close()
		msg.WriteString(strings.ReplaceAll(strings.ReplaceAll(buf.String(), "\r\n", "\n"), "\n", "\r\n"))
	} else {
		fmt.Fprintf(msg, "\r\n")
		msg.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	}
}

// hasLongLine reports whether s has a line longer than max bytes (RFC 5322
// limits lines to 998 characters; HTML signatures often exceed it).
func hasLongLine(s string, max int) bool {
	for _, l := range strings.Split(s, "\n") {
		if len(l) > max {
			return true
		}
	}
	return false
}

// writeInlinePart writes a base64-encoded inline part referenced by Content-ID.
func writeInlinePart(msg *strings.Builder, p InlinePart) {
	ct := p.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	filename := sanitizeHeaderValue(p.Filename)
	if filename == "" {
		filename = "image"
	}
	fmt.Fprintf(msg, "Content-Type: %s; name=\"%s\"\r\n", ct, filename)
	fmt.Fprintf(msg, "Content-Transfer-Encoding: base64\r\n")
	fmt.Fprintf(msg, "Content-ID: <%s>\r\n", sanitizeHeaderValue(p.ContentID))
	fmt.Fprintf(msg, "Content-Disposition: inline; filename=\"%s\"\r\n", filename)
	fmt.Fprintf(msg, "\r\n")
	encoded := base64.StdEncoding.EncodeToString(p.Data)
	for i := 0; i < len(encoded); i += 76 {
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		fmt.Fprintf(msg, "%s\r\n", encoded[i:end])
	}
}

// writeAttachmentPart writes a single base64-encoded attachment part (without
// the leading boundary, which the caller emits).
func writeAttachmentPart(msg *strings.Builder, att Attachment) {
	ct := att.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	filename := sanitizeHeaderValue(att.Filename)
	if isASCII(filename) {
		fmt.Fprintf(msg, "Content-Type: %s; name=%s\r\n", ct, filename)
	} else {
		fmt.Fprintf(msg, "Content-Type: %s; name=%s\r\n", ct, mime.QEncoding.Encode("utf-8", filename))
	}
	fmt.Fprintf(msg, "Content-Disposition: attachment; filename=%s\r\n", mime.QEncoding.Encode("utf-8", filename))
	fmt.Fprintf(msg, "Content-Transfer-Encoding: base64\r\n")
	fmt.Fprintf(msg, "\r\n")
	encoded := base64.StdEncoding.EncodeToString(att.Data)
	for i := 0; i < len(encoded); i += 76 {
		end := i + 76
		if end > len(encoded) {
			end = len(encoded)
		}
		fmt.Fprintf(msg, "%s\r\n", encoded[i:end])
	}
}

// newBoundary returns a unique MIME multipart boundary.
func newBoundary() string {
	return fmt.Sprintf("==boundary_%s_%d", hex.EncodeToString(randomBytes(12)), time.Now().UnixNano())
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

// SendRaw sends a pre-built RFC 5322 message via SMTP.
// Returns the message unchanged for IMAP Sent copy.
func SendRaw(acc *config.Account, password string, recipients []string, msg []byte) ([]byte, error) {
	var err error
	if acc.SMTP.TLS {
		err = sendTLS(acc, password, recipients, msg)
	} else {
		err = sendStartTLS(acc, password, recipients, msg)
	}
	return msg, err
}

func sendStartTLS(acc *config.Account, password string, recipients []string, msg []byte) error {
	addr := acc.SMTP.Addr()
	host := acc.SMTP.Host

	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("SMTP dial %s: %w", addr, err)
	}
	defer c.Close()

	if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
		return fmt.Errorf("STARTTLS: %w", err)
	}

	authMech := smtp.PlainAuth("", acc.Email, password, host)
	if err := c.Auth(authMech); err != nil {
		return fmt.Errorf("SMTP auth: %w", err)
	}

	if err := c.Mail(acc.Email); err != nil {
		return fmt.Errorf("SMTP MAIL FROM: %w", err)
	}
	for _, rcpt := range recipients {
		if err := c.Rcpt(extractEmail(rcpt)); err != nil {
			return fmt.Errorf("SMTP RCPT TO %s: %w", rcpt, err)
		}
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("SMTP write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP close data: %w", err)
	}

	return c.Quit()
}

func sendTLS(acc *config.Account, password string, recipients []string, msg []byte) error {
	addr := acc.SMTP.Addr()
	host := acc.SMTP.Host

	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
	if err != nil {
		return fmt.Errorf("TLS dial %s: %w", addr, err)
	}

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("SMTP client: %w", err)
	}
	defer c.Close()

	authMech := smtp.PlainAuth("", acc.Email, password, host)
	if err := c.Auth(authMech); err != nil {
		return fmt.Errorf("SMTP auth: %w", err)
	}

	if err := c.Mail(acc.Email); err != nil {
		return fmt.Errorf("SMTP MAIL FROM: %w", err)
	}
	for _, rcpt := range recipients {
		if err := c.Rcpt(extractEmail(rcpt)); err != nil {
			return fmt.Errorf("SMTP RCPT TO %s: %w", rcpt, err)
		}
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("SMTP write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP close data: %w", err)
	}

	return c.Quit()
}
