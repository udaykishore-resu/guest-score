// Package scoring computes explainable guest reputation scores.
//
// Constitution Principle III: everything here is pure. Compute takes the
// evaluation time as a parameter rather than calling time.Now, performs no I/O,
// and mutates nothing. Given identical inputs it returns an identical result,
// which is what makes the six mandated table tests possible and what makes a
// score defensible when someone asks how it was reached.
package scoring

import (
	"fmt"
	"math"
	"sort"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
)

// Model holds every tunable constant in one place. It is exposed verbatim over
// /api/scoring-model so the weights are inspectable from outside the binary
// (Constitution Principle IV).
type Model struct {
	Weights          map[domain.Dimension]float64 `json:"weights"`
	ReviewHalfLife   float64                      `json:"review_half_life_days"`
	IncidentHalfLife float64                      `json:"incident_half_life_days"`
	PriorMean        float64                      `json:"prior_mean"`
	PriorStrength    float64                      `json:"prior_strength"`
	GradeBands       []GradeBand                  `json:"grade_bands"`
}

// GradeBand maps a score floor to a letter grade and its description.
type GradeBand struct {
	Min         float64 `json:"min"`
	Grade       string  `json:"grade"`
	Label       string  `json:"label"`
	Description string  `json:"description"`
}

// DefaultModel is the production scoring configuration.
//
// The weights encode a judgment about host risk: rule compliance and property
// care carry the most weight because they are where the money and the permits
// are, and booking accuracy carries the least because it is usually a
// correctable annoyance rather than a loss. They sum to 1.0.
var DefaultModel = Model{
	Weights: map[domain.Dimension]float64{
		domain.DimHouseRules:    0.28,
		domain.DimPropertyCare:  0.26,
		domain.DimCommunication: 0.18,
		domain.DimNoise:         0.16,
		domain.DimAccuracy:      0.12,
	},
	ReviewHalfLife:   365.0,
	IncidentHalfLife: 180.0,
	PriorMean:        3.9,
	PriorStrength:    3.0,
	GradeBands: []GradeBand{
		{85, "A", "Excellent", "Consistently strong history. Low risk."},
		{70, "B", "Good", "Solid history with minor blemishes."},
		{55, "C", "Fair", "Mixed history. Worth a closer look before accepting."},
		{40, "D", "Poor", "Repeated problems on record."},
		{0, "F", "High risk", "Serious or repeated incidents on record."},
	},
}

// Confidence expresses how much evidence the score rests on.
type Confidence string

