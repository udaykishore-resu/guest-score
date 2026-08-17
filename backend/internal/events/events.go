// Package events ingests stay activity from member properties over MQTT.
//
// Why a message broker rather than an HTTP endpoint: the publishers are
// property-side systems — a PMS, a front-desk terminal, a housekeeping tablet —
// on hotel networks with unreliable uplinks, and they are the wrong place to
// implement retry. MQTT gives them a session with QoS 1 delivery, so an
// incident filed while the uplink is down is queued by the client and delivered
// when it returns, rather than lost or retried into a duplicate. A retained
// last-will on the status topic also makes "this property has stopped
// reporting" observable, which an HTTP endpoint cannot express at all: silence
// and health look identical.
//
// The cost is at-least-once delivery, so every event carries an event_id and
// the ingest deduplicates on it. Without that, a broker reconnect files the same
// incident twice and the guest is penalised twice for one event.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
)

// Kind enumerates what a property can report.
type Kind string

const (
	KindCheckIn      Kind = "check_in"
	KindCheckOut     Kind = "check_out"
	KindIncident     Kind = "incident"
	KindCommendation Kind = "commendation"
)

// Valid reports whether the kind is one this ingest handles.
func (k Kind) Valid() bool {
	switch k {
	case KindCheckIn, KindCheckOut, KindIncident, KindCommendation:
		return true
	}
	return false
}

// Ratings mirrors domain.Ratings but every field is a pointer, so "not
// supplied" is distinguishable from "rated 0".
//
// This matters because the two mean opposite things. A mid-stay incident report
// carries no assessment of the guest's overall conduct, and treating that
// absence as a rating of zero would be a fabrication that moves the score.
type Ratings struct {
	HouseRules    *int `json:"house_rules,omitempty"`
	PropertyCare  *int `json:"property_care,omitempty"`
	Communication *int `json:"communication,omitempty"`
	Noise         *int `json:"noise,omitempty"`
	Accuracy      *int `json:"accuracy,omitempty"`
}

// Event is the wire payload published on guestscore/{property_id}/events.
type Event struct {
	EventID    string `json:"event_id"`
	Type       Kind   `json:"type"`
	PropertyID string `json:"property_id"`
	MemberID   string `json:"member_id"`
	MemberName string `json:"member_name,omitempty"`

	// One of these identifies the guest. GuestID is the bureau's internal
	// identifier; GuestGlobalID is the portable one a property is more likely
	// to hold, since it is what appeared on the desk screen at check-in.
	GuestID       string `json:"guest_id,omitempty"`
	GuestGlobalID string `json:"guest_global_id,omitempty"`

	StayID     string    `json:"stay_id"`
	RoomNumber string    `json:"room_number,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`

	Ratings      *Ratings             `json:"ratings,omitempty"`
	Incident     *domain.Incident     `json:"incident,omitempty"`
	Commendation *domain.Commendation `json:"commendation,omitempty"`
	Comment      string               `json:"comment,omitempty"`
}

