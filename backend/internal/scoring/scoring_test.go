package scoring

import (
	"math"
	"testing"
	"time"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
)

// eval is a fixed evaluation instant. Every test computes against it so results
// are reproducible to the decimal (Constitution Principle III, SC-003).
var eval = At(time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC))

func daysAgo(n float64) time.Time {
	return eval.Std().Add(-time.Duration(n * 24 * float64(time.Hour)))
}

func review(ageDays float64, r domain.Ratings, incs ...domain.Incident) domain.Review {
	return domain.Review{
		ID: "r", GuestID: "g", HostID: "h", StayID: "s",
		Ratings: r, Incidents: incs, SubmittedAt: daysAgo(ageDays),
	}
}

func all(v int) domain.Ratings {
	return domain.Ratings{HouseRules: v, PropertyCare: v, Communication: v, Noise: v, Accuracy: v}
}

func approx(t *testing.T, got, want, tol float64, label string) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.4f, want %.4f (tolerance %.4f)", label, got, want, tol)
	}
}

// --- The six mandated cases (Constitution Principle V) -----------------------

func TestCompute_MandatedCases(t *testing.T) {
	tests := []struct {
		name           string
		reviews        []domain.Review
		wantRated      bool
		wantComposite  float64
		tol            float64
		wantGrade      string
		wantConfidence Confidence
		wantRec        Recommendation
	}{
		{
			name:           "empty: unrated, never a fabricated zero",
			reviews:        nil,
			wantRated:      false,
			wantComposite:  0,
			tol:            0.001,
			wantGrade:      "",
			wantConfidence: ConfidenceNone,
			wantRec:        RecInsufficient,
		},
		{
			// One fresh perfect review. Shrinkage dominates: weight 1.0 against
			// a prior strength of 3.0 means the prior carries 75% of the mass.
			// adjusted = (3.9*3 + 5*1)/4 = 4.175 -> (4.175-1)/4*100 = 79.375
			name:           "single review: shrinkage keeps one rave from reading as a track record",
			reviews:        []domain.Review{review(0, all(5))},
			wantRated:      true,
			wantComposite:  79.4,
			tol:            0.15,
			wantGrade:      "B",
			wantConfidence: ConfidenceLow,
			wantRec:        RecAcceptWithCare,
		},
		{
			// Twenty fresh perfect reviews: totalWeight=20.
			// adjusted = (11.7 + 100)/23 = 4.8565 -> 96.41
			name:           "perfect: many fresh 5s approach but never reach 100",
			reviews:        repeat(20, review(0, all(5))),
			wantRated:      true,
			wantComposite:  96.4,
			tol:            0.15,
			wantGrade:      "A",
			wantConfidence: ConfidenceHigh,
			wantRec:        RecAccept,
		},
		{
			// Twenty fresh 1s: adjusted = (11.7+20)/23 = 1.3783 -> 9.46
			name:           "floor: many fresh 1s bottom out without going negative",
			reviews:        repeat(20, review(0, all(1))),
			wantRated:      true,
			wantComposite:  9.5,
			tol:            0.15,
			wantGrade:      "F",
			wantConfidence: ConfidenceHigh,
			wantRec:        RecDecline,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Compute(tc.reviews, eval, DefaultModel)
			if got.Rated != tc.wantRated {
				t.Fatalf("Rated = %v, want %v", got.Rated, tc.wantRated)
			}
			approx(t, got.Composite, tc.wantComposite, tc.tol, "Composite")
			if tc.wantGrade != "" && got.Grade != tc.wantGrade {
				t.Errorf("Grade = %q, want %q", got.Grade, tc.wantGrade)
			}
			if got.Confidence != tc.wantConfidence {
				t.Errorf("Confidence = %q, want %q", got.Confidence, tc.wantConfidence)
			}
			if got.Recommendation != tc.wantRec {
				t.Errorf("Recommendation = %q, want %q", got.Recommendation, tc.wantRec)
			}
		})
	}
}

// TestCompute_ScoreNeverLeavesRange is the hard invariant behind the floor and
// ceiling edge cases: no combination of ratings and stacked severe incidents
// may push the composite outside [0,100].
func TestCompute_ScoreNeverLeavesRange(t *testing.T) {
	worst := []domain.Review{}
	for i := 0; i < 10; i++ {
		worst = append(worst, review(float64(i), all(1),
			domain.Incident{Type: domain.IncPropertyDamage, Severity: domain.SevSevere},
			domain.Incident{Type: domain.IncNoiseComplaint, Severity: domain.SevSevere},
			domain.Incident{Type: domain.IncUnauthorizedGuest, Severity: domain.SevSevere},
		))
	}
	s := Compute(worst, eval, DefaultModel)
	if s.Composite < 0 {
		t.Errorf("composite went negative: %.2f", s.Composite)
	}
	if s.Composite != 0 {
		t.Errorf("expected the worst possible record to floor at exactly 0, got %.2f", s.Composite)
	}

	best := repeat(50, review(0, all(5)))
	s = Compute(best, eval, DefaultModel)
	if s.Composite > 100 {
		t.Errorf("composite exceeded 100: %.2f", s.Composite)
	}
}