const (
	ConfidenceNone   Confidence = "none"
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

// Recommendation is the booking guidance derived from the score.
type Recommendation string

const (
	RecAccept          Recommendation = "accept"
	RecAcceptWithCare  Recommendation = "accept_with_conditions"
	RecReview          Recommendation = "manual_review"
	RecDecline         Recommendation = "decline"
	RecInsufficient    Recommendation = "insufficient_data"
)

// DimensionScore is one dimension's contribution to the composite.
type DimensionScore struct {
	Dimension   domain.Dimension `json:"dimension"`
	Label       string           `json:"label"`
	Average     float64          `json:"average"`      // decay-weighted mean, 1-5
	Weight      float64          `json:"weight"`       // from the model
	Contributes float64          `json:"contributes"`  // points of the 100 attributable here
}

// Factor is one human-readable reason the score is what it is.
type Factor struct {
	Kind        string  `json:"kind"` // "strength" | "concern" | "penalty" | "context"
	Description string  `json:"description"`
	Impact      float64 `json:"impact"` // signed points, 0 for pure context
}

// Score is the fully explained result. Nothing here is persisted; it is
// recomputed from reviews on every read.
type Score struct {
	Rated bool `json:"rated"`

	Composite  float64 `json:"composite"`  // 0-100, one decimal
	Grade      string  `json:"grade"`
	GradeLabel string  `json:"grade_label"`

	Confidence     Confidence     `json:"confidence"`
	Recommendation Recommendation `json:"recommendation"`
	Headline       string         `json:"headline"`

	ReviewCount          int     `json:"review_count"`
	EffectiveReviewCount float64 `json:"effective_review_count"` // sum of decay weights
	IncidentCount        int     `json:"incident_count"`

	RawAverage      float64 `json:"raw_average"`      // 1-5 before shrinkage
	AdjustedAverage float64 `json:"adjusted_average"` // 1-5 after shrinkage
	BaseScore       float64 `json:"base_score"`       // 0-100 before penalties
	IncidentPenalty float64 `json:"incident_penalty"` // total points deducted

	Dimensions []DimensionScore `json:"dimensions"`
	Factors    []Factor         `json:"factors"`
}

// Compute produces an explained score from a guest's reviews as of time `now`.
//
// The four stages match plan.md: per-review weighted quality, time-decayed
// aggregation, Bayesian shrinkage toward the population prior, then incident
// penalties applied on the 0-100 scale.
func Compute(reviews []domain.Review, now Time, m Model) Score {
	if len(reviews) == 0 {
		return Score{
			Rated:          false,
			Confidence:     ConfidenceNone,
			Recommendation: RecInsufficient,
			Headline:       "No stay history on record. Fall back to standard ID and payment verification.",
			Dimensions:     emptyDimensions(m),
			Factors: []Factor{{
				Kind:        "context",
				Description: "This guest has no reviews yet. The absence of a score is not a negative signal.",
			}},
		}
	}

	// --- Stage 1 & 2: per-dimension decay-weighted means ---------------------
	type acc struct{ weighted, weight float64 }
	dimAcc := make(map[domain.Dimension]*acc, len(domain.AllDimensions))
	for _, d := range domain.AllDimensions {
		dimAcc[d] = &acc{}
	}

	var totalWeight float64
	var weightedQualitySum float64
	var rawQualitySum float64

	for _, r := range reviews {
		w := decay(now.DaysSince(r.SubmittedAt), m.ReviewHalfLife)
		totalWeight += w

		var q float64 // this review's weighted 1-5 quality
		for _, d := range domain.AllDimensions {
			v := float64(r.Ratings.Get(d))
			q += v * m.Weights[d]
			a := dimAcc[d]
			a.weighted += v * w
			a.weight += w
		}
		weightedQualitySum += q * w
		rawQualitySum += q
	}

	rawAverage := rawQualitySum / float64(len(reviews))

	// --- Stage 3: Bayesian shrinkage toward the population prior -------------
	// Two glowing reviews should not read the same as twenty. The pseudo-count
	// C acts as C imaginary reviews at the prior mean.
	adjusted := (m.PriorMean*m.PriorStrength + weightedQualitySum) /
		(m.PriorStrength + totalWeight)

	base := clamp((adjusted-1.0)/4.0*100.0, 0, 100)

	// --- Stage 4: incident penalties on the 0-100 scale ----------------------
	var penalty float64
	incidentCount := 0
	penaltyByType := map[domain.IncidentType]float64{}
	countByType := map[domain.IncidentType]int{}

	for _, r := range reviews {
		for _, inc := range r.Incidents {
			incidentCount++
			p := inc.Type.BasePenalty() *
				inc.Severity.Multiplier() *
				decay(now.DaysSince(r.SubmittedAt), m.IncidentHalfLife)
			penalty += p
			penaltyByType[inc.Type] += p
			countByType[inc.Type]++
		}
	}

	composite := clamp(base-penalty, 0, 100)

	// --- Assemble the explanation -------------------------------------------
	dims := make([]DimensionScore, 0, len(domain.AllDimensions))
	for _, d := range domain.AllDimensions {
		a := dimAcc[d]
		avg := 0.0
		if a.weight > 0 {
			avg = a.weighted / a.weight
		}
		w := m.Weights[d]
		dims = append(dims, DimensionScore{
			Dimension:   d,
			Label:       d.Label(),
			Average:     round1(avg),
			Weight:      w,
			Contributes: round1(clamp((avg-1.0)/4.0*100.0, 0, 100) * w),
		})
	}

	conf := confidenceFor(totalWeight)
	grade, gradeLabel := gradeFor(composite, m)
	rec := recommend(composite, conf, reviews, now, m)

	return Score{
		Rated:                true,
		Composite:            round1(composite),
		Grade:                grade,
		GradeLabel:           gradeLabel,
		Confidence:           conf,
		Recommendation:       rec,
		Headline:             headline(rec, conf, composite),
		ReviewCount:          len(reviews),
		EffectiveReviewCount: round2(totalWeight),
		IncidentCount:        incidentCount,
		RawAverage:           round2(rawAverage),
		AdjustedAverage:      round2(adjusted),
		BaseScore:            round1(base),
		IncidentPenalty:      round1(penalty),
		Dimensions:           dims,
		Factors:              buildFactors(dims, penaltyByType, countByType, totalWeight, len(reviews), rawAverage, adjusted, m),
	}
}

// buildFactors turns the numbers into the plain-language list required by FR-007.
func buildFactors(
	dims []DimensionScore,
	penaltyByType map[domain.IncidentType]float64,
	countByType map[domain.IncidentType]int,
	effective float64,
	reviewCount int,
	raw, adjusted float64,
	m Model,
) []Factor {
	factors := make([]Factor, 0, 8)

	// Strongest and weakest dimensions, but only when they are actually
	// notable — praising a 3.1 as a strength would be noise.
	sorted := append([]DimensionScore(nil), dims...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Average > sorted[j].Average })

	if len(sorted) > 0 && sorted[0].Average >= 4.5 {
		factors = append(factors, Factor{
			Kind:        "strength",
			Description: fmt.Sprintf("%s is a standout at %.1f/5 across all stays.", sorted[0].Label, sorted[0].Average),
			Impact:      round1((sorted[0].Average - m.PriorMean) * sorted[0].Weight * 25),
		})
	}
	if last := sorted[len(sorted)-1]; last.Average > 0 && last.Average <= 3.4 {
		factors = append(factors, Factor{
			Kind:        "concern",
			Description: fmt.Sprintf("%s is the weakest area at %.1f/5.", last.Label, last.Average),
			Impact:      round1((last.Average - m.PriorMean) * last.Weight * 25),
		})
	}

	// Incident penalties, largest first, so the biggest deduction is read first.
	types := make([]domain.IncidentType, 0, len(penaltyByType))
	for t := range penaltyByType {
		types = append(types, t)
	}
	sort.SliceStable(types, func(i, j int) bool { return penaltyByType[types[i]] > penaltyByType[types[j]] })
	for _, t := range types {
		n := countByType[t]
		plural := ""
		if n > 1 {
			plural = "s"
		}
		factors = append(factors, Factor{
			Kind: "penalty",
			Description: fmt.Sprintf("%d %s incident%s on record, costing %.1f points after age adjustment.",
				n, t.Label(), plural, penaltyByType[t]),
			Impact: -round1(penaltyByType[t]),
		})
	}

	// Evidence volume, and the direction shrinkage moved the score.
	switch {
	case effective < 1.5:
		factors = append(factors, Factor{
			Kind: "context",
			Description: fmt.Sprintf("Only %.1f effective reviews of evidence (%d total, aged). The score is pulled hard toward the %.1f population average, so treat it as provisional.",
				effective, reviewCount, m.PriorMean),
		})
	case effective < 4.0:
		factors = append(factors, Factor{
			Kind: "context",
			Description: fmt.Sprintf("%.1f effective reviews of evidence. Enough to be indicative, not enough to be conclusive.", effective),
		})
	default:
		factors = append(factors, Factor{
			Kind: "context",
			Description: fmt.Sprintf("%.1f effective reviews of evidence across %d stays. The score rests on a solid base.", effective, reviewCount),
		})
	}

	if float64(reviewCount) > 0 && effective < float64(reviewCount)*0.6 {
		factors = append(factors, Factor{
			Kind:        "context",
			Description: "Much of this history is over a year old and has been discounted accordingly.",
		})
	}

	if diff := adjusted - raw; math.Abs(diff) >= 0.08 {
		dir := "up toward"
		if diff < 0 {
			dir = "down toward"
		}
		factors = append(factors, Factor{
			Kind: "context",
			Description: fmt.Sprintf("Limited-history adjustment moved the raw %.2f/5 %s the %.1f population average, landing at %.2f/5.",
				raw, dir, m.PriorMean, adjusted),
		})
	}

	return factors
}

