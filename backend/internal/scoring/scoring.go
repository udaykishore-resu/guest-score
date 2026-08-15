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
	Weights              map[domain.Dimension]float64 `json:"weights"`
	ReviewHalfLife       float64                      `json:"review_half_life_days"`
	IncidentHalfLife     float64                      `json:"incident_half_life_days"`
	CommendationHalfLife float64                      `json:"commendation_half_life_days"`
	PriorMean            float64                      `json:"prior_mean"`
	PriorStrength        float64                      `json:"prior_strength"`

	// ScoreMin/ScoreMax and NewGuestScore come from the invention disclosure's
	// "Score Ranges and Interpretation" section: a 0–1000 scale on which a new
	// guest opens at 500.
	ScoreMin      float64 `json:"score_min"`
	ScoreMax      float64 `json:"score_max"`
	NewGuestScore float64 `json:"new_guest_score"`

	// The 1–5 quality is mapped onto the published range by anchoring the
	// population mean and scaling asymmetrically either side of it.
	//
	// A naive linear map of [1,5] onto [0,1000] was tried first and produced a
	// useless distribution: it placed an average guest (3.9) at 725, so eight
	// of thirteen seeded guests landed in "Excellent" and several pinned the
	// ceiling. A band the disclosure describes as "low risk" has to be earned,
	// not the default.
	//
	// Anchoring the mean at 600 and scaling up/down separately means Excellent
	// needs roughly 4.5/5 sustained, Good roughly 4.2, and a guest only falls
	// below 500 — where the disclosure permits refusing a booking — under about
	// 3.3, rather than merely being below average.
	AnchorQuality float64 `json:"anchor_quality"`
	AnchorScore   float64 `json:"anchor_score"`
	PointsPerUp   float64 `json:"points_per_quality_point_up"`
	PointsPerDown float64 `json:"points_per_quality_point_down"`

	// Tenure implements the disclosure's "+100 for positive stays over a year".
	// It is also the correction to a real modelling error: recency decay alone
	// punishes a long, quiet relationship by treating it as a thin file, which
	// is the opposite of how consumer credit treats age of file.
	TenurePointsPerYear float64 `json:"tenure_points_per_year"`
	TenureMaxPoints     float64 `json:"tenure_max_points"`

	Tiers []Tier `json:"tiers"`
}

// Tier maps a score floor to a loyalty category and the discount it earns.
//
// The thresholds and discounts are carried over verbatim from the original
// app's DiscountDisplay and GuestCategory components, so a guest's standing
// does not silently change meaning between versions.
type Tier struct {
	Min      float64 `json:"min"`
	Name     string  `json:"name"`
	Discount int     `json:"discount_percent"`

	// DepositMultiplier scales the property's standard security deposit. This
	// is the disclosure's primary commercial lever, alongside the discount:
	// the DFD's terminal path is retrieve score → adjust deposit and discount →
	// guest checked in.
	DepositMultiplier float64 `json:"deposit_multiplier"`

	// Flagged marks the band at which the disclosure allows a hotel to deny a
	// booking or ban the guest. It is a permission, never an automatic action.
	Flagged bool `json:"flagged"`

	Description string `json:"description"`
}

