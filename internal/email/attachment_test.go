package email

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestFallbackFilename(t *testing.T) {
	cases := []struct {
		contentType string
		want        string
	}{
		{"text/calendar", "invite.ics"},
		{"image/png", "attachment.png"},
		{"image/jpeg", "attachment.jpeg"},
		{"application/pdf", "attachment.pdf"},
		{"application/x-unknown-thing", "attachment.bin"},
		{"", "attachment.bin"},
	}
	for _, c := range cases {
		if got := FallbackFilename(c.contentType); got != c.want {
			t.Errorf("FallbackFilename(%q) = %q, want %q", c.contentType, got, c.want)
		}
	}
}

// inviteMessage builds a multipart/mixed message with a text/html body and a
// nameless text/calendar part carrying an Outlook-style Content-ID — the
// #370 meeting-invite shape.
func inviteMessage(ics []byte) []byte {
	var buf bytes.Buffer
	boundary := "BOUNDARY123"
	fmt.Fprintf(&buf, "From: a@example.com\r\n")
	fmt.Fprintf(&buf, "To: b@example.com\r\n")
	fmt.Fprintf(&buf, "Subject: Meeting\r\n")
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", boundary)
	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	fmt.Fprintf(&buf, "Content-Type: text/html; charset=utf-8\r\n\r\n")
	fmt.Fprintf(&buf, "<p>You're invited</p>\r\n")
	fmt.Fprintf(&buf, "--%s\r\n", boundary)
	fmt.Fprintf(&buf, "Content-Type: text/calendar; method=REQUEST; charset=utf-8\r\n")
	fmt.Fprintf(&buf, "Content-ID: <ABC123DEF456@SV1.prod.outlook.com>\r\n\r\n")
	buf.Write(ics)
	fmt.Fprintf(&buf, "\r\n--%s--\r\n", boundary)
	return buf.Bytes()
}

func TestExtractAttachments_CalendarInviteNamedICS(t *testing.T) {
	ics := []byte("BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nEND:VCALENDAR")
	raw := inviteMessage(ics)

	e := NewAttachmentExtractor()
	atts, err := e.ExtractAttachments("msg-1", raw)
	if err != nil {
		t.Fatalf("ExtractAttachments error: %v", err)
	}
	if len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(atts))
	}
	att := atts[0]
	if att.Attachment.Filename != "invite.ics" {
		t.Errorf("filename = %q, want invite.ics", att.Attachment.Filename)
	}
	if !att.Attachment.IsInline {
		t.Errorf("expected invite part (Content-ID, no disposition) to be inline")
	}
	if !strings.Contains(att.Attachment.ContentID, "prod.outlook.com") {
		t.Errorf("content id not preserved: %q", att.Attachment.ContentID)
	}
}

// The downloader locates parts by comparing the STORED filename against its
// own fallback synthesis — this proves extractor and downloader share it
// (a divergence would break Save/Open of nameless attachments).
func TestExtractAttachmentContent_FallbackNameLockstep(t *testing.T) {
	ics := []byte("BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nEND:VCALENDAR")
	raw := inviteMessage(ics)

	d := NewAttachmentDownloader(t.TempDir())
	got, err := d.ExtractAttachmentContent(raw, "invite.ics")
	if err != nil {
		t.Fatalf("ExtractAttachmentContent(invite.ics) error: %v", err)
	}
	if !bytes.Equal(got, ics) {
		t.Errorf("content mismatch: got %q, want %q", got, ics)
	}
}

// go-message undoes the Content-Transfer-Encoding while reading, so the
// extractor and the downloader must not decode again: a second
// quoted-printable pass turns "PARTSTAT=ACCEPTED" into "PARTSTAT\xacCEPTED".
func TestAttachmentContent_DecodedOnce(t *testing.T) {
	ics := []byte("BEGIN:VCALENDAR\r\nATTENDEE;PARTSTAT=ACCEPTED:mailto:me@example.com\r\nEND:VCALENDAR")
	bin := []byte{0x00, 0xff, '=', 'A', 'C', 0x10}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n")
	fmt.Fprintf(&buf, "--B\r\nContent-Type: text/plain\r\n\r\nbody\r\n")
	fmt.Fprintf(&buf, "--B\r\nContent-Type: text/calendar; method=REPLY\r\nContent-Transfer-Encoding: quoted-printable\r\n")
	fmt.Fprintf(&buf, "Content-Disposition: attachment; filename=reply.ics\r\n\r\n")
	fmt.Fprintf(&buf, "BEGIN:VCALENDAR\r\nATTENDEE;PARTSTAT=3DACCEPTED:mailto:me@example.com\r\nEND:VCALENDAR\r\n")
	fmt.Fprintf(&buf, "--B\r\nContent-Type: application/octet-stream\r\nContent-Transfer-Encoding: base64\r\n")
	fmt.Fprintf(&buf, "Content-Disposition: attachment; filename=data.bin\r\n\r\nAP89QUMQ\r\n--B--\r\n")
	raw := buf.Bytes()

	want := map[string][]byte{"reply.ics": ics, "data.bin": bin}

	atts, err := NewAttachmentExtractor().ExtractAttachments("msg-1", raw)
	if err != nil {
		t.Fatalf("ExtractAttachments: %v", err)
	}
	if len(atts) != len(want) {
		t.Fatalf("got %d attachments, want %d", len(atts), len(want))
	}
	for _, a := range atts {
		if !bytes.Equal(a.Content, want[a.Attachment.Filename]) {
			t.Errorf("extractor %s = %q, want %q", a.Attachment.Filename, a.Content, want[a.Attachment.Filename])
		}
	}

	d := NewAttachmentDownloader(t.TempDir())
	for name, w := range want {
		got, err := d.ExtractAttachmentContent(raw, name)
		if err != nil {
			t.Fatalf("ExtractAttachmentContent(%s): %v", name, err)
		}
		if !bytes.Equal(got, w) {
			t.Errorf("downloader %s = %q, want %q", name, got, w)
		}
	}
}
