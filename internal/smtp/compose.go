package smtp

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html"
	"mime"
	"mime/quotedprintable"
	"regexp"
	"strings"
	"time"
)

// Signature is the signature resolved for one message. HTML references
// Images through "cid:<Name>" URLs, rewritten to unique Content-IDs at build
// time; images the HTML does not reference are left out of the message.
type Signature struct {
	Text   string
	HTML   string
	Images []InlineImage
}

// InlineImage is an image embedded in the message (multipart/related) and
// displayed by the HTML part through its Content-ID.
type InlineImage struct {
	Name        string // identifier used in the HTML: cid:<Name>
	Filename    string
	ContentType string
	Data        []byte
}

// Quote is the original message quoted at the bottom of a reply.
type Quote struct {
	Header string // attribution line, e.g. "On <date>, <from> wrote:"
	Text   string
	// HTML is the original HTML body, quoted as is in the HTML version so
	// its layout (signature, tables, links) survives. Text is used when empty.
	HTML string
}

// Body is the content of a message: the text written by the user, then the
// signature, then the quoted original (replies), in that order.
type Body struct {
	Text      string
	Signature *Signature
	Quote     *Quote
}

// SignatureDelimiter is the RFC 3676 §4.3 signature separator ("-- " + CRLF).
const SignatureDelimiter = "-- \n"

// Plain renders the text/plain version.
func (b Body) Plain() string {
	var sb strings.Builder
	sb.WriteString(strings.TrimRight(b.Text, " \t\r\n"))
	if s := b.Signature; s != nil && strings.TrimSpace(s.Text) != "" {
		sb.WriteString("\n\n" + SignatureDelimiter)
		sb.WriteString(strings.Trim(s.Text, "\r\n"))
	}
	if q := b.Quote; q != nil {
		sb.WriteString("\n\n" + q.Header + "\n")
		for _, line := range strings.Split(q.Text, "\n") {
			sb.WriteString("> " + line + "\n")
		}
	}
	return sb.String()
}

// Inline styles only: Gmail and Outlook ignore or strip most <style> blocks.
const (
	htmlTextStyle  = "font-family:Arial,Helvetica,sans-serif;font-size:14px;line-height:1.5;color:#222222;"
	htmlQuoteStyle = "margin:0 0 0 0.8ex;border-left:1px solid #cccccc;padding-left:1ex;color:#555555;"
)

var (
	reCID      = regexp.MustCompile(`(?i)\bcid:([^"'\s>)]+)`)
	reHTMLBody = regexp.MustCompile(`(?is)<body[^>]*>(.*)</\s*body\s*>`)
	reHTMLHead = regexp.MustCompile(`(?is)<!DOCTYPE[^>]*>|</?html[^>]*>|<head[^>]*>.*?</\s*head\s*>`)
)

// HTML renders the text/html version; cids maps image names to Content-IDs.
func (b Body) HTML(cids map[string]string) string {
	var sb strings.Builder
	sb.WriteString("<!DOCTYPE html>\n<html><head>" +
		`<meta http-equiv="Content-Type" content="text/html; charset=UTF-8">` +
		`<meta name="viewport" content="width=device-width, initial-scale=1">` +
		"</head>\n<body>\n")
	fmt.Fprintf(&sb, "<div style=\"%s\">%s</div>\n", htmlTextStyle, textToHTML(strings.TrimRight(b.Text, " \t\r\n")))
	if s := b.Signature; s != nil && strings.TrimSpace(s.HTML) != "" {
		fmt.Fprintf(&sb, "<br>\n<div class=\"mailbridge-signature\" style=\"%s\">\n%s\n</div>\n",
			htmlTextStyle, rewriteCIDs(signatureFragment(s.HTML), cids))
	}
	if q := b.Quote; q != nil {
		// The blockquote is outside the styled div: a quoted HTML message
		// keeps its own font sizes and spacing instead of inheriting ours.
		content := textToHTML(q.Text)
		if frag := quoteFragment(q.HTML); frag != "" {
			content = frag
		}
		fmt.Fprintf(&sb, "<br>\n<div class=\"gmail_quote\">\n<div style=\"%s\">%s</div>\n", htmlTextStyle, html.EscapeString(q.Header))
		fmt.Fprintf(&sb, "<blockquote type=\"cite\" class=\"gmail_quote\" style=\"%s\">\n%s\n</blockquote>\n</div>\n",
			htmlQuoteStyle, content)
	}
	sb.WriteString("</body></html>\n")
	return sb.String()
}

// textToHTML escapes plain text and keeps its line breaks.
func textToHTML(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(html.EscapeString(s), "\n", "<br>\n")
}

