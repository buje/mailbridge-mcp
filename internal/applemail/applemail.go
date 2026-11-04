// Package applemail imports an account signature from the macOS Mail app into
// the mailbridge signature format (signature.html_file + signature.images, see
// docs/signature.md).
//
// It is only used by the "setup" and "signature" commands: the running server
// (serve) never reads ~/Library, it uses the files written here under the
// mailbridge config directory.
//
// Mail stores each signature as a "<UUID>.mailsignature" MIME entity in
// ~/Library/Mail/V<n>/MailData/Signatures, next to two plists:
//   - AllSignatures.plist: the signatures that exist (UUID -> name). Deleted
//     signatures often leave their .mailsignature file behind.
//   - AccountsMap.plist: which signatures each mail account uses.
package applemail

import (
	"bytes"
	"errors"
	"fmt"
	htmlpkg "html"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/edouard-claude/mailbridge-mcp/internal/config"
	imappool "github.com/edouard-claude/mailbridge-mcp/internal/imap"
	message "github.com/emersion/go-message"
)

// ErrAccessDenied is reported when macOS privacy protection prevents reading
// Mail's signatures from the app that launched mailbridge.
var ErrAccessDenied = errors.New("macOS denied access to ~/Library/Mail (Mail data)")

// Signature is a signature found in Mail.
type Signature struct {
	Path     string   // absolute path of the .mailsignature file
	Name     string   // name given in Mail (AllSignatures.plist), if known
	Preview  string   // short plain-text preview
	Accounts []string // addresses Mail uses this signature for (AccountsMap.plist), lowercased
	content  string   // decoded HTML + text, lowercased, for content matching
}

// Parsed is a decoded .mailsignature.
type Parsed struct {
	HTML   string  // HTML fragment (no <html>/<body>), images referenced as cid:<CID>
	Images []Image // inline images referenced by HTML
}

// Image is an inline image of a signature.
type Image struct {
	CID         string
	ContentType string
	Data        []byte
}

// Same constraints as the signature loader (internal/tools/signature.go):
// images outside them would make every send fail, so they are dropped.
var supportedImageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true}

const maxImageSize = 1 << 20

// ---------------------------------------------------------------------------
// Discovery
// ---------------------------------------------------------------------------

// Discover lists the signatures of Mail's active local store. dir is the
// folder read. Copies kept elsewhere (iCloud, Mail container) are ignored as
// they can be outdated.
func Discover() (sigs []Signature, dir string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, "", err
	}
	dir, denied := localStore(home)
	if denied {
		return nil, "", ErrAccessDenied
	}
	if dir == "" {
		return nil, "", errors.New("no Mail signatures folder found (~/Library/Mail/V*/MailData/Signatures)")
	}
	return discoverIn(dir), dir, nil
}

// localStore finds ~/Library/Mail/V<n>/MailData/Signatures, newest version
// first, by direct probing: macOS forbids listing ~/Library/Mail but allows
// reading the Signatures folder itself.
func localStore(home string) (dir string, denied bool) {
	for v := 30; v >= 2; v-- {
		p := filepath.Join(home, "Library", "Mail", fmt.Sprintf("V%d", v), "MailData", "Signatures")
		_, err := os.ReadDir(p)
		switch {
		case err == nil:
			return p, false
		case errors.Is(err, fs.ErrPermission):
			return "", true
		}
	}
	return "", false
}

