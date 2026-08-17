// Package search provides guest directory lookup.
//
// The substring match in the API handler is correct and is kept as the
// fallback, but it cannot do the thing this directory actually needs. A bureau
// whose files are opened in Mumbai and pulled in Lisbon has names that were
// keyed by different desks, in different scripts, with different
// transliterations and ordinary typos. "Rohan Mehta" must find a file keyed as
// "Rohit Mehtaa", and "Muller" must find "Müller", or the desk agent concludes
// there is no file and opens a duplicate — which is the specific failure that
// destroys a bureau's value, because the guest's history splits in two.
//
// One thing is never indexed: document numbers, or the hashes derived from
// them. Elasticsearch is a search index, not a system of record, and a
// full-text store is the wrong custodian for an identifier whose storage is
// restricted by statute. Only what a desk agent could read off the screen goes
// in.
package search

import (
	"context"
	"strings"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
)

// Doc is the indexed projection of a guest. It is deliberately a subset of
// domain.Guest — see the package comment on what is excluded and why.
type Doc struct {
	ID          string `json:"id"`
	GlobalID    string `json:"global_id"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	City        string `json:"city,omitempty"`
	Nationality string `json:"nationality,omitempty"`
	Verified    bool   `json:"verified"`

	// Denormalised score facets. They are what the directory filters on, and
	// filtering in the index avoids scoring the whole population to discard
	// most of it.
	Tier          string  `json:"tier"`
	Composite     float64 `json:"composite"`
	Flagged       bool    `json:"flagged"`
	IncidentCount int     `json:"incident_count"`
	StayCount     int     `json:"stay_count"`

	// Countries the guest holds documents from — the type and the country, never
	// the number.
	DocumentCountries []string `json:"document_countries,omitempty"`
	DocumentTypes     []string `json:"document_types,omitempty"`

	JoinedAt string `json:"joined_at,omitempty"`
	IndexedAt string `json:"indexed_at"`
}

// Query is a directory lookup.
type Query struct {
	Text     string
	Tier     string
	Country  string
	Flagged  *bool
	MinScore *float64
	Limit    int
	Offset   int
}

// Result is one hit with the relevance score the engine assigned.
type Result struct {
	Doc       Doc     `json:"doc"`
	Relevance float64 `json:"relevance"`
}

// Results is a page of hits.
type Results struct {
	Hits  []Result `json:"hits"`
	Total int      `json:"total"`

	// Engine names what actually answered — "elasticsearch" or "in-process".
	// The desk agent needs to know when fuzzy matching is not available,
	// because "no file found" means something different in each case.
	Engine string `json:"engine"`
}

// Index is the contract.
type Index interface {
	// Ensure creates the index and mapping if absent. Safe to call on every boot.
	Ensure(ctx context.Context) error

	// Put indexes or replaces one guest.
	Put(ctx context.Context, d Doc) error

	// PutBatch indexes many in one round trip, for reindexing.
	PutBatch(ctx context.Context, docs []Doc) error

	Delete(ctx context.Context, id string) error
	Search(ctx context.Context, q Query) (Results, error)
	Ping(ctx context.Context) error
	Close() error
}

// DocFor projects a guest and their score facets into an indexable document.
func DocFor(g domain.Guest, tier string, composite float64, flagged bool, incidents, stays int, indexedAt string) Doc {
	countries := make([]string, 0, len(g.Documents))
	types := make([]string, 0, len(g.Documents))
	seenC, seenT := map[string]bool{}, map[string]bool{}
	for _, d := range g.Documents {
		if c := string(d.Country); c != "" && !seenC[c] {
			countries = append(countries, c)
			seenC[c] = true
		}
		if t := string(d.Type); t != "" && !seenT[t] {
			types = append(types, t)
			seenT[t] = true
		}
	}
	doc := Doc{
		ID:                g.ID,
		GlobalID:          string(g.GlobalID),
		Name:              g.Name,
		Email:             g.Email,
		City:              g.City,
		Nationality:       string(g.Nationality),
		Verified:          g.Verified,
		Tier:              tier,
		Composite:         composite,
		Flagged:           flagged,
		IncidentCount:     incidents,
		StayCount:         stays,
		DocumentCountries: countries,
		DocumentTypes:     types,
		IndexedAt:         indexedAt,
	}
	if !g.JoinedAt.IsZero() {
		doc.JoinedAt = g.JoinedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return doc
}

// Nop is the index used when GS_ELASTIC_URL is unset.
//
// Search returns Engine "in-process" and no hits, which is the signal to the
// caller to fall back to its own substring scan. It does not return an error,
// because "search is not configured" is a deployment choice, not a fault.
type Nop struct{}

func (Nop) Ensure(context.Context) error            { return nil }
func (Nop) Put(context.Context, Doc) error          { return nil }
func (Nop) PutBatch(context.Context, []Doc) error   { return nil }
func (Nop) Delete(context.Context, string) error    { return nil }
func (Nop) Ping(context.Context) error              { return nil }
func (Nop) Close() error                            { return nil }

func (Nop) Search(context.Context, Query) (Results, error) {
	return Results{Engine: EngineInProcess}, nil
}

// Engine names for the Results.Engine field.
const (
	EngineElastic   = "elasticsearch"
	EngineInProcess = "in-process"
)

// MatchesInProcess is the fallback matcher, extracted so the API handler and
// the reindex verification agree on what a match means.
//
// It is a plain lowercase substring test over name and email. It is never
// compiled as a pattern, so regex metacharacters in a query are inert — a guest
// searching for "(" gets no results rather than a 500.
func MatchesInProcess(g domain.Guest, text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return true
	}
	return strings.Contains(strings.ToLower(g.Name), text) ||
		strings.Contains(strings.ToLower(g.Email), text) ||
		strings.Contains(strings.ToLower(string(g.GlobalID)), text)
}
