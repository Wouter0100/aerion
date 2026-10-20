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

// A bare invite part (no Content-ID, no disposition) inside
// multipart/alternative, as SabreDAV-backed and Nextcloud senders produce it.
func TestExtractAttachments_BareCalendarInvite(t *testing.T) {
	ics := []byte("BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nEND:VCALENDAR")
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: multipart/alternative; boundary=ALT\r\n\r\n")
	fmt.Fprintf(&buf, "--ALT\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Invite</p>\r\n")
	fmt.Fprintf(&buf, "--ALT\r\nContent-Type: text/calendar; charset=utf-8; method=REQUEST\r\n")
	fmt.Fprintf(&buf, "Content-Transfer-Encoding: 8bit\r\n\r\n")
	buf.Write(ics)
	fmt.Fprintf(&buf, "\r\n--ALT--\r\n")
	raw := buf.Bytes()

	atts, err := NewAttachmentExtractor().ExtractAttachments("msg-1", raw)
	if err != nil {
		t.Fatalf("ExtractAttachments error: %v", err)
	}
	if len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(atts))
	}
	if atts[0].Attachment.Filename != "invite.ics" || atts[0].Attachment.IsInline {
		t.Errorf("got filename %q inline %v, want invite.ics not inline",
			atts[0].Attachment.Filename, atts[0].Attachment.IsInline)
	}

	got, err := NewAttachmentDownloader(t.TempDir()).ExtractAttachmentContent(raw, "invite.ics")
	if err != nil {
		t.Fatalf("ExtractAttachmentContent(invite.ics) error: %v", err)
	}
	if !bytes.Equal(got, ics) {
		t.Errorf("content mismatch: got %q, want %q", got, ics)
	}
}
