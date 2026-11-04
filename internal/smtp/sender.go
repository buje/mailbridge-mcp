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

// BuildMessage constructs an RFC 5322 compliant MIME message.
// When attachments is non-empty, the message uses multipart/mixed with
// the text body as the first part and each attachment base64-encoded.
func BuildMessage(from string, to, cc []string, subject, body string, attachments []Attachment, extraHeaders map[string]string) []byte {
	var msg strings.Builder

	// Date (RFC 5322 §3.6 — required)
	fmt.Fprintf(&msg, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))

	// From
	fmt.Fprintf(&msg, "From: %s\r\n", sanitizeHeaderValue(from))

	// To
	fmt.Fprintf(&msg, "To: %s\r\n", sanitizeHeaderValue(strings.Join(to, ", ")))

	// Cc
	if len(cc) > 0 {
		fmt.Fprintf(&msg, "Cc: %s\r\n", sanitizeHeaderValue(strings.Join(cc, ", ")))
	}

	// Message-ID (RFC 5322 §3.6.4 — required)
	fmt.Fprintf(&msg, "Message-ID: %s\r\n", generateMessageID(from))

	// Subject (RFC 2047 encoding for non-ASCII)
	subject = sanitizeHeaderValue(subject)
	if isASCII(subject) {
		fmt.Fprintf(&msg, "Subject: %s\r\n", subject)
	} else {
		fmt.Fprintf(&msg, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	}

	// Extra headers with folding for long values (e.g. References)
	for k, v := range extraHeaders {
		if v != "" {
			v = sanitizeHeaderValue(v)
			fmt.Fprintf(&msg, "%s: %s\r\n", k, foldHeader(v, len(k)+2))
		}
	}

	// MIME headers
	fmt.Fprintf(&msg, "MIME-Version: 1.0\r\n")

	if len(attachments) == 0 {
		buildSimpleBody(&msg, body)
	} else {
		buildMultipartMixed(&msg, body, attachments)
	}

	return []byte(msg.String())
}

// buildSimpleBody writes a text/plain body (with quoted-printable for non-ASCII).
func buildSimpleBody(msg *strings.Builder, body string) {
	fmt.Fprintf(msg, "Content-Type: text/plain; charset=UTF-8\r\n")
	if !isASCII(body) {
		fmt.Fprintf(msg, "Content-Transfer-Encoding: quoted-printable\r\n")
		fmt.Fprintf(msg, "\r\n")
		var buf bytes.Buffer
		w := quotedprintable.NewWriter(&buf)
		w.Write([]byte(body))
		w.Close()
		msg.Write(buf.Bytes())
	} else {
		fmt.Fprintf(msg, "\r\n")
		fmt.Fprintf(msg, "%s", body)
	}
}

// buildMultipartMixed builds a multipart/mixed MIME message with a text/plain
// part followed by base64-encoded attachment parts.
func buildMultipartMixed(msg *strings.Builder, body string, attachments []Attachment) {
	boundary := fmt.Sprintf("==boundary_%s_%d", hex.EncodeToString(randomBytes(12)), time.Now().UnixNano())
	// The boundary starts with "=", a MIME tspecial: it must be quoted
	// (RFC 2045 §5.1), otherwise strict parsers reject the whole message.
	fmt.Fprintf(msg, "Content-Type: multipart/mixed; boundary=\"%s\"\r\n", boundary)
	fmt.Fprintf(msg, "\r\n")

	// First part: text body
	fmt.Fprintf(msg, "--%s\r\n", boundary)
	buildSimpleBody(msg, body)

	// Attachment parts
	for _, att := range attachments {
		fmt.Fprintf(msg, "\r\n--%s\r\n", boundary)
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

	// Closing boundary
	fmt.Fprintf(msg, "--%s--\r\n", boundary)
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

