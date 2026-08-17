package events_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
	"github.com/udaykishore-resu/guest-score/backend/internal/events"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoring"
	"github.com/udaykishore-resu/guest-score/backend/internal/store"
)

// newStore returns an ephemeral store holding one guest with a known global ID.
func newStore(t *testing.T) (*store.FileStore, domain.Guest) {
	t.Helper()
	st, err := store.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	g, err := st.CreateGuest(domain.Guest{
		ID: "g_test", GlobalID: "GS-ABCDEF123456", Name: "Rohan Mehta",
		Email: "rohan@example.com", Nationality: "IN", JoinedAt: time.Now().UTC().AddDate(-1, 0, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	return st, g
}

func payload(t *testing.T, e events.Event) []byte {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func baseEvent() events.Event {
	return events.Event{
		EventID:    "evt_1",
		Type:       events.KindIncident,
		PropertyID: "prop_mum_01",
		MemberID:   "m_taj",
		MemberName: "Taj Colaba",
		GuestID:    "g_test",
		StayID:     "s_1187",
		OccurredAt: time.Now().UTC(),
		Incident: &domain.Incident{
			Type: domain.IncNoiseComplaint, Severity: domain.SevModerate, Note: "3am corridor",
		},
	}
}

func TestApplyFilesAnIncident(t *testing.T) {
	st, g := newStore(t)
	dd := events.NewMemoryDeduper()

	ack, err := events.Apply(context.Background(), st, dd, payload(t, baseEvent()))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !ack.Accepted || ack.ReviewID == "" {
		t.Fatalf("expected an accepted ack with a record id, got %+v", ack)
	}

	reviews, err := st.ReviewsForGuest(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 1 {
		t.Fatalf("filed %d records, want 1", len(reviews))
	}
	if len(reviews[0].Incidents) != 1 || reviews[0].Incidents[0].Type != domain.IncNoiseComplaint {
		t.Fatalf("incident not attached: %+v", reviews[0].Incidents)
	}
	if reviews[0].MemberID != "m_taj" {
		t.Errorf("member %q not recorded; an unattributed record cannot be disputed", reviews[0].MemberID)
	}
}

// TestRedeliveryIsNotDoublePenalised is the reason event_id exists. MQTT QoS 1
// is at-least-once, so this is normal operation, not an edge case.
func TestRedeliveryIsNotDoublePenalised(t *testing.T) {
	st, g := newStore(t)
	dd := events.NewMemoryDeduper()
	raw := payload(t, baseEvent())

	first, err := events.Apply(context.Background(), st, dd, raw)
	if err != nil {
		t.Fatal(err)
	}
	second, err := events.Apply(context.Background(), st, dd, raw)
	if err != nil {
		t.Fatalf("a redelivery must not error: %v", err)
	}

	if !second.Accepted || !second.Duplicate {
		t.Fatalf("redelivery should ack as a duplicate, got %+v", second)
	}
	if second.ReviewID == first.ReviewID && second.ReviewID != "" {
		t.Errorf("a duplicate should not report a newly filed record")
	}

	reviews, _ := st.ReviewsForGuest(g.ID)
	if len(reviews) != 1 {
		t.Fatalf("the same event filed %d records; the guest is penalised twice", len(reviews))
	}
}

func TestResolvesByGlobalID(t *testing.T) {
	st, g := newStore(t)
	dd := events.NewMemoryDeduper()

	e := baseEvent()
	e.GuestID = ""
	// Lowercased on purpose: a property re-keying it by hand will not match case.
	e.GuestGlobalID = strings.ToLower(string(g.GlobalID))

	ack, err := events.Apply(context.Background(), st, dd, payload(t, e))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if ack.GuestID != g.ID {
		t.Fatalf("resolved to %q, want %q", ack.GuestID, g.ID)
	}
}

// TestUnratedRecordSitsAtThePrior pins the neutral-rating decision.
//
// A mid-stay incident report carries no assessment of the guest's overall
// conduct. The record it creates must therefore behave exactly as if the
// property had rated the stay at the population prior — not at zero, and not at
// anything the ingest invented. Two properties are checked: an omitted rating
// is identical to the explicit neutral rating, and that neutral value really
// does land at the model's anchor rather than somewhere arbitrary.
func TestUnratedRecordSitsAtThePrior(t *testing.T) {
	now := scoring.Now()
	m := scoring.DefaultModel

	score := func(t *testing.T, e events.Event) scoring.Score {
		t.Helper()
		st, g := newStore(t)
		if _, err := events.Apply(context.Background(), st, events.NewMemoryDeduper(), payload(t, e)); err != nil {
			t.Fatal(err)
		}
		reviews, _ := st.ReviewsForGuest(g.ID)
		return scoring.Compute(reviews, now, m)
	}

	bare := baseEvent()
	bare.Type, bare.Incident = events.KindCheckOut, nil
	unrated := score(t, bare)

	four := 4
	explicit := bare
	explicit.Ratings = &events.Ratings{
		HouseRules: &four, PropertyCare: &four, Communication: &four, Noise: &four, Accuracy: &four,
	}
	rated := score(t, explicit)

	if unrated.Composite != rated.Composite {
		t.Errorf("an omitted rating scored %.1f but the explicit neutral rating scored %.1f; "+
			"the two must be indistinguishable", unrated.Composite, rated.Composite)
	}

	// The anchor is where the model places an average guest. A record carrying
	// no information must land there, not at an extreme.
	if drift := unrated.Composite - m.AnchorScore; drift < -40 || drift > 40 {
		t.Errorf("an unrated record scored %.1f, %.1f points from the anchor of %.1f; "+
			"it should sit at the prior", unrated.Composite, drift, m.AnchorScore)
	}
}

// TestIncidentPenaltyIsTheOnlyMovementFromAnIncidentEvent checks that filing an
// incident moves the score by the penalty and not by a fabricated rating.
func TestIncidentPenaltyIsTheOnlyMovementFromAnIncidentEvent(t *testing.T) {
	now := scoring.Now()

	withIncident := func(t *testing.T, e events.Event) scoring.Score {
		t.Helper()
		st, g := newStore(t)
		if _, err := events.Apply(context.Background(), st, events.NewMemoryDeduper(), payload(t, e)); err != nil {
			t.Fatal(err)
		}
		reviews, _ := st.ReviewsForGuest(g.ID)
		return scoring.Compute(reviews, now, scoring.DefaultModel)
	}

	bare := baseEvent()
	bare.Type, bare.Incident = events.KindCheckOut, nil
	clean := withIncident(t, bare)
	dirty := withIncident(t, baseEvent()) // same record, plus a moderate noise complaint

	drop := clean.Composite - dirty.Composite
	want := domain.IncNoiseComplaint.BasePenalty() * domain.SevModerate.Multiplier()

	// Recency decay is negligible for a record filed now, so the drop should be
	// the catalogued penalty within rounding.
	if drop < want-2 || drop > want+2 {
		t.Errorf("filing a moderate noise complaint moved the score by %.1f, want about %.1f "+
			"(clean %.1f, with incident %.1f)", drop, want, clean.Composite, dirty.Composite)
	}
	if dirty.IncidentCount != 1 {
		t.Errorf("incident count %d, want 1", dirty.IncidentCount)
	}
}

func TestRejections(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*events.Event)
		wantSub string
	}{
		{"no event id", func(e *events.Event) { e.EventID = "" }, "event_id is required"},
		{"unknown type", func(e *events.Event) { e.Type = "exploded" }, "unknown event type"},
		{"no guest", func(e *events.Event) { e.GuestID = ""; e.GuestGlobalID = "" }, "guest_id or guest_global_id"},
		{"no member", func(e *events.Event) { e.MemberID = "" }, "member_id is required"},
		{"no stay", func(e *events.Event) { e.StayID = "" }, "stay_id is required"},
		{"incident without payload", func(e *events.Event) { e.Incident = nil }, "must carry an incident"},
		{"unknown incident type", func(e *events.Event) {
			e.Incident = &domain.Incident{Type: "meteor_strike", Severity: domain.SevMinor}
		}, "unknown incident type"},
		{"bad severity", func(e *events.Event) {
			e.Incident = &domain.Incident{Type: domain.IncNoiseComplaint, Severity: "apocalyptic"}
		}, "severity must be"},
		{"unknown guest", func(e *events.Event) { e.GuestID = "g_nobody" }, "no guest with id"},
		{"rating out of range", func(e *events.Event) {
			seven := 7
			e.Ratings = &events.Ratings{HouseRules: &seven}
		}, "must be 1..5"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := newStore(t)
			dd := events.NewMemoryDeduper()
			e := baseEvent()
			tc.mutate(&e)

			ack, err := events.Apply(context.Background(), st, dd, payload(t, e))
			if err == nil {
				t.Fatalf("expected a rejection, got an accepted ack: %+v", ack)
			}
			if !events.IsReject(err) {
				t.Fatalf("expected a permanent rejection so the broker stops redelivering, got %T: %v", err, err)
			}
			if !strings.Contains(ack.Reason, tc.wantSub) {
				t.Errorf("reason %q does not mention %q; the publisher cannot fix what it is not told",
					ack.Reason, tc.wantSub)
			}
		})
	}
}

func TestMalformedPayloadIsRejectedNotRetried(t *testing.T) {
	st, _ := newStore(t)
	dd := events.NewMemoryDeduper()

	_, err := events.Apply(context.Background(), st, dd, []byte(`{"event_id": `))
	if err == nil {
		t.Fatal("malformed JSON was accepted")
	}
	if !events.IsReject(err) {
		t.Fatal("malformed JSON must be a permanent rejection; retrying it forever helps nobody")
	}
}

func TestCheckInRecordsAnInquiryNotARecord(t *testing.T) {
	st, g := newStore(t)
	dd := events.NewMemoryDeduper()

	e := baseEvent()
	e.Type = events.KindCheckIn
	e.Incident = nil

	ack, err := events.Apply(context.Background(), st, dd, payload(t, e))
	if err != nil {
		t.Fatal(err)
	}
	if !ack.Accepted {
		t.Fatalf("check-in rejected: %s", ack.Reason)
	}
	if ack.ReviewID != "" {
		t.Error("a check-in must not file a stay record")
	}
	if inq := st.InquiriesFor(g.ID); len(inq) != 1 {
		t.Fatalf("recorded %d inquiries, want 1", len(inq))
	}
	reviews, _ := st.ReviewsForGuest(g.ID)
	if len(reviews) != 0 {
		t.Fatalf("a check-in created %d stay records", len(reviews))
	}
}
