package search

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
)

// The query DSL is the part of a search integration most likely to be silently
// wrong, because a malformed clause usually returns zero hits rather than an
// error — indistinguishable from "no such guest". These tests assert the JSON
// body shape without needing a cluster.

func buildFor(t *testing.T, q Query) map[string]any {
	t.Helper()
	e := &Elastic{index: "guest-score-guests"}
	b, err := json.Marshal(e.buildQuery(q))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestQueryWithNoTextRanksByStanding(t *testing.T) {
	body := buildFor(t, Query{})

	if _, ok := body["query"].(map[string]any)["match_all"]; !ok {
		t.Fatalf("an empty query should be match_all, got %v", body["query"])
	}
	sort, ok := body["sort"].([]any)
	if !ok || len(sort) == 0 {
		t.Fatal("with no text there is no relevance to rank by, so a sort is required")
	}
	if body["track_total_hits"] != true {
		t.Error("track_total_hits must be set or the directory count silently caps at 10,000")
	}
}

func TestTextQueryIsFuzzyOnNamesAndExactOnGlobalID(t *testing.T) {
	body := buildFor(t, Query{Text: "gs-abc123"})
	raw, _ := json.Marshal(body)
	s := string(raw)

	if !strings.Contains(s, `"fuzziness":"AUTO"`) {
		t.Error("name matching must be fuzzy; a desk that cannot find a misspelled name opens a duplicate file")
	}
	if !strings.Contains(s, `"name.exact"`) {
		t.Error("a correctly typed name must be boosted over a fuzzy hit on someone else")
	}
	// A global ID is an identifier: fuzzy matching it would resolve to the
	// wrong person's file, which is worse than finding nothing.
	if !strings.Contains(s, `"global_id"`) || !strings.Contains(s, `"GS-ABC123"`) {
		t.Errorf("global_id must be matched exactly and upper-cased, body was %s", s)
	}
	if strings.Contains(s, `"global_id":{"fuzziness"`) {
		t.Error("global_id must never be fuzzy-matched")
	}
}

func TestFiltersAreFiltersNotQueries(t *testing.T) {
	flagged := true
	minScore := 700.0
	body := buildFor(t, Query{
		Text: "mehta", Tier: "Excellent", Country: "IN",
		Flagged: &flagged, MinScore: &minScore,
	})

	bool_, ok := body["query"].(map[string]any)["bool"].(map[string]any)
	if !ok {
		t.Fatalf("expected a bool query, got %v", body["query"])
	}
	filters, ok := bool_["filter"].([]any)
	if !ok {
		t.Fatal("tier, country, flagged and score bounds must be filters, not scoring clauses")
	}
	if len(filters) != 4 {
		t.Fatalf("expected 4 filter clauses, got %d: %v", len(filters), filters)
	}
}

func TestLimitIsClamped(t *testing.T) {
	for _, tc := range []struct{ in, want int }{{0, 50}, {-3, 50}, {10, 10}, {5000, 50}} {
		body := buildFor(t, Query{Limit: tc.in})
		if got := int(body["size"].(float64)); got != tc.want {
			t.Errorf("limit %d became size %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestDocForNeverCarriesDocumentNumbers(t *testing.T) {
	g := domain.Guest{
		ID: "g1", GlobalID: "GS-1", Name: "Rohan Mehta", Email: "r@example.com",
		Nationality: "IN",
		Documents: []domain.IdentityDocument{
			{Country: "IN", Type: domain.DocAadhaar, Hash: "SECRETHASHVALUE", Last4: "0124"},
			{Country: "IN", Type: domain.DocPassport, Hash: "ANOTHERSECRET", Last4: "4567"},
		},
	}
	doc := DocFor(g, "Good", 712.0, false, 1, 4, time.Now().UTC().Format(time.RFC3339))

	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)

	// The index is a full-text store and the wrong custodian for an identifier
	// whose storage is restricted by statute. Not the number, and not the hash
	// derived from it.
	for _, forbidden := range []string{"SECRETHASHVALUE", "ANOTHERSECRET", "0124", "4567"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("indexed document leaks %q: %s", forbidden, body)
		}
	}
	if len(doc.DocumentCountries) != 1 || doc.DocumentCountries[0] != "IN" {
		t.Errorf("country facet should be deduplicated, got %v", doc.DocumentCountries)
	}
	if len(doc.DocumentTypes) != 2 {
		t.Errorf("expected both document types as facets, got %v", doc.DocumentTypes)
	}
}

func TestMatchesInProcessTreatsQueryAsLiteral(t *testing.T) {
	g := domain.Guest{Name: "Rohan Mehta", Email: "rohan@example.com", GlobalID: "GS-ABC"}

	cases := []struct {
		q    string
		want bool
	}{
		{"", true},
		{"rohan", true},
		{"MEHTA", true},
		{"gs-abc", true},
		{"example.com", true},
		{"nobody", false},
		// A regex metacharacter must be inert, not compiled: a guest searching
		// for "(" should get no results, not a 500.
		{"(", false},
		{".*", false},
		{"Roh.n", false},
	}
	for _, tc := range cases {
		if got := MatchesInProcess(g, tc.q); got != tc.want {
			t.Errorf("MatchesInProcess(%q) = %v, want %v", tc.q, got, tc.want)
		}
	}
}

func TestNopSignalsTheFallbackEngine(t *testing.T) {
	res, err := Nop{}.Search(context.Background(), Query{Text: "anything"})
	if err != nil {
		t.Fatalf("an unconfigured index is a deployment choice, not an error: %v", err)
	}
	if res.Engine != EngineInProcess {
		t.Errorf("engine %q, want %q — the caller must be able to tell fuzzy matching was not attempted",
			res.Engine, EngineInProcess)
	}
}
