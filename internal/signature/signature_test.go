package signature

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edouard-claude/mailbridge-mcp/internal/config"
)

func writeMailsignature(t *testing.T, dir, name, html string) string {
	t.Helper()
	content := "Content-Transfer-Encoding: 7bit\n" +
		"Content-Type: text/html; charset=us-ascii\n" +
		"Message-Id: <" + name + ">\n" +
		"Mime-Version: 1.0\n" +
		"\n" + html
	path := filepath.Join(dir, name+".mailsignature")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveMailsignatureFile(t *testing.T) {
	dir := t.TempDir()
	path := writeMailsignature(t, dir, "sig", "<div>Thomas Lemaitre<br>Squirrel</div>")

	sig, err := Resolve(config.SignatureConfig{File: path})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if sig == nil || sig.Empty() {
		t.Fatal("expected a non-empty signature")
	}
	if !strings.Contains(sig.HTML, "Squirrel") {
		t.Fatalf("HTML should keep the markup, got: %q", sig.HTML)
	}
	if strings.Contains(sig.HTML, "Content-Type:") {
		t.Fatalf("HTML should not contain header block, got: %q", sig.HTML)
	}
	if !strings.Contains(sig.Text, "Thomas Lemaitre") || strings.Contains(sig.Text, "<div>") {
		t.Fatalf("Text rendering unexpected: %q", sig.Text)
	}
}

func TestResolveEmptyConfig(t *testing.T) {
	sig, err := Resolve(config.SignatureConfig{})
	if err != nil {
		t.Fatalf("empty config should not error, got: %v", err)
	}
	if sig != nil {
		t.Fatalf("expected nil signature for empty config, got %+v", sig)
	}
}

func TestDiscoverAndSuggestForEmail(t *testing.T) {
	dir := t.TempDir()
	writeMailsignature(t, dir, "A-perso", "<div>Thomas<br>thomas@gmail.com</div>")
	writeMailsignature(t, dir, "B-pro", "<div>Thomas Lemaitre<br>Directeur, Squirrel CORP<br>thomas@squirrel.fr</div>")

	sigs := discoverIn([]string{dir})
	if len(sigs) != 2 {
		t.Fatalf("expected 2 signatures, got %d", len(sigs))
	}
	if sigs[0].Preview == "" {
		t.Fatalf("expected a non-empty preview")
	}

	// Domain-based suggestion picks the Squirrel signature.
	idx := SuggestForEmail(sigs, "thomas@squirrel.fr")
	if idx < 0 || !strings.Contains(strings.ToLower(sigs[idx].SourcePath), "b-pro") {
		t.Fatalf("expected the pro signature to be suggested, got idx=%d", idx)
	}

	// Unknown domain: no suggestion.
	if got := SuggestForEmail(sigs, "x@unknown.example"); got != -1 {
		t.Fatalf("expected -1 for unknown email, got %d", got)
	}
}

func TestImportCopiesIntoConfigDir(t *testing.T) {
	// Redirect HOME so ConfigDir()/SignaturesDir() point into a temp tree.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	srcDir := t.TempDir()
	src := writeMailsignature(t, srcDir, "mysig", "<div>Squirrel</div>")

	rel, err := Import(src, "pro")
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if rel != filepath.Join("signatures", "pro.mailsignature") {
		t.Fatalf("unexpected stored path: %q", rel)
	}
	// The copy must be resolvable via Resolve using the relative path.
	sig, err := Resolve(config.SignatureConfig{File: rel})
	if err != nil {
		t.Fatalf("Resolve copied signature: %v", err)
	}
	if !strings.Contains(sig.HTML, "Squirrel") {
		t.Fatalf("copied signature not resolved correctly: %q", sig.HTML)
	}
}

// Apple Mail stores signatures with a logo as multipart/related: a
// quoted-printable HTML part plus an inline image referenced via cid:.
func TestResolveAppleMultipartSignature(t *testing.T) {
	dir := t.TempDir()
	raw := "Content-Type: multipart/related;\r\n\ttype=\"text/html\";\r\n\tboundary=\"Apple-Mail=_ABC\"\r\n" +
		"Mime-Version: 1.0 (Mac OS X Mail 16.0)\r\n\r\n\r\n" +
		"--Apple-Mail=_ABC\r\nContent-Transfer-Encoding: quoted-printable\r\nContent-Type: text/html;\r\n\tcharset=utf-8\r\n\r\n" +
		"<html><body><div>Cordialement,=C2=A0</div><div>Thomas=C2=A0Lemaitre<br>thomas@squirrel.fr</div><img src=3D\"cid:LOGO@x\"></body></html>\r\n" +
		"--Apple-Mail=_ABC\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: inline;\r\n\tfilename=logo.png\r\n" +
		"Content-Type: image/png;\r\n\tname=\"logo.png\"\r\nContent-Id: <LOGO@x>\r\n\r\niVBORw0KGgo=\r\n--Apple-Mail=_ABC--\r\n"
	path := filepath.Join(dir, "apple.mailsignature")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	sig, err := Resolve(config.SignatureConfig{File: path})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"=C2=A0", "Apple-Mail=", "Content-Transfer-Encoding", "<body"} {
		if strings.Contains(sig.HTML, bad) {
			t.Fatalf("HTML not decoded (contains %q): %q", bad, sig.HTML)
		}
	}
	if !strings.Contains(sig.HTML, `src="cid:LOGO@x"`) || !strings.Contains(sig.Text, "Thomas Lemaitre") {
		t.Fatalf("unexpected signature: HTML=%q Text=%q", sig.HTML, sig.Text)
	}
	if len(sig.Inline) != 1 || sig.Inline[0].ContentID != "LOGO@x" || sig.Inline[0].ContentType != "image/png" {
		t.Fatalf("inline logo not extracted: %+v", sig.Inline)
	}
	sigs := discoverIn([]string{dir})
	if len(sigs) != 1 || !strings.Contains(sigs[0].Preview, "Thomas Lemaitre") || strings.Contains(sigs[0].Preview, "Cordialement") {
		t.Fatalf("preview should skip greetings and be decoded: %+v", sigs)
	}
}

func TestAppleObjectsToImg(t *testing.T) {
	in := `<SPAN class="Apple-string-attachment"><OBJECT height=70 width=250 type=application/x-apple-msg-attachment data="cid:4A1B-77"></OBJECT></SPAN><object data="https://x/y.swf"></object>`
	got := appleObjectsToImg(in)
	if !strings.Contains(got, `<img src="cid:4A1B-77" alt="" width="250" height="70"`) {
		t.Fatalf("Apple object not converted: %s", got)
	}
	if strings.Contains(strings.ToLower(got), `x-apple-msg-attachment`) {
		t.Fatalf("Apple object left in place: %s", got)
	}
	if !strings.Contains(got, `<object data="https://x/y.swf"></object>`) {
		t.Fatalf("non-cid object should be left untouched: %s", got)
	}
}

// Signatures deleted in Apple Mail disappear from AllSignatures.plist but their
// .mailsignature file often stays on disk: they must not be listed.
func TestDiscoverUsesAllSignaturesIndex(t *testing.T) {
	dir := t.TempDir()
	writeMailsignature(t, dir, "AAAA-1111", "<div>Thomas Lemaitre<br>thomas@squirrel.fr</div>")
	writeMailsignature(t, dir, "BBBB-2222", "<div>Ancienne signature supprimée</div>")
	writeMailsignature(t, dir, "ubiquitous_CCCC-3333", "<div>Senzu<br>contact@senzu.tech</div>")
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><array>
<dict><key>SignatureIsRich</key><true/><key>SignatureName</key><string>Squirrel Pro</string><key>SignatureUniqueId</key><string>AAAA-1111</string></dict>
<dict><key>SignatureName</key><string>Senzu &amp; Co</string><key>SignatureUniqueId</key><string>CCCC-3333</string></dict>
</array></plist>`
	if err := os.WriteFile(filepath.Join(dir, "AllSignatures.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	sigs := discoverIn([]string{dir})
	if len(sigs) != 2 {
		t.Fatalf("expected 2 signatures (orphan filtered), got %d: %+v", len(sigs), sigs)
	}
	if sigs[0].Name != "Senzu & Co" || sigs[1].Name != "Squirrel Pro" {
		t.Fatalf("names not read from the index: %+v", sigs)
	}
	if !strings.HasPrefix(sigs[1].Preview, "Squirrel Pro — Thomas Lemaitre") {
		t.Fatalf("preview should start with the Mail name: %q", sigs[1].Preview)
	}
	if got := SuggestForEmail(sigs, "contact@senzu.tech"); got != 0 {
		t.Fatalf("expected Senzu suggested, got %d", got)
	}
}

// Old macOS versions leave ~/Library/Mail/V2…V9 behind with stale signatures:
// only the newest Mail data folder must be scanned.
func TestOnlyLatestMailVersionIsScanned(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, v := range []string{"V2", "V9", "V10"} {
		d := filepath.Join(home, "Library", "Mail", v, "MailData", "Signatures")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		writeMailsignature(t, d, "SIG-"+v, "<div>Signature "+v+"</div>")
	}
	sigs, _, _ := DiscoverAppleMail()
	if len(sigs) != 1 || !strings.Contains(sigs[0].Preview, "V10") {
		t.Fatalf("expected only the V10 signature, got %+v", sigs)
	}
}

func TestPreviewSkipsURLOnlyLines(t *testing.T) {
	sig := &Signature{Text: "https://www.facebook.com/squirrelagence/\nhttps://twitter.com/squirrelfrance\nThomas Lemaitre\nDirecteur"}
	if got := preview(sig); got != "Thomas Lemaitre / Directeur" {
		t.Fatalf("unexpected preview: %q", got)
	}
}

// A clean HTML signature (e.g. copied from a sent email) is imported as .html
// and resolved as an HTML fragment, replacing any previous copy.
func TestImportHTMLSignatureKeepsExtension(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	src := filepath.Join(t.TempDir(), "sig.html")
	if err := os.WriteFile(src, []byte(`<html><body><table><tr><td><img src="https://x/thomas.png"></td><td>Thomas Lemaitre</td></tr></table></body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := writeMailsignature(t, t.TempDir(), "old", "<div>old</div>")
	if _, err := Import(old, "pro"); err != nil {
		t.Fatal(err)
	}
	rel, err := Import(src, "pro")
	if err != nil {
		t.Fatal(err)
	}
	if rel != filepath.Join("signatures", "pro.html") {
		t.Fatalf("unexpected stored path %q", rel)
	}
	if _, err := os.Stat(filepath.Join(SignaturesDir(), "pro.mailsignature")); !os.IsNotExist(err) {
		t.Fatalf("previous .mailsignature copy should be removed")
	}
	sig, err := Resolve(config.SignatureConfig{File: rel})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sig.HTML, "<body") || !strings.Contains(sig.HTML, `<img src="https://x/thomas.png">`) || !strings.Contains(sig.Text, "Thomas Lemaitre") {
		t.Fatalf("unexpected signature: %+v", sig)
	}
}

// When Mail's local store has an index, the iCloud/container copies (which may
// hold stale or deleted signatures) must be ignored, as must index entries
// whose file no longer exists.
func TestDiscoverReadsOnlyMailActiveStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	local := filepath.Join(home, "Library", "Mail", "V10", "MailData", "Signatures")
	icloud := filepath.Join(home, "Library", "Mobile Documents", "com~apple~mail", "Data", "V3", "MailData", "Signatures")
	for _, d := range []string{local, icloud} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeMailsignature(t, local, "D110C921-AAAA", "<div>Thomas Lemaitre<br>Prendre RDV</div>")
	plist := `<plist><array>
<dict><key>SignatureName</key><string>Thomas SQUIRREL</string><key>SignatureUniqueId</key><string>D110C921-AAAA</string></dict>
<dict><key>SignatureName</key><string>Squirrel</string><key>SignatureUniqueId</key><string>C95832A9-GONE</string></dict>
</array></plist>`
	if err := os.WriteFile(filepath.Join(local, "AllSignatures.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	writeMailsignature(t, icloud, "ubiquitous_OLD1", "<div>LEMAITRE Thomas<br>thomas.webgo@gmail.com</div>")

	sigs, scanned, _ := DiscoverAppleMail()
	if len(scanned) != 1 || scanned[0] != local {
		t.Fatalf("expected only the local store to be scanned, got %v", scanned)
	}
	if len(sigs) != 1 || sigs[0].Name != "Thomas SQUIRREL" {
		t.Fatalf("expected only 'Thomas SQUIRREL', got %+v", sigs)
	}
}

// Two indexed stores hold different versions of the same signature: the store
// Mail actually writes to (most recent signature file) must win, even if it is
// not first in preference order.
func TestDiscoverPicksMostRecentIndexedStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	local := filepath.Join(home, "Library", "Mail", "V10", "MailData", "Signatures")
	other := filepath.Join(home, "Library", "Containers", "com.apple.mail", "Data", "Library", "Mail", "V10", "MailData", "Signatures")
	plist := `<plist><array><dict><key>SignatureName</key><string>Thomas SQUIRREL</string><key>SignatureUniqueId</key><string>D110C921</string></dict></array></plist>`
	for _, d := range []string{local, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "AllSignatures.plist"), []byte(plist), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldPath := writeMailsignature(t, local, "D110C921", "<div>Ancienne version</div>")
	newPath := writeMailsignature(t, other, "D110C921", "<div>Version actuelle Prendre RDV</div>")
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldPath, past, past); err != nil {
		t.Fatal(err)
	}
	_ = newPath

	sigs, scanned, _ := DiscoverAppleMail()
	if len(scanned) != 1 || scanned[0] != other {
		t.Fatalf("expected the most recent store %s, got %v", other, scanned)
	}
	if len(sigs) != 1 || !strings.Contains(sigs[0].Preview, "Prendre RDV") {
		t.Fatalf("expected the current version, got %+v", sigs)
	}
}

func TestAccountsMapDrivesSuggestion(t *testing.T) {
	dir := t.TempDir()
	writeMailsignature(t, dir, "D110C921-1B6F", "<div>Thomas Lemaitre<br>Prendre RDV</div>")
	writeMailsignature(t, dir, "3967F4C6-2C3C", "<div>Senzu</div>")
	idx := `<plist><array>
<dict><key>SignatureName</key><string>Thomas SQUIRREL</string><key>SignatureUniqueId</key><string>D110C921-1B6F</string></dict>
<dict><key>SignatureName</key><string>Senzu</string><key>SignatureUniqueId</key><string>3967F4C6-2C3C</string></dict>
</array></plist>`
	amap := `<plist><dict>
<key>A</key><dict><key>AccountURL</key><string>imap://thomas%40squirrel.fr@ssl0.ovh.net/</string><key>Signatures</key><array><string>D110C921-1B6F</string></array></dict>
<key>B</key><dict><key>AccountURL</key><string>imap://support%40squirrel.fr@ssl0.ovh.net/</string><key>Signatures</key><array><string>3967F4C6-2C3C</string></array></dict>
<key>C</key><dict><key>AccountURL</key><string>imap://john%40gmail.com/</string><key>Signatures</key><array/></dict>
</dict></plist>`
	os.WriteFile(filepath.Join(dir, "AllSignatures.plist"), []byte(idx), 0o644)
	os.WriteFile(filepath.Join(dir, "AccountsMap.plist"), []byte(amap), 0o644)

	sigs := discoverIn([]string{dir})
	// support@squirrel.fr uses Senzu in Mail even though the Squirrel domain
	// appears in another signature: AccountsMap must win over content matching.
	if i := SuggestForEmail(sigs, "support@squirrel.fr"); i < 0 || sigs[i].Name != "Senzu" {
		t.Fatalf("support@squirrel.fr should map to Senzu, got %d", i)
	}
	if i := SuggestForEmail(sigs, "thomas@squirrel.fr"); i < 0 || sigs[i].Name != "Thomas SQUIRREL" {
		t.Fatalf("thomas@squirrel.fr should map to Thomas SQUIRREL, got %d", i)
	}
	if got := accountEmail("imap://john%40gmail.com/"); got != "john@gmail.com" {
		t.Fatalf("accountEmail gmail form: %q", got)
	}
}

// If ~/Library/Mail is unreadable (macOS privacy block), stale iCloud copies
// must not be used: discovery returns nothing and ErrMailAccessDenied.
func TestDiscoverRefusesStaleCopiesWhenMailIsBlocked(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions are not enforced for root")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	mail := filepath.Join(home, "Library", "Mail", "V10", "MailData", "Signatures")
	icloud := filepath.Join(home, "Library", "Mobile Documents", "com~apple~mail", "Data", "V4", "Signatures")
	os.MkdirAll(mail, 0o755)
	os.MkdirAll(icloud, 0o755)
	writeMailsignature(t, icloud, "D110C921", "<div>Ancienne version</div>")
	mailRoot := filepath.Join(home, "Library", "Mail")
	if err := os.Chmod(mailRoot, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(mailRoot, 0o755)

	sigs, _, warnings := DiscoverAppleMail()
	if len(sigs) != 0 {
		t.Fatalf("stale iCloud copy must not be used, got %+v", sigs)
	}
	if len(warnings) != 1 || warnings[0] != ErrMailAccessDenied.Error() {
		t.Fatalf("expected ErrMailAccessDenied, got %v", warnings)
	}
}
