package tools

import (
	"strings"
	"testing"

	"github.com/edouard-claude/mailbridge-mcp/internal/config"
	imappool "github.com/edouard-claude/mailbridge-mcp/internal/imap"
)

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

	p := buildReplyParts(acc, original, "Merci !", true)

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
	if p := buildReplyParts(acc, original, "ok", false); p.subject != "RE: Devis" {
		t.Fatalf("subject should not get a second prefix: %q", p.subject)
	}
}
