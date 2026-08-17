// Command propertysim publishes stay events to the MQTT broker as a member
// property would.
//
// It exists so the ingest path is demoable without a hotel: run it, watch the
// API log file the record, and watch the guest's score move. It is also the
// quickest way to exercise the deduplication — publish the same event_id twice
// and the second one comes back acked as a duplicate.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
	"github.com/udaykishore-resu/guest-score/backend/internal/events"
)

func main() {
	var (
		broker   = flag.String("broker", envOr("GS_MQTT_URL", "tcp://localhost:1883"), "broker URL")
		property = flag.String("property", "prop_mum_01", "property id; becomes the topic segment")
		member   = flag.String("member", "m_taj", "member id filing the record")
		name     = flag.String("member-name", "Taj Colaba", "member display name")
		guest    = flag.String("guest", "", "guest id")
		globalID = flag.String("global-id", "", "guest global id, if the guest id is not known")
		stay     = flag.String("stay", "", "stay id; defaults to a generated one")
		kind     = flag.String("type", "incident", "check_in | check_out | incident | commendation")
		incType  = flag.String("incident", "noise_complaint", "incident type, for -type=incident")
		severity = flag.String("severity", "moderate", "minor | moderate | severe")
		comType  = flag.String("commendation", "exceptional_room_care", "commendation type, for -type=commendation")
		note     = flag.String("note", "", "free-text note attached to the event")
		eventID  = flag.String("event-id", "", "event id; defaults to a generated one. Reuse it to test deduplication")
		repeat   = flag.Int("repeat", 1, "publish the event this many times, reusing the same event id")
		username = flag.String("username", os.Getenv("GS_MQTT_USERNAME"), "broker username")
		password = flag.String("password", os.Getenv("GS_MQTT_PASSWORD"), "broker password")
		ratings  = flag.String("ratings", "", "comma-separated house_rules,property_care,communication,noise,accuracy (1-5); omit to leave the stay unrated")
	)
	flag.Parse()

	if *guest == "" && *globalID == "" {
		fail("one of -guest or -global-id is required")
	}

	now := time.Now().UTC()
	if *stay == "" {
		*stay = fmt.Sprintf("s_sim_%d", now.Unix())
	}
	if *eventID == "" {
		*eventID = fmt.Sprintf("evt_%d", now.UnixNano())
	}

	e := events.Event{
		EventID:       *eventID,
		Type:          events.Kind(*kind),
		PropertyID:    *property,
		MemberID:      *member,
		MemberName:    *name,
		GuestID:       *guest,
		GuestGlobalID: *globalID,
		StayID:        *stay,
		OccurredAt:    now,
		Comment:       *note,
	}

	switch e.Type {
	case events.KindIncident:
		e.Incident = &domain.Incident{
			Type:     domain.IncidentType(*incType),
			Severity: domain.Severity(*severity),
			Note:     *note,
		}
	case events.KindCommendation:
		e.Commendation = &domain.Commendation{
			Type: domain.CommendationType(*comType),
			Note: *note,
		}
	case events.KindCheckIn, events.KindCheckOut:
	default:
		fail("unknown -type %q", *kind)
	}

	if *ratings != "" {
		r, err := parseRatings(*ratings)
		if err != nil {
			fail("%v", err)
		}
		e.Ratings = r
	}

	if err := e.Validate(); err != nil {
		fail("the event this would publish is invalid: %v", err)
	}

	payload, _ := json.MarshalIndent(e, "", "  ")
	fmt.Printf("publishing to guestscore/%s/events:\n%s\n\n", *property, payload)

	for i := 0; i < *repeat; i++ {
		if err := events.Publish(*broker, fmt.Sprintf("propertysim-%s-%d", *property, now.UnixNano()),
			*property, e, *username, *password); err != nil {
			fail("publish failed: %v", err)
		}
		fmt.Printf("published %d/%d  event_id=%s\n", i+1, *repeat, e.EventID)
		if i+1 < *repeat {
			// The second and later copies exercise deduplication: same
			// event_id, so the bureau must ack them as duplicates rather than
			// filing the incident again.
			time.Sleep(300 * time.Millisecond)
		}
	}

	fmt.Printf("\nWatch the acks with:\n  mosquitto_sub -h localhost -t 'guestscore/_bureau/acks' -v\n")
}

func parseRatings(s string) (*events.Ratings, error) {
	var v [5]int
	n, err := fmt.Sscanf(s, "%d,%d,%d,%d,%d", &v[0], &v[1], &v[2], &v[3], &v[4])
	if err != nil || n != 5 {
		return nil, fmt.Errorf("-ratings wants five values 1-5, like 4,5,4,3,5")
	}
	for _, x := range v {
		if x < 1 || x > 5 {
			return nil, fmt.Errorf("-ratings values must be 1-5, got %d", x)
		}
	}
	return &events.Ratings{
		HouseRules: &v[0], PropertyCare: &v[1], Communication: &v[2],
		Noise: &v[3], Accuracy: &v[4],
	}, nil
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "propertysim: "+format+"\n", args...)
	os.Exit(1)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