// TestDecay_AcrossYearBoundaries pins the half-life curve itself.
func TestDecay_AcrossYearBoundaries(t *testing.T) {
	tests := []struct {
		ageDays float64
		want    float64
	}{
		{0, 1.0},
		{365, 0.5},    // one half-life
		{730, 0.25},   // two
		{1095, 0.125}, // three
		{-10, 1.0},    // future timestamp treated as fresh, not amplified
	}
	for _, tc := range tests {
		got := decay(tc.ageDays, DefaultModel.ReviewHalfLife)
		approx(t, got, tc.want, 0.0001, "decay at "+itoa(tc.ageDays)+"d")
	}
}

// TestCompute_TimeDecayReducesInfluence checks the property that matters at the
// API level: an identical review set scored later yields a score closer to the
// prior, because the evidence has aged.
func TestCompute_TimeDecayReducesInfluence(t *testing.T) {
	rs := repeat(6, review(0, all(5)))
	fresh := Compute(rs, eval, DefaultModel)

	later := At(eval.Std().AddDate(3, 0, 0))
	aged := Compute(rs, later, DefaultModel)

	if !(aged.Composite < fresh.Composite) {
		t.Errorf("expected aged score %.2f to be below fresh score %.2f", aged.Composite, fresh.Composite)
	}
	if aged.EffectiveReviewCount >= fresh.EffectiveReviewCount {
		t.Errorf("effective review count should shrink with age: fresh=%.2f aged=%.2f",
			fresh.EffectiveReviewCount, aged.EffectiveReviewCount)
	}
	if aged.Confidence == ConfidenceHigh {
		t.Errorf("confidence should degrade once all evidence is 3 years old, got %q", aged.Confidence)
	}
}

// TestCompute_IncidentPenaltiesStack covers the stacking edge case and confirms
// severity and recency both scale the deduction.
func TestCompute_IncidentPenaltiesStack(t *testing.T) {
	base := repeat(8, review(30, all(4)))

	clean := Compute(base, eval, DefaultModel)

	oneMinor := Compute(append(clone(base),
		review(30, all(4), domain.Incident{Type: domain.IncLateCheckout, Severity: domain.SevMinor})),
		eval, DefaultModel)

	oneSevere := Compute(append(clone(base),
		review(30, all(4), domain.Incident{Type: domain.IncPropertyDamage, Severity: domain.SevSevere})),
		eval, DefaultModel)

	twoSevere := Compute(append(clone(base),
		review(30, all(4),
			domain.Incident{Type: domain.IncPropertyDamage, Severity: domain.SevSevere},
			domain.Incident{Type: domain.IncUnauthorizedGuest, Severity: domain.SevSevere})),
		eval, DefaultModel)

	if !(clean.IncidentPenalty == 0) {
		t.Errorf("clean record should carry no penalty, got %.2f", clean.IncidentPenalty)
	}
	if !(oneMinor.IncidentPenalty < oneSevere.IncidentPenalty) {
		t.Errorf("severe should outweigh minor: minor=%.2f severe=%.2f",
			oneMinor.IncidentPenalty, oneSevere.IncidentPenalty)
	}
	if !(twoSevere.IncidentPenalty > oneSevere.IncidentPenalty) {
		t.Errorf("penalties must stack: one=%.2f two=%.2f",
			oneSevere.IncidentPenalty, twoSevere.IncidentPenalty)
	}
	if !(twoSevere.Composite < oneSevere.Composite && oneSevere.Composite < clean.Composite) {
		t.Errorf("composite must fall as incidents accumulate: clean=%.2f one=%.2f two=%.2f",
			clean.Composite, oneSevere.Composite, twoSevere.Composite)
	}

	// Recency: the same severe incident should cost less once it is old.
	recent := Compute([]domain.Review{
		review(10, all(4), domain.Incident{Type: domain.IncPropertyDamage, Severity: domain.SevSevere})},
		eval, DefaultModel)
	old := Compute([]domain.Review{
		review(900, all(4), domain.Incident{Type: domain.IncPropertyDamage, Severity: domain.SevSevere})},
		eval, DefaultModel)
	if !(old.IncidentPenalty < recent.IncidentPenalty) {
		t.Errorf("aged incident should cost less: recent=%.2f old=%.2f",
			recent.IncidentPenalty, old.IncidentPenalty)
	}
}