var (
	reHTMLScript = regexp.MustCompile(`(?is)<script\b.*?</\s*script\s*>`)
	reCIDImage   = regexp.MustCompile(`(?is)<img\b[^>]*\bsrc\s*=\s*["']?cid:[^>]*>`)
	reQuoteBody  = regexp.MustCompile(`(?is)<body[^>]*>(.*?)</\s*body\s*>`)
)

// quoteFragment prepares the original HTML body for the quote: the content of
// each <body> (Apple Mail may split a message in several HTML parts), without
// scripts, and without the cid: images of the original message, which are
// not attached to the reply and would show as broken images.
func quoteFragment(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	var frag string
	if ms := reQuoteBody.FindAllStringSubmatch(s, -1); ms != nil {
		parts := make([]string, 0, len(ms))
		for _, m := range ms {
			parts = append(parts, strings.TrimSpace(m[1]))
		}
		frag = strings.Join(parts, "\n")
	} else {
		frag = reHTMLHead.ReplaceAllString(s, "")
	}
	frag = reHTMLScript.ReplaceAllString(frag, "")
	frag = reCIDImage.ReplaceAllString(frag, "")
	return strings.TrimSpace(frag)
}

// signatureFragment keeps the inner content of a full HTML document, so a
// signature exported from a mail client can be used as is.
func signatureFragment(s string) string {
	if m := reHTMLBody.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSpace(reHTMLHead.ReplaceAllString(s, ""))
}

