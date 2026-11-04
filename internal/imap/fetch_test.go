package imap

import (
	"strings"
	"testing"
)

func TestHTMLToText(t *testing.T) {
	in := `<html><head><style>p{color:red}</style></head><body>
	<p>Bonjour,</p><div>caroline vous a contact&eacute;.</div>
	<ul><li>Pr&eacute;nom : Caro</li><li>Ville : Pessac</li></ul>
	<a href="mailto:x@messagerie.leboncoin.fr">R&eacute;pondre</a>
	<script>alert(1)</script></body></html>`

	got := HTMLToText(in)

	for _, want := range []string{"Bonjour,", "caroline vous a contacté.", "- Prénom : Caro", "- Ville : Pessac"} {
		if !strings.Contains(got, want) {
			t.Errorf("HTMLToText() manque %q\n--- got ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "alert(1)") || strings.Contains(got, "color:red") {
		t.Errorf("HTMLToText() garde du script/style:\n%s", got)
	}
	if !strings.Contains(got, "Répondre (mailto:x@messagerie.leboncoin.fr)") {
		t.Errorf("HTMLToText() perd l'URL du lien:\n%s", got)
	}
}

func TestExtractLinks(t *testing.T) {
	in := `<a href="mailto:relay@messagerie.leboncoin.fr">Répondre</a>
	<a href="tel:+33612345678">Appeler</a>
	<a href="https://www.leboncoin.fr/ad/123">Voir l'annonce</a>
	<a href="https://l.mailtrack.com/l/abcdef?w=zz">.</a>
	<a href="https://example.com/unsubscribe">Se désabonner</a>
	<a href="#">ancre</a>
	<a href="https://www.leboncoin.fr/ad/123">doublon</a>`

	got := ExtractLinks(in)

	if len(got) < 3 {
		t.Fatalf("ExtractLinks() = %v, attendu au moins 3 liens", got)
	}
	// Les mailto/tel doivent précéder les URL http.
	if !strings.HasPrefix(got[0], "mailto:") {
		t.Errorf("ExtractLinks() ne priorise pas les mailto: %v", got)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"mailto:relay@messagerie.leboncoin.fr", "tel:+33612345678", "https://www.leboncoin.fr/ad/123"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ExtractLinks() manque %q: %v", want, got)
		}
	}
	for _, unwanted := range []string{"mailtrack.com", "unsubscribe"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("ExtractLinks() garde le tracker %q: %v", unwanted, got)
		}
	}
	if strings.Count(joined, "leboncoin.fr/ad/123") != 1 {
		t.Errorf("ExtractLinks() ne déduplique pas: %v", got)
	}
}

// mail transféré : multipart/mixed contenant un message/rfc822 dont le corps
// est lui-même multipart/alternative HTML-only.
const forwardedMail = "MIME-Version: 1.0\r\n" +
	"Subject: Fwd: PROJET\r\n" +
	"Content-Type: multipart/mixed; boundary=\"OUT\"\r\n" +
	"\r\n" +
	"--OUT\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Voir ci-dessous.\r\n" +
	"--OUT\r\n" +
	"Content-Type: message/rfc822\r\n" +
	"\r\n" +
	"From: leboncoin <no-reply@leboncoin.fr>\r\n" +
	"Reply-To: relay@messagerie.leboncoin.fr\r\n" +
	"Subject: Nouveau message\r\n" +
	"Content-Type: multipart/alternative; boundary=\"IN\"\r\n" +
	"\r\n" +
	"--IN\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<p>Nom : Jicie</p><a href=\"mailto:relay@messagerie.leboncoin.fr\">R&eacute;pondre</a>\r\n" +
	"--IN--\r\n" +
	"--OUT\r\n" +
	"Content-Type: application/pdf; name=\"dossier.pdf\"\r\n" +
	"Content-Disposition: attachment; filename=\"dossier.pdf\"\r\n" +
	"\r\n" +
	"%PDF-\r\n" +
	"--OUT--\r\n"

func TestParseBodyWalksForwardedMessage(t *testing.T) {
	p := &ParsedEmail{}
	p.parseBody([]byte(forwardedMail), 0, BodyFormatAuto)

	if !strings.Contains(p.Body, "Voir ci-dessous.") {
		t.Errorf("partie text/plain de premier niveau perdue:\n%s", p.Body)
	}
	if !strings.Contains(p.Body, "Nom : Jicie") {
		t.Errorf("le HTML du message transféré n'est pas remonté:\n%s", p.Body)
	}
	if !strings.Contains(p.Body, "Message transféré") || !strings.Contains(p.Body, "relay@messagerie.leboncoin.fr") {
		t.Errorf("les en-têtes du message transféré sont perdus:\n%s", p.Body)
	}
	if p.HTMLBody == "" || !strings.Contains(p.HTMLBody, "<p>Nom : Jicie</p>") {
		t.Errorf("HTMLBody vide ou incomplet: %q", p.HTMLBody)
	}
	if len(p.Links) == 0 || !strings.Contains(p.Links[0], "relay@messagerie.leboncoin.fr") {
		t.Errorf("relais de réponse non extrait: %v", p.Links)
	}
	if len(p.Attachments) != 1 || p.Attachments[0].Filename != "dossier.pdf" {
		t.Errorf("pièce jointe non détectée: %+v", p.Attachments)
	}
}

func TestParseBodyFormats(t *testing.T) {
	p := &ParsedEmail{}
	p.parseBody([]byte(forwardedMail), 0, BodyFormatHTML)
	if !strings.Contains(p.Body, "<p>Nom : Jicie</p>") {
		t.Errorf("format html ne renvoie pas la source HTML:\n%s", p.Body)
	}

	p = &ParsedEmail{}
	p.parseBody([]byte(forwardedMail), 0, BodyFormatBoth)
	if !strings.Contains(p.Body, "HTML source") || !strings.Contains(p.Body, "Nom : Jicie") {
		t.Errorf("format both incomplet:\n%s", p.Body)
	}
}

const alternativeMail = "From: a@x.fr\r\nSubject: T\r\nMIME-Version: 1.0\r\n" +
	"Content-Type: multipart/alternative; boundary=\"b1\"\r\n\r\n" +
	"--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nBonjour texte\r\n" +
	"--b1\r\nContent-Type: multipart/related; boundary=\"b2\"\r\n\r\n" +
	"--b2\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<html><body><p>Bonjour html</p></body></html>\r\n" +
	"--b2\r\nContent-Type: image/png\r\nContent-ID: <logo>\r\n\r\nPNG\r\n--b2--\r\n" +
	"--b1--\r\n"

func TestParseBodyAlternativeNotDuplicated(t *testing.T) {
	p := &ParsedEmail{}
	p.parseBody([]byte(alternativeMail), 0, BodyFormatAuto)
	if p.TextBody != "Bonjour texte" {
		t.Errorf("text body = %q, want only the text/plain alternative", p.TextBody)
	}
	if !strings.Contains(p.HTMLBody, "<p>Bonjour html</p>") {
		t.Errorf("html body = %q", p.HTMLBody)
	}

	htmlOnly := strings.Replace(alternativeMail, "--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nBonjour texte\r\n", "", 1)
	p = &ParsedEmail{}
	p.parseBody([]byte(htmlOnly), 0, BodyFormatAuto)
	if !strings.Contains(p.TextBody, "Bonjour html") {
		t.Errorf("text from html = %q", p.TextBody)
	}
}