// discoverIn lists the signatures of a Mail Signatures folder.
func discoverIn(dir string) []Signature {
	index, hasIndex := readIndex(dir)
	accounts := readAccountsMap(dir)
	matches, _ := filepath.Glob(filepath.Join(dir, "*.mailsignature"))

	var out []Signature
	for _, path := range matches {
		uid := signatureUID(path)
		name := ""
		if hasIndex {
			n, ok := index[uid]
			if !ok {
				continue // orphan file of a signature deleted in Mail
			}
			name = n
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		p := Parse(data)
		text := imappool.HTMLToText(p.HTML)
		prev := preview(text, len(p.Images) > 0)
		if name != "" {
			prev = name + " — " + prev
		}
		out = append(out, Signature{
			Path:     path,
			Name:     name,
			Preview:  prev,
			Accounts: accounts[uid],
			content:  strings.ToLower(p.HTML + "\n" + text),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// Suggest returns the index of the signature to propose for email: the one
// Mail uses for that address, else one mentioning the address, then its
// domain. -1 when nothing matches.
func Suggest(sigs []Signature, email string) int {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return -1
	}
	for i, s := range sigs {
		for _, a := range s.Accounts {
			if a == email {
				return i
			}
		}
	}
	domain := ""
	if i := strings.LastIndex(email, "@"); i >= 0 {
		domain = email[i+1:]
	}
	for _, needle := range []string{email, domain} {
		if needle == "" {
			continue
		}
		for i, s := range sigs {
			if strings.Contains(s.content, needle) {
				return i
			}
		}
	}
	return -1
}

// UsedBy reports whether Mail uses s for email.
func (s Signature) UsedBy(email string) bool {
	for _, a := range s.Accounts {
		if strings.EqualFold(a, email) {
			return true
		}
	}
	return false
}

func signatureUID(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return strings.ToUpper(strings.TrimPrefix(base, "ubiquitous_"))
}

var (
	reHasAlnum  = regexp.MustCompile(`[\p{L}\p{N}]`)
	reURLOnly   = regexp.MustCompile(`(?i)^(https?://|www\.)\S*$`)
	reGreetings = regexp.MustCompile(`(?i)^(cordialement|bien (à|a) vous|bonne (journée|journee)|regards|best( regards)?|merci)[\s,.!]*$`)
)

// preview keeps the first meaningful lines (no greeting, separator or bare URL).
func preview(text string, hasImages bool) string {
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(strings.ReplaceAll(l, " ", " "))
		if l == "" || !reHasAlnum.MatchString(l) || reGreetings.MatchString(l) || reURLOnly.MatchString(l) {
			continue
		}
		lines = append(lines, l)
		if len(lines) == 3 {
			break
		}
	}
	p := strings.Join(lines, " / ")
	if hasImages {
		p += " [images]"
	}
	if p == "" {
		p = "(empty signature)"
	}
	return p
}

// ---------------------------------------------------------------------------
// Plists
// ---------------------------------------------------------------------------

var (
	rePlistDict  = regexp.MustCompile(`(?s)<dict>(.*?)</dict>`)
	rePlistName  = regexp.MustCompile(`(?s)<key>SignatureName</key>\s*<string>(.*?)</string>`)
	rePlistUID   = regexp.MustCompile(`(?s)<key>SignatureUniqueId</key>\s*<string>(.*?)</string>`)
	reAccount    = regexp.MustCompile(`(?s)<key>AccountURL</key>\s*<string>(.*?)</string>\s*<key>Signatures</key>\s*(?:<array/>|<array>(.*?)</array>)`)
	rePlistValue = regexp.MustCompile(`(?s)<string>(.*?)</string>`)
)

// readPlistXML returns a plist as XML, converting binary plists with plutil
// (always present on macOS, where this code runs).
func readPlistXML(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	if bytes.HasPrefix(data, []byte("bplist")) {
		xml, err := exec.Command("plutil", "-convert", "xml1", "-o", "-", path).Output()
		if err != nil {
			return "", false
		}
		data = xml
	}
	return string(data), true
}

// readIndex reads AllSignatures.plist: UUID -> signature name.
func readIndex(dir string) (map[string]string, bool) {
	data, ok := readPlistXML(filepath.Join(dir, "AllSignatures.plist"))
	if !ok {
		return nil, false
	}
	index := map[string]string{}
	for _, d := range rePlistDict.FindAllStringSubmatch(data, -1) {
		uid := rePlistUID.FindStringSubmatch(d[1])
		if uid == nil {
			continue
		}
		name := ""
		if n := rePlistName.FindStringSubmatch(d[1]); n != nil {
			name = htmlpkg.UnescapeString(strings.TrimSpace(n[1]))
		}
		index[strings.ToUpper(strings.TrimSpace(uid[1]))] = name
	}
	return index, len(index) > 0
}

// readAccountsMap reads AccountsMap.plist: signature UUID -> account addresses.
func readAccountsMap(dir string) map[string][]string {
	data, ok := readPlistXML(filepath.Join(dir, "AccountsMap.plist"))
	if !ok {
		return nil
	}
	out := map[string][]string{}
	for _, m := range reAccount.FindAllStringSubmatch(data, -1) {
		email := accountEmail(htmlpkg.UnescapeString(m[1]))
		if email == "" {
			continue
		}
		for _, s := range rePlistValue.FindAllStringSubmatch(m[2], -1) {
			uid := strings.ToUpper(strings.TrimSpace(s[1]))
			out[uid] = append(out[uid], email)
		}
	}
	return out
}

// accountEmail extracts the address from a Mail account URL such as
// "imap://thomas%40squirrel.fr@ssl0.ovh.net/" or "imap://john%40gmail.com/".
func accountEmail(accountURL string) string {
	s := accountURL
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?"); i >= 0 {
		s = s[:i]
	}
	if u, err := url.PathUnescape(s); err == nil {
		s = u
	}
	if strings.Count(s, "@") >= 2 {
		s = s[:strings.LastIndex(s, "@")]
	}
	if !strings.Contains(s, "@") {
		return ""
	}
	return strings.ToLower(s)
}

// ---------------------------------------------------------------------------
// Parsing
// ---------------------------------------------------------------------------

// Parse decodes a .mailsignature: a single text/html part (often
// quoted-printable) or a multipart/related with inline images.
func Parse(data []byte) Parsed {
	entity, err := message.Read(bytes.NewReader(data))
	if err != nil && !message.IsUnknownCharset(err) {
		// Not MIME: header block (if any) + body.
		s := strings.ReplaceAll(string(data), "\r\n", "\n")
		if i := strings.Index(s, "\n\n"); i >= 0 {
			s = s[i+2:]
		}
		return Parsed{HTML: cleanHTML(s)}
	}
	var htm, text strings.Builder
	var images []Image
	walk(entity, &htm, &text, &images, 0)

	p := Parsed{Images: images}
	switch {
	case strings.TrimSpace(htm.String()) != "":
		p.HTML = cleanHTML(htm.String())
	case strings.TrimSpace(text.String()) != "":
		p.HTML = textToHTML(strings.TrimRight(text.String(), "\r\n"))
	}
	return p
}

func walk(e *message.Entity, htm, text *strings.Builder, images *[]Image, depth int) {
	if depth > 8 {
		return
	}
	if mr := e.MultipartReader(); mr != nil {
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				return
			}
			if err != nil && !(message.IsUnknownCharset(err) && part != nil) {
				return
			}
			walk(part, htm, text, images, depth+1)
		}
	}
	ct, _, _ := e.Header.ContentType()
	body, err := io.ReadAll(e.Body)
	if err != nil {
		return
	}
	switch {
	case ct == "text/html":
		htm.Write(body)
	case ct == "text/plain":
		text.Write(body)
	case strings.HasPrefix(ct, "image/"):
		if cid := strings.Trim(strings.TrimSpace(e.Header.Get("Content-Id")), "<>"); cid != "" {
			*images = append(*images, Image{CID: cid, ContentType: ct, Data: body})
		}
	}
}

