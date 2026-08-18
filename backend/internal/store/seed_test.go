package store

import (
	"strings"
	"testing"
	"time"
)

// The seed dataset has to satisfy every constraint the real store enforces,
// because it is the first thing written to a fresh database and a violation
// there means the service crash-loops on first boot rather than failing on some
// later user action.
//
// These constraints were invisible while the only store was an in-memory map
// that seeding bypassed via LoadSeed. Postgres does not bypass them.

func TestSeedSatisfiesTheUniquenessConstraints(t *testing.T) {
	t.Parallel()
	guests, reviews := Seed(time.Now().UTC())

	if len(guests) == 0 || len(reviews) == 0 {
		t.Fatalf("seed produced %d guests and %d reviews", len(guests), len(reviews))
	}

	// UNIQUE (lower(email)) — matching guests_email_lower_key.
	seenEmail := map[string]string{}
	for _, g := range guests {
		key := strings.ToLower(strings.TrimSpace(g.Email))
		if key == "" {
			t.Errorf("guest %s (%s) has no email; the column is NOT NULL", g.ID, g.Name)
			continue
		}
		if prev, dup := seenEmail[key]; dup {
			t.Errorf("guests %s and %s share the email %q; the unique index rejects the second insert",
				prev, g.ID, g.Email)
		}
		seenEmail[key] = g.ID
	}

	// PRIMARY KEY (id).
	seenID := map[string]bool{}
	for _, g := range guests {
		if seenID[g.ID] {
			t.Errorf("duplicate guest id %q", g.ID)
		}
		seenID[g.ID] = true
	}

	// UNIQUE (lower(host_id), lower(stay_id)) — matching reviews_host_stay_key,
	// which is FR-010: one member reviews one stay at most once.
	seenStay := map[string]string{}
	for _, r := range reviews {
		key := strings.ToLower(r.HostID) + "\x00" + strings.ToLower(r.StayID)
		if prev, dup := seenStay[key]; dup {
			t.Errorf("reviews %s and %s are both host %q / stay %q; the unique index rejects the second",
				prev, r.ID, r.HostID, r.StayID)
		}
		seenStay[key] = r.ID
	}

	// FOREIGN KEY (guest_id) REFERENCES guests.
	for _, r := range reviews {
		if !seenID[r.GuestID] {
			t.Errorf("review %s references guest %q, which the seed does not create", r.ID, r.GuestID)
		}
	}
}

// TestSeedSatisfiesTheCheckConstraints mirrors the CHECK clauses in the schema.
// A rating outside 1..5 that reaches the table shifts the population prior and
// therefore quietly changes everyone else's score.
func TestSeedSatisfiesTheCheckConstraints(t *testing.T) {
	t.Parallel()
	_, reviews := Seed(time.Now().UTC())

	for _, r := range reviews {
		if errs := r.Ratings.Validate(); errs.Any() {
			t.Errorf("review %s has out-of-range ratings: %v", r.ID, errs)
		}
		if len(r.Comment) > 2000 {
			t.Errorf("review %s has a %d-character comment; the column caps at 2000", r.ID, len(r.Comment))
		}
		if !r.CheckIn.IsZero() && !r.CheckOut.IsZero() && r.CheckOut.Before(r.CheckIn) {
			t.Errorf("review %s checks out before it checks in", r.ID)
		}
		switch r.Dispute.Status {
		case "", "open", "upheld", "rejected":
		default:
			t.Errorf("review %s has dispute status %q, which the CHECK constraint rejects",
				r.ID, r.Dispute.Status)
		}
	}

	for _, g := range mustGuests(t) {
		if n := len(strings.TrimSpace(g.Name)); n == 0 || n > 120 {
			t.Errorf("guest %s has a %d-character name; the CHECK constraint requires 1..120", g.ID, n)
		}
	}
}

// TestSeedIsReplayable checks the seed can be loaded twice without changing the
// result, which is what makes ON CONFLICT DO NOTHING correct in the Postgres
// loader and what lets two replicas boot into an empty database at once.
func TestSeedIsReplayable(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	g1, r1 := Seed(at)
	g2, r2 := Seed(at)

	if len(g1) != len(g2) || len(r1) != len(r2) {
		t.Fatalf("seed is not deterministic: %d/%d guests, %d/%d reviews",
			len(g1), len(g2), len(r1), len(r2))
	}
	for i := range g1 {
		if g1[i].ID != g2[i].ID || g1[i].Email != g2[i].Email {
			t.Fatalf("guest %d differs between runs: %s/%s", i, g1[i].ID, g2[i].ID)
		}
	}
	for i := range r1 {
		if r1[i].ID != r2[i].ID {
			t.Fatalf("review %d differs between runs: %s/%s", i, r1[i].ID, r2[i].ID)
		}
	}
}

func mustGuests(t *testing.T) []guestForTest {
	t.Helper()
	guests, _ := Seed(time.Now().UTC())
	out := make([]guestForTest, 0, len(guests))
	for _, g := range guests {
		out = append(out, guestForTest{ID: g.ID, Name: g.Name})
	}
	return out
}

type guestForTest struct{ ID, Name string }