// ReferencedCIDs returns the cid: names used by an HTML fragment.
func ReferencedCIDs(htmlStr string) []string {
	var names []string
	seen := map[string]bool{}
	for _, m := range reCID.FindAllStringSubmatch(htmlStr, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	return names
}

func rewriteCIDs(s string, cids map[string]string) string {
	return reCID.ReplaceAllStringFunc(s, func(m string) string {
		if id, ok := cids[m[len("cid:"):]]; ok {
			return "cid:" + id
		}
		return m
	})
}

var reCIDUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// newContentID builds a globally unique Content-ID (RFC 2392, addr-spec form)
// so the image never collides with one quoted from an earlier message.
func newContentID(name, from string) string {
	local := strings.Trim(reCIDUnsafe.ReplaceAllString(name, "-"), "-.")
	if local == "" {
		local = "img"
	}
	return fmt.Sprintf("%s.%s@%s", local, hex.EncodeToString(randomBytes(6)), addrDomain(from))
}

func newBoundary() string {
	return fmt.Sprintf("mb_%s_%d", hex.EncodeToString(randomBytes(12)), time.Now().UnixNano())
}

// writeBody writes the MIME headers and content of the message body:
//
//	multipart/mixed                (only with attachments)
//	├─ multipart/related           (only with inline images)
//	│  ├─ multipart/alternative    (with an HTML signature or quote)
//	│  │  ├─ text/plain
//	│  │  └─ text/html
//	│  └─ image/* (Content-ID, inline)
//	└─ attachments
func writeBody(msg *strings.Builder, b Body, from string, attachments []Attachment) {
	if len(attachments) == 0 {
		writeContent(msg, b, from)
		return
	}
	boundary := newBoundary()
	fmt.Fprintf(msg, "Content-Type: multipart/mixed;\r\n boundary=\"%s\"\r\n\r\n", boundary)
	fmt.Fprintf(msg, "--%s\r\n", boundary)
	writeContent(msg, b, from)
	for _, att := range attachments {
		fmt.Fprintf(msg, "\r\n--%s\r\n", boundary)
		writeAttachment(msg, att)
	}
	fmt.Fprintf(msg, "\r\n--%s--\r\n", boundary)
}

// writeContent writes the readable part: text/plain alone, or text + HTML
// alternatives, wrapped with the inline images they reference.
func writeContent(msg *strings.Builder, b Body, from string) {
	sig := b.Signature
	sigHTML := sig != nil && strings.TrimSpace(sig.HTML) != ""
	quoteHTML := b.Quote != nil && quoteFragment(b.Quote.HTML) != ""
	if !sigHTML && !quoteHTML {
		writeTextPart(msg, "text/plain", b.Plain())
		return
	}

	referenced := map[string]bool{}
	cids := map[string]string{}
	var images []InlineImage
	var sigImages []InlineImage
	if sigHTML {
		for _, name := range ReferencedCIDs(sig.HTML) {
			referenced[name] = true
		}
		sigImages = sig.Images
	}
	for _, img := range sigImages {
		if referenced[img.Name] && cids[img.Name] == "" {
			cids[img.Name] = newContentID(img.Name, from)
			images = append(images, img)
		}
	}

	plain, htmlStr := b.Plain(), b.HTML(cids)
	if len(images) == 0 {
		writeAlternative(msg, plain, htmlStr)
		return
	}

	boundary := newBoundary()
	fmt.Fprintf(msg, "Content-Type: multipart/related; type=\"multipart/alternative\";\r\n boundary=\"%s\"\r\n\r\n", boundary)
	fmt.Fprintf(msg, "--%s\r\n", boundary)
	writeAlternative(msg, plain, htmlStr)
	for _, img := range images {
		fmt.Fprintf(msg, "\r\n--%s\r\n", boundary)
		writeInlineImage(msg, img, cids[img.Name])
	}
	fmt.Fprintf(msg, "\r\n--%s--\r\n", boundary)
}

// writeAlternative writes text/plain then text/html: clients render the last
// alternative they support (RFC 2046 §5.1.4).
func writeAlternative(msg *strings.Builder, plain, htmlStr string) {
	boundary := newBoundary()
	fmt.Fprintf(msg, "Content-Type: multipart/alternative;\r\n boundary=\"%s\"\r\n\r\n", boundary)
	fmt.Fprintf(msg, "--%s\r\n", boundary)
	writeTextPart(msg, "text/plain", plain)
	fmt.Fprintf(msg, "\r\n--%s\r\n", boundary)
	writeTextPart(msg, "text/html", htmlStr)
	fmt.Fprintf(msg, "\r\n--%s--\r\n", boundary)
}

// writeTextPart writes a UTF-8 text part, quoted-printable when it is not
// plain ASCII or has lines too long for SMTP (RFC 5322 §2.1.1).
func writeTextPart(msg *strings.Builder, ctype, s string) {
	fmt.Fprintf(msg, "Content-Type: %s; charset=UTF-8\r\n", ctype)
	if isASCII(s) && maxLineLen(s) <= 76 {
		msg.WriteString("\r\n")
		msg.WriteString(toCRLF(s))
		return
	}
	msg.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	var buf bytes.Buffer
	w := quotedprintable.NewWriter(&buf)
	w.Write([]byte(s))
	w.Close()
	msg.Write(buf.Bytes())
}

func writeInlineImage(msg *strings.Builder, img InlineImage, contentID string) {
	filename := sanitizeHeaderValue(img.Filename)
	fmt.Fprintf(msg, "Content-Type: %s\r\n", mediaType(img.ContentType, "name", filename))
	fmt.Fprintf(msg, "Content-Transfer-Encoding: base64\r\n")
	fmt.Fprintf(msg, "Content-ID: <%s>\r\n", contentID)
	fmt.Fprintf(msg, "X-Attachment-Id: %s\r\n", contentID)
	fmt.Fprintf(msg, "Content-Disposition: %s\r\n\r\n", mediaType("inline", "filename", filename))
	writeBase64(msg, img.Data)
}

func writeAttachment(msg *strings.Builder, att Attachment) {
	ct := att.ContentType
	if ct == "" {
		ct = "application/octet-stream"
	}
	filename := sanitizeHeaderValue(att.Filename)
	fmt.Fprintf(msg, "Content-Type: %s\r\n", mediaType(ct, "name", filename))
	fmt.Fprintf(msg, "Content-Disposition: %s\r\n", mediaType("attachment", "filename", filename))
	fmt.Fprintf(msg, "Content-Transfer-Encoding: base64\r\n\r\n")
	writeBase64(msg, att.Data)
}

// mediaType formats a Content-Type or Content-Disposition value with a file
// name parameter (quoted, RFC 2231-encoded when non-ASCII).
func mediaType(value, param, filename string) string {
	mt, params, err := mime.ParseMediaType(value)
	if err != nil {
		mt, params = "application/octet-stream", map[string]string{}
	}
	if filename != "" {
		params[param] = filename
	}
	if s := mime.FormatMediaType(mt, params); s != "" {
		return s
	}
	return mt
}

func writeBase64(msg *strings.Builder, data []byte) {
	encoded := base64.StdEncoding.EncodeToString(data)
	for i := 0; i < len(encoded); i += 76 {
		msg.WriteString(encoded[i:min(i+76, len(encoded))])
		msg.WriteString("\r\n")
	}
}

func maxLineLen(s string) int {
	longest := 0
	for _, line := range strings.Split(s, "\n") {
		longest = max(longest, len(strings.TrimSuffix(line, "\r")))
	}
	return longest
}

func toCRLF(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}
