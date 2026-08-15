package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoring"
	"github.com/udaykishore-resu/guest-score/backend/internal/store"
)

var testNow = time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

// newTestServer builds an ephemeral store (no path => no disk) seeded with the
// demo dataset, and pins the evaluation clock so scores are deterministic.
func newTestServer(t *testing.T) (http.Handler, *store.FileStore) {
	t.Helper()
	st, err := store.NewFileStore("")
	if err != nil {
		t.Fatalf("creating store: %v", err)
	}
	guests, reviews := store.Seed(testNow)
	st.LoadSeed(guests, reviews)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, log, WithClock(func() scoring.Time { return scoring.At(testNow) }))
	return srv.Routes(), st
}

func do(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encoding request: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decoding response %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestHealth(t *testing.T) {
	h, _ := newTestServer(t)
	rec := do(t, h, "GET", "/api/health", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// TestListGuests_ReturnsScoredDirectory is the P1 read path.
func TestListGuests_ReturnsScoredDirectory(t *testing.T) {
	h, _ := newTestServer(t)
	rec := do(t, h, "GET", "/api/guests", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	got := decode[listGuestsResponse](t, rec)
	if got.Total == 0 {
		t.Fatal("seeded store returned no guests")
	}
	// Default sort is score descending across every guest, including those
	// still on their opening score.
	prev := scoring.DefaultModel.ScoreMax + 1
	for _, g := range got.Guests {
		if g.Score.Composite > prev {
			t.Errorf("guests not sorted by score descending: %.1f after %.1f", g.Score.Composite, prev)
		}
		prev = g.Score.Composite
	}
}

// TestListGuests_SearchIsLiteralSubstring covers the metacharacter edge case:
// a regex-looking query must be matched literally, not compiled.
func TestListGuests_SearchIsLiteralSubstring(t *testing.T) {
	h, _ := newTestServer(t)

	rec := do(t, h, "GET", "/api/guests?q=priya", nil)
	got := decode[listGuestsResponse](t, rec)
	if got.Total != 1 {
		t.Errorf("case-insensitive search for 'priya' returned %d guests, want 1", got.Total)
	}

	for _, q := range []string{".*", "'; DROP TABLE guests;--", "[a-z", "%", "Pri.a"} {
		rec := do(t, h, "GET", "/api/guests?q="+url.QueryEscape(q), nil)
		if rec.Code != http.StatusOK {
			t.Errorf("search %q returned status %d, want 200", q, rec.Code)
		}
		got := decode[listGuestsResponse](t, rec)
		if got.Total != 0 {
			t.Errorf("search %q matched %d guests; it should be treated as a literal and match nothing", q, got.Total)
		}
	}
}

func TestListGuests_FilterByIncidents(t *testing.T) {
	h, _ := newTestServer(t)
	rec := do(t, h, "GET", "/api/guests?incidents=true", nil)
	got := decode[listGuestsResponse](t, rec)
	if got.Total == 0 {
		t.Fatal("expected at least one guest with incidents in the seed data")
	}
	for _, g := range got.Guests {
		if g.Score.IncidentCount == 0 {
			t.Errorf("guest %q has no incidents but passed the incidents filter", g.Name)
		}
	}
}

func TestListGuests_FilterByTier(t *testing.T) {
	h, _ := newTestServer(t)
	for _, tier := range []string{"Excellent", "Good", "Fair", "Poor"} {
		rec := do(t, h, "GET", "/api/guests?tier="+url.QueryEscape(tier), nil)
		got := decode[listGuestsResponse](t, rec)
		for _, g := range got.Guests {
			if g.Score.Tier != tier {
				t.Errorf("tier=%s returned a guest in tier %s", tier, g.Score.Tier)
			}
		}
	}
}

// TestGetGuest_UnratedIsNotZero is acceptance scenario 1.4 / FR-002.
func TestGetGuest_UnratedIsNotZero(t *testing.T) {
	h, st := newTestServer(t)
	guests, _ := st.ListGuests()

	var unratedID string
	for _, g := range guests {
		rs, _ := st.ReviewsForGuest(g.ID)
		if len(rs) == 0 {
			unratedID = g.ID
			break
		}
	}
	if unratedID == "" {
		t.Fatal("seed data must include at least one guest with no reviews")
	}

	rec := do(t, h, "GET", "/api/guests/"+unratedID, nil)
	got := decode[GuestDetail](t, rec)
	if got.Score.Rated {
		t.Error("an opening balance must not be reported as an earned standing")
	}
	if got.Score.Composite != scoring.DefaultModel.NewGuestScore {
		t.Errorf("new guest opens at %.0f, want %.0f",
			got.Score.Composite, scoring.DefaultModel.NewGuestScore)
	}
	if got.Score.Tier != "New" {
		t.Errorf("new guest tier = %q, want \"New\"", got.Score.Tier)
	}
	if got.Score.Handling != scoring.HandlingInsufficient {
		t.Errorf("handling = %q, want %q", got.Score.Handling, scoring.HandlingInsufficient)
	}
	if len(got.Score.Factors) == 0 {
		t.Error("even an unrated guest should explain why there is no score")
	}
}

func TestGetGuest_NotFound(t *testing.T) {
	h, _ := newTestServer(t)
	rec := do(t, h, "GET", "/api/guests/does-not-exist", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	body := decode[errorBody](t, rec)
	if body.Error.Code != "not_found" {
		t.Errorf("error code = %q, want %q", body.Error.Code, "not_found")
	}
}

// TestGetGuest_ScoreIsExplained enforces Constitution Principle IV at the
// API boundary, not just inside the engine.
func TestGetGuest_ScoreIsExplained(t *testing.T) {
	h, st := newTestServer(t)
	guests, _ := st.ListGuests()

	checked := 0
	for _, g := range guests {
		rec := do(t, h, "GET", "/api/guests/"+g.ID, nil)
		got := decode[GuestDetail](t, rec)
		if !got.Score.Rated {
			continue
		}
		checked++
		if len(got.Score.Dimensions) != len(domain.AllDimensions) {
			t.Errorf("%s: got %d dimensions, want %d", g.Name, len(got.Score.Dimensions), len(domain.AllDimensions))
		}
		if len(got.Score.Factors) == 0 {
			t.Errorf("%s: score returned with no explanatory factors", g.Name)
		}
		if got.Score.Headline == "" {
			t.Errorf("%s: score returned with no headline", g.Name)
		}
		if got.Score.Tier == "" {
			t.Errorf("%s: score returned with no grade", g.Name)
		}
	}
	if checked == 0 {
		t.Fatal("no rated guests were checked")
	}
}

// TestCreateReview_HappyPath is User Story 2's acceptance scenario 2.1.
func TestCreateReview_HappyPath(t *testing.T) {
	h, st := newTestServer(t)
	guests, _ := st.ListGuests()
	target := guests[0]

	rec := do(t, h, "POST", "/api/reviews", map[string]any{
		"guest_id": target.ID,
		"host_id":  "h_test",
		"stay_id":  "stay_happy_1",
		"ratings": map[string]int{
			"house_rules": 5, "property_care": 5, "communication": 4, "noise": 5, "accuracy": 5,
		},
		"comment": "Excellent guest.",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	got := decode[createReviewResponse](t, rec)
	if got.Review.ID == "" {
		t.Error("created review has no ID")
	}
	if !got.ScoreAfter.Rated {
		t.Error("score after submission should be rated")
	}
	if got.ScoreAfter.StayCount != got.ScoreBefore.StayCount+1 {
		t.Errorf("review count did not increase: before=%d after=%d",
			got.ScoreBefore.StayCount, got.ScoreAfter.StayCount)
	}

	// The review must actually be readable back, not merely echoed.
	rec = do(t, h, "GET", "/api/guests/"+target.ID, nil)
	detail := decode[GuestDetail](t, rec)
	found := false
	for _, r := range detail.Reviews {
		if r.ID == got.Review.ID {
			found = true
		}
	}
	if !found {
		t.Error("submitted review does not appear in the guest's history")
	}
}

// TestCreateReview_RejectsOutOfRangeRatings is acceptance scenario 2.2 /
// FR-009: the whole submission is rejected and nothing is stored.
func TestCreateReview_RejectsOutOfRangeRatings(t *testing.T) {
	h, st := newTestServer(t)
	guests, _ := st.ListGuests()
	target := guests[0]

	before, _ := st.ReviewsForGuest(target.ID)

	for _, bad := range []int{0, 6, -1, 99} {
		rec := do(t, h, "POST", "/api/reviews", map[string]any{
			"guest_id": target.ID,
			"host_id":  "h_test",
			"stay_id":  fmt.Sprintf("stay_bad_%d", bad),
			"ratings": map[string]int{
				"house_rules": bad, "property_care": 4, "communication": 4, "noise": 4, "accuracy": 4,
			},
		})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("rating %d: status = %d, want 422", bad, rec.Code)
		}
		body := decode[errorBody](t, rec)
		if _, ok := body.Error.Fields["ratings.house_rules"]; !ok {
			t.Errorf("rating %d: expected a field-level error on ratings.house_rules, got %+v", bad, body.Error.Fields)
		}
	}

	after, _ := st.ReviewsForGuest(target.ID)
	if len(after) != len(before) {
		t.Errorf("rejected submissions were persisted: %d reviews before, %d after", len(before), len(after))
	}
}

// TestCreateReview_RejectsDuplicateStay is acceptance scenario 2.3 / FR-010.
func TestCreateReview_RejectsDuplicateStay(t *testing.T) {
	h, st := newTestServer(t)
	guests, _ := st.ListGuests()
	target := guests[0]

	payload := map[string]any{
		"guest_id": target.ID,
		"host_id":  "h_dup",
		"stay_id":  "stay_dup_1",
		"ratings": map[string]int{
			"house_rules": 4, "property_care": 4, "communication": 4, "noise": 4, "accuracy": 4,
		},
	}
	if rec := do(t, h, "POST", "/api/reviews", payload); rec.Code != http.StatusCreated {
		t.Fatalf("first submission status = %d, want 201: %s", rec.Code, rec.Body)
	}
	rec := do(t, h, "POST", "/api/reviews", payload)
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate submission status = %d, want 409", rec.Code)
	}
}

// TestCreateReview_RejectsUnknownGuest covers the spec edge case that a review
// must not silently create a guest.
func TestCreateReview_RejectsUnknownGuest(t *testing.T) {
	h, st := newTestServer(t)
	before, _ := st.ListGuests()

	rec := do(t, h, "POST", "/api/reviews", map[string]any{
		"guest_id": "g_nonexistent",
		"host_id":  "h_test",
		"stay_id":  "stay_ghost",
		"ratings": map[string]int{
			"house_rules": 4, "property_care": 4, "communication": 4, "noise": 4, "accuracy": 4,
		},
	})
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	after, _ := st.ListGuests()
	if len(after) != len(before) {
		t.Error("a review for an unknown guest created a guest")
	}
}

// TestCreateReview_IncidentAppearsAsPenaltyFactor is acceptance scenario 2.4.
func TestCreateReview_IncidentAppearsAsPenaltyFactor(t *testing.T) {
	h, st := newTestServer(t)
	guests, _ := st.ListGuests()

	// Pick a guest with a clean record so the new incident is unambiguous.
	var target domain.Guest
	for _, g := range guests {
		rs, _ := st.ReviewsForGuest(g.ID)
		clean := len(rs) > 0
		for _, r := range rs {
			if len(r.Incidents) > 0 {
				clean = false
			}
		}
		if clean {
			target = g
			break
		}
	}
	if target.ID == "" {
		t.Fatal("no clean-record guest in seed data")
	}

	rec := do(t, h, "POST", "/api/reviews", map[string]any{
		"guest_id": target.ID,
		"host_id":  "h_test",
		"stay_id":  "stay_incident_1",
		"ratings": map[string]int{
			"house_rules": 3, "property_care": 4, "communication": 4, "noise": 2, "accuracy": 4,
		},
		"incidents": []map[string]string{
			{"type": "noise_complaint", "severity": "moderate", "note": "Neighbors complained twice."},
		},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	got := decode[createReviewResponse](t, rec)

	if got.ScoreAfter.IncidentPenalty <= 0 {
		t.Error("expected a non-zero incident penalty after flagging a noise complaint")
	}
	if got.CompositeDelta >= 0 {
		t.Errorf("expected the composite to fall, delta was %+.1f", got.CompositeDelta)
	}
	hasPenalty := false
	for _, f := range got.ScoreAfter.Factors {
		if f.Kind == "penalty" {
			hasPenalty = true
		}
	}
	if !hasPenalty {
		t.Error("the incident should appear as a distinct penalty factor, separate from dimension ratings")
	}
}

func TestCreateReview_RejectsUnknownIncidentType(t *testing.T) {
	h, st := newTestServer(t)
	guests, _ := st.ListGuests()

	rec := do(t, h, "POST", "/api/reviews", map[string]any{
		"guest_id": guests[0].ID,
		"host_id":  "h_test",
		"stay_id":  "stay_badinc",
		"ratings": map[string]int{
			"house_rules": 4, "property_care": 4, "communication": 4, "noise": 4, "accuracy": 4,
		},
		"incidents": []map[string]string{{"type": "alien_abduction", "severity": "severe"}},
	})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
}

// TestCreateReview_ConcurrentSubmissionsAllPersist is the write-race edge case
// from the spec: simultaneous reviews must all survive.
func TestCreateReview_ConcurrentSubmissionsAllPersist(t *testing.T) {
	h, st := newTestServer(t)
	guests, _ := st.ListGuests()
	target := guests[0]

	before, _ := st.ReviewsForGuest(target.ID)
	const n = 24

	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := do(t, h, "POST", "/api/reviews", map[string]any{
				"guest_id": target.ID,
				"host_id":  fmt.Sprintf("h_conc_%d", i),
				"stay_id":  fmt.Sprintf("stay_conc_%d", i),
				"ratings": map[string]int{
					"house_rules": 4, "property_care": 4, "communication": 4, "noise": 4, "accuracy": 4,
				},
			})
			codes[i] = rec.Code
		}(i)
	}
	wg.Wait()

	for i, c := range codes {
		if c != http.StatusCreated {
			t.Errorf("concurrent submission %d returned %d, want 201", i, c)
		}
	}
	after, _ := st.ReviewsForGuest(target.ID)
	if len(after) != len(before)+n {
		t.Errorf("lost writes: %d reviews before, %d after, expected %d", len(before), len(after), len(before)+n)
	}

	// Distinct IDs — a collision would silently overwrite in a map-backed store.
	ids := map[string]bool{}
	for _, r := range after {
		if ids[r.ID] {
			t.Errorf("duplicate review ID generated: %s", r.ID)
		}
		ids[r.ID] = true
	}
}

func TestCreateReview_RejectsUnknownFields(t *testing.T) {
	h, st := newTestServer(t)
	guests, _ := st.ListGuests()

	body := bytes.NewBufferString(fmt.Sprintf(
		`{"guest_id":%q,"host_id":"h","stay_id":"s1","ratings":{"house_rules":4,"property_care":4,"communication":4,"noise":4,"accuracy":4},"is_admin":true}`,
		guests[0].ID))
	req := httptest.NewRequest("POST", "/api/reviews", body)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an unknown field", rec.Code)
	}
}

func TestCreateGuest_ValidatesAndRejectsDuplicateEmail(t *testing.T) {
	h, _ := newTestServer(t)

	rec := do(t, h, "POST", "/api/guests", map[string]any{"name": "", "email": "nope"})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
	body := decode[errorBody](t, rec)
	if _, ok := body.Error.Fields["name"]; !ok {
		t.Error("expected a field error on name")
	}
	if _, ok := body.Error.Fields["email"]; !ok {
		t.Error("expected a field error on email")
	}

	payload := map[string]any{"name": "New Person", "email": "new.person@example.com"}
	if rec := do(t, h, "POST", "/api/guests", payload); rec.Code != http.StatusCreated {
		t.Fatalf("creating a valid guest returned %d: %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "POST", "/api/guests", payload); rec.Code != http.StatusConflict {
		t.Errorf("duplicate email status = %d, want 409", rec.Code)
	}
}

// TestStats_ReconcilesWithUnderlyingData is acceptance scenario 3.1: the
// dashboard numbers must be computed, not decorative.
func TestStats_ReconcilesWithUnderlyingData(t *testing.T) {
	h, st := newTestServer(t)
	rec := do(t, h, "GET", "/api/stats", nil)
	stats := decode[map[string]any](t, rec)

	guests, _ := st.ListGuests()
	reviews, _ := st.AllReviews()

	if int(stats["total_guests"].(float64)) != len(guests) {
		t.Errorf("total_guests = %v, want %d", stats["total_guests"], len(guests))
	}
	if int(stats["total_reviews"].(float64)) != len(reviews) {
		t.Errorf("total_reviews = %v, want %d", stats["total_reviews"], len(reviews))
	}
	if stats["empty"].(bool) {
		t.Error("seeded store reported an empty portfolio")
	}

	rated := int(stats["rated_guests"].(float64))
	unrated := int(stats["unrated_guests"].(float64))
	if rated+unrated != len(guests) {
		t.Errorf("rated(%d) + unrated(%d) != total guests(%d)", rated, unrated, len(guests))
	}

	tiers := stats["tier_distribution"].(map[string]any)
	sum := 0
	for _, v := range tiers {
		sum += int(v.(float64))
	}
	if sum != rated {
		t.Errorf("tier distribution sums to %d, want %d rated guests", sum, rated)
	}

	if len(stats["review_timeline"].([]any)) != 12 {
		t.Error("expected a 12-month review timeline")
	}
}

// TestStats_EmptyPortfolio is acceptance scenario 3.2.
func TestStats_EmptyPortfolio(t *testing.T) {
	st, _ := store.NewFileStore("")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := New(st, log, WithClock(func() scoring.Time { return scoring.At(testNow) })).Routes()

	rec := do(t, h, "GET", "/api/stats", nil)
	stats := decode[map[string]any](t, rec)
	if !stats["empty"].(bool) {
		t.Error("an empty store should report empty=true rather than zeros as measurements")
	}
	if stats["average_score"].(float64) != 0 {
		t.Error("average score of nothing should be 0 and flagged empty, not invented")
	}
}

// TestScoringModel_IsPublished backs FR-007: the client can render the exact
// model without hardcoding the constants.
func TestScoringModel_IsPublished(t *testing.T) {
	h, _ := newTestServer(t)
	rec := do(t, h, "GET", "/api/scoring-model", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	m := decode[map[string]any](t, rec)

	dims := m["dimensions"].([]any)
	if len(dims) != len(domain.AllDimensions) {
		t.Errorf("published %d dimensions, want %d", len(dims), len(domain.AllDimensions))
	}
	var sum float64
	for _, d := range dims {
		sum += d.(map[string]any)["weight"].(float64)
	}
	if sum < 0.999 || sum > 1.001 {
		t.Errorf("published weights sum to %.4f, want 1.0", sum)
	}
	if len(m["incident_catalog"].([]any)) != len(domain.IncidentCatalog) {
		t.Error("incident catalog not fully published")
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h, _ := newTestServer(t)
	rec := do(t, h, "DELETE", "/api/guests", nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestCORSPreflight(t *testing.T) {
	h, _ := newTestServer(t)
	req := httptest.NewRequest("OPTIONS", "/api/guests", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Error("preflight response is missing CORS headers")
	}
}

// TestScoreLookupPerformance backs SC-005.
func TestScoreLookupPerformance(t *testing.T) {
	h, st := newTestServer(t)
	guests, _ := st.ListGuests()
	target := guests[0]

	for i := 0; i < 50; i++ {
		do(t, h, "POST", "/api/reviews", map[string]any{
			"guest_id": target.ID,
			"host_id":  fmt.Sprintf("h_perf_%d", i),
			"stay_id":  fmt.Sprintf("stay_perf_%d", i),
			"ratings": map[string]int{
				"house_rules": 4, "property_care": 5, "communication": 4, "noise": 5, "accuracy": 4,
			},
		})
	}

	start := time.Now()
	rec := do(t, h, "GET", "/api/guests/"+target.ID+"/score", nil)
	elapsed := time.Since(start)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("score lookup took %v, SC-005 budget is 50ms", elapsed)
	}
}
