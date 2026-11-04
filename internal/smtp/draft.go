package smtp

import (
	"bytes"
	"encoding/hex"
	"strings"
)

// Apple Mail only treats a message of the Drafts folder as one of its own
// drafts when it carries these headers. Without them, sending the draft from
// Mail sends a copy and leaves the original in Drafts. Mail removes them when
// it sends; send_draft does the same (StripDraftHeaders).
const (
	hdrDraftType = "X-Uniform-Type-Identifier"
	hdrDraftUUID = "X-Universally-Unique-Identifier"
	draftTypeVal = "com.apple.mail-draft"
)

// draftOnlyHeaders are removed before a draft is sent.
var draftOnlyHeaders = []string{
	hdrDraftType,
	hdrDraftUUID,
	"X-Apple-Base-Url",
	"X-Apple-Mail-Remote-Attachments",
	"X-Apple-Windows-Friendly",
	"X-Apple-Mail-Plain-Text-Draft",
}

// MarkDraft adds the draft headers to a message built by BuildMessage or
// BuildReply, before it is appended to the Drafts folder.
func MarkDraft(msg []byte) []byte {
	var b bytes.Buffer
	b.WriteString(hdrDraftType + ": " + draftTypeVal + "\r\n")
	b.WriteString(hdrDraftUUID + ": " + newUUID() + "\r\n")
	b.Write(msg)
	return b.Bytes()
}

// StripDraftHeaders removes the draft-only headers (and their folded
// continuation lines) from the header block of a raw message. The body is
// left untouched.
func StripDraftHeaders(msg []byte) []byte {
	end, sepLen := headerEnd(msg)
	if end < 0 {
		return msg
	}
	head := string(msg[:end])
	nl := "\r\n"
	if !strings.Contains(head, "\r\n") {
		nl = "\n"
	}
	lines := strings.Split(head, nl)
	out := make([]string, 0, len(lines))
	skipping := false
	for _, line := range lines {
		if line != "" && (line[0] == ' ' || line[0] == '\t') {
			if !skipping {
				out = append(out, line)
			}
			continue
		}
		skipping = isDraftOnly(line)
		if !skipping {
			out = append(out, line)
		}
	}
	var b bytes.Buffer
	b.WriteString(strings.Join(out, nl))
	b.Write(msg[end : end+sepLen])
	b.Write(msg[end+sepLen:])
	return b.Bytes()
}

func isDraftOnly(line string) bool {
	i := strings.IndexByte(line, ':')
	if i <= 0 {
		return false
	}
	name := strings.TrimSpace(line[:i])
	for _, h := range draftOnlyHeaders {
		if strings.EqualFold(name, h) {
			return true
		}
	}
	return false
}

// headerEnd returns the offset of the blank line ending the header block and
// the length of that separator.
func headerEnd(msg []byte) (int, int) {
	crlf := bytes.Index(msg, []byte("\r\n\r\n"))
	lf := bytes.Index(msg, []byte("\n\n"))
	switch {
	case crlf >= 0 && (lf < 0 || crlf <= lf):
		return crlf, 4
	case lf >= 0:
		return lf, 2
	}
	return -1, 0
}

// newUUID returns a random (version 4) UUID in upper case, the form Mail uses.
func newUUID() string {
	b := randomBytes(16)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := strings.ToUpper(hex.EncodeToString(b))
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
