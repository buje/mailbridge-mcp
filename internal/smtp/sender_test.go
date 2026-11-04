package smtp

import (
	"bytes"
	"io"
	"strings"
	"testing"

	message "github.com/emersion/go-message"
)

func TestBuildMessageMIMEPlainOnly(t *testing.T) {
	msg := string(BuildMessageMIME(Message{
		From:     "me@example.com",
		To:       []string{"you@example.com"},
		Subject:  "Hello",
		TextBody: "Bonjour",
	}))
	if !strings.Contains(msg, "Content-Type: text/plain; charset=UTF-8") {
		t.Fatalf("expected a text/plain body, got:\n%s", msg)
	}
	if strings.Contains(msg, "multipart/") {
		t.Fatalf("plain-only message should not be multipart, got:\n%s", msg)
	}
}

func TestBuildMessageMIMEAlternative(t *testing.T) {
	msg := string(BuildMessageMIME(Message{
		From:     "me@example.com",
		To:       []string{"you@example.com"},
		Subject:  "Hello",
		TextBody: "Bonjour",
		HTMLBody: "<p>Bonjour</p>",
	}))
	if !strings.Contains(msg, "Content-Type: multipart/alternative;") {
		t.Fatalf("expected multipart/alternative, got:\n%s", msg)
	}
	if !strings.Contains(msg, "Content-Type: text/plain; charset=UTF-8") {
		t.Fatalf("missing text/plain alternative part")
	}
	if !strings.Contains(msg, "Content-Type: text/html; charset=UTF-8") {
		t.Fatalf("missing text/html alternative part")
	}
}

func TestBuildMessageMIMEMixedWrapsAlternative(t *testing.T) {
	msg := string(BuildMessageMIME(Message{
		From:        "me@example.com",
		To:          []string{"you@example.com"},
		Subject:     "Hello",
		TextBody:    "Bonjour",
		HTMLBody:    "<p>Bonjour</p>",
		Attachments: []Attachment{{Filename: "a.txt", ContentType: "text/plain", Data: []byte("hi")}},
	}))
	if !strings.Contains(msg, "Content-Type: multipart/mixed;") {
		t.Fatalf("expected multipart/mixed wrapper, got:\n%s", msg)
	}
	if !strings.Contains(msg, "Content-Type: multipart/alternative;") {
		t.Fatalf("expected nested multipart/alternative")
	}
	if !strings.Contains(msg, "Content-Disposition: attachment;") {
		t.Fatalf("expected attachment part")
	}
}

func TestBuildMessageMIMEThreadingHeaders(t *testing.T) {
	msg := string(BuildMessageMIME(Message{
		From:     "me@example.com",
		To:       []string{"you@example.com"},
		Subject:  "Re: Hello",
		TextBody: "Bonjour",
		ExtraHeaders: map[string]string{
			"In-Reply-To": "<parent@example.com>",
			"References":  "<a@example.com> <parent@example.com>",
		},
	}))
	if !strings.Contains(msg, "In-Reply-To: <parent@example.com>") {
		t.Fatalf("missing In-Reply-To header, got:\n%s", msg)
	}
	if !strings.Contains(msg, "References:") {
		t.Fatalf("missing References header")
	}
}

func TestBuildMessageMIMEInlineRelatedAndQuotedBoundaries(t *testing.T) {
	msg := string(BuildMessageMIME(Message{
		From:     "me@example.com",
		To:       []string{"you@example.com"},
		Subject:  "Hello",
		TextBody: "Bonjour",
		HTMLBody: `<p>Bonjour</p><img src="cid:logo@x">`,
		Inline:   []InlinePart{{ContentID: "logo@x", ContentType: "image/png", Filename: "logo.png", Data: []byte("png")}},
	}))
	if strings.Contains(msg, "boundary===") {
		t.Fatalf("boundaries must be quoted (they start with '='):\n%s", msg)
	}
	for _, want := range []string{"Content-Type: multipart/related;", "Content-ID: <logo@x>", "Content-Disposition: inline;"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in:\n%s", want, msg)
		}
	}
}

func TestBuildMessageMultipartBoundaryIsQuotedAndParseable(t *testing.T) {
	raw := BuildMessage("me@example.com", []string{"you@example.com"}, nil, "Hello", "Bonjour",
		[]Attachment{{Filename: "a.txt", ContentType: "text/plain", Data: []byte("hi")}}, nil)

	if bytes.Contains(raw, []byte("boundary===")) {
		t.Fatalf("boundary must be quoted, got:\n%s", raw)
	}

	// The message must be readable by a strict MIME parser.
	e, err := message.Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("message not parseable: %v", err)
	}
	mr := e.MultipartReader()
	if mr == nil {
		t.Fatal("expected a multipart message")
	}
	parts := 0
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading part %d: %v", parts, err)
		}
		io.Copy(io.Discard, p.Body)
		parts++
	}
	if parts != 2 {
		t.Fatalf("expected 2 parts (text + attachment), got %d", parts)
	}
}

func TestBuildMessageThreadingHeaders(t *testing.T) {
	raw := string(BuildMessage("me@example.com", []string{"you@example.com"}, nil, "Re: Hello", "Bonjour", nil,
		map[string]string{
			"In-Reply-To": "<parent@example.com>",
			"References":  "<root@example.com> <parent@example.com>",
		}))
	if !strings.Contains(raw, "In-Reply-To: <parent@example.com>") {
		t.Fatalf("missing In-Reply-To header:\n%s", raw)
	}
	if !strings.Contains(raw, "References: <root@example.com> <parent@example.com>") {
		t.Fatalf("missing References header:\n%s", raw)
	}
}
