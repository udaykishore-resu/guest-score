package scoring

import (
	"math"
	"testing"
	"time"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
)

// The scale used to clip with a hard clamp, and it produced a defect worth
// keeping a test for: every exceptional guest landed on exactly 1000.0. A
// bureau score whose whole job is to rank people must not collapse its best
// records into a tie, and a guest already at the ceiling gains nothing from
// continued good behaviour.

func TestSaturateIsStrictlyIncreasingAcrossTheWholeRange(t *testing.T) {
	t.Parallel()
	m := DefaultModel

	prev := math.Inf(-1)
	// Sweep well outside the published range in both directions — the raw total
	// really does exceed it (a 5/5 guest with commendations and tenure totals
	// about 1146 before saturation).
	for raw := -500.0; raw <= 2000.0; raw += 0.5 {
		got := saturate(raw, m)
		if got <= prev {
			t.Fatalf("saturate is not strictly increasing at raw=%.1f: %.6f followed %.6f", raw, got, prev)
		}
		if got < m.ScoreMin || got > m.ScoreMax {
			t.Fatalf("saturate(%.1f) = %.6f, outside the published range %.0f..%.0f",
				raw, got, m.ScoreMin, m.ScoreMax)
		}
		prev = got
	}
}

func TestSaturateIsTheIdentityInTheOrdinaryRange(t *testing.T) {
	t.Parallel()
	m := DefaultModel

	// Every tier threshold must fall in the untouched region, or a tier stops
	// meaning what the published model says it means.
	for _, tier := range m.Tiers {
		if tier.Min <= m.ScoreMin {
			continue
		}
		if got := saturate(tier.Min, m); got != tier.Min {
			t.Errorf("tier %q floor %.0f was rescaled to %.4f; thresholds must be exact",
				tier.Name, tier.Min, got)
		}
	}
	for _, raw := range []float64{100, 250, 500, 600, 750, 899.9, 900} {
		if got := saturate(raw, m); got != raw {
			t.Errorf("saturate(%.1f) = %.4f, want the identity below the soft ceiling", raw, got)
		}
	}
}

func TestSaturateApproachesButNeverReachesTheBounds(t *testing.T) {
	t.Parallel()
	m := DefaultModel

	// The realistic envelope by a wide margin: the largest total the model can
	// actually produce is around 1150 (5/5 sustained, commendations at every
	// stay, tenure capped), and the largest deficit around -400.
	for _, raw := range []float64{1000, 1146, 1500, 2000, 3000, 4000} {
		got := saturate(raw, m)
		if got >= m.ScoreMax {
			t.Errorf("saturate(%.0f) = %.6f, which reached the ceiling; it must only approach it", raw, got)
		}
	}
	for _, raw := range []float64{0, -100, -400, -1000, -4000} {
		got := saturate(raw, m)
		if got <= m.ScoreMin {
			t.Errorf("saturate(%.0f) = %.6f, which reached the floor", raw, got)
		}
	}

	// Far outside that envelope the exponential underflows and the result
	// rounds to the bound exactly. That is a float64 limit, not a modelling
	// decision, and it is harmless — a raw total of 100,000 is not reachable.
	// What must hold everywhere is that the published range is never exceeded.
	for _, raw := range []float64{1e6, -1e6, math.Inf(1), math.Inf(-1)} {
		got := saturate(raw, m)
		if got < m.ScoreMin || got > m.ScoreMax {
			t.Errorf("saturate(%v) = %v, outside the published range", raw, got)
		}
	}

	// Still visibly near the top, though — a compressed scale that never gets
	// close would just be a shorter scale.
	if got := saturate(1146, m); got < 985 || got > 995 {
		t.Errorf("saturate(1146) = %.1f, want roughly 991 — near the ceiling but not on it", got)
	}
}

// TestTwoExceptionalGuestsDoNotTie is the end-to-end version: the defect was
// visible through Compute, so the guard is too.
func TestTwoExceptionalGuestsDoNotTie(t *testing.T) {
	t.Parallel()
	now := At(time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC))

	perfect := func(n int, commendations int) []domain.Review {
		out := make([]domain.Review, 0, n)
		for i := 0; i < n; i++ {
			r := domain.Review{
				ID: "r", GuestID: "g", HostID: "h", StayID: "s",
				Ratings: domain.Ratings{HouseRules: 5, PropertyCare: 5, Communication: 5, Noise: 5, Accuracy: 5},
				// Spread over two years so tenure is at its cap for both.
				SubmittedAt: now.Std().AddDate(0, 0, -(30 + i*40)),
			}
			if i < commendations {
				r.Commendations = []domain.Commendation{{Type: domain.ComExceptionalCare}}
			}
			out = append(out, r)
		}
		return out
	}

	good := Compute(perfect(12, 2), now, DefaultModel)
	better := Compute(perfect(12, 8), now, DefaultModel)

	if good.Composite >= better.Composite {
		t.Fatalf("six extra commendations did not raise the score: %.1f then %.1f",
			good.Composite, better.Composite)
	}
	if good.Composite >= DefaultModel.ScoreMax || better.Composite >= DefaultModel.ScoreMax {
		t.Fatalf("a guest reached the published maximum (%.1f, %.1f); the top of the scale "+
			"must stay unreachable so it can keep ranking", good.Composite, better.Composite)
	}
	t.Logf("distinguishable at the top: %.1f vs %.1f", good.Composite, better.Composite)
}

// TestIncidentsStillAlwaysMoveTheScoreDown re-checks the property the ordering
// of lift-then-deduct exists to protect, now that saturation sits in between.
func TestIncidentsStillAlwaysMoveTheScoreDown(t *testing.T) {
	t.Parallel()
	now := At(time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC))

	base := make([]domain.Review, 0, 12)
	for i := 0; i < 12; i++ {
		base = append(base, domain.Review{
			ID: "r", GuestID: "g", HostID: "h", StayID: "s",
			Ratings:       domain.Ratings{HouseRules: 5, PropertyCare: 5, Communication: 5, Noise: 5, Accuracy: 5},
			Commendations: []domain.Commendation{{Type: domain.ComExceptionalCare}},
			SubmittedAt:   now.Std().AddDate(0, 0, -(30 + i*40)),
		})
	}
	clean := Compute(base, now, DefaultModel)

	damaged := append([]domain.Review{}, base...)
	damaged[0].Incidents = []domain.Incident{{Type: domain.IncPropertyDamage, Severity: domain.SevSevere}}
	after := Compute(damaged, now, DefaultModel)

	if after.Composite >= clean.Composite {
		t.Fatalf("severe property damage did not lower the score: %.1f then %.1f. "+
			"A pile of commendations must never buy immunity.", clean.Composite, after.Composite)
	}
}