// TestCompute_SevereIncidentBlocksCleanAccept is acceptance scenario 1.3: a
// strong average plus a recent severe incident must not return "accept".
func TestCompute_SevereIncidentBlocksCleanAccept(t *testing.T) {
	rs := repeat(15, review(60, all(5)))
	rs = append(rs, review(45, all(5),
		domain.Incident{Type: domain.IncPropertyDamage, Severity: domain.SevSevere}))

	s := Compute(rs, eval, DefaultModel)
	if s.Recommendation == RecAccept {
		t.Errorf("a recent severe damage incident must not yield a clean accept (score %.1f)", s.Composite)
	}
	found := false
	for _, f := range s.Factors {
		if f.Kind == "penalty" {
			found = true
		}
	}
	if !found {
		t.Error("expected the incident to appear as a distinct penalty factor (FR-007)")
	}
}

// TestCompute_IsPure is SC-003: identical inputs and a fixed evaluation time
// must produce a bit-identical result, twice.
func TestCompute_IsPure(t *testing.T) {
	rs := []domain.Review{
		review(5, domain.Ratings{HouseRules: 5, PropertyCare: 4, Communication: 5, Noise: 3, Accuracy: 4}),
		review(200, domain.Ratings{HouseRules: 3, PropertyCare: 3, Communication: 4, Noise: 2, Accuracy: 5},
			domain.Incident{Type: domain.IncNoiseComplaint, Severity: domain.SevModerate}),
		review(700, domain.Ratings{HouseRules: 4, PropertyCare: 5, Communication: 5, Noise: 5, Accuracy: 4}),
	}
	a := Compute(rs, eval, DefaultModel)
	b := Compute(rs, eval, DefaultModel)

	if a.Composite != b.Composite || a.BaseScore != b.BaseScore || a.IncidentPenalty != b.IncidentPenalty {
		t.Errorf("Compute is not deterministic:\n  a=%+v\n  b=%+v", a, b)
	}
	if len(a.Factors) != len(b.Factors) {
		t.Errorf("factor list length differs between runs: %d vs %d", len(a.Factors), len(b.Factors))
	}
	for i := range a.Factors {
		if a.Factors[i] != b.Factors[i] {
			t.Errorf("factor %d differs between runs:\n  a=%+v\n  b=%+v", i, a.Factors[i], b.Factors[i])
		}
	}
}

// TestCompute_ExplanationIsComplete enforces Constitution Principle IV: the API
// never returns a bare number.
func TestCompute_ExplanationIsComplete(t *testing.T) {
	s := Compute(repeat(5, review(20, domain.Ratings{HouseRules: 5, PropertyCare: 4, Communication: 4, Noise: 3, Accuracy: 5})), eval, DefaultModel)

	if len(s.Dimensions) != len(domain.AllDimensions) {
		t.Errorf("expected a breakdown for all %d dimensions, got %d", len(domain.AllDimensions), len(s.Dimensions))
	}
	for _, d := range s.Dimensions {
		if d.Label == "" {
			t.Errorf("dimension %q has no human-readable label", d.Dimension)
		}
		if d.Weight <= 0 {
			t.Errorf("dimension %q reports no weight", d.Dimension)
		}
	}
	if len(s.Factors) == 0 {
		t.Error("expected at least one explanatory factor")
	}
	if s.Headline == "" {
		t.Error("expected a headline recommendation")
	}
}

// TestModel_WeightsSumToOne guards the invariant the 0-100 rescale depends on.
func TestModel_WeightsSumToOne(t *testing.T) {
	var sum float64
	for _, d := range domain.AllDimensions {
		w, ok := DefaultModel.Weights[d]
		if !ok {
			t.Fatalf("model is missing a weight for dimension %q", d)
		}
		sum += w
	}
	approx(t, sum, 1.0, 0.0001, "sum of dimension weights")
}

// TestGradeBands_AreContiguousAndDescending guards against a gap or an
// out-of-order band silently mis-grading a score.
func TestGradeBands_AreContiguousAndDescending(t *testing.T) {
	bands := DefaultModel.GradeBands
	for i := 1; i < len(bands); i++ {
		if bands[i].Min >= bands[i-1].Min {
			t.Errorf("grade bands must descend: %q(%.0f) followed by %q(%.0f)",
				bands[i-1].Grade, bands[i-1].Min, bands[i].Grade, bands[i].Min)
		}
	}
	if bands[len(bands)-1].Min != 0 {
		t.Error("the lowest grade band must start at 0 so every score grades")
	}
	for score := 0.0; score <= 100.0; score += 0.5 {
		if g, _ := gradeFor(score, DefaultModel); g == "" {
			t.Fatalf("score %.1f produced no grade", score)
		}
	}
}

// --- helpers -----------------------------------------------------------------

func repeat(n int, r domain.Review) []domain.Review {
	out := make([]domain.Review, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, r)
	}
	return out
}

func clone(rs []domain.Review) []domain.Review {
	return append([]domain.Review(nil), rs...)
}

func itoa(f float64) string {
	return time.Duration(f * float64(time.Hour) * 24).String()
}
