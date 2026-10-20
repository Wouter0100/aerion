package backend

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

// A recurring REQUEST with a UID that is not path-safe and an unknown
// X- property that must survive every rewrite.
const inviteRequestICS = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"PRODID:-//Test//Test//EN\r\n" +
	"METHOD:REQUEST\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:board/2026@example.com\r\n" +
	"DTSTAMP:20260916T100859Z\r\n" +
	"SEQUENCE:3\r\n" +
	"DTSTART;TZID=Europe/Amsterdam:20261116T173000\r\n" +
	"DTEND;TZID=Europe/Amsterdam:20261116T210000\r\n" +
	"RRULE:FREQ=WEEKLY;INTERVAL=8\r\n" +
	"SUMMARY:Board\\; meeting\r\n" +
	"LOCATION:Tuxis\\, Ede\r\n" +
	"X-CUSTOM-KEEP:yes\r\n" +
	"ORGANIZER;CN=Organizer:mailto:org@example.com\r\n" +
	"ATTENDEE;CN=Me;RSVP=TRUE;PARTSTAT=NEEDS-ACTION:mailto:Me@Example.com\r\n" +
	"ATTENDEE;CN=Other;RSVP=TRUE;PARTSTAT=NEEDS-ACTION:mailto:other@example.com\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

var inviteSelf = []string{"me@example.com"}

type inviteFixture struct {
	api      *API
	store    *Store
	caldavID string
	localID  string
	googleID string
	puts     *[]recordedRequest
}