// DefaultModel is the production scoring configuration.
//
// The weights encode a judgment about hotel risk: policy compliance and room
// condition carry the most weight because they are where the cost and the
// complaints are, and booking accuracy carries the least because it is usually
// a correctable annoyance rather than a loss. They sum to 1.0.
var DefaultModel = Model{
	Weights: map[domain.Dimension]float64{
		domain.DimHouseRules:    0.28,
		domain.DimPropertyCare:  0.26,
		domain.DimCommunication: 0.18,
		domain.DimNoise:         0.16,
		domain.DimAccuracy:      0.12,
	},
	ReviewHalfLife:       365.0,
	IncidentHalfLife:     180.0,
	CommendationHalfLife: 270.0,
	PriorMean:            3.9,
	PriorStrength:        3.0,

	ScoreMin:      0,
	ScoreMax:      1000,
	NewGuestScore: 500,

	AnchorQuality: 3.9,
	AnchorScore:   600,
	PointsPerUp:   318,
	PointsPerDown: 172,

	TenurePointsPerYear: 100.0,
	TenureMaxPoints:     100.0,

	// Bands are taken verbatim from the invention disclosure. Deposit
	// multipliers apply to the property's standard deposit: an Excellent guest
	// posts a quarter of it, a Poor guest four times — the disclosure's "high
	// scores result in lower deposits, low scores may necessitate higher
	// deposits or booking denial".
	Tiers: []Tier{
		{800, "Excellent", 20, 0.25, false, "Low risk. Highest discount and the smallest deposit."},
		{700, "Good", 10, 0.5, false, "Low to moderate risk. Reduced deposit."},
		{500, "Fair", 0, 1.0, false, "Moderate risk. Standard deposit, no discount."},
		{0, "Poor", 0, 2.0, true, "High risk. Elevated deposit; booking may be denied."},
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

// Handling is the front-desk guidance derived from the score. It sits
// alongside the tier: the tier is what the guest earns, handling is what staff
// should do.
type Handling string

const (
	HandlingVIP         Handling = "vip_treatment"
	HandlingStandard    Handling = "standard"
	HandlingWatch       Handling = "watch"
	HandlingEscalate    Handling = "escalate"
	HandlingInsufficient Handling = "insufficient_data"
)

// DimensionScore is one dimension's contribution to the composite.
type DimensionScore struct {
	Dimension   domain.Dimension `json:"dimension"`
	Label       string           `json:"label"`
	Average     float64          `json:"average"`      // decay-weighted mean, 1-5
	Weight      float64          `json:"weight"`       // from the model
	Contributes float64          `json:"contributes"`  // points of the range attributable here
}

// Factor is one human-readable reason the score is what it is.
type Factor struct {
	Kind        string  `json:"kind"` // "strength" | "concern" | "penalty" | "bonus" | "context"
	Description string  `json:"description"`
	Impact      float64 `json:"impact"` // signed points, 0 for pure context
}

// Score is the fully explained result. Nothing here is persisted; it is
// recomputed from reviews on every read.
type Score struct {
	Rated bool `json:"rated"`

	Composite float64 `json:"composite"` // ScoreMin..ScoreMax, one decimal

	// Tier is the loyalty standing and the discount it earns — the primary
	// thing both the guest and the front desk care about.
	Tier              string  `json:"tier"`
	DiscountPercent   int     `json:"discount_percent"`
	DepositMultiplier float64 `json:"deposit_multiplier"`
	TierNote          string  `json:"tier_note"`

	// Flagged marks a guest in the band where the disclosure permits a hotel to
	// deny a booking or ban. The system never bans on its own — it surfaces the
	// flag and leaves the decision, and the appeal, to people.
	Flagged bool `json:"flagged"`

	// PointsToNextTier is how many points separate this guest from the next
	// tier up, or 0 at the top. A loyalty score is only motivating if the gap
	// is visible.
	PointsToNextTier float64 `json:"points_to_next_tier"`
	NextTier         string  `json:"next_tier,omitempty"`

	Confidence Confidence `json:"confidence"`
	Handling   Handling   `json:"handling"`
	Headline   string     `json:"headline"`

	StayCount          int     `json:"stay_count"`
	DisputedCount      int     `json:"disputed_count"`
	EffectiveStayCount float64 `json:"effective_stay_count"` // sum of decay weights
	IncidentCount      int     `json:"incident_count"`
	CommendationCount  int     `json:"commendation_count"`

	RawAverage         float64 `json:"raw_average"`         // 1-5 before shrinkage
	AdjustedAverage    float64 `json:"adjusted_average"`    // 1-5 after shrinkage
	BaseScore          float64 `json:"base_score"`          // from ratings alone, before tenure/events
	IncidentPenalty    float64 `json:"incident_penalty"`    // points deducted
	CommendationBonus  float64 `json:"commendation_bonus"`  // points added
	TenureBonus        float64 `json:"tenure_bonus"`        // points added for length of history
	TenureYears        float64 `json:"tenure_years"`

	Dimensions []DimensionScore `json:"dimensions"`
	Factors    []Factor         `json:"factors"`
}

// Compute produces an explained score from a guest's reviews as of time `now`.
//
// The stages match plan.md: per-stay weighted quality, time-decayed
// aggregation, Bayesian shrinkage toward the population prior, a tenure bonus
// for length of history, then commendations and incident penalties applied on
// the published 300–850 scale.
func Compute(all []domain.Review, now Time, m Model) Score {
	// Disputed records are held out of the maths entirely. Scoring a record the
	// guest is actively contesting — and which the review may overturn — is the
	// thing a dispute process exists to prevent.
	reviews := make([]domain.Review, 0, len(all))
	disputed := 0
	for _, r := range all {
		if r.Scoreable() {
			reviews = append(reviews, r)
			continue
		}
		disputed++
	}

	if len(reviews) == 0 {
		// The disclosure is explicit: "New guests receive an initial score of
		// 500, categorized as 'New'." That is a deliberate departure from
		// treating a thin file as unscored — an opening score lets a first-time
		// guest be processed on standard terms rather than as an unknown, and
		// it is the same convention a credit bureau uses for a thin file.
		//
		// Rated stays false so downstream consumers can still tell an opening
		// balance from an earned standing.
		opening := tierFor(m.NewGuestScore, m)
		next := nextTierAbove(m.NewGuestScore, m)
		return Score{
			Rated:             false,
			Composite:         m.NewGuestScore,
			Tier:              "New",
			DiscountPercent:   opening.Discount,
			DepositMultiplier: opening.DepositMultiplier,
			Flagged:           false,
			TierNote:          "No stay history yet. Opens at the standard starting score.",
			NextTier:          next,
			PointsToNextTier:  round1(tierFloor(next, m) - m.NewGuestScore),
			Confidence:        ConfidenceNone,
			Handling:          HandlingInsufficient,
			Headline: fmt.Sprintf("New guest, opening at %.0f. Standard deposit and standard check-in.",
				m.NewGuestScore),
			Dimensions:    emptyDimensions(m),
			DisputedCount: disputed,
			Factors:       unratedFactors(disputed, m),
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

	// Map the 1–5 quality onto the published range, anchored at the population
	// mean and scaled asymmetrically (see the Model fields for why).
	span := m.ScoreMax - m.ScoreMin
	base := clamp(qualityToScore(adjusted, m), m.ScoreMin, m.ScoreMax)

	// Tenure: years between the earliest scoreable stay and now, capped.
	tenureYears := 0.0
	for _, r := range reviews {
		if age := now.DaysSince(r.SubmittedAt) / 365.0; age > tenureYears {
			tenureYears = age
		}
	}
	tenureBonus := math.Min(m.TenureMaxPoints, tenureYears*m.TenurePointsPerYear)

	// --- Stage 4: incident penalties and commendation bonuses ----------------
	// Both apply on the 0-100 scale after the rescale, both fade with age.
	// Commendations are the upward channel the original app expressed as
	// "+10 room left in excellent condition"; without them a loyalty tier is
	// reachable only by a guest with a flawless and lengthy record.
	var penalty, bonus float64
	incidentCount, commendationCount := 0, 0
	penaltyByType := map[domain.IncidentType]float64{}
	countByType := map[domain.IncidentType]int{}
	bonusByType := map[domain.CommendationType]float64{}
	comCountByType := map[domain.CommendationType]int{}

	for _, r := range reviews {
		age := now.DaysSince(r.SubmittedAt)
		for _, inc := range r.Incidents {
			incidentCount++
			p := inc.Type.BasePenalty() *
				inc.Severity.Multiplier() *
				decay(age, m.IncidentHalfLife)
			penalty += p
			penaltyByType[inc.Type] += p
			countByType[inc.Type]++
		}
		for _, com := range r.Commendations {
			commendationCount++
			b := com.Type.BaseBonus() * decay(age, m.CommendationHalfLife)
			bonus += b
			bonusByType[com.Type] += b
			comCountByType[com.Type]++
		}
	}

	// Order matters, and the obvious `clamp(base - penalty + bonus)` is wrong.
	// Applying both at once lets a pile of commendations push the raw total far
	// above 100, where the clamp silently absorbs any penalty — a guest with ten
	// commendations and a severe damage incident scored an unchanged 100.0.
	//
	// Lifting first and clamping, then deducting, guarantees an incident always
	// moves the score down. Commendations can carry a guest to the ceiling; they
	// cannot buy immunity.
	lifted := clamp(base+bonus+tenureBonus, m.ScoreMin, m.ScoreMax)
	composite := clamp(lifted-penalty, m.ScoreMin, m.ScoreMax)

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
			Contributes: round1(clamp(qualityToScore(avg, m)-m.ScoreMin, 0, span) * w),
		})
	}

	conf := confidenceFor(totalWeight)

	// Resolve the tier from the *rounded* composite, which is the number the
	// guest is shown. Using the raw value instead produced a guest displaying
	// 90.0 while sitting in Premium — the underlying 89.96 was below the VIP
	// floor, and "you have 0.0 points to go" is indefensible to someone
	// looking at a score that already reads as 90.
	composite = round1(composite)

	tier := tierFor(composite, m)
	handling := handlingFor(composite, conf, reviews, now, m)

	next := nextTierAbove(composite, m)
	gap := 0.0
	if next != "" {
		gap = round1(tierFloor(next, m) - composite)
	}

	return Score{
		Rated:              true,
		Composite:          composite,
		Tier:               tier.Name,
		DiscountPercent:    tier.Discount,
		DepositMultiplier:  tier.DepositMultiplier,
		Flagged:            tier.Flagged,
		TierNote:           tier.Description,
		NextTier:           next,
		PointsToNextTier:   gap,
		Confidence:         conf,
		Handling:           handling,
		Headline:           headline(handling, conf, composite, tier),
		StayCount:          len(reviews),
		DisputedCount:      disputed,
		EffectiveStayCount: round2(totalWeight),
		IncidentCount:      incidentCount,
		CommendationCount:  commendationCount,
		RawAverage:         round2(rawAverage),
		AdjustedAverage:    round2(adjusted),
		BaseScore:          round1(base),
		IncidentPenalty:    round1(penalty),
		CommendationBonus:  round1(bonus),
		TenureBonus:        round1(tenureBonus),
		TenureYears:        round1(tenureYears),
		Dimensions:         dims,
		Factors: buildFactors(dims, penaltyByType, countByType, bonusByType, comCountByType,
			totalWeight, len(reviews), rawAverage, adjusted, tenureBonus, tenureYears, disputed, m),
	}
}

// buildFactors turns the numbers into the plain-language list required by FR-007.
func buildFactors(
	dims []DimensionScore,
	penaltyByType map[domain.IncidentType]float64,
	countByType map[domain.IncidentType]int,
	bonusByType map[domain.CommendationType]float64,
	comCountByType map[domain.CommendationType]int,
	effective float64,
	reviewCount int,
	raw, adjusted float64,
	tenureBonus, tenureYears float64,
	disputed int,
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

	// Commendations, largest first.
	comTypes := make([]domain.CommendationType, 0, len(bonusByType))
	for t := range bonusByType {
		comTypes = append(comTypes, t)
	}
	sort.SliceStable(comTypes, func(i, j int) bool { return bonusByType[comTypes[i]] > bonusByType[comTypes[j]] })
	for _, t := range comTypes {
		n := comCountByType[t]
		plural := ""
		if n > 1 {
			plural = "s"
		}
		factors = append(factors, Factor{
			Kind: "bonus",
			Description: fmt.Sprintf("%d %s commendation%s on record, adding %.1f points after age adjustment.",
				n, t.Label(), plural, bonusByType[t]),
			Impact: round1(bonusByType[t]),
		})
	}

	if tenureBonus > 0 {
		factors = append(factors, Factor{
			Kind: "bonus",
			Description: fmt.Sprintf("%.1f years of stay history with the bureau, adding %.0f points for tenure.",
				tenureYears, tenureBonus),
			Impact: round1(tenureBonus),
		})
	}

	if disputed > 0 {
		plural := ""
		if disputed > 1 {
			plural = "s"
		}
		factors = append(factors, Factor{
			Kind: "context",
			Description: fmt.Sprintf("%d stay record%s excluded while under dispute. The score is computed without %s.",
				disputed, plural, map[bool]string{true: "them", false: "it"}[disputed > 1]),
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

// qualityToScore maps a 1–5 quality onto the published scale, anchoring the
// population mean and scaling separately above and below it.
func qualityToScore(quality float64, m Model) float64 {
	d := quality - m.AnchorQuality
	if d >= 0 {
		return m.AnchorScore + d*m.PointsPerUp
	}
	return m.AnchorScore + d*m.PointsPerDown
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

// tierFor resolves a score to its loyalty tier. Tiers are ordered highest
// floor first, so the first match wins.
func tierFor(score float64, m Model) Tier {
	for _, t := range m.Tiers {
		if score >= t.Min {
			return t
		}
	}
	return m.Tiers[len(m.Tiers)-1]
}

// nextTierAbove returns the name of the tier a guest is currently working
// toward, or "" when they are already at the top.
func nextTierAbove(score float64, m Model) string {
	best := ""
	bestMin := math.MaxFloat64
	for _, t := range m.Tiers {
		if t.Min > score && t.Min < bestMin {
			best, bestMin = t.Name, t.Min
		}
	}
	return best
}

// tierFloor returns the score threshold for a named tier.
func tierFloor(name string, m Model) float64 {
	for _, t := range m.Tiers {
		if t.Name == name {
			return t.Min
		}
	}
	return 0
}

// handlingFor maps score, confidence, and recent incident severity to
// front-desk guidance.
//
// Tier and handling are deliberately separate. A guest can hold Premium on
// accumulated history and still warrant a flag at check-in because of one
// recent severe incident — collapsing both into a single number would hide
// exactly the thing the front desk needs to see.
func handlingFor(score float64, conf Confidence, reviews []domain.Review, now Time, m Model) Handling {
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

	watchFloor := tierFloor("Fair", m)      // below this the guest is Poor / flagged
	vipFloor := tierFloor("Excellent", m)
	premiumFloor := tierFloor("Good", m)

	switch {
	case score < watchFloor:
		return HandlingEscalate
	case recentSevere:
		return HandlingWatch
	case score < premiumFloor:
		return HandlingStandard
	case recentAny:
		return HandlingStandard
	case conf == ConfidenceLow:
		return HandlingStandard
	case score >= vipFloor:
		return HandlingVIP
	default:
		return HandlingStandard
	}
}

// unratedFactors explains why there is no score.
func unratedFactors(disputed int, m Model) []Factor {
	if disputed > 0 {
		return []Factor{{
			Kind: "context",
			Description: fmt.Sprintf(
				"Every stay on file (%d) is under dispute and excluded from scoring, so the guest is held at the opening score of %.0f.",
				disputed, m.NewGuestScore),
		}}
	}
	return []Factor{{
		Kind: "context",
		Description: fmt.Sprintf(
			"No stay history yet. Every guest opens at %.0f — this is a starting point, not a negative signal.",
			m.NewGuestScore),
	}}
}

// SevereSeverity aliases the domain constant so recommend reads cleanly.
const SevereSeverity = domain.SevSevere

func headline(h Handling, conf Confidence, score float64, tier Tier) string {
	switch h {
	case HandlingVIP:
		return fmt.Sprintf("%s standing at %.0f. Apply the %d%% discount and a %.0f%% security deposit.",
			tier.Name, score, tier.Discount, tier.DepositMultiplier*100)
	case HandlingStandard:
		if conf == ConfidenceLow {
			return fmt.Sprintf("%s at %.0f, but on thin history. The %d%% discount applies; treat the standing as provisional.",
				tier.Name, score, tier.Discount)
		}
		if tier.Discount > 0 {
			return fmt.Sprintf("%s standing at %.0f. %d%% discount and a %.0f%% deposit.",
				tier.Name, score, tier.Discount, tier.DepositMultiplier*100)
		}
		return fmt.Sprintf("%s standing at %.0f. Standard deposit, no discount.", tier.Name, score)
	case HandlingWatch:
		return "Recent serious incident on record. Flag at check-in and note the room assignment, regardless of tier."
	case HandlingEscalate:
		return fmt.Sprintf("Poor standing at %.0f. Escalate to the duty manager; the booking may be declined.", score)
	default:
		return "Not enough history to assign a tier."
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
