package smtp

import (
	"bytes"
	"io"
	"strings"
	"testing"

	message "github.com/emersion/go-message"
)

func TestBuildReply(t *testing.T) {
	msg := string(BuildReply("me@example.com", []string{"Bob <bob@example.com>"}, nil,
		"Projet", Body{Text: "Salut"}, "<parent@x>", "<root@x> <parent@x>", nil))

	for _, want := range []string{
		"Subject: Re: Projet\r\n",
		"In-Reply-To: <parent@x>\r\n",
		"References: <root@x> <parent@x>\r\n",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}

	again := string(BuildReply("me@example.com", nil, nil, "RE: Projet", Body{Text: "x"}, "", "", nil))
	if !strings.Contains(again, "Subject: RE: Projet\r\n") {
		t.Errorf("subject re-prefixed:\n%s", again)
	}
}

// leaf is a decoded MIME leaf part with its position in the tree.
type leaf struct {
	path      string // e.g. "multipart/mixed>multipart/related>image/png"
	header    message.Header
	body      string // decoded, CRLF normalized to LF
	raw       []byte // decoded
	mediaType string
}

func walk(t *testing.T, e *message.Entity, prefix string, out *[]leaf) {
	t.Helper()
	mt, _, err := e.Header.ContentType()
	if err != nil {
		t.Fatalf("content type: %v", err)
	}
	path := mt
	if prefix != "" {
		path = prefix + ">" + mt
	}
	if mr := e.MultipartReader(); mr != nil {
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				return
			}
			if err != nil {
				t.Fatalf("next part under %s: %v", path, err)
			}
			walk(t, p, path, out)
		}
	}
	data, err := io.ReadAll(e.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	*out = append(*out, leaf{path: path, header: e.Header, body: strings.ReplaceAll(string(data), "\r\n", "\n"), raw: data, mediaType: mt})
}

func parse(t *testing.T, raw []byte) []leaf {
	t.Helper()
	e, err := message.Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, raw)
	}
	var leaves []leaf
	walk(t, e, "", &leaves)
	return leaves
}

func paths(leaves []leaf) []string {
	var p []string
	for _, l := range leaves {
		p = append(p, l.path)
	}
	return p
}

var png = []byte("\x89PNG\r\n\x1a\nfake")