var (
	reBody   = regexp.MustCompile(`(?is)<body[^>]*>(.*)</body>`)
	reHead   = regexp.MustCompile(`(?is)<!doctype[^>]*>|<head\b.*?</head>|</?html\b[^>]*>|</?body\b[^>]*>`)
	reObject = regexp.MustCompile(`(?is)<object\b([^>]*)>.*?</object>`)
)

// cleanHTML keeps the inner content of a full document and rewrites Apple's
// <object type="application/x-apple-msg-attachment" data="cid:..."> image
// embeds into standard <img> tags (Gmail and Outlook ignore <object>).
func cleanHTML(s string) string {
	if m := reBody.FindStringSubmatch(s); m != nil {
		s = m[1]
	} else {
		s = reHead.ReplaceAllString(s, "")
	}
	s = reObject.ReplaceAllStringFunc(s, func(tag string) string {
		attrs := reObject.FindStringSubmatch(tag)[1]
		data := attrValue(attrs, "data")
		if !strings.HasPrefix(strings.ToLower(data), "cid:") {
			return tag
		}
		img := `<img src="` + data + `" alt=""`
		if w := attrValue(attrs, "width"); w != "" {
			img += ` width="` + w + `"`
		}
		if h := attrValue(attrs, "height"); h != "" {
			img += ` height="` + h + `"`
		}
		return img + ` style="display:inline-block;border:0;">`
	})
	return strings.TrimSpace(s)
}

func attrValue(attrs, name string) string {
	re := regexp.MustCompile(`(?i)\b` + name + `\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	m := re.FindStringSubmatch(attrs)
	if m == nil {
		return ""
	}
	for _, v := range m[1:] {
		if v != "" {
			return v
		}
	}
	return ""
}

func textToHTML(s string) string {
	s = htmlpkg.EscapeString(s)
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "<br>\n")
}

// ---------------------------------------------------------------------------
// Import
// ---------------------------------------------------------------------------

// Import converts the Mail signature at src into the mailbridge format: it
// writes signatures/<accountID>.html and its images (signatures/<accountID>-
// sig<n>.<ext>) under the config directory, and returns the signature config
// to store on the account. Images that the signature loader would reject
// (format, size) are dropped from the HTML so that sending never fails.
// skipped lists the dropped images.
func Import(src, accountID string) (sig *config.SignatureConfig, skipped []string, err error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", src, err)
	}
	p := Parse(data)
	if strings.TrimSpace(p.HTML) == "" {
		return nil, nil, errors.New("empty signature")
	}

	dir, err := config.Dir()
	if err != nil {
		return nil, nil, err
	}
	sigDir := filepath.Join(dir, "signatures")
	if err := os.MkdirAll(sigDir, 0o700); err != nil {
		return nil, nil, err
	}
	base := safeName(accountID)

	// Remove the files of a previous import of this account.
	old, _ := filepath.Glob(filepath.Join(sigDir, base+"-sig*"))
	for _, f := range old {
		os.Remove(f)
	}

	sig = &config.SignatureConfig{HTMLFile: filepath.Join("signatures", base+".html")}
	referenced := map[string]bool{}
	for _, m := range reCID.FindAllStringSubmatch(p.HTML, -1) {
		referenced[m[1]] = true
	}

	// Save the usable images under short cids (sig1, sig2...).
	rename := map[string]string{}
	for _, img := range p.Images {
		if !referenced[img.CID] || rename[img.CID] != "" {
			continue // not shown by the signature, or duplicate
		}
		ct := img.ContentType
		if mt, _, err := mime.ParseMediaType(ct); err == nil {
			ct = mt
		}
		if !supportedImageTypes[ct] {
			ct = http.DetectContentType(img.Data)
		}
		if !supportedImageTypes[ct] || len(img.Data) > maxImageSize {
			skipped = append(skipped, fmt.Sprintf("%s (%s, %d bytes)", img.CID, img.ContentType, len(img.Data)))
			continue
		}
		cid := fmt.Sprintf("sig%d", len(rename)+1)
		name := fmt.Sprintf("%s-%s%s", base, cid, extension(ct))
		if err := os.WriteFile(filepath.Join(sigDir, name), img.Data, 0o600); err != nil {
			return nil, nil, err
		}
		rename[img.CID] = cid
		sig.Images = append(sig.Images, config.SignatureImage{CID: cid, Path: filepath.Join("signatures", name), ContentType: ct})
	}

	// Drop the images that cannot be sent (unsupported or missing), then
	// rewrite the remaining cid: references in a single pass.
	html := p.HTML
	for cid := range referenced {
		if rename[cid] == "" {
			html = removeImage(html, cid)
			if !containsCID(p.Images, cid) {
				skipped = append(skipped, cid+" (missing from the signature file)")
			}
		}
	}
	html = reCID.ReplaceAllStringFunc(html, func(m string) string {
		if n, ok := rename[m[len("cid:"):]]; ok {
			return "cid:" + n
		}
		return m
	})
	sort.Strings(skipped)

	if err := os.WriteFile(filepath.Join(dir, sig.HTMLFile), []byte(html+"\n"), 0o600); err != nil {
		return nil, nil, err
	}
	return sig, skipped, nil
}

// Same pattern as the MIME composer (internal/smtp/compose.go).
var reCID = regexp.MustCompile(`(?i)\bcid:([^"'\s>)]+)`)

func containsCID(images []Image, cid string) bool {
	for _, img := range images {
		if img.CID == cid {
			return true
		}
	}
	return false
}

// removeImage drops the <img> tags showing cid.
func removeImage(html, cid string) string {
	re := regexp.MustCompile(`(?is)<img\b[^>]*?cid:` + regexp.QuoteMeta(cid) + `(?:["'\s)][^>]*)?>`)
	return re.ReplaceAllString(html, "")
}

func extension(ct string) string {
	switch ct {
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	default:
		return ".png"
	}
}

var reUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// safeName turns an account ID into a file name ("Squirrel Thomas" -> "Squirrel-Thomas").
func safeName(id string) string {
	s := strings.Trim(reUnsafe.ReplaceAllString(id, "-"), "-.")
	if s == "" {
		return "account"
	}
	return s
}
