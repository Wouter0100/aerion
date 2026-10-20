package sync

import (
	"strings"
	"testing"
)

const inviteICS = "BEGIN:VCALENDAR\r\nMETHOD:REQUEST\r\nBEGIN:VEVENT\r\nUID:invite-1@example.com\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

// alternativeInvite builds the common iTIP shape: multipart/alternative with an
// HTML body and a nameless text/calendar part with no Content-Disposition.
func alternativeInvite(calendarHeaders string) []byte {
	var sb strings.Builder
	sb.WriteString("MIME-Version: 1.0\r\n")
	sb.WriteString("Content-Type: multipart/alternative; boundary=\"ALT\"\r\n\r\n")
	sb.WriteString("--ALT\r\n")
	sb.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	sb.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	sb.WriteString("<p>You are invited</p>\r\n")
	sb.WriteString("--ALT\r\n")
	sb.WriteString("Content-Type: text/calendar; charset=utf-8; method=REQUEST\r\n")
	sb.WriteString(calendarHeaders)
	sb.WriteString("\r\n")
	sb.WriteString(inviteICS)
	sb.WriteString("--ALT--\r\n")
	return []byte(sb.String())
}

func TestParseMultipartBody_CalendarInvite(t *testing.T) {
	tests := []struct {
		name       string
		headers    string
		wantInline bool
	}{
		{"bare part", "Content-Transfer-Encoding: 8bit\r\n", false},
		{"Outlook Content-ID", "Content-ID: <ABC123@prod.outlook.com>\r\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseAttachments(t, alternativeInvite(tt.headers))

			if len(result.Attachments) != 1 {
				t.Fatalf("expected 1 attachment, got %d: %+v", len(result.Attachments), result.Attachments)
			}
			att := result.Attachments[0]
			if att.Filename != "invite.ics" {
				t.Errorf("filename = %q, want invite.ics", att.Filename)
			}
			if att.ContentType != "text/calendar" {
				t.Errorf("content type = %q, want text/calendar", att.ContentType)
			}
			if att.IsInline != tt.wantInline {
				t.Errorf("IsInline = %v, want %v", att.IsInline, tt.wantInline)
			}
			if !result.HasAttachments {
				t.Error("HasAttachments = false, want true")
			}
			if !strings.Contains(result.BodyHTML, "You are invited") {
				t.Errorf("HTML body lost: %q", result.BodyHTML)
			}
		})
	}
}