func TestSignatureStructure(t *testing.T) {
	sig := &Signature{
		Text: "Édouard\nSquirrel",
		HTML: `<html><head><style>p{}</style></head><body><p>Édouard</p><img src="cid:logo" width="120" height="40" alt="Squirrel"></body></html>`,
		Images: []InlineImage{
			{Name: "logo", Filename: "logo.png", ContentType: "image/png", Data: png},
			{Name: "unused", Filename: "unused.png", ContentType: "image/png", Data: png},
		},
	}
	body := Body{Text: "Bonjour,\n\nVoici le devis.", Signature: sig,
		Quote: &Quote{Header: "On Mon, Bob wrote:", Text: "Une question <urgente>"}}
	att := []Attachment{{Filename: "devis été.pdf", ContentType: "application/pdf", Data: []byte("%PDF")}}

	leaves := parse(t, BuildMessage("Me <me@squirrel.fr>", []string{"bob@x.fr"}, nil, "Devis", body, att, nil))

	want := []string{
		"multipart/mixed>multipart/related>multipart/alternative>text/plain",
		"multipart/mixed>multipart/related>multipart/alternative>text/html",
		"multipart/mixed>multipart/related>image/png",
		"multipart/mixed>application/pdf",
	}
	if got := paths(leaves); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("structure:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	plain, htm, img, pdf := leaves[0].body, leaves[1].body, leaves[2], leaves[3]

	// Signature before the quote, with the RFC 3676 delimiter.
	if i, j := strings.Index(plain, "-- \nÉdouard"), strings.Index(plain, "> Une question"); i < 0 || j < i {
		t.Errorf("text signature missing or after quote:\n%s", plain)
	}
	if i, j := strings.Index(htm, "mailbridge-signature"), strings.Index(htm, "<blockquote"); i < 0 || j < i {
		t.Errorf("html signature missing or after quote:\n%s", htm)
	}
	if strings.Contains(htm, "<style>") || strings.Count(htm, "<html>") != 1 {
		t.Errorf("signature document not reduced to its body:\n%s", htm)
	}
	if !strings.Contains(htm, "Une question &lt;urgente&gt;") {
		t.Errorf("quote not escaped:\n%s", htm)
	}
	if strings.Contains(htm, `src="data:`) || strings.Contains(htm, `src="http`) {
		t.Errorf("html must not reference external or base64 images:\n%s", htm)
	}

	// cid in the HTML matches the image Content-ID.
	cid := strings.Trim(img.header.Get("Content-Id"), "<>")
	if !strings.HasPrefix(cid, "logo.") || !strings.HasSuffix(cid, "@squirrel.fr") {
		t.Errorf("content-id %q", cid)
	}
	if !strings.Contains(htm, `src="cid:`+cid+`"`) {
		t.Errorf("html does not reference cid:%s:\n%s", cid, htm)
	}
	if d := img.header.Get("Content-Disposition"); !strings.HasPrefix(d, "inline") {
		t.Errorf("image disposition %q", d)
	}
	if !bytes.Equal(img.raw, png) {
		t.Errorf("image data corrupted")
	}

	if d := pdf.header.Get("Content-Disposition"); !strings.HasPrefix(d, "attachment") || !strings.Contains(d, "filename*=") {
		t.Errorf("attachment disposition %q", d)
	}
}

func TestSignatureVariants(t *testing.T) {
	cases := []struct {
		name string
		sig  *Signature
		want []string
	}{
		{"none", nil, []string{"text/plain"}},
		{"text only", &Signature{Text: "Ed"}, []string{"text/plain"}},
		{"html without image", &Signature{Text: "Ed", HTML: "<b>Ed</b>"},
			[]string{"multipart/alternative>text/plain", "multipart/alternative>text/html"}},
		{"image not referenced", &Signature{Text: "Ed", HTML: "<b>Ed</b>",
			Images: []InlineImage{{Name: "logo", ContentType: "image/png", Data: png}}},
			[]string{"multipart/alternative>text/plain", "multipart/alternative>text/html"}},
	}
	for _, c := range cases {
		leaves := parse(t, BuildMessage("me@x.fr", []string{"a@x.fr"}, nil, "S", Body{Text: "Hi", Signature: c.sig}, nil, nil))
		if got := paths(leaves); strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
		if c.sig != nil && !strings.Contains(leaves[0].body, "-- \nEd") {
			t.Errorf("%s: text signature missing:\n%s", c.name, leaves[0].body)
		}
	}
}

func TestLongASCIILinesAreEncoded(t *testing.T) {
	long := strings.Repeat("a", 1200)
	raw := BuildMessage("me@x.fr", []string{"a@x.fr"}, nil, "S", Body{Text: long}, nil, nil)
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line of %d chars exceeds RFC 5322 limit", len(line))
		}
	}
	if got := parse(t, raw)[0].body; got != long {
		t.Errorf("round trip lost content")
	}
}

func TestQuoteKeepsOriginalHTML(t *testing.T) {
	orig := `<html><head><style>.x{}</style></head><body><p>Bonjour</p>` +
		`<table><tr><td style="height:20px">Thomas</td></tr></table>` +
		`<img src="cid:old-logo@x" alt="logo"><img src="https://x.fr/a.png"><script>alert(1)</script></body></html>`
	body := Body{Text: "Réponse", Quote: &Quote{Header: "On Mon, Bob wrote:", Text: "Bonjour\nThomas", HTML: orig}}

	leaves := parse(t, BuildMessage("me@squirrel.fr", []string{"bob@x.fr"}, nil, "Re: X", body, nil, nil))
	if got := strings.Join(paths(leaves), ","); got != "multipart/alternative>text/plain,multipart/alternative>text/html" {
		t.Fatalf("structure: %s", got)
	}
	plain, htm := leaves[0].body, leaves[1].body
	if !strings.Contains(plain, "> Bonjour\n> Thomas") {
		t.Errorf("plain quote:\n%s", plain)
	}
	for _, want := range []string{`<table><tr><td style="height:20px">Thomas</td></tr></table>`, `src="https://x.fr/a.png"`} {
		if !strings.Contains(htm, want) {
			t.Errorf("quoted html lost %q:\n%s", want, htm)
		}
	}
	for _, bad := range []string{"cid:old-logo", "<script", "<style>"} {
		if strings.Contains(htm, bad) {
			t.Errorf("quoted html keeps %q:\n%s", bad, htm)
		}
	}
	// The quote container carries no style: the quoted HTML does not inherit
	// our font size and line height.
	if !strings.Contains(htm, "<div class=\"gmail_quote\">\n") {
		t.Errorf("quote container is styled:\n%s", htm)
	}
}