func confidenceFor(effective float64) Confidence {
	switch {
	case effective <= 0:
		return ConfidenceNone
	case effective < 1.5:
		return ConfidenceLow
	case effective < 4.0:
		return ConfidenceMedium
	default:
		return ConfidenceHigh
	}
}

func gradeFor(score float64, m Model) (string, string) {
	for _, b := range m.GradeBands {
		if score >= b.Min {
			return b.Grade, b.Label
		}
	}
	return "F", "High risk"
}

// recommend maps score, confidence, and recent incident severity to guidance.
//
// A high score alone is not enough: a guest with an A average and a severe
// incident in the last six months does not get a clean "accept", which is the
// behavior acceptance scenario 1.3 requires.
func recommend(score float64, conf Confidence, reviews []domain.Review, now Time, m Model) Recommendation {
	recentSevere := false
	recentAny := false
	for _, r := range reviews {
		age := now.DaysSince(r.SubmittedAt)
		for _, inc := range r.Incidents {
			if age <= 365 {
				recentAny = true
				if inc.Severity == SevereSeverity || inc.Type == domain.IncPropertyDamage {
					if age <= 240 {
						recentSevere = true
					}
				}
			}
		}
	}

	switch {
	case score < 40:
		return RecDecline
	case recentSevere:
		return RecReview
	case score < 55:
		return RecReview
	case score < 70:
		return RecAcceptWithCare
	case recentAny:
		return RecAcceptWithCare
	case conf == ConfidenceLow:
		return RecAcceptWithCare
	default:
		return RecAccept
	}
}

