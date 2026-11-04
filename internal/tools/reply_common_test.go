package tools

import (
	"strings"
	"testing"

	"github.com/edouard-claude/mailbridge-mcp/internal/config"
	imappool "github.com/edouard-claude/mailbridge-mcp/internal/imap"
	"github.com/edouard-claude/mailbridge-mcp/internal/signature"
)

func TestBuildReplyPartsThreadingQuoteAndSignature(t *testing.T) {
	acc := &config.Account{Email: "thomas@squirrel.fr"}
	original := &imappool.ParsedEmail{
		From:       "Raphael <raphael@example.com>",
		To:         []string{"thomas@squirrel.fr"},
		Cc:         []string{"paul@example.com"},
		Subject:    "MAJ app Android",
		Date:       "2026-11-03T12:30:16Z",
		MessageID:  "parent@example.com",
		References: []string{"<root@example.com>"},
		Body:       "Bonjour Thomas",
		HTMLBody:   `<html><head><style>p{}</style></head><body><p>Bonjour Thomas</p></body></html>`,
	}
	sig := &signature.Signature{HTML: "<div>SIG HTML</div>", Text: "SIG TEXT"}

	p := buildReplyParts(acc, original, "Merci !", true, sig)

	if p.subject != "Re: MAJ app Android" {
		t.Fatalf("subject: %q", p.subject)
	}
	if p.inReplyTo != "<parent@example.com>" || p.references != "<root@example.com> <parent@example.com>" {
		t.Fatalf("threading headers: in-reply-to=%q references=%q", p.inReplyTo, p.references)
	}
	if len(p.cc) != 1 || p.cc[0] != "paul@example.com" {
		t.Fatalf("reply-all cc should exclude own address: %v", p.cc)
	}
	// Order: reply, then signature, then quoted original.
	iReply, iSig, iQuote := strings.Index(p.html, "Merci"), strings.Index(p.html, "SIG HTML"), strings.Index(p.html, "<blockquote")
	if !(iReply >= 0 && iReply < iSig && iSig < iQuote) {
		t.Fatalf("unexpected HTML order (reply %d, sig %d, quote %d):\n%s", iReply, iSig, iQuote, p.html)
	}
	// The quoted original must not carry a nested <html>/<head>/<body> document.
	quoted := p.html[iQuote:]
	for _, bad := range []string{"<html", "<head", "<body", "<style"} {
		if strings.Contains(strings.ToLower(quoted), bad) {
			t.Fatalf("quoted original still contains %s:\n%s", bad, quoted)
		}
	}
	if !strings.Contains(quoted, "<p>Bonjour Thomas</p>") {
		t.Fatalf("quoted original content missing:\n%s", quoted)
	}
	if !strings.Contains(p.text, "SIG TEXT") || !strings.Contains(p.text, "> Bonjour Thomas") {
		t.Fatalf("text part should contain signature and quoted original:\n%s", p.text)
	}
}

func TestBuildReplyPartsThreadingAndQuote(t *testing.T) {
	acc := &config.Account{Email: "thomas@example.com"}
	original := &imappool.ParsedEmail{
		From:       "Raphael <raphael@example.com>",
		To:         []string{"thomas@example.com"},
		Cc:         []string{"paul@example.com", "Thomas <THOMAS@example.com>"},
		Subject:    "MAJ app Android",
		Date:       "2026-11-03T12:30:16Z",
		MessageID:  "parent@example.com",
		References: []string{"<root@example.com>"},
		Body:       "Bonjour Thomas\nUne question",
	}

	p := buildReplyParts(acc, original, "Merci !", true, nil)

	if p.subject != "Re: MAJ app Android" {
		t.Fatalf("subject: %q", p.subject)
	}
	if p.inReplyTo != "<parent@example.com>" {
		t.Fatalf("In-Reply-To: %q", p.inReplyTo)
	}
	if p.references != "<root@example.com> <parent@example.com>" {
		t.Fatalf("References: %q", p.references)
	}
	if len(p.to) != 1 || p.to[0] != original.From {
		t.Fatalf("to: %v", p.to)
	}
	// Reply-all keeps the other recipients but never our own address.
	if len(p.cc) != 1 || p.cc[0] != "paul@example.com" {
		t.Fatalf("cc: %v", p.cc)
	}
	if !strings.HasPrefix(p.text, "Merci !") ||
		!strings.Contains(p.text, "> Bonjour Thomas") ||
		!strings.Contains(p.text, "> Une question") {
		t.Fatalf("reply text should start with the body and quote the original:\n%s", p.text)
	}
}

func TestBuildReplyPartsKeepsExistingRePrefix(t *testing.T) {
	acc := &config.Account{Email: "me@example.com"}
	original := &imappool.ParsedEmail{From: "a@example.com", Subject: "RE: Devis", MessageID: "<x@y>"}
	if p := buildReplyParts(acc, original, "ok", false, nil); p.subject != "RE: Devis" {
		t.Fatalf("subject should not get a second prefix: %q", p.subject)
	}
}
