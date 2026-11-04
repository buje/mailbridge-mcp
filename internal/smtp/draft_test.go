package smtp

import (
	"strings"
	"testing"
)

func TestMarkAndStripDraft(t *testing.T) {
	msg := BuildMessage("me@x.fr", []string{"bob@x.fr"}, nil, "Sujet", Body{Text: "Bonjour\r\nX-Uniform-Type-Identifier: dans le corps"}, nil, nil)
	draft := string(MarkDraft(msg))
	if !strings.HasPrefix(draft, "X-Uniform-Type-Identifier: com.apple.mail-draft\r\nX-Universally-Unique-Identifier: ") {
		t.Fatalf("draft headers missing:\n%s", draft[:200])
	}
	uuid := strings.SplitN(strings.SplitN(draft, "X-Universally-Unique-Identifier: ", 2)[1], "\r\n", 2)[0]
	if len(uuid) != 36 || uuid != strings.ToUpper(uuid) || uuid[14] != '4' {
		t.Errorf("uuid %q", uuid)
	}

	// Mail adds its own headers, some folded, when it saves the draft again.
	edited := strings.Replace(draft, "MIME-Version: 1.0\r\n",
		"X-Apple-Base-Url: x-msg://4/\r\nX-Apple-Mail-Remote-Attachments: YES\r\nX-Apple-Windows-Friendly:\r\n 1\r\nMIME-Version: 1.0\r\n", 1)
	sent := string(StripDraftHeaders([]byte(edited)))
	if sent != string(msg) {
		t.Errorf("strip did not restore the message:\n%q\nwant\n%q", sent, string(msg))
	}

	lf := strings.ReplaceAll(draft, "\r\n", "\n")
	if got := string(StripDraftHeaders([]byte(lf))); got != strings.ReplaceAll(string(msg), "\r\n", "\n") {
		t.Errorf("LF message not stripped:\n%s", got)
	}
}