// SevereSeverity aliases the domain constant so recommend reads cleanly.
const SevereSeverity = domain.SevSevere

func headline(rec Recommendation, conf Confidence, score float64) string {
	switch rec {
	case RecAccept:
		return fmt.Sprintf("Strong history at %.0f/100. No conditions suggested.", score)
	case RecAcceptWithCare:
		if conf == ConfidenceLow {
			return "Acceptable, but the history is thin. Confirm party size and purpose in writing before accepting."
		}
		return "Acceptable with conditions. Restate house rules and confirm occupancy in the booking thread."
	case RecReview:
		return "Do not auto-accept. There is something on this record worth reading before you decide."
	case RecDecline:
		return fmt.Sprintf("Serious pattern on record at %.0f/100. Declining is the defensible call.", score)
	default:
		return "Not enough history to score."
	}
}

func emptyDimensions(m Model) []DimensionScore {
	out := make([]DimensionScore, 0, len(domain.AllDimensions))
	for _, d := range domain.AllDimensions {
		out = append(out, DimensionScore{
			Dimension: d, Label: d.Label(), Average: 0, Weight: m.Weights[d], Contributes: 0,
		})
	}
	return out
}

// decay returns exp(-ln2 * age / halfLife): 1.0 at age zero, 0.5 at one
// half-life, continuous everywhere. Negative ages (a submission timestamp in
// the future, e.g. clock skew) are treated as fresh rather than amplified.
func decay(ageDays, halfLifeDays float64) float64 {
	if ageDays <= 0 {
		return 1.0
	}
	if halfLifeDays <= 0 {
		return 1.0
	}
	return math.Exp(-math.Ln2 * ageDays / halfLifeDays)
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }
