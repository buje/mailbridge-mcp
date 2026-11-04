package applemail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/edouard-claude/mailbridge-mcp/internal/config"
)

// Minimal PNG header, enough for http.DetectContentType.
var pngData = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00")

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func htmlSignature(t *testing.T, dir, uid, html string) string {
	t.Helper()
	path := filepath.Join(dir, uid+".mailsignature")
	writeFile(t, path, "Content-Transfer-Encoding: 7bit\nContent-Type: text/html;\n\tcharset=utf-8\nMessage-Id: <"+uid+">\nMime-Version: 1.0\n\n"+html)
	return path
}

const index = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><array>
<dict><key>SignatureName</key><string>Thomas SQUIRREL</string><key>SignatureUniqueId</key><string>D110C921</string></dict>
<dict><key>SignatureName</key><string>Senzu &amp; Co</string><key>SignatureUniqueId</key><string>3967F4C6</string></dict>
<dict><key>SignatureName</key><string>Squirrel</string><key>SignatureUniqueId</key><string>C95832A9</string></dict>
</array></plist>`

const accountsMap = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>A</key><dict><key>AccountURL</key><string>imap://thomas%40squirrel.fr@ssl0.ovh.net/</string><key>Signatures</key><array><string>D110C921</string></array></dict>
<key>B</key><dict><key>AccountURL</key><string>imap://support%40squirrel.fr@ssl0.ovh.net/</string><key>Signatures</key><array><string>3967F4C6</string></array></dict>
<key>C</key><dict><key>AccountURL</key><string>imap://john%40gmail.com/</string><key>Signatures</key><array/></dict>
</dict></plist>`

func mailStore(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, "Library", "Mail", "V10", "MailData", "Signatures")
	htmlSignature(t, dir, "D110C921", `<div>Thomas Lemaitre<br>Squirrel<br><a href="https://calendly.com/x">Prendre RDV</a></div>`)
	htmlSignature(t, dir, "3967F4C6", `<div>Senzu<br>contact@senzu.tech</div>`)
	htmlSignature(t, dir, "ORPHAN01", `<div>Signature supprimée dans Mail</div>`)
	writeFile(t, filepath.Join(dir, "AllSignatures.plist"), index)
	writeFile(t, filepath.Join(dir, "AccountsMap.plist"), accountsMap)
	return dir
}

func TestDiscoverUsesIndexAndAccountsMap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := mailStore(t, home)
	// A stale iCloud copy must never be read.
	htmlSignature(t, filepath.Join(home, "Library", "Mobile Documents", "com~apple~mail", "Data", "V4", "Signatures"), "D110C921", "<div>Ancienne version</div>")

	sigs, got, err := Discover()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("expected Mail's local store %s, got %s", dir, got)
	}
	// Orphan file and index entry without file (C95832A9) are not listed.
	if len(sigs) != 2 || sigs[0].Name != "Senzu & Co" || sigs[1].Name != "Thomas SQUIRREL" {
		t.Fatalf("unexpected signatures: %+v", sigs)
	}
	if !strings.HasPrefix(sigs[1].Preview, "Thomas SQUIRREL — Thomas Lemaitre") {
		t.Fatalf("preview: %q", sigs[1].Preview)
	}
	if i := Suggest(sigs, "thomas@squirrel.fr"); i != 1 || !sigs[i].UsedBy("thomas@squirrel.fr") {
		t.Fatalf("thomas@ should get Thomas SQUIRREL (used by in Mail), got %d", i)
	}
	// AccountsMap wins over content matching (squirrel.fr appears in the other signature).
	if i := Suggest(sigs, "support@squirrel.fr"); i != 0 {
		t.Fatalf("support@ should get Senzu as configured in Mail, got %d", i)
	}
	if i := Suggest(sigs, "contact@senzu.tech"); i != 0 {
		t.Fatalf("content match should find Senzu, got %d", i)
	}
	if i := Suggest(sigs, "x@unknown.example"); i != -1 {
		t.Fatalf("expected no suggestion, got %d", i)
	}
}

func TestDiscoverOnlyLatestMailVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, v := range []string{"V2", "V9", "V10"} {
		htmlSignature(t, filepath.Join(home, "Library", "Mail", v, "MailData", "Signatures"), "SIG"+v, "<div>Signature "+v+"</div>")
	}
	sigs, _, err := Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 1 || !strings.Contains(sigs[0].Preview, "V10") {
		t.Fatalf("expected only the V10 signature, got %+v", sigs)
	}
}

func TestDiscoverWhenMailRootIsNotListable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions are not enforced for root")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	mailStore(t, home)
	root := filepath.Join(home, "Library", "Mail")
	// macOS: ~/Library/Mail cannot be listed, the Signatures folder can be read.
	os.Chmod(root, 0o311)
	defer os.Chmod(root, 0o755)
	sigs, _, err := Discover()
	if err != nil || len(sigs) != 2 {
		t.Fatalf("expected 2 signatures by direct access, got %d (%v)", len(sigs), err)
	}
}

func TestDiscoverAccessDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions are not enforced for root")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	mailStore(t, home)
	root := filepath.Join(home, "Library", "Mail")
	os.Chmod(root, 0o000)
	defer os.Chmod(root, 0o755)
	if _, _, err := Discover(); err != ErrAccessDenied {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

// Apple Mail signature with a logo: multipart/related, quoted-printable HTML,
// image embedded through Apple's <object> tag, plus an unsupported TIFF image
// and a remote image.
const appleMultipart = "Content-Type: multipart/related;\r\n\ttype=\"text/html\";\r\n\tboundary=\"Apple-Mail=_ABC\"\r\n" +
	"Mime-Version: 1.0 (Mac OS X Mail 16.0)\r\n\r\n\r\n" +
	"--Apple-Mail=_ABC\r\nContent-Transfer-Encoding: quoted-printable\r\nContent-Type: text/html;\r\n\tcharset=utf-8\r\n\r\n" +
	"<html><head><style>p{}</style></head><body><div>Cordialement,=C2=A0</div><div>Thomas=C2=A0Lemaitre</div>" +
	"<SPAN class=3D\"Apple-string-attachment\"><OBJECT height=3D42 width=3D195 type=3Dapplication/x-apple-msg-attachment data=3D\"cid:LOGO-1\"></OBJECT></SPAN>" +
	"<img src=3D\"cid:TIFF-2\" width=3D10><img src=3D\"https://example.com/remote.png\"></body></html>\r\n" +
	"--Apple-Mail=_ABC\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: inline;\r\n\tfilename=logo.png\r\n" +
	"Content-Type: image/png;\r\n\tname=\"logo.png\"\r\nContent-Id: <LOGO-1>\r\n\r\niVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1Pe\r\n" +
	"--Apple-Mail=_ABC\r\nContent-Transfer-Encoding: base64\r\nContent-Type: image/tiff;\r\n\tname=\"x.tiff\"\r\nContent-Id: <TIFF-2>\r\n\r\nTU0AKgAAAAg=\r\n" +
	"--Apple-Mail=_ABC--\r\n"

func TestParseAppleMultipart(t *testing.T) {
	p := Parse([]byte(appleMultipart))
	for _, bad := range []string{"=C2=A0", "Apple-Mail=", "<body", "<style", "x-apple-msg-attachment"} {
		if strings.Contains(p.HTML, bad) {
			t.Fatalf("HTML not cleaned (contains %q): %s", bad, p.HTML)
		}
	}
	if !strings.Contains(p.HTML, `<img src="cid:LOGO-1" alt="" width="195" height="42"`) {
		t.Fatalf("Apple <object> not converted to <img>: %s", p.HTML)
	}
	if len(p.Images) != 2 {
		t.Fatalf("expected 2 inline images, got %d", len(p.Images))
	}
}

func TestImportWritesMailbridgeFormat(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := filepath.Join(t.TempDir(), "sig.mailsignature")
	writeFile(t, src, appleMultipart)

	sig, skipped, err := Import(src, "Squirrel Thomas")
	if err != nil {
		t.Fatal(err)
	}
	if sig.HTMLFile != filepath.Join("signatures", "Squirrel-Thomas.html") {
		t.Fatalf("html_file: %q", sig.HTMLFile)
	}
	if len(sig.Images) != 1 || sig.Images[0].CID != "sig1" || sig.Images[0].ContentType != "image/png" {
		t.Fatalf("images: %+v", sig.Images)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "TIFF-2") {
		t.Fatalf("the TIFF image should be dropped: %v", skipped)
	}

	dir, _ := config.Dir()
	html, err := os.ReadFile(filepath.Join(dir, sig.HTMLFile))
	if err != nil {
		t.Fatal(err)
	}
	h := string(html)
	if !strings.Contains(h, `src="cid:sig1"`) || strings.Contains(h, "LOGO-1") || strings.Contains(h, "TIFF-2") {
		t.Fatalf("cids not rewritten / unsupported image not removed: %s", h)
	}
	if !strings.Contains(h, "https://example.com/remote.png") || !strings.Contains(h, "Thomas") {
		t.Fatalf("remote image and text must be kept: %s", h)
	}
	if _, err := os.Stat(filepath.Join(dir, sig.Images[0].Path)); err != nil {
		t.Fatalf("image file not written: %v", err)
	}

	// The config produced is the one documented in docs/signature.md.
	out, _ := json.Marshal(sig)
	if !strings.Contains(string(out), `"html_file":"signatures/Squirrel-Thomas.html"`) ||
		!strings.Contains(string(out), `"cid":"sig1"`) {
		t.Fatalf("unexpected config JSON: %s", out)
	}
}

func TestImportReplacesPreviousFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := filepath.Join(t.TempDir(), "a.mailsignature")
	writeFile(t, src, appleMultipart)
	if _, _, err := Import(src, "pro"); err != nil {
		t.Fatal(err)
	}
	plain := htmlSignature(t, t.TempDir(), "PLAIN", `<div>Sans image</div>`)
	sig, _, err := Import(plain, "pro")
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := config.Dir()
	left, _ := filepath.Glob(filepath.Join(dir, "signatures", "pro-sig*"))
	if len(sig.Images) != 0 || len(left) != 0 {
		t.Fatalf("old images should be removed: images=%v files=%v", sig.Images, left)
	}
}

func TestAccountEmail(t *testing.T) {
	for in, want := range map[string]string{
		"imap://thomas%40squirrel.fr@ssl0.ovh.net/": "thomas@squirrel.fr",
		"imap://john%40gmail.com/":                  "john@gmail.com",
		"ews://nobody/":                             "",
	} {
		if got := accountEmail(in); got != want {
			t.Errorf("accountEmail(%q) = %q, want %q", in, got, want)
		}
	}
}
