package smtp

import (
	"bytes"
	"io"
	"strings"
	"testing"

	gomessage "github.com/emersion/go-message"
)

const replyICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REPLY\r\nBEGIN:VEVENT\r\nUID:invite-1@example.com\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

func TestToRFC822_IMIP(t *testing.T) {
	msg := &ComposeMessage{
		From:           Address{Address: "me@example.com"},
		To:             []Address{{Address: "organizer@example.com"}},
		Subject:        "Accepted: Meeting",
		TextBody:       "I accepted.",
		HTMLBody:       "<p>ignored</p>",
		CalendarMethod: "reply",
		CalendarBody:   replyICS,
	}
	data, err := msg.ToRFC822()
	if err != nil {
		t.Fatalf("ToRFC822() returned error: %v", err)
	}

	entity, err := gomessage.Read(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("parse message: %v", err)
	}
	if ct, _, _ := entity.Header.ContentType(); ct != "multipart/alternative" {
		t.Fatalf("top-level Content-Type = %q, want multipart/alternative", ct)
	}
	mr := entity.MultipartReader()
	var types []string
	var calendar string
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next part: %v", err)
		}
		ct, params, _ := part.Header.ContentType()
		types = append(types, ct)
		if ct == "text/calendar" {
			if params["method"] != "REPLY" {
				t.Errorf("calendar method = %q, want REPLY", params["method"])
			}
			body, _ := io.ReadAll(part.Body)
			calendar = string(body)
		}
	}
	if strings.Join(types, ",") != "text/plain,text/calendar" {
		t.Errorf("parts = %v, want [text/plain text/calendar]", types)
	}
	if calendar != replyICS {
		t.Errorf("calendar body = %q, want %q", calendar, replyICS)
	}
}

func TestToRFC822_IMIPInvalidMethod(t *testing.T) {
	msg := &ComposeMessage{
		From:           Address{Address: "me@example.com"},
		To:             []Address{{Address: "organizer@example.com"}},
		TextBody:       "x",
		CalendarMethod: "REPLY\r\nBcc: someone@example.com",
		CalendarBody:   replyICS,
	}
	if _, err := msg.ToRFC822(); err == nil {
		t.Error("ToRFC822() accepted a method with a header injection")
	}
}
