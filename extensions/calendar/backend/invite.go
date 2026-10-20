package backend

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/google/uuid"
)

// Invite card support: the mail viewer hands over the text/calendar part of
// a message (an iTIP object, RFC 5546) and the self emails; these functions
// preview it, store the user's answer, and build the REPLY to mail.

// InvitePreview is what the invite card shows for one iTIP part.
type InvitePreview struct {
	Method      string     `json:"method"`
	UID         string     `json:"uid"`
	Summary     string     `json:"summary"`
	Location    string     `json:"location,omitempty"`
	DTStartUnix int64      `json:"dtstartUnix"`
	DTEndUnix   int64      `json:"dtendUnix"`
	IsAllDay    bool       `json:"isAllDay"`
	TZName      string     `json:"tzName,omitempty"`
	Recurring   bool       `json:"recurring"`
	Organizer   *Organizer `json:"organizer,omitempty"`
	// MyEmail is the self address the invite lists as an attendee and
	// MyPartStat its PARTSTAT in the mailed object. Both are empty when no
	// self address is invited.
	MyEmail    string          `json:"myEmail,omitempty"`
	MyPartStat string          `json:"myPartStat,omitempty"`
	Existing   *InviteExisting `json:"existing,omitempty"`
	Targets    []InviteTarget  `json:"targets"`
}

// InviteExisting is the stored copy of the invited event (matched by UID).
type InviteExisting struct {
	EventID           string `json:"eventId"`
	CalendarName      string `json:"calendarName"`
	MyPartStat        string `json:"myPartStat,omitempty"`
	NotifiesOrganizer bool   `json:"notifiesOrganizer"`
}

// InviteTarget is a calendar an invite can be imported into.
type InviteTarget struct {
	CalendarID        string `json:"calendarId"`
	CalendarName      string `json:"calendarName"`
	SourceName        string `json:"sourceName"`
	NotifiesOrganizer bool   `json:"notifiesOrganizer"`
}

// InviteReply is an iMIP REPLY (RFC 6047) ready to mail to the organizer.
type InviteReply struct {
	From    string `json:"from"`
	To      string `json:"to"`
	ToName  string `json:"toName,omitempty"`
	Summary string `json:"summary"`
	ICS     string `json:"ics"`
}

var errNotInvited = errors.New("calendar: you are not an attendee of this invite")

// PreviewInvite describes rawICS for the invite card.
func (a *API) PreviewInvite(rawICS string, selfEmails []string) (*InvitePreview, error) {
	cal, err := ical.NewDecoder(strings.NewReader(rawICS)).Decode()
	if err != nil {
		return nil, fmt.Errorf("decode invite: %w", err)
	}
	parsed, err := ParseCalendarObject(rawICS)
	if err != nil {
		return nil, fmt.Errorf("parse invite: %w", err)
	}
	m := parsed.Master
	self := selfEmailSet(selfEmails)

	p := &InvitePreview{
		Method:      strings.ToUpper(propTextOnComp(cal.Component, ical.PropMethod)),
		UID:         m.UID,
		Summary:     m.Summary,
		Location:    m.Location,
		DTStartUnix: m.DTStartUnix,
		DTEndUnix:   m.DTEndUnix,
		IsAllDay:    m.IsAllDay,
		TZName:      m.TZName,
		Recurring:   m.RRuleText != "",
		Organizer:   m.Organizer,
	}
	if att := findSelfAttendee(m.Attendees, self); att != nil {
		p.MyEmail = att.Email
		p.MyPartStat = att.PartStat
	}

	existing, err := a.findInviteEvent(m.UID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		cal, src, err := a.calendarAndSource(existing.CalendarID)
		if err != nil {
			return nil, err
		}
		ex := &InviteExisting{
			EventID:           existing.ID,
			CalendarName:      cal.DisplayName,
			NotifiesOrganizer: notifiesOrganizer(*src, p.MyEmail),
		}
		if att := findSelfAttendee(existing.Attendees, self); att != nil {
			ex.MyPartStat = att.PartStat
		}
		p.Existing = ex
	}

	p.Targets, err = a.inviteTargets(p.MyEmail)
	if err != nil {
		return nil, err
	}
	return p, nil
}