func newInviteFixture(t *testing.T) inviteFixture {
	t.Helper()
	var puts []recordedRequest
	srv := newFakeCalDAVServer(t, func(req recordedRequest, w http.ResponseWriter) {
		if req.method == http.MethodPut {
			puts = append(puts, req)
			w.Header().Set("ETag", `"etag-`+string(rune('0'+len(puts)))+`"`)
			w.WriteHeader(http.StatusCreated)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	t.Cleanup(srv.Close)

	store := newTestStore(t)
	now := time.Now().Unix()
	seed := []struct {
		src Source
		cal Calendar
	}{
		{
			Source{ID: "src-dav", Type: SourceTypeCalDAV, Name: "Soverin", URL: srv.URL, Username: "me", Enabled: true, Writable: true,
				ITIPMode: "server", OrganizerIdentities: []string{"me@example.com"}, CreatedAt: now},
			Calendar{ID: "cal-dav", SourceID: "src-dav", URL: srv.URL + "/calendars/me/personal/", DisplayName: "Personal", Visible: true, Writable: true, CreatedAt: now},
		},
		{
			Source{ID: "src-local", Type: SourceTypeLocal, Name: "Local", Enabled: true, Writable: true, CreatedAt: now},
			Calendar{ID: "cal-local", SourceID: "src-local", DisplayName: "On this device", Visible: true, Writable: true, CreatedAt: now},
		},
		{
			Source{ID: "src-google", Type: SourceTypeGoogle, Name: "Google", AccountID: "acct-1", Enabled: true, Writable: true, CreatedAt: now},
			Calendar{ID: "cal-google", SourceID: "src-google", URL: "primary", DisplayName: "Google", Visible: true, Writable: true, CreatedAt: now},
		},
	}
	err := store.WithTx(func(tx *sql.Tx) error {
		for _, s := range seed {
			if err := store.CreateSourceTx(tx, s.src); err != nil {
				return err
			}
			if err := store.CreateCalendarTx(tx, s.cal); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return inviteFixture{
		api:      NewAPI(store, fakeSecrets{password: "secret"}, nil, nil),
		store:    store,
		caldavID: "cal-dav",
		localID:  "cal-local",
		googleID: "cal-google",
		puts:     &puts,
	}
}

// decodeMaster decodes an ICS blob and returns its METHOD and master VEVENT.
func decodeMaster(t *testing.T, blob string) (string, *ical.Component) {
	t.Helper()
	cal, err := ical.NewDecoder(strings.NewReader(blob)).Decode()
	if err != nil {
		t.Fatalf("decode: %v\n%s", err, blob)
	}
	m := masterVEVENT(cal)
	if m == nil {
		t.Fatalf("no master VEVENT in\n%s", blob)
	}
	return propTextOnComp(cal.Component, ical.PropMethod), m
}

func partStatOf(t *testing.T, ev *ical.Component, email string) string {
	t.Helper()
	for _, p := range ev.Props.Values(ical.PropAttendee) {
		if calAddressEmail(p.Value) == email {
			return p.Params.Get(ical.ParamParticipationStatus)
		}
	}
	t.Fatalf("attendee %s not found", email)
	return ""
}

func TestPreviewInvite(t *testing.T) {
	f := newInviteFixture(t)

	p, err := f.api.PreviewInvite(inviteRequestICS, inviteSelf)
	if err != nil {
		t.Fatalf("PreviewInvite: %v", err)
	}
	if p.Method != "REQUEST" || p.UID != "board/2026@example.com" || p.Summary != "Board; meeting" {
		t.Errorf("got method %q uid %q summary %q", p.Method, p.UID, p.Summary)
	}
	if !p.Recurring {
		t.Error("Recurring = false, want true")
	}
	if p.MyEmail != "me@example.com" || p.MyPartStat != PartStatNeedsAction {
		t.Errorf("MyEmail %q MyPartStat %q", p.MyEmail, p.MyPartStat)
	}
	if p.Organizer == nil || p.Organizer.Email != "org@example.com" {
		t.Errorf("Organizer = %+v", p.Organizer)
	}
	if p.Existing != nil {
		t.Errorf("Existing = %+v, want nil", p.Existing)
	}
	notifies := map[string]bool{}
	for _, tg := range p.Targets {
		notifies[tg.CalendarID] = tg.NotifiesOrganizer
	}
	want := map[string]bool{f.caldavID: true, f.localID: false}
	if len(notifies) != len(want) || notifies[f.caldavID] != true || notifies[f.localID] != false {
		t.Errorf("targets = %v, want %v (Google is not an import target)", notifies, want)
	}
}

func TestRespondToInvite_ImportCalDAV(t *testing.T) {
	f := newInviteFixture(t)

	eventID, err := f.api.RespondToInvite(inviteRequestICS, f.caldavID, inviteSelf, "accepted")
	if err != nil {
		t.Fatalf("RespondToInvite: %v", err)
	}
	if len(*f.puts) != 1 {
		t.Fatalf("got %d PUTs, want 1", len(*f.puts))
	}
	put := (*f.puts)[0]
	if put.ifNoneMatch != "*" {
		t.Errorf("If-None-Match = %q, want *", put.ifNoneMatch)
	}
	if !strings.HasSuffix(put.escapedPath, "/calendars/me/personal/board%2F2026@example.com.ics") {
		t.Errorf("path = %q, want the escaped UID as one segment", put.escapedPath)
	}

	method, ev := decodeMaster(t, put.body)
	if method != "" {
		t.Errorf("stored object keeps METHOD %q", method)
	}
	if got := partStatOf(t, ev, "me@example.com"); got != PartStatAccepted {
		t.Errorf("my PARTSTAT = %q, want ACCEPTED", got)
	}
	if got := partStatOf(t, ev, "other@example.com"); got != PartStatNeedsAction {
		t.Errorf("other PARTSTAT = %q, want NEEDS-ACTION", got)
	}
	for _, name := range []string{ical.PropRecurrenceRule, "X-CUSTOM-KEEP", ical.PropOrganizer} {
		if ev.Props.Get(name) == nil {
			t.Errorf("%s dropped from the stored object", name)
		}
	}

	stored, err := f.store.GetEvent(eventID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if stored.CalendarID != f.caldavID || stored.Href == "" || stored.ETag == "" {
		t.Errorf("stored calendar %q href %q etag %q", stored.CalendarID, stored.Href, stored.ETag)
	}

	p, err := f.api.PreviewInvite(inviteRequestICS, inviteSelf)
	if err != nil {
		t.Fatalf("PreviewInvite: %v", err)
	}
	if p.Existing == nil || p.Existing.EventID != eventID || p.Existing.MyPartStat != PartStatAccepted || !p.Existing.NotifiesOrganizer {
		t.Errorf("Existing = %+v", p.Existing)
	}
}

func TestRespondToInvite_UpdatesStoredCopy(t *testing.T) {
	f := newInviteFixture(t)
	eventID, err := f.api.RespondToInvite(inviteRequestICS, f.caldavID, inviteSelf, "ACCEPTED")
	if err != nil {
		t.Fatalf("import: %v", err)
	}

	// The calendar argument is ignored once a calendar holds the event.
	gotID, err := f.api.RespondToInvite(inviteRequestICS, f.localID, inviteSelf, "DECLINED")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if gotID != eventID {
		t.Errorf("event ID = %q, want %q", gotID, eventID)
	}
	if len(*f.puts) != 2 {
		t.Fatalf("got %d PUTs, want 2", len(*f.puts))
	}
	put := (*f.puts)[1]
	if put.ifMatch == "" || put.ifNoneMatch != "" {
		t.Errorf("update sent If-Match %q If-None-Match %q", put.ifMatch, put.ifNoneMatch)
	}
	_, ev := decodeMaster(t, put.body)
	if got := partStatOf(t, ev, "me@example.com"); got != PartStatDeclined {
		t.Errorf("my PARTSTAT = %q, want DECLINED", got)
	}
	if ev.Props.Get(ical.PropRecurrenceRule) == nil || ev.Props.Get("X-CUSTOM-KEEP") == nil {
		t.Error("answering the stored copy dropped RRULE or X-CUSTOM-KEEP")
	}
}

func TestRespondToInvite_ImportLocal(t *testing.T) {
	f := newInviteFixture(t)
	eventID, err := f.api.RespondToInvite(inviteRequestICS, f.localID, inviteSelf, "TENTATIVE")
	if err != nil {
		t.Fatalf("RespondToInvite: %v", err)
	}
	if len(*f.puts) != 0 {
		t.Errorf("local import sent %d PUTs", len(*f.puts))
	}
	stored, err := f.store.GetEvent(eventID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if att := findSelfAttendee(stored.Attendees, selfEmailSet(inviteSelf)); att == nil || att.PartStat != PartStatTentative {
		t.Errorf("stored self attendee = %+v", att)
	}
}

func TestRespondToInvite_Errors(t *testing.T) {
	f := newInviteFixture(t)
	tests := []struct {
		name     string
		calendar string
		self     []string
		partStat string
		wantErr  func(error) bool
	}{
		{"not invited", f.caldavID, []string{"nobody@example.com"}, "ACCEPTED", func(err error) bool { return errors.Is(err, errNotInvited) }},
		{"google target", f.googleID, inviteSelf, "ACCEPTED", func(err error) bool { return err != nil && strings.Contains(err.Error(), "not supported") }},
		{"no calendar", "", inviteSelf, "ACCEPTED", func(err error) bool { return err != nil }},
		{"bad answer", f.caldavID, inviteSelf, "NEEDS-ACTION", func(err error) bool { return err != nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.api.RespondToInvite(inviteRequestICS, tt.calendar, tt.self, tt.partStat)
			if !tt.wantErr(err) {
				t.Errorf("err = %v", err)
			}
			if len(*f.puts) != 0 {
				t.Errorf("a failed answer sent %d PUTs", len(*f.puts))
			}
		})
	}
}

func TestBuildInviteReply(t *testing.T) {
	f := newInviteFixture(t)
	r, err := f.api.BuildInviteReply(inviteRequestICS, inviteSelf, "tentative", "")
	if err != nil {
		t.Fatalf("BuildInviteReply: %v", err)
	}
	if r.From != "me@example.com" || r.To != "org@example.com" || r.ToName != "Organizer" || r.Summary != "Board; meeting" {
		t.Errorf("reply = %+v", r)
	}

	method, ev := decodeMaster(t, r.ICS)
	if method != "REPLY" {
		t.Errorf("METHOD = %q, want REPLY", method)
	}
	atts := ev.Props.Values(ical.PropAttendee)
	if len(atts) != 1 || calAddressEmail(atts[0].Value) != "me@example.com" {
		t.Fatalf("attendees = %+v, want only me", atts)
	}
	if got := atts[0].Params.Get(ical.ParamParticipationStatus); got != PartStatTentative {
		t.Errorf("PARTSTAT = %q, want TENTATIVE", got)
	}
	if atts[0].Params.Get(ical.ParamRSVP) != "" {
		t.Error("REPLY keeps RSVP")
	}
	for name, want := range map[string]string{ical.PropUID: "board/2026@example.com", ical.PropSequence: "3"} {
		if got := propTextOnComp(ev, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if ev.Props.Get(ical.PropOrganizer) == nil || ev.Props.Get(ical.PropDateTimeStamp) == nil {
		t.Error("REPLY lacks ORGANIZER or DTSTAMP")
	}

	if _, err := f.api.BuildInviteReply(inviteRequestICS, []string{"nobody@example.com"}, "ACCEPTED", ""); !errors.Is(err, errNotInvited) {
		t.Errorf("not invited: err = %v", err)
	}
}

func TestBuildInviteReply_SentBy(t *testing.T) {
	f := newInviteFixture(t)
	tests := []struct {
		name, sentBy, wantFrom, wantSentBy string
	}{
		{"other mailbox", "Wouter@DCC.example", "wouter@dcc.example", "mailto:wouter@dcc.example"},
		{"invited mailbox", "me@example.com", "me@example.com", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := f.api.BuildInviteReply(inviteRequestICS, inviteSelf, "ACCEPTED", tt.sentBy)
			if err != nil {
				t.Fatalf("BuildInviteReply: %v", err)
			}
			if r.From != tt.wantFrom {
				t.Errorf("From = %q, want %q", r.From, tt.wantFrom)
			}
			_, ev := decodeMaster(t, r.ICS)
			att := ev.Props.Get(ical.PropAttendee)
			if calAddressEmail(att.Value) != "me@example.com" {
				t.Errorf("ATTENDEE = %q, want the invited address", att.Value)
			}
			if got := att.Params.Get(ical.ParamSentBy); got != tt.wantSentBy {
				t.Errorf("SENT-BY = %q, want %q", got, tt.wantSentBy)
			}
		})
	}
}