// Ack is published to guestscore/_bureau/acks for every event.
//
// A publisher that never learns what happened to its message will file the same
// incident again by hand, so the ack is not a nicety: it is what stops the
// operator working around the system.
type Ack struct {
	EventID    string    `json:"event_id"`
	PropertyID string    `json:"property_id"`
	Accepted   bool      `json:"accepted"`
	Duplicate  bool      `json:"duplicate,omitempty"`
	ReviewID   string    `json:"review_id,omitempty"`
	GuestID    string    `json:"guest_id,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	At         time.Time `json:"at"`
}

// ErrReject marks an event the ingest will never accept, however many times it
// is redelivered. It is distinguished from a transient failure because the two
// need opposite handling: a rejection is acked so the broker stops resending,
// a transient failure is not.
type ErrReject struct{ Reason string }

func (e ErrReject) Error() string { return e.Reason }

func reject(format string, args ...any) error {
	return ErrReject{Reason: fmt.Sprintf(format, args...)}
}

// Validate checks an event before it touches the store.
func (e Event) Validate() error {
	if strings.TrimSpace(e.EventID) == "" {
		return reject("event_id is required; without it a redelivery cannot be deduplicated " +
			"and the guest would be penalised twice for one event")
	}
	if !e.Type.Valid() {
		return reject("unknown event type %q", e.Type)
	}
	if e.GuestID == "" && e.GuestGlobalID == "" {
		return reject("one of guest_id or guest_global_id is required")
	}
	if e.Type != KindCheckIn && strings.TrimSpace(e.StayID) == "" {
		return reject("stay_id is required for %s", e.Type)
	}
	if e.MemberID == "" {
		return reject("member_id is required; an unattributed record cannot be disputed")
	}
	if e.Type == KindIncident {
		if e.Incident == nil {
			return reject("an incident event must carry an incident")
		}
		if !e.Incident.Type.Valid() {
			return reject("unknown incident type %q", e.Incident.Type)
		}
		if !e.Incident.Severity.Valid() {
			return reject("incident severity must be minor, moderate or severe, got %q", e.Incident.Severity)
		}
	}
	if e.Type == KindCommendation {
		if e.Commendation == nil {
			return reject("a commendation event must carry a commendation")
		}
		if !e.Commendation.Type.Valid() {
			return reject("unknown commendation type %q", e.Commendation.Type)
		}
	}
	if e.Ratings != nil {
		for field, v := range map[string]*int{
			"house_rules": e.Ratings.HouseRules, "property_care": e.Ratings.PropertyCare,
			"communication": e.Ratings.Communication, "noise": e.Ratings.Noise,
			"accuracy": e.Ratings.Accuracy,
		} {
			if v != nil && (*v < 1 || *v > 5) {
				return reject("ratings.%s must be 1..5, got %d", field, *v)
			}
		}
	}
	return nil
}

// neutralRating is what an unsupplied dimension becomes.
//
// A mid-stay incident report says nothing about how well the guest kept the
// room, so its record must not move the quality average in either direction.
// The model shrinks toward a population prior of 3.9, so a rating at the prior
// is the score-neutral value; 4 is the nearest integer the schema permits.
//
// The residual is a bias of +0.1 on one dimension of one record, which the
// Bayesian shrinkage damps further. It is not zero, and the correct fix is a
// flag on the record marking it unrated so the quality stage skips it entirely
// — a domain change that touches the scoring engine and its tests, and so is
// deliberately not bundled into the transport layer.
const neutralRating = 4

func (r *Ratings) resolve() domain.Ratings {
	pick := func(v *int) int {
		if v == nil {
			return neutralRating
		}
		return *v
	}
	if r == nil {
		return domain.Ratings{
			HouseRules: neutralRating, PropertyCare: neutralRating,
			Communication: neutralRating, Noise: neutralRating, Accuracy: neutralRating,
		}
	}
	return domain.Ratings{
		HouseRules:    pick(r.HouseRules),
		PropertyCare:  pick(r.PropertyCare),
		Communication: pick(r.Communication),
		Noise:         pick(r.Noise),
		Accuracy:      pick(r.Accuracy),
	}
}

// ToReview projects an event onto a stay record.
func (e Event) ToReview(guestID string) domain.Review {
	occurred := e.OccurredAt
	if occurred.IsZero() {
		occurred = time.Now().UTC()
	}
	r := domain.Review{
		GuestID:  guestID,
		HostID:   e.PropertyID,
		HostName: e.MemberName,
		// PropertyID and MemberID are both recorded: a chain files under one
		// member with many properties, and a dispute needs to reach the desk
		// that filed it, not just the head office.
		PropertyID:    e.PropertyID,
		MemberID:      e.MemberID,
		MemberName:    e.MemberName,
		StayID:        e.StayID,
		RoomNumber:    e.RoomNumber,
		Ratings:       e.Ratings.resolve(),
		Comment:       e.Comment,
		SubmittedAt:   occurred,
		Incidents:     []domain.Incident{},
		Commendations: []domain.Commendation{},
	}
	if e.Incident != nil {
		r.Incidents = append(r.Incidents, *e.Incident)
	}
	if e.Commendation != nil {
		r.Commendations = append(r.Commendations, *e.Commendation)
	}
	if e.Type == KindCheckOut {
		r.CheckOut = occurred
	}
	return r
}

// Store is the subset of the store this package needs.
//
// Narrow by design: the ingest can look guests up and file records, and cannot
// do anything else. A message arriving from a hotel network should not be able
// to reach a method that was not written with that threat model in mind.
type Store interface {
	GetGuest(id string) (domain.Guest, error)
	ListGuests() ([]domain.Guest, error)
	CreateReview(r domain.Review) (domain.Review, error)
	RecordInquiry(q domain.Inquiry)
}

// Deduper records which events have already been applied.
type Deduper interface {
	// MarkEventProcessed returns true when this is the first sighting.
	MarkEventProcessed(ctx context.Context, eventID, propertyID, kind, result string) (bool, error)
}

// MemoryDeduper is the fallback when the store is the JSON file rather than
// Postgres. It is honest about its limit: a restart forgets everything, so a
// redelivery across a restart is applied twice.
type MemoryDeduper struct {
	seen map[string]time.Time
	mu   chan struct{} // a 1-buffered channel used as a mutex, to keep this allocation-free at init
}

// NewMemoryDeduper builds an in-process deduplicator.
func NewMemoryDeduper() *MemoryDeduper {
	d := &MemoryDeduper{seen: map[string]time.Time{}, mu: make(chan struct{}, 1)}
	d.mu <- struct{}{}
	return d
}

func (d *MemoryDeduper) MarkEventProcessed(_ context.Context, eventID, _, _, _ string) (bool, error) {
	<-d.mu
	defer func() { d.mu <- struct{}{} }()
	if _, ok := d.seen[eventID]; ok {
		return false, nil
	}
	// Bound the map so a long-running process with a chatty broker does not
	// grow without limit. An hour is far longer than any redelivery window.
	cutoff := time.Now().Add(-time.Hour)
	for k, t := range d.seen {
		if t.Before(cutoff) {
			delete(d.seen, k)
		}
	}
	d.seen[eventID] = time.Now()
	return true, nil
}

// resolveGuest finds the guest an event refers to.
func resolveGuest(st Store, e Event) (domain.Guest, error) {
	if e.GuestID != "" {
		g, err := st.GetGuest(e.GuestID)
		if err != nil {
			return domain.Guest{}, reject("no guest with id %q", e.GuestID)
		}
		return g, nil
	}
	// A global ID lookup is a scan because the store indexes documents, not
	// global IDs. The population is small enough that this is not yet worth an
	// index; if the directory grows past a few thousand files it is.
	guests, err := st.ListGuests()
	if err != nil {
		return domain.Guest{}, err // transient: do not ack
	}
	want := strings.ToUpper(strings.TrimSpace(e.GuestGlobalID))
	for _, g := range guests {
		if strings.ToUpper(string(g.GlobalID)) == want {
			return g, nil
		}
	}
	return domain.Guest{}, reject("no guest with global id %q", e.GuestGlobalID)
}

// Apply files one event and returns the ack to publish.
//
// Separated from the MQTT client so the whole ingest path is testable with no
// broker: the handler tests call this directly.
func Apply(ctx context.Context, st Store, dd Deduper, raw []byte) (Ack, error) {
	ack := Ack{At: time.Now().UTC()}

	var e Event
	if err := json.Unmarshal(raw, &e); err != nil {
		ack.Reason = "payload is not valid JSON: " + err.Error()
		return ack, ErrReject{Reason: ack.Reason}
	}
	ack.EventID, ack.PropertyID = e.EventID, e.PropertyID

	if err := e.Validate(); err != nil {
		ack.Reason = err.Error()
		return ack, err
	}

	// Deduplicate before doing any work. QoS 1 is at-least-once, so a
	// redelivery is routine operation rather than an anomaly.
	first, err := dd.MarkEventProcessed(ctx, e.EventID, e.PropertyID, string(e.Type), "accepted")
	if err != nil {
		ack.Reason = "could not check for duplicate delivery"
		return ack, err // transient
	}
	if !first {
		ack.Accepted, ack.Duplicate = true, true
		ack.Reason = "already applied; this was a redelivery"
		return ack, nil
	}

	g, err := resolveGuest(st, e)
	if err != nil {
		ack.Reason = err.Error()
		return ack, err
	}
	ack.GuestID = g.ID

	// A check-in is a lookup, not a record: the property pulled the file to
	// decide a deposit. It is logged for the guest's access report and changes
	// no score.
	if e.Type == KindCheckIn {
		st.RecordInquiry(domain.Inquiry{
			GuestID: g.ID, MemberID: e.MemberID, MemberName: e.MemberName,
			Purpose: domain.InquiryCheckIn, At: e.OccurredAt,
		})
		ack.Accepted = true
		ack.Reason = "recorded as an inquiry; check-in does not affect the score"
		return ack, nil
	}

	rev := e.ToReview(g.ID)
	if errs := rev.Validate(); errs.Any() {
		ack.Reason = errs.Error()
		return ack, ErrReject{Reason: ack.Reason}
	}

	created, err := st.CreateReview(rev)
	if err != nil {
		// A duplicate stay record means another channel already filed this
		// stay. Ack it: redelivering will not change the outcome.
		if strings.Contains(err.Error(), "duplicate") {
			ack.Accepted, ack.Duplicate = true, true
			ack.Reason = "a record already exists for this member and stay"
			return ack, nil
		}
		ack.Reason = "could not file the record"
		return ack, err // transient
	}

	ack.Accepted, ack.ReviewID = true, created.ID
	return ack, nil
}

// IsReject reports whether an error is permanent.
func IsReject(err error) bool {
	var r ErrReject
	return errors.As(err, &r)
}
