package smtp

import (
	"bytes"
	"io"
	"strings"
	"testing"

	message "github.com/emersion/go-message"
)

func TestBuildMessageMultipartBoundaryIsQuotedAndParseable(t *testing.T) {
	raw := BuildMessage("me@example.com", []string{"you@example.com"}, nil, "Hello", "Bonjour",
		[]Attachment{{Filename: "a.txt", ContentType: "text/plain", Data: []byte("hi")}}, nil)

	if bytes.Contains(raw, []byte("boundary===")) {
		t.Fatalf("boundary must be quoted, got:\n%s", raw)
	}

	// The message must be readable by a strict MIME parser.
	e, err := message.Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("message not parseable: %v", err)
	}
	mr := e.MultipartReader()
	if mr == nil {
		t.Fatal("expected a multipart message")
	}
	parts := 0
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading part %d: %v", parts, err)
		}
		io.Copy(io.Discard, p.Body)
		parts++
	}
	if parts != 2 {
		t.Fatalf("expected 2 parts (text + attachment), got %d", parts)
	}
}

func TestBuildMessageThreadingHeaders(t *testing.T) {
	raw := string(BuildMessage("me@example.com", []string{"you@example.com"}, nil, "Re: Hello", "Bonjour", nil,
		map[string]string{
			"In-Reply-To": "<parent@example.com>",
			"References":  "<root@example.com> <parent@example.com>",
		}))
	if !strings.Contains(raw, "In-Reply-To: <parent@example.com>") {
		t.Fatalf("missing In-Reply-To header:\n%s", raw)
	}
	if !strings.Contains(raw, "References: <root@example.com> <parent@example.com>") {
		t.Fatalf("missing References header:\n%s", raw)
	}
}
