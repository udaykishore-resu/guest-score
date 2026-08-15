package api

import (
	"context"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
)

// Verifier confirms a document with its issuing authority — the DFD's
// "External Authorized Entity" step.
//
// It is an interface for the same reason MarketMate's providers are: the real
// implementations (UIDAI, a state DMV, a passport office) need credentials,
// contracts, and in most cases a legal basis to call at all. Behind an
// interface the whole check-in flow is runnable and testable today, and a real
// verifier drops in per country without touching the handler.
type Verifier interface {
	Verify(ctx context.Context, c domain.Country, t domain.DocumentType, normalisedNumber string) VerificationResult
}

// VerificationResult is what the authority said.
type VerificationResult struct {
	OK bool
	// Status distinguishes a real confirmation from a simulated one. A
	// simulated result must never be presentable as a real check — the same
	// rule MarketMate's provenance block enforces.
	Status  string // "verified" | "simulated" | "unavailable"
	Message string
}

// FixtureVerifier accepts any document that already passed local format and
// checksum validation.
//
// It is honest about what it is: every result is labelled "simulated", so the
// desk sees that no authority was actually contacted. That matters more here
// than in most fixtures — an agent who believes a passport was confirmed
// against the issuing office, when it was not, has been actively misled.
type FixtureVerifier struct{}

func (FixtureVerifier) Verify(_ context.Context, c domain.Country, t domain.DocumentType, _ string) VerificationResult {
	rules, ok := domain.RulesFor(c)
	if !ok {
		return VerificationResult{Status: "unavailable", Message: "No verifier is configured for this country."}
	}
	authority := "the issuing authority"
	for _, a := range rules.Documents {
		if a.Type == t {
			authority = a.Authority
		}
	}
	return VerificationResult{
		OK:     true,
		Status: "simulated",
		Message: "Format and check digit are valid. No request was made to " + authority +
			" — this deployment has no verifier credentials configured.",
	}
}