// RespondToInvite stores the user's answer to rawICS and returns the event
// ID. When a calendar already holds the event, only the user's PARTSTAT in
// the stored object changes; otherwise the invite is imported into
// calendarID as-is (UID, ORGANIZER and ATTENDEEs kept, METHOD dropped).
func (a *API) RespondToInvite(rawICS, calendarID string, selfEmails []string, partStat string) (string, error) {
	ps, err := invitePartStat(partStat)
	if err != nil {
		return "", err
	}
	self := selfEmailSet(selfEmails)
	if len(self) == 0 {
		return "", errors.New("calendar: no identity emails supplied; cannot resolve self attendee")
	}
	parsed, err := ParseCalendarObject(rawICS)
	if err != nil {
		return "", fmt.Errorf("parse invite: %w", err)
	}

	existing, err := a.findInviteEvent(parsed.Master.UID)
	if err != nil {
		return "", err
	}
	if existing != nil {
		return existing.ID, a.answerStoredInvite(*existing, self, ps)
	}
	if calendarID == "" {
		return "", errors.New("calendar: choose a calendar for this invite")
	}
	return a.importInvite(rawICS, calendarID, self, ps)
}

// BuildInviteReply builds the iMIP REPLY for the user's answer to rawICS.
// It carries only what RFC 5546 §3.2.3 requires: UID, SEQUENCE, DTSTAMP,
// ORGANIZER and the user's own ATTENDEE.
func (a *API) BuildInviteReply(rawICS string, selfEmails []string, partStat string) (*InviteReply, error) {
	ps, err := invitePartStat(partStat)
	if err != nil {
		return nil, err
	}
	cal, err := ical.NewDecoder(strings.NewReader(rawICS)).Decode()
	if err != nil {
		return nil, fmt.Errorf("decode invite: %w", err)
	}
	master := masterVEVENT(cal)
	if master == nil {
		return nil, errors.New("calendar: invite has no VEVENT")
	}
	org := master.Props.Get(ical.PropOrganizer)
	if org == nil || calAddressEmail(org.Value) == "" {
		return nil, errors.New("calendar: invite has no organizer to reply to")
	}

	self := selfEmailSet(selfEmails)
	var me *ical.Prop
	for _, p := range master.Props.Values(ical.PropAttendee) {
		if _, ok := self[calAddressEmail(p.Value)]; ok {
			me = &ical.Prop{Name: p.Name, Params: cloneParams(p.Params), Value: p.Value}
			break
		}
	}
	if me == nil {
		return nil, errNotInvited
	}
	if me.Params == nil {
		me.Params = ical.Params{}
	}
	me.Params.Set(ical.ParamParticipationStatus, ps)
	me.Params.Del(ical.ParamRSVP)

	ev := ical.NewEvent()
	for _, name := range []string{ical.PropUID, ical.PropSequence, ical.PropRecurrenceID, ical.PropSummary} {
		if p := master.Props.Get(name); p != nil {
			ev.Props.Set(&ical.Prop{Name: p.Name, Params: cloneParams(p.Params), Value: p.Value})
		}
	}
	ev.Props.Set(&ical.Prop{Name: org.Name, Params: cloneParams(org.Params), Value: org.Value})
	ev.Props.Set(me)
	ev.Props.SetDateTime(ical.PropDateTimeStamp, time.Now().UTC())

	reply := ical.NewCalendar()
	reply.Props.SetText(ical.PropVersion, "2.0")
	reply.Props.SetText(ical.PropProductID, "-//Aerion//Calendar//EN")
	reply.Props.SetText(ical.PropMethod, "REPLY")
	reply.Children = append(reply.Children, ev.Component)

	var sb strings.Builder
	if err := ical.NewEncoder(&sb).Encode(reply); err != nil {
		return nil, fmt.Errorf("encode reply: %w", err)
	}
	return &InviteReply{
		From:    calAddressEmail(me.Value),
		To:      calAddressEmail(org.Value),
		ToName:  strings.TrimSpace(org.Params.Get(ical.ParamCommonName)),
		Summary: propTextOnComp(master, ical.PropSummary),
		ICS:     sb.String(),
	}, nil
}

func (a *API) answerStoredInvite(ev Event, self map[string]struct{}, partStat string) error {
	blob, attendees, err := setSelfPartStat(ev.ICSBlob, self, partStat, false)
	if err != nil {
		return err
	}
	ev.ICSBlob = blob
	ev.Attendees = attendees

	cal, src, err := a.calendarAndSource(ev.CalendarID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pushed, err := a.pushRSVPThroughProvider(ctx, *src, *cal, ev, partStat)
	if err != nil {
		return fmt.Errorf("push rsvp: %w", err)
	}
	if pushed.ETag != "" {
		ev.ETag = pushed.ETag
	}
	if pushed.ProviderEventID != "" {
		ev.ProviderEventID = pushed.ProviderEventID
	}
	return a.store.WithTx(func(tx *sql.Tx) error {
		return a.store.UpsertEventTx(tx, ev)
	})
}

func (a *API) importInvite(rawICS, calendarID string, self map[string]struct{}, partStat string) (string, error) {
	cal, src, err := a.calendarAndSource(calendarID)
	if err != nil {
		return "", err
	}
	// Google and Microsoft put invites into the calendar themselves, and a
	// plain create there would make the user the organizer.
	if src.Type != SourceTypeCalDAV && src.Type != SourceTypeLocal {
		return "", fmt.Errorf("calendar: importing an invite into a %s calendar is not supported", src.Type)
	}
	if !cal.Writable {
		return "", errors.New("calendar: this calendar is read-only")
	}

	blob, _, err := setSelfPartStat(rawICS, self, partStat, true)
	if err != nil {
		return "", err
	}
	parsed, err := ParseCalendarObject(blob)
	if err != nil {
		return "", fmt.Errorf("parse invite: %w", err)
	}
	ev := parsed.Master
	ev.ID = uuid.NewString()
	ev.CalendarID = cal.ID

	if provider := ProviderForSource(*src, ProviderDeps{Store: a.store, Secrets: a.secrets, Auth: a.auth}); provider != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := provider.PushEvent(ctx, *src, *cal, ev)
		if errors.Is(err, ErrConflict) {
			return "", errors.New("calendar: the server already has this event in that calendar; sync the calendar and try again")
		}
		if err != nil {
			return "", fmt.Errorf("push invite: %w", err)
		}
		ev.ETag = result.ETag
		ev.ProviderEventID = result.ProviderEventID
		ev.Href = joinHref(cal.URL, caldavResourceName(ev.UID))
	}

	err = a.store.WithTx(func(tx *sql.Tx) error {
		if err := a.store.UpsertEventTx(tx, ev); err != nil {
			return err
		}
		for _, ov := range parsed.Overrides {
			if err := a.store.UpsertOverrideTx(tx, ev.ID, ov.RecurrenceIDUnix, ov.ICSBlob); err != nil {
				return err
			}
		}
		return a.extractAndUpsertAlarmsTx(tx, ev)
	})
	if err != nil {
		return "", fmt.Errorf("persist invite: %w", err)
	}
	return ev.ID, nil
}

