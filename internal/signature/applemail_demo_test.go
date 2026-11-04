package signature

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edouard-claude/mailbridge-mcp/internal/config"
)

// TestAppleMailEndToEnd exercises the real DiscoverAppleMail() entry point (it
// globs ~/Library/.../Signatures) by pointing HOME at a temporary tree that
// mimics the Apple Mail container layout with two signatures, then runs the full
// setup-time flow: discover -> suggest by email -> import -> resolve at runtime.
func TestAppleMailEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	appleDir := filepath.Join(home, "Library", "Containers", "com.apple.mail",
		"Data", "Library", "Mail", "V10", "MailData", "Signatures")
	if err := os.MkdirAll(appleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMailsignature(t, appleDir, "AAAA-perso",
		`<div>Thomas<br>thomas.perso@gmail.com</div>`)
	writeMailsignature(t, appleDir, "BBBB-pro",
		`<div>Thomas Lemaitre<br>Directeur — Squirrel CORP<br><a href="mailto:thomas@squirrel.fr">thomas@squirrel.fr</a></div>`)

	sigs, _, _ := DiscoverAppleMail()
	if len(sigs) != 2 {
		t.Fatalf("expected 2 Apple Mail signatures, got %d", len(sigs))
	}
	t.Log("Signatures découvertes :")
	for i, s := range sigs {
		t.Logf("  [%d] %s", i+1, s.Preview)
	}

	idx := SuggestForEmail(sigs, "thomas@squirrel.fr")
	if idx < 0 {
		t.Fatal("expected a suggestion for thomas@squirrel.fr")
	}
	t.Logf("Suggestion pour thomas@squirrel.fr -> [%d] %s", idx+1, sigs[idx].Preview)

	rel, err := Import(sigs[idx].SourcePath, "pro")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	t.Logf("Copiée dans le dossier mailbridge : %s", rel)

	sig, err := Resolve(config.SignatureConfig{File: rel})
	if err != nil || sig.Empty() {
		t.Fatalf("Resolve after import failed: sig=%v err=%v", sig, err)
	}
	t.Logf("Rendu texte runtime :\n%s", sig.Text)

	// The resolved signature must be the pro one, not the perso one.
	if !strings.Contains(sig.Text, "Thomas Lemaitre") || !strings.Contains(sig.Text, "Squirrel") {
		t.Fatalf("resolved the wrong signature: %q", sig.Text)
	}
}
