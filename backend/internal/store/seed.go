package store

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
)

// Seed builds a deterministic demonstration dataset (FR-014).
//
// The generator is seeded from a constant, and all timestamps are computed
// relative to the `now` argument, so the same call produces the same dataset
// every time while the data still looks current whenever the demo is run.
//
// The archetypes below are chosen to put at least one guest in every grade
// band, plus the two states that are easy to get wrong: a guest with no reviews
// at all (unrated, not zero) and a guest whose only history is years old
// (scored, but with visibly degraded confidence).
func Seed(now time.Time) ([]domain.Guest, []domain.Review) {
	rng := rand.New(rand.NewSource(20260814))

	type archetype struct {
		name       string
		email      string
		city       string
		verified   bool
		reviews    int
		ratingLow  int // inclusive rating range the generator draws from
		ratingHigh int
		maxAgeDays int
		minAgeDays int
		incidents  []domain.Incident
		commends   []domain.CommendationType
		blurb      string
	}

	archetypes := []archetype{
		{
			name: "Priya Raghavan", email: "priya.raghavan@example.com", city: "Austin, TX",
			verified: true, reviews: 11, ratingLow: 5, ratingHigh: 5,
			minAgeDays: 12, maxAgeDays: 500,
			commends: []domain.CommendationType{domain.ComExceptionalCare, domain.ComStaffPraise, domain.ComLoyalReturn, domain.ComExceptionalCare, domain.ComStaffPraise},
blurb: "Immaculate. Left the room cleaner than she found it.",
		},
		{
			name: "Marcus Bellweather", email: "m.bellweather@example.com", city: "Portland, OR",
			verified: true, reviews: 9, ratingLow: 4, ratingHigh: 5,
			minAgeDays: 20, maxAgeDays: 620,
			commends: []domain.CommendationType{domain.ComStaffPraise, domain.ComExceptionalCare},
blurb: "Easy to deal with, respected quiet hours, no issues at all.",
		},
		{
			name: "Sofia Lindqvist", email: "sofia.l@example.com", city: "Chicago, IL",
			verified: true, reviews: 7, ratingLow: 4, ratingHigh: 5,
			minAgeDays: 35, maxAgeDays: 400,
			incidents: []domain.Incident{
				{Type: domain.IncLateCheckout, Severity: domain.SevMinor, Note: "Left about two hours past checkout, called ahead."},
			},
			commends: []domain.CommendationType{domain.ComStaffPraise},
blurb: "Good guest overall. Checkout ran late but she called the desk in advance.",
		},
		{
			name: "Daniel Okonkwo", email: "d.okonkwo@example.com", city: "Atlanta, GA",
			verified: true, reviews: 14, ratingLow: 4, ratingHigh: 5,
			minAgeDays: 8, maxAgeDays: 900,
			commends: []domain.CommendationType{domain.ComLoyalReturn, domain.ComStaffPraise, domain.ComEarlyFlexibility, domain.ComLoyalReturn},
blurb: "Repeat corporate traveller, always straightforward.",
		},
		{
			name: "Chloe Vandermeer", email: "chloe.v@example.com", city: "Denver, CO",
			verified: false, reviews: 5, ratingLow: 3, ratingHigh: 4,
			minAgeDays: 40, maxAgeDays: 300,
			incidents: []domain.Incident{
				{Type: domain.IncRulesViolation, Severity: domain.SevModerate, Note: "Smoking on the balcony of a non-smoking room."},
				{Type: domain.IncNoiseComplaint, Severity: domain.SevMinor, Note: "Adjacent room complained about music after 11pm."},
			},
			blurb: "Mixed stay. Pleasant enough, but hotel policy was treated as a suggestion.",
		},
		{
			name: "Trevor Halstead", email: "t.halstead@example.com", city: "Miami, FL",
			verified: false, reviews: 6, ratingLow: 1, ratingHigh: 3,
			minAgeDays: 25, maxAgeDays: 500,
			incidents: []domain.Incident{
				{Type: domain.IncPropertyDamage, Severity: domain.SevSevere, Note: "Burn marks on the desk and a cracked bathroom mirror."},
				{Type: domain.IncUnauthorizedGuest, Severity: domain.SevSevere, Note: "Booked for two; at least fifteen people in the room Saturday night."},
				{Type: domain.IncNoiseComplaint, Severity: domain.SevSevere, Note: "Two separate rooms called security; police attended."},
			},
			blurb: "Booked as a couple's weekend. It was a party. Do not rebook.",
		},
		{
			name: "Amara Nwosu", email: "amara.nwosu@example.com", city: "Seattle, WA",
			verified: true, reviews: 2, ratingLow: 5, ratingHigh: 5,
			minAgeDays: 15, maxAgeDays: 90,
			commends: []domain.CommendationType{domain.ComExceptionalCare},
blurb: "Only two stays with us so far, both excellent.",
		},
		{
			name: "Grant Whitfield", email: "g.whitfield@example.com", city: "Nashville, TN",
			verified: true, reviews: 4, ratingLow: 4, ratingHigh: 5,
			minAgeDays: 1100, maxAgeDays: 1600,
			blurb: "Great guest, but this history is genuinely old at this point.",
		},
		{
			name: "Yuki Tanaka", email: "yuki.tanaka@example.com", city: "San Jose, CA",
			verified: true, reviews: 8, ratingLow: 4, ratingHigh: 5,
			minAgeDays: 18, maxAgeDays: 450,
			commends: []domain.CommendationType{domain.ComLoyalReturn, domain.ComStaffPraise},
blurb: "Business traveller. Quiet, tidy, in and out.",
		},
		{
			name: "Rosalind Ferrer", email: "r.ferrer@example.com", city: "Boston, MA",
			verified: false, reviews: 3, ratingLow: 2, ratingHigh: 3,
			minAgeDays: 30, maxAgeDays: 200,
			incidents: []domain.Incident{
				{Type: domain.IncPaymentIssue, Severity: domain.SevModerate, Note: "Disputed the cleaning fee after checkout, chargeback filed."},
			},
			blurb: "Stay itself was fine. The billing dispute afterward was not.",
		},
		{
			name: "Elena Castellanos", email: "elena.c@example.com", city: "Phoenix, AZ",
			verified: true, reviews: 0,
			blurb: "",
		},
		{
			name: "Nathan Brightwater", email: "n.brightwater@example.com", city: "Brooklyn, NY",
			verified: false, reviews: 0,
			blurb: "",
		},
		{
			name: "Isabelle Moreau", email: "i.moreau@example.com", city: "New Orleans, LA",
			verified: true, reviews: 6, ratingLow: 3, ratingHigh: 5,
			minAgeDays: 22, maxAgeDays: 380,
			incidents: []domain.Incident{
				{Type: domain.IncUnauthorizedGuest, Severity: domain.SevModerate, Note: "Two extra occupants beyond the booking, disclosed when asked."},
			},
			blurb: "Pleasant to deal with, but the headcount did not match the reservation.",
		},
		{
			name: "Owen Castellane", email: "owen.castellane@example.com", city: "Salt Lake City, UT",
			verified: true, reviews: 12, ratingLow: 4, ratingHigh: 5,
			minAgeDays: 6, maxAgeDays: 700,
			commends: []domain.CommendationType{domain.ComLoyalReturn, domain.ComReferral, domain.ComExceptionalCare, domain.ComLoyalReturn},
blurb: "Frequent guest across three properties. Never a problem.",
		},
		{
			name: "Delphine Aubert", email: "d.aubert@example.com", city: "Charleston, SC",
			verified: false, reviews: 4, ratingLow: 2, ratingHigh: 4,
			minAgeDays: 60, maxAgeDays: 340,
			incidents: []domain.Incident{
				{Type: domain.IncPropertyDamage, Severity: domain.SevModerate, Note: "Stained the carpet and did not report it."},
				{Type: domain.IncLateCheckout, Severity: domain.SevMinor},
			},
			blurb: "Damage went unreported. Housekeeping found it during turnover.",
		},
	}

	// Front-desk and housekeeping staff who file stay records.
	hosts := []struct{ id, name string }{
		{"h_001", "Renata Cardoso — Front Desk"},
		{"h_002", "James Whitlock — Duty Manager"},
		{"h_003", "Aisha Rahman — Housekeeping"},
		{"h_004", "Peter Lindgren — Front Desk"},
	}
	properties := []struct{ id, name string }{
		{"p_001", "Grand Meridian — Downtown"},
		{"p_002", "The Harbourfront"},
		{"p_003", "Meridian Airport"},
		{"p_004", "Ridgeline Resort & Spa"},
		{"p_005", "Meridian Express"},
	}

	positiveComments := []string{
		"Courteous at check-in and checkout. No issues at any point.",
		"Room left spotless. Housekeeping flagged it as a model turnover.",
		"Quiet, considerate of neighbouring rooms, easy to deal with.",
		"Checked out early and dropped a thank-you note at the desk.",
		"Followed every hotel policy without needing a reminder.",
	}
	neutralComments := []string{
		"Fine stay overall, a couple of small things to note.",
		"No real problems, though the guest was slow to respond to messages.",
		"Acceptable. Room needed more turnover time than usual.",
	}
	negativeComments := []string{
		"Would not welcome back. Multiple issues over a two-night stay.",
		"Significant cleanup required after checkout. Unreachable when contacted.",
		"Hotel policy ignored despite being restated at check-in.",
	}

	var guests []domain.Guest
	var reviews []domain.Review
	reviewSeq := 0

	for i, a := range archetypes {
		gid := fmt.Sprintf("g_%03d", i+1)
		guests = append(guests, domain.Guest{
			ID:         gid,
			Name:       a.name,
			Email:      a.email,
			City:       a.city,
			Verified:   a.verified,
			JoinedAt:   now.AddDate(0, 0, -(400 + rng.Intn(900))),
			AvatarSeed: gid,
			Phone:      fmt.Sprintf("+1 (%03d) 555-%04d", 200+rng.Intn(700), rng.Intn(10000)),
		})

		// Spread this guest's reviews evenly across their age window, newest
		// first, so the timeline in the UI reads as a real history rather than
		// a cluster.
		for j := 0; j < a.reviews; j++ {
			reviewSeq++
			span := a.maxAgeDays - a.minAgeDays
			if span < 1 {
				span = 1
			}
			var age int
			if a.reviews == 1 {
				age = a.minAgeDays
			} else {
				age = a.minAgeDays + (span*j)/(a.reviews-1) + rng.Intn(9) - 4
			}
			if age < 1 {
				age = 1
			}
			submitted := now.AddDate(0, 0, -age)
			nights := 2 + rng.Intn(5)
			checkOut := submitted.AddDate(0, 0, -1)
			checkIn := checkOut.AddDate(0, 0, -nights)

			host := hosts[rng.Intn(len(hosts))]
			prop := properties[rng.Intn(len(properties))]

			r := domain.Review{
				ID:           fmt.Sprintf("r_%04d", reviewSeq),
				GuestID:      gid,
				HostID:       host.id,
				HostName:     host.name,
				PropertyID:   prop.id,
				PropertyName: prop.name,
				StayID:       fmt.Sprintf("s_%04d", reviewSeq),
				Ratings:      drawRatings(rng, a.ratingLow, a.ratingHigh),
				Incidents:     []domain.Incident{},
				Commendations: []domain.Commendation{},
				CheckIn:      checkIn,
				CheckOut:     checkOut,
				SubmittedAt:  submitted,
			}

			// Attach the archetype's incidents to its most recent stays, so
			// recency weighting has something visible to act on.
			if j < len(a.incidents) {
				r.Incidents = append(r.Incidents, a.incidents[j])
			}
			// Commendations spread across the record rather than clustering, so
			// a guest's tier reflects sustained behaviour and not one good week.
			if j < len(a.commends) {
				r.Commendations = append(r.Commendations, domain.Commendation{Type: a.commends[j]})
			}

			switch {
			case len(r.Incidents) > 0 && a.ratingHigh <= 3:
				r.Comment = negativeComments[rng.Intn(len(negativeComments))]
			case len(r.Incidents) > 0:
				r.Comment = neutralComments[rng.Intn(len(neutralComments))]
			case a.ratingLow >= 4:
				r.Comment = positiveComments[rng.Intn(len(positiveComments))]
			default:
				r.Comment = neutralComments[rng.Intn(len(neutralComments))]
			}
			if j == 0 && a.blurb != "" {
				r.Comment = a.blurb
			}

			reviews = append(reviews, r)
		}
	}

	return guests, reviews
}

// drawRatings samples each dimension independently within [lo,hi], with an
// occasional one-point dip so the per-dimension bars are not all identical.
func drawRatings(rng *rand.Rand, lo, hi int) domain.Ratings {
	draw := func() int {
		if hi <= lo {
			v := lo
			if rng.Intn(5) == 0 && v > 1 {
				v--
			}
			return v
		}
		v := lo + rng.Intn(hi-lo+1)
		if rng.Intn(6) == 0 && v > 1 {
			v--
		}
		return v
	}
	return domain.Ratings{
		HouseRules:    draw(),
		PropertyCare:  draw(),
		Communication: draw(),
		Noise:         draw(),
		Accuracy:      draw(),
	}
}
