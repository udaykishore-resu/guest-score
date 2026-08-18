package scoring

import (
	"math"
	"strings"
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
	if aged.EffectiveStayCount >= fresh.EffectiveStayCount {
		t.Errorf("effective review count should shrink with age: fresh=%.2f aged=%.2f",
			fresh.EffectiveStayCount, aged.EffectiveStayCount)
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
	if s.Handling == HandlingVIP {
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

// --- Tests anchored to the invention disclosure ------------------------------

// TestScoreRanges_MatchDisclosure pins "Score Ranges and Interpretation"
// verbatim. These bands decide what a guest is charged and whether they can be
// refused a room; drifting from the spec silently is not acceptable.
func TestScoreRanges_MatchDisclosure(t *testing.T) {
	cases := []struct {
		score   float64
		tier    string
		flagged bool
	}{
		{1000, "Excellent", false},
		{850, "Excellent", false},
		{800, "Excellent", false},
		{799, "Good", false},
		{700, "Good", false},
		{699, "Fair", false},
		{500, "Fair", false},
		{499, "Poor", true},
		{0, "Poor", true},
	}
	for _, tc := range cases {
		got := tierFor(tc.score, DefaultModel)
		if got.Name != tc.tier {
			t.Errorf("score %.0f -> %q, want %q", tc.score, got.Name, tc.tier)
		}
		if got.Flagged != tc.flagged {
			t.Errorf("score %.0f flagged=%v, want %v", tc.score, got.Flagged, tc.flagged)
		}
	}
}

// TestWorkedExamples_MatchDisclosure checks the two point values the disclosure
// states outright. If the catalogue is retuned, these are the numbers a reader
// of the spec will check first.
func TestWorkedExamples_MatchDisclosure(t *testing.T) {
	// "Minor policy violation: −50 points"
	minor := domain.IncRulesViolation.BasePenalty() * domain.SevMinor.Multiplier()
	approx(t, minor, 50, 0.01, "minor policy violation")

	// "Severe property damage: −100 points"
	severe := domain.IncPropertyDamage.BasePenalty() * domain.SevSevere.Multiplier()
	approx(t, severe, 100, 1.0, "severe property damage")

	// "Positive stays over a year: +100 points"
	approx(t, DefaultModel.TenureMaxPoints, 100, 0.01, "tenure cap")
	approx(t, DefaultModel.TenurePointsPerYear, 100, 0.01, "tenure per year")
}

// TestNewGuest_OpensAtFiveHundred is the disclosure's "New guests receive an
// initial score of 500, categorized as 'New'." It deliberately contradicts the
// earlier "never fabricate a score" rule, and the spec wins.
func TestNewGuest_OpensAtFiveHundred(t *testing.T) {
	s := Compute(nil, eval, DefaultModel)

	if s.Composite != 500 {
		t.Errorf("opening score = %.1f, want 500", s.Composite)
	}
	if s.Tier != "New" {
		t.Errorf("opening tier = %q, want \"New\"", s.Tier)
	}
	if s.Rated {
		t.Error("an opening balance must not report as an earned standing")
	}
	if s.Flagged {
		t.Error("a new guest must never open flagged")
	}
	if s.DepositMultiplier != 1.0 {
		t.Errorf("new guest deposit multiplier = %.2f, want 1.0 (standard)", s.DepositMultiplier)
	}
	if len(s.Factors) == 0 {
		t.Error("the opening score still needs an explanation")
	}
}

// TestDeposit_FallsAsScoreRises is the disclosure's core commercial rule:
// "High scores result in lower deposits. Low scores may necessitate higher
// deposits or booking denial."
func TestDeposit_FallsAsScoreRises(t *testing.T) {
	// Tiers are ordered highest floor first, so walking the list is walking
	// *down* the standing — each successive deposit must be larger.
	prev := 0.0
	for _, tier := range DefaultModel.Tiers {
		if tier.DepositMultiplier <= prev {
			t.Errorf("deposit must rise as standing falls: %q=%.2f is not above the tier above it (%.2f)",
				tier.Name, tier.DepositMultiplier, prev)
		}
		prev = tier.DepositMultiplier
	}
	if tierFor(1000, DefaultModel).DepositMultiplier >= 1.0 {
		t.Error("the top tier should post less than a standard deposit")
	}
	if tierFor(0, DefaultModel).DepositMultiplier <= 1.0 {
		t.Error("the bottom tier should post more than a standard deposit")
	}
}

// TestScoreStaysInPublishedRange guards the 0–1000 bounds under the worst and
// best possible records.
func TestScoreStaysInPublishedRange(t *testing.T) {
	worst := []domain.Review{}
	for i := 0; i < 10; i++ {
		worst = append(worst, review(float64(i), all(1),
			domain.Incident{Type: domain.IncPropertyDamage, Severity: domain.SevSevere},
			domain.Incident{Type: domain.IncRulesViolation, Severity: domain.SevSevere},
			domain.Incident{Type: domain.IncMisconduct, Severity: domain.SevSevere},
		))
	}
	if s := Compute(worst, eval, DefaultModel); s.Composite != DefaultModel.ScoreMin {
		t.Errorf("worst possible record = %.1f, want the floor %.0f", s.Composite, DefaultModel.ScoreMin)
	}

	best := make([]domain.Review, 0, 30)
	for i := 0; i < 30; i++ {
		r := review(float64(i), all(5))
		r.Commendations = []domain.Commendation{{Type: domain.ComExceptionalCare}, {Type: domain.ComStaffPraise}}
		best = append(best, r)
	}
	if s := Compute(best, eval, DefaultModel); s.Composite > DefaultModel.ScoreMax {
		t.Errorf("best possible record = %.1f, exceeds the ceiling %.0f", s.Composite, DefaultModel.ScoreMax)
	}
}

// TestTenure_RewardsLengthOfHistory is the modelling correction: recency decay
// alone treats a long, quiet guest as a thin file, which is backwards.
func TestTenure_RewardsLengthOfHistory(t *testing.T) {
	recent := repeat(6, review(30, all(4)))

	longstanding := make([]domain.Review, 0, 6)
	for i := 0; i < 6; i++ {
		longstanding = append(longstanding, review(float64(30+i*220), all(4)))
	}

	a := Compute(recent, eval, DefaultModel)
	b := Compute(longstanding, eval, DefaultModel)

	if a.TenureBonus >= b.TenureBonus {
		t.Errorf("a longer file should earn more tenure: recent=%.1f longstanding=%.1f",
			a.TenureBonus, b.TenureBonus)
	}
	if b.TenureBonus > DefaultModel.TenureMaxPoints {
		t.Errorf("tenure bonus %.1f exceeds the cap %.1f", b.TenureBonus, DefaultModel.TenureMaxPoints)
	}
	for _, f := range b.Factors {
		if f.Kind == "bonus" && f.Impact > 0 {
			return
		}
	}
	t.Error("tenure should appear as a positive factor in the explanation")
}

// TestDisputedRecordsAreExcluded is the appeal mechanism the legal section of
// the disclosure makes mandatory: a guest must be able to contest a record, and
// a contested record must not silently keep costing them.
func TestDisputedRecordsAreExcluded(t *testing.T) {
	damaging := review(20, all(1),
		domain.Incident{Type: domain.IncPropertyDamage, Severity: domain.SevSevere})
	clean := repeat(6, review(40, all(5)))

	withRecord := Compute(append(clone(clean), damaging), eval, DefaultModel)

	disputedRecord := damaging
	disputedRecord.Dispute = domain.Dispute{Status: domain.DisputeOpen, Reason: "Damage predated the stay."}
	underDispute := Compute(append(clone(clean), disputedRecord), eval, DefaultModel)

	if !(underDispute.Composite > withRecord.Composite) {
		t.Errorf("an open dispute must lift the score by excluding the record: %.1f vs %.1f",
			withRecord.Composite, underDispute.Composite)
	}
	if underDispute.DisputedCount != 1 {
		t.Errorf("disputed count = %d, want 1", underDispute.DisputedCount)
	}
	if underDispute.StayCount != len(clean) {
		t.Errorf("stay count = %d, want %d (the disputed record is excluded)", underDispute.StayCount, len(clean))
	}

	// A rejected dispute means the record stands and must count again.
	rejected := damaging
	rejected.Dispute = domain.Dispute{Status: domain.DisputeRejected}
	after := Compute(append(clone(clean), rejected), eval, DefaultModel)
	if math.Abs(after.Composite-withRecord.Composite) > 0.05 {
		t.Errorf("a rejected dispute should score identically to no dispute: %.1f vs %.1f",
			after.Composite, withRecord.Composite)
	}

	// The exclusion must be disclosed, not silent.
	found := false
	for _, f := range underDispute.Factors {
		if f.Kind == "context" && strings.Contains(f.Description, "dispute") {
			found = true
		}
	}
	if !found {
		t.Error("an excluded record must be disclosed in the explanation")
	}
}

// TestFlaggedOnlyBelowFiveHundred: the disclosure permits banning below 500 and
// nowhere else. A flag is a permission for a human, never an automatic action.
func TestFlaggedOnlyBelowFiveHundred(t *testing.T) {
	for score := 0.0; score <= 1000; score += 25 {
		tier := tierFor(score, DefaultModel)
		wantFlag := score < 500
		if tier.Flagged != wantFlag {
			t.Errorf("score %.0f flagged=%v, want %v", score, tier.Flagged, wantFlag)
		}
	}
}

// TestTier_AgreesWithDisplayedScore guards the rounding class of bug: the tier
// must follow the number the guest is shown, not the raw float behind it.
func TestTier_AgreesWithDisplayedScore(t *testing.T) {
	sets := [][]domain.Review{
		repeat(4, review(30, all(4))),
		repeat(11, review(20, all(5))),
		repeat(7, review(300, all(4))),
		{review(0, all(5))},
		repeat(20, review(0, all(1))),
	}
	for _, rs := range sets {
		s := Compute(rs, eval, DefaultModel)
		want := tierFor(s.Composite, DefaultModel)
		if s.Tier != want.Name {
			t.Errorf("displays %.1f (tier %s) but %.1f resolves to %s", s.Composite, s.Tier, s.Composite, want.Name)
		}
		if s.DiscountPercent != want.Discount || s.DepositMultiplier != want.DepositMultiplier {
			t.Errorf("score %.1f: terms do not match its tier", s.Composite)
		}
		if s.NextTier != "" && s.PointsToNextTier <= 0 {
			t.Errorf("score %.1f in %s claims %.1f points to %s", s.Composite, s.Tier, s.PointsToNextTier, s.NextTier)
		}
	}
}

// TestCommendationsCannotMaskAnIncident is the regression for the clamp bug:
// bonuses lift toward the ceiling, but a penalty must always bite.
func TestCommendationsCannotMaskAnIncident(t *testing.T) {
	praised := make([]domain.Review, 0, 10)
	for i := 0; i < 10; i++ {
		r := review(float64(10*i), all(5))
		r.Commendations = []domain.Commendation{{Type: domain.ComExceptionalCare}}
		praised = append(praised, r)
	}
	clean := Compute(praised, eval, DefaultModel)

	damaged := append(clone(praised), review(20, all(1),
		domain.Incident{Type: domain.IncPropertyDamage, Severity: domain.SevSevere}))
	after := Compute(damaged, eval, DefaultModel)

	if !(after.Composite < clean.Composite) {
		t.Errorf("a severe incident must lower even a heavily commended guest: %.1f -> %.1f",
			clean.Composite, after.Composite)
	}
	if after.Handling == HandlingVIP {
		t.Error("a recent severe incident must not leave the guest on top-tier handling")
	}
}
