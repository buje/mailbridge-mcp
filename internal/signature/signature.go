// Package signature resolves a user's email signature at runtime and, during
// setup only, imports signatures from Apple Mail.
//
// Design: the running server (serve) is fully self-contained. It reads the
// signature from a copy stored under the mailbridge config directory
// (~/.config/mailbridge/signatures/) and never touches Apple Mail or needs disk
// access to ~/Library. Importing from Apple Mail happens once, interactively,
// when an account is added (setup): the chosen ".mailsignature" is copied into
// the mailbridge directory. A signature rarely changes once created, so a
// one-time copy is enough.
package signature

import (
	"bytes"
	"errors"
	"fmt"
	htmlpkg "html"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/edouard-claude/mailbridge-mcp/internal/config"
	imappkg "github.com/edouard-claude/mailbridge-mcp/internal/imap"
	smtpsender "github.com/edouard-claude/mailbridge-mcp/internal/smtp"
	message "github.com/emersion/go-message"
)

// Signature is a rendered signature in both representations.
type Signature struct {
	HTML string // HTML fragment, safe to drop into a text/html part
	Text string // plain-text rendering, for the text/plain part
	// Inline holds the images the HTML references via "cid:" (e.g. a logo).
	Inline []smtpsender.InlinePart
}

// Empty reports whether the signature has no usable content.
func (s *Signature) Empty() bool {
	return s == nil || (strings.TrimSpace(s.HTML) == "" && strings.TrimSpace(s.Text) == "")
}

// ConfigDir returns the mailbridge config directory (~/.config/mailbridge).
func ConfigDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "mailbridge")
}

// SignaturesDir returns the directory where imported signatures are stored.
func SignaturesDir() string {
	return filepath.Join(ConfigDir(), "signatures")
}

// Resolve returns the signature described by cfg, or nil when nothing is
// configured. The file path is taken as-is when absolute, otherwise resolved
// relative to the mailbridge config directory. No Apple Mail access is involved.
func Resolve(cfg config.SignatureConfig) (*Signature, error) {
	path := strings.TrimSpace(cfg.File)
	if path == "" {
		return nil, nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(ConfigDir(), path)
	}
	return loadFile(path)
}

// loadFile reads a signature file and renders both representations. Supported
// extensions: .mailsignature (Apple Mail), .html/.htm, .txt.
func loadFile(path string) (*Signature, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read signature %s: %w", path, err)
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".txt":
		text := string(data)
		return &Signature{HTML: textToHTML(text), Text: strings.TrimRight(text, "\r\n")}, nil
	case ".html", ".htm":
		html := appleObjectsToImg(bodyContent(string(data)))
		return &Signature{HTML: html, Text: imappkg.HTMLToText(html)}, nil
	default: // .mailsignature: a MIME entity (possibly multipart/related)
		return parseMailSignature(data), nil
	}
}

// parseMailSignature decodes an Apple Mail ".mailsignature" file. These files
// are MIME entities: a single text/html part (often quoted-printable) or a
// multipart/related holding the HTML plus inline images (logo) referenced via
// "cid:". Transfer encodings and charsets are decoded by go-message.
func parseMailSignature(data []byte) *Signature {
	entity, err := message.Read(bytes.NewReader(data))
	if err != nil && !message.IsUnknownCharset(err) {
		// Not parseable as MIME: fall back to the raw header/body split.
		contentType, body := splitMailSignature(data)
		if strings.Contains(strings.ToLower(contentType), "text/plain") {
			return &Signature{HTML: textToHTML(body), Text: strings.TrimRight(body, "\r\n")}
		}
		return &Signature{HTML: body, Text: imappkg.HTMLToText(body)}
	}

	var html, text strings.Builder
	var inline []smtpsender.InlinePart
	walkSignature(entity, &html, &text, &inline, 0)

	sig := &Signature{Inline: inline}
	switch {
	case strings.TrimSpace(html.String()) != "":
		sig.HTML = appleObjectsToImg(bodyContent(html.String()))
		sig.Text = imappkg.HTMLToText(sig.HTML)
	case strings.TrimSpace(text.String()) != "":
		sig.Text = strings.TrimRight(text.String(), "\r\n")
		sig.HTML = textToHTML(sig.Text)
	}
	return sig
}

// walkSignature collects the decoded HTML/text parts and the inline images of a
// signature entity, recursing into multiparts.
func walkSignature(e *message.Entity, html, text *strings.Builder, inline *[]smtpsender.InlinePart, depth int) {
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
			walkSignature(part, html, text, inline, depth+1)
		}
	}

	ct, params, _ := e.Header.ContentType()
	body, err := io.ReadAll(e.Body)
	if err != nil {
		return
	}
	switch {
	case ct == "text/html":
		html.Write(body)
	case ct == "text/plain":
		text.Write(body)
	case strings.HasPrefix(ct, "image/"):
		cid := strings.Trim(strings.TrimSpace(e.Header.Get("Content-Id")), "<>")
		if cid == "" {
			return
		}
		name := params["name"]
		if _, dp, err := e.Header.ContentDisposition(); err == nil && dp["filename"] != "" {
			name = dp["filename"]
		}
		*inline = append(*inline, smtpsender.InlinePart{
			ContentID:   cid,
			ContentType: ct,
			Filename:    name,
			Data:        body,
		})
	}
}

var (
	reBody   = regexp.MustCompile(`(?is)<body[^>]*>(.*)</body>`)
	reObject = regexp.MustCompile(`(?is)<object\b([^>]*)>.*?</object>`)
)

// attrValue extracts an HTML attribute value (quoted or not) from an attribute
// string such as `height=70 width=250 data="cid:XYZ"`.
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

// appleObjectsToImg rewrites Apple Mail's proprietary image embeds
// (<object type="application/x-apple-msg-attachment" data="cid:...">) into
// standard <img src="cid:..."> tags. Apple Mail renders both, but Gmail,
// Outlook and most other clients only render <img>, so without this the
// recipients would not see the signature images.
func appleObjectsToImg(html string) string {
	return reObject.ReplaceAllStringFunc(html, func(tag string) string {
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
		return img + ` style="border:0; display:inline-block;">`
	})
}

// bodyContent returns the inner content of <body> when the signature is a full
// HTML document, so it can be embedded inside the message HTML.
func bodyContent(html string) string {
	if m := reBody.FindStringSubmatch(html); m != nil {
		return strings.TrimSpace(m[1])
	}
	return strings.TrimSpace(html)
}

// splitMailSignature separates the leading header block of a .mailsignature
// file from its body and returns the declared Content-Type plus the body.
func splitMailSignature(data []byte) (contentType, body string) {
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	idx := strings.Index(s, "\n\n")
	if idx < 0 {
		return "", s
	}
	headers := s[:idx]
	body = s[idx+2:]
	for _, line := range strings.Split(headers, "\n") {
		if strings.HasPrefix(strings.ToLower(line), "content-type:") {
			contentType = strings.TrimSpace(line[len("content-type:"):])
		}
	}
	return contentType, body
}

// ---------------------------------------------------------------------------
// Setup-time Apple Mail import (never called by `serve`).
// ---------------------------------------------------------------------------

// AppleSignature is a signature discovered in the local Apple Mail store.
type AppleSignature struct {
	SourcePath string // absolute path to the .mailsignature file
	Name       string // name given in Apple Mail (from AllSignatures.plist), if known
	Preview    string // short plain-text preview to help the user recognize it
	// Accounts lists the email addresses Mail uses this signature for
	// (from AccountsMap.plist), lowercased.
	Accounts []string
	content  string // decoded HTML + text, lowercased, used for email matching
}

// appleMailSignatureDirs returns the Apple Mail signature directories that exist
// on this machine, plus warnings for locations macOS refused to let us read
// (privacy protection), so the caller can explain an empty result.
func appleMailSignatureDirs() (dirs []string, warnings []string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, []string{fmt.Sprintf("cannot determine home directory: %v", err)}
	}

	// Mail's local data folder first: it is the store Mail actually uses. It
	// is located by direct probing (no listing of ~/Library/Mail, which macOS
	// may protect more strictly than the Signatures folder itself).
	local, _ := localMailSignatureDir(home)
	if local != "" {
		dirs = append(dirs, local)
	} else {
		// Only when Mail's local folder is missing do the secondary copies
		// matter: report the ones macOS refuses to let us read, since
		// filepath.Glob silently ignores unreadable directories.
		for _, r := range []string{
			filepath.Join(home, "Library", "Containers", "com.apple.mail"),
			filepath.Join(home, "Library", "Mobile Documents", "com~apple~mail"),
		} {
			if _, err := os.ReadDir(r); err != nil && errors.Is(err, fs.ErrPermission) {
				warnings = append(warnings, fmt.Sprintf("macOS denied access to %s", r))
			}
		}
	}

	// Then the container and iCloud copies, which can hold stale signatures.
	patterns := []string{
		filepath.Join(home, "Library", "Containers", "com.apple.mail", "Data", "Library", "Mail", "V*", "MailData", "Signatures"),
		filepath.Join(home, "Library", "Mobile Documents", "com~apple~mail", "Data", "V*", "Signatures"),
		filepath.Join(home, "Library", "Mobile Documents", "com~apple~mail", "Data", "V*", "MailData", "Signatures"),
	}
	for _, p := range patterns {
		matches, _ := filepath.Glob(p)
		var existing []string
		for _, m := range matches {
			if info, err := os.Stat(m); err == nil && info.IsDir() {
				existing = append(existing, m)
			}
		}
		// Each macOS upgrade creates a new V<n> folder and leaves the old ones
		// (V2…V9) behind with stale signatures: only the newest one is current.
		if latest := latestVersionDir(existing); latest != "" {
			dirs = append(dirs, latest)
		}
	}
	return dirs, warnings
}

// localMailSignatureDir finds Mail's local Signatures folder by probing
// ~/Library/Mail/V<n>/MailData/Signatures directly, newest version first,
// without listing ~/Library/Mail. denied is true when macOS refuses access.
func localMailSignatureDir(home string) (dir string, denied bool) {
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

var reMailVersion = regexp.MustCompile(`/V(\d+)/`)

// latestVersionDir returns the path with the highest Mail data version
// (".../V10/..." beats ".../V9/..."), or "" when paths is empty.
func latestVersionDir(paths []string) string {
	best, bestV := "", -1
	for _, p := range paths {
		v := -1
		if m := reMailVersion.FindStringSubmatch(filepath.ToSlash(p)); m != nil {
			fmt.Sscanf(m[1], "%d", &v)
		}
		if v > bestV {
			best, bestV = p, v
		}
	}
	return best
}

// DiscoverAppleMail lists the signatures found in the local Apple Mail store
// (local and iCloud-synced). warnings explains an empty result, e.g. when macOS
// privacy protection blocked access to the Mail folders.
//
// Only Mail's active store is read: the first directory (in preference order)
// that has an AllSignatures.plist index. Other copies (iCloud, container, older
// layouts) are ignored because they may contain stale or deleted signatures.
// When no directory has an index, every candidate is scanned. scanned lists
// the directories actually read, so the caller can show where signatures come from.
func DiscoverAppleMail() (sigs []AppleSignature, scanned []string, warnings []string) {
	// Mail's own data folder is the source of truth. If macOS blocks it, do not
	// fall back to iCloud/container copies: they can hold outdated signatures.
	if home, err := os.UserHomeDir(); err == nil {
		if _, denied := localMailSignatureDir(home); denied {
			return nil, nil, []string{ErrMailAccessDenied.Error()}
		}
	}
	dirs, warnings := appleMailSignatureDirs()
	// Several stores can have an index (local, iCloud, container) with
	// different versions of the same signature. Mail writes to the active
	// store, so pick the indexed store holding the most recently modified
	// signature file; on a tie, the earlier (preferred) store wins.
	// AccountsMap.plist (account -> signatures) only lives in Mail's active
	// local store, never in iCloud copies: a store that has it always wins.
	best, bestTime, bestHasMap := "", time.Time{}, false
	for _, d := range dirs {
		if _, ok := readSignatureIndex(d); !ok {
			continue
		}
		hasMap := len(readAccountsMap(d)) > 0
		t := newestSignatureTime(d)
		switch {
		case best == "",
			hasMap && !bestHasMap,
			hasMap == bestHasMap && t.After(bestTime):
			best, bestTime, bestHasMap = d, t, hasMap
		}
	}
	if best != "" {
		return discoverIn([]string{best}), []string{best}, warnings
	}
	return discoverIn(dirs), dirs, warnings
}

// ErrMailAccessDenied is reported when macOS privacy protection prevents
// reading ~/Library/Mail from the app that launched mailbridge.
var ErrMailAccessDenied = errors.New("macOS denied access to ~/Library/Mail (Mail data)")

// newestSignatureTime returns the modification time of the most recently
// modified .mailsignature file in dir (zero time when there is none).
func newestSignatureTime(dir string) time.Time {
	var newest time.Time
	matches, _ := filepath.Glob(filepath.Join(dir, "*.mailsignature"))
	for _, m := range matches {
		if info, err := os.Stat(m); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

// discoverIn scans the given directories for ".mailsignature" files. Split out
// from DiscoverAppleMail so it can be tested against a synthetic directory.
func discoverIn(dirs []string) []AppleSignature {
	var out []AppleSignature
	for _, dir := range dirs {
		// Apple Mail's index of the signatures that still exist. Deleting a
		// signature in Mail removes it from this index but often leaves its
		// .mailsignature file on disk, so the index is the source of truth.
		index, hasIndex := readSignatureIndex(dir)
		accounts := readAccountsMap(dir)
		matches, _ := filepath.Glob(filepath.Join(dir, "*.mailsignature"))
		for _, path := range matches {
			name := ""
			if hasIndex {
				n, ok := index[signatureUID(path)]
				if !ok {
					continue // orphan file of a signature deleted in Mail
				}
				name = n
			}
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			sig := parseMailSignature(data)
			p := preview(sig)
			if name != "" {
				p = name + " — " + p
			}
			out = append(out, AppleSignature{
				SourcePath: path,
				Name:       name,
				Preview:    p,
				Accounts:   accounts[signatureUID(path)],
				content:    strings.ToLower(sig.HTML + "\n" + sig.Text),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].SourcePath < out[j].SourcePath
	})
	return out
}

// signatureUID returns the signature UUID encoded in a .mailsignature file name
// ("<UUID>.mailsignature", or "ubiquitous_<UUID>.mailsignature" for iCloud).
func signatureUID(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return strings.ToUpper(strings.TrimPrefix(base, "ubiquitous_"))
}

var (
	rePlistDict = regexp.MustCompile(`(?s)<dict>(.*?)</dict>`)
	rePlistName = regexp.MustCompile(`(?s)<key>SignatureName</key>\s*<string>(.*?)</string>`)
	rePlistUID  = regexp.MustCompile(`(?s)<key>SignatureUniqueId</key>\s*<string>(.*?)</string>`)
)

// readSignatureIndex reads AllSignatures.plist from a Signatures directory and
// returns UUID -> signature name. ok is false when there is no readable index,
// in which case every .mailsignature file is listed.
func readSignatureIndex(dir string) (index map[string]string, ok bool) {
	data, ok := readPlistXML(filepath.Join(dir, "AllSignatures.plist"))
	if !ok {
		return nil, false
	}
	index = map[string]string{}
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
	if len(index) == 0 {
		return nil, false
	}
	return index, true
}

var (
	reHasAlnum  = regexp.MustCompile(`[\p{L}\p{N}]`)
	reURLOnly   = regexp.MustCompile(`(?i)^(https?://|www\.)\S*$`)
	reGreetings = regexp.MustCompile(`(?i)^(cordialement|bien (à|a) vous|bonne (journée|journee)|regards|best( regards)?|merci)[\s,.!]*$`)
)

// preview renders a short plain-text snippet of a signature so the user can
// tell signatures apart at setup time: greetings ("Cordialement,") and
// separator lines are skipped, and a "[logo]" marker flags inline images.
func preview(sig *Signature) string {
	var lines []string
	for _, l := range strings.Split(sig.Text, "\n") {
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
	if len(sig.Inline) > 0 {
		p += " [logo]"
	}
	if p == "" {
		p = "(empty signature)"
	}
	return p
}

// readPlistXML returns the XML text of a plist file, converting binary plists
// with plutil (always present on macOS; this code only runs at setup time).
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

var reAccountEntry = regexp.MustCompile(`(?s)<key>AccountURL</key>\s*<string>(.*?)</string>\s*<key>Signatures</key>\s*(?:<array/>|<array>(.*?)</array>)`)
var rePlistString = regexp.MustCompile(`(?s)<string>(.*?)</string>`)

// readAccountsMap reads Mail's AccountsMap.plist (which signatures each mail
// account uses) and returns signature UUID -> account email addresses.
func readAccountsMap(dir string) map[string][]string {
	data, ok := readPlistXML(filepath.Join(dir, "AccountsMap.plist"))
	if !ok {
		return nil
	}
	out := map[string][]string{}
	for _, m := range reAccountEntry.FindAllStringSubmatch(data, -1) {
		email := accountEmail(htmlpkg.UnescapeString(m[1]))
		if email == "" {
			continue
		}
		for _, s := range rePlistString.FindAllStringSubmatch(m[2], -1) {
			uid := strings.ToUpper(strings.TrimSpace(s[1]))
			out[uid] = append(out[uid], email)
		}
	}
	return out
}

// accountEmail extracts the email address from a Mail account URL such as
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
	// "user@domain@host": the address is everything before the last "@".
	if strings.Count(s, "@") >= 2 {
		s = s[:strings.LastIndex(s, "@")]
	}
	if !strings.Contains(s, "@") {
		return ""
	}
	return strings.ToLower(s)
}

// SuggestForEmail returns the index of the signature whose content best matches
// email (by full address, then by domain), or -1 when nothing matches. This is
// the simple heuristic to pick the right signature for the account being added.
func SuggestForEmail(sigs []AppleSignature, email string) int {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return -1
	}
	// Best source: the signature Mail itself uses for this account.
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
	// First pass: full address match. Second pass: domain match.
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

// Import copies a discovered signature into the mailbridge signatures directory
// under "<accountID>.mailsignature" and returns the path to store in the config,
// relative to the config directory (e.g. "signatures/pro.mailsignature").
func Import(srcPath, accountID string) (string, error) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return "", fmt.Errorf("read source signature: %w", err)
	}
	dir := SignaturesDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create signatures dir: %w", err)
	}
	// Keep the source extension: it tells loadFile how to parse the copy
	// (.mailsignature MIME entity, .html fragment or .txt).
	ext := strings.ToLower(filepath.Ext(srcPath))
	switch ext {
	case ".mailsignature", ".html", ".htm", ".txt":
	default:
		ext = ".mailsignature"
	}
	// Remove copies with another extension so only one signature remains.
	for _, old := range []string{".mailsignature", ".html", ".htm", ".txt"} {
		if old != ext {
			os.Remove(filepath.Join(dir, accountID+old))
		}
	}
	name := accountID + ext
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		return "", fmt.Errorf("write signature copy: %w", err)
	}
	return filepath.Join("signatures", name), nil
}

// textToHTML escapes a plain-text signature and preserves its line breaks so it
// can be dropped into a text/html part.
func textToHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "<br>\n")
}