func (a *API) findInviteEvent(uid string) (*Event, error) {
	if uid == "" {
		return nil, nil
	}
	ids, err := a.store.FindEventIDsByUID(uid)
	if err != nil {
		return nil, fmt.Errorf("find event by uid: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	ev, err := a.store.GetEvent(ids[0])
	if err != nil {
		return nil, fmt.Errorf("load event: %w", err)
	}
	return ev, nil
}

func (a *API) calendarAndSource(calendarID string) (*Calendar, *Source, error) {
	cal, err := a.store.GetCalendar(calendarID)
	if err != nil {
		return nil, nil, fmt.Errorf("load calendar: %w", err)
	}
	if cal == nil {
		return nil, nil, fmt.Errorf("calendar %s not found", calendarID)
	}
	src, err := a.store.GetSource(cal.SourceID)
	if err != nil {
		return nil, nil, fmt.Errorf("load source: %w", err)
	}
	if src == nil {
		return nil, nil, fmt.Errorf("source %s not found", cal.SourceID)
	}
	return cal, src, nil
}

func (a *API) inviteTargets(myEmail string) ([]InviteTarget, error) {
	sources, err := a.store.ListSources()
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	out := []InviteTarget{}
	for _, src := range sources {
		if !src.Enabled || (src.Type != SourceTypeCalDAV && src.Type != SourceTypeLocal) {
			continue
		}
		cals, err := a.store.ListCalendars(src.ID)
		if err != nil {
			return nil, fmt.Errorf("list calendars: %w", err)
		}
		for _, c := range cals {
			if !c.Writable {
				continue
			}
			out = append(out, InviteTarget{
				CalendarID:        c.ID,
				CalendarName:      c.DisplayName,
				SourceName:        src.Name,
				NotifiesOrganizer: notifiesOrganizer(src, myEmail),
			})
		}
	}
	return out, nil
}

// notifiesOrganizer reports whether storing the user's answer on src makes
// the provider send the iTIP REPLY itself. That needs a scheduling-capable
// provider (Google, Microsoft, or CalDAV with RFC 6638) AND an attendee
// address the provider knows is the user's: a server does not reply for an
// address outside the principal's calendar-user-address-set.
func notifiesOrganizer(src Source, attendeeEmail string) bool {
	if attendeeEmail == "" {
		return false
	}
	switch src.Type {
	case SourceTypeCalDAV:
		if src.ITIPMode != "server" {
			return false
		}
	case SourceTypeGoogle, SourceTypeMicrosoft:
	default:
		return false
	}
	for _, id := range src.OrganizerIdentities {
		if strings.EqualFold(strings.TrimSpace(id), attendeeEmail) {
			return true
		}
	}
	return false
}

// setSelfPartStat sets PARTSTAT on every ATTENDEE matching self, in every
// VEVENT of rawICS, and re-encodes it. Everything else in the object is
// kept. forImport also drops METHOD, which RFC 4791 §4.1 forbids in stored
// calendar objects. Returns the master VEVENT's attendees.
func setSelfPartStat(rawICS string, self map[string]struct{}, partStat string, forImport bool) (string, []Attendee, error) {
	cal, err := ical.NewDecoder(strings.NewReader(rawICS)).Decode()
	if err != nil {
		return "", nil, fmt.Errorf("decode invite: %w", err)
	}
	if forImport {
		cal.Props.Del(ical.PropMethod)
	}

	found := false
	for _, child := range cal.Children {
		if child.Name != ical.CompEvent {
			continue
		}
		props := child.Props[ical.PropAttendee]
		for i := range props {
			if _, ok := self[calAddressEmail(props[i].Value)]; !ok {
				continue
			}
			if props[i].Params == nil {
				props[i].Params = ical.Params{}
			}
			props[i].Params.Set(ical.ParamParticipationStatus, partStat)
			found = true
		}
	}
	if !found {
		return "", nil, errNotInvited
	}

	var attendees []Attendee
	if master := masterVEVENT(cal); master != nil {
		attendees = parseAttendeesFromVEVENT(&ical.Event{Component: master})
	}
	var sb strings.Builder
	if err := ical.NewEncoder(&sb).Encode(cal); err != nil {
		return "", nil, fmt.Errorf("encode invite: %w", err)
	}
	return sb.String(), attendees, nil
}

func masterVEVENT(cal *ical.Calendar) *ical.Component {
	for _, child := range cal.Children {
		if child.Name == ical.CompEvent && child.Props.Get(ical.PropRecurrenceID) == nil {
			return child
		}
	}
	return nil
}

func findSelfAttendee(attendees []Attendee, self map[string]struct{}) *Attendee {
	for i := range attendees {
		if _, ok := self[strings.ToLower(attendees[i].Email)]; ok {
			return &attendees[i]
		}
	}
	return nil
}

func selfEmailSet(emails []string) map[string]struct{} {
	set := make(map[string]struct{}, len(emails))
	for _, e := range emails {
		if t := strings.ToLower(strings.TrimSpace(e)); t != "" {
			set[t] = struct{}{}
		}
	}
	return set
}

// invitePartStat accepts the three answers an invite card offers.
func invitePartStat(s string) (string, error) {
	ps := strings.ToUpper(strings.TrimSpace(s))
	switch ps {
	case PartStatAccepted, PartStatTentative, PartStatDeclined:
		return ps, nil
	}
	return "", fmt.Errorf("calendar: invalid partStat %q", s)
}
