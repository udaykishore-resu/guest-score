package graphqlapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/graphql-go/graphql"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
	"github.com/udaykishore-resu/guest-score/backend/internal/graphqlapi"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoring"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoringsvc"
	"github.com/udaykishore-resu/guest-score/backend/internal/search"
	"github.com/udaykishore-resu/guest-score/backend/internal/store"
)

func newSchema(t *testing.T) (graphql.Schema, *store.FileStore) {
	t.Helper()
	st, err := store.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	guests, reviews := store.Seed(time.Now().UTC())
	st.LoadSeed(guests, reviews)

	schema, err := graphqlapi.New(graphqlapi.Deps{
		Store:  st,
		Scorer: scoringsvc.NewLocal(scoring.DefaultModel),
		Search: search.Nop{},
		Now:    scoring.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return schema, st
}

func exec(t *testing.T, schema graphql.Schema, query string) map[string]any {
	t.Helper()
	res := graphql.Do(graphql.Params{
		Schema:        schema,
		RequestString: query,
		Context:       context.Background(),
	})
	if len(res.Errors) > 0 {
		t.Fatalf("query failed: %v\nquery was:\n%s", res.Errors, query)
	}
	b, err := json.Marshal(res.Data)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestOneQueryReplacesFourRequests is the case for having GraphQL at all: the
// guest profile screen needs identity, score, factor breakdown, documents,
// stays and the inquiry log, which is five REST calls.
func TestOneQueryReplacesFourRequests(t *testing.T) {
	schema, st := newSchema(t)
	guests, err := st.ListGuests()
	if err != nil || len(guests) == 0 {
		t.Fatalf("seed produced no guests: %v", err)
	}
	id := guests[0].ID

	out := exec(t, schema, `{
		guest(id: "`+id+`") {
			id globalId name nationality portable
			score { composite tier discountPercent depositMultiplier flagged modelVersion
			        factors { kind description impact }
			        dimensions { dimension label average weight } }
			documents { country label last4 portable }
			stays { id memberName nights dispute { status countsTowardScore } }
			inquiries { memberId purpose }
		}
	}`)

	g, ok := out["guest"].(map[string]any)
	if !ok {
		t.Fatalf("no guest in response: %v", out)
	}
	if g["id"] != id {
		t.Errorf("id %v, want %v", g["id"], id)
	}
	sc, ok := g["score"].(map[string]any)
	if !ok {
		t.Fatalf("no score in response: %v", g)
	}
	if sc["tier"] == "" || sc["tier"] == nil {
		t.Error("score carries no tier")
	}
	// A score without its model version is not reproducible, which is the whole
	// point of publishing it.
	if sc["modelVersion"] != scoringsvc.ModelVersion {
		t.Errorf("modelVersion %v, want %q", sc["modelVersion"], scoringsvc.ModelVersion)
	}
	if dims, _ := sc["dimensions"].([]any); len(dims) != 5 {
		t.Errorf("expected 5 dimensions, got %d", len(dims))
	}
}

// TestDirectoryScoresInOneBatch is the N+1 guard. If a future change scores
// guests inside the field resolver instead of the list resolver, the batch
// count goes up with the page size and this catches it.
func TestDirectoryScoresInOneBatch(t *testing.T) {
	st, err := store.NewFileStore("")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	guests, reviews := store.Seed(time.Now().UTC())
	st.LoadSeed(guests, reviews)

	counting := &countingScorer{inner: scoringsvc.NewLocal(scoring.DefaultModel)}
	schema, err := graphqlapi.New(graphqlapi.Deps{
		Store: st, Scorer: counting, Search: search.Nop{}, Now: scoring.Now,
	})
	if err != nil {
		t.Fatal(err)
	}

	out := exec(t, schema, `{ guests(limit: 50) { id name score { composite tier } } }`)
	list, _ := out["guests"].([]any)
	if len(list) < 2 {
		t.Fatalf("expected a populated directory, got %d guests", len(list))
	}

	if counting.batches != 1 {
		t.Errorf("directory made %d batch calls, want exactly 1", counting.batches)
	}
	if counting.singles != 0 {
		t.Errorf("directory made %d per-guest scoring calls; that is the N+1 this batching exists to avoid",
			counting.singles)
	}
}

func TestSearchReportsTheFallbackEngine(t *testing.T) {
	schema, _ := newSchema(t)

	out := exec(t, schema, `{ searchGuests(query: "a", limit: 5) { total engine hits { relevance guest { name } } } }`)
	res, ok := out["searchGuests"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", out)
	}
	// Without Elasticsearch the caller must be told that fuzzy matching was not
	// attempted, because "no file found" then means something much weaker.
	if res["engine"] != search.EngineInProcess {
		t.Errorf("engine %v, want %q", res["engine"], search.EngineInProcess)
	}
}

func TestScoringModelIsPublished(t *testing.T) {
	schema, _ := newSchema(t)
	out := exec(t, schema, `{ scoringModel { modelVersion scoreMin scoreMax newGuestScore
		tiers { name min discountPercent depositMultiplier flagged } } }`)

	m, ok := out["scoringModel"].(map[string]any)
	if !ok {
		t.Fatalf("no model: %v", out)
	}
	if m["scoreMax"] != scoring.DefaultModel.ScoreMax {
		t.Errorf("scoreMax %v, want %v", m["scoreMax"], scoring.DefaultModel.ScoreMax)
	}
	tiers, _ := m["tiers"].([]any)
	if len(tiers) != len(scoring.DefaultModel.Tiers) {
		t.Errorf("published %d tiers, model has %d", len(tiers), len(scoring.DefaultModel.Tiers))
	}
}

// TestUnknownGuestIsAnError checks the query does not silently return null for
// a guest that does not exist — a desk agent must be able to tell "no file"
// from "field omitted".
func TestUnknownGuestIsAnError(t *testing.T) {
	schema, _ := newSchema(t)
	res := graphql.Do(graphql.Params{
		Schema:        schema,
		RequestString: `{ guest(id: "g_nobody") { id name } }`,
		Context:       context.Background(),
	})
	if len(res.Errors) == 0 {
		t.Fatal("querying a missing guest succeeded")
	}
	if !strings.Contains(strings.ToLower(res.Errors[0].Message), "not found") {
		t.Errorf("error %q does not say the guest was not found", res.Errors[0].Message)
	}
}

// TestIdentityNumbersAreNotReachable walks the whole schema looking for a field
// that could expose a document hash. The type simply must not have one.
func TestIdentityNumbersAreNotReachable(t *testing.T) {
	schema, _ := newSchema(t)
	out := exec(t, schema, `{ __type(name: "IdentityDocument") { fields { name } } }`)

	typ, _ := out["__type"].(map[string]any)
	fields, _ := typ["fields"].([]any)
	if len(fields) == 0 {
		t.Fatal("IdentityDocument has no fields; introspection is not working")
	}
	for _, f := range fields {
		name, _ := f.(map[string]any)["name"].(string)
		switch strings.ToLower(name) {
		case "hash", "number", "documentnumber", "raw":
			t.Errorf("IdentityDocument exposes %q; the keyed hash must never leave the server", name)
		}
	}
}

// countingScorer records how the directory resolver used the scorer.
type countingScorer struct {
	inner            scoringsvc.Scorer
	singles, batches int
}

func (c *countingScorer) Score(ctx context.Context, id string, r []domain.Review, now scoring.Time) scoring.Score {
	c.singles++
	return c.inner.Score(ctx, id, r, now)
}

func (c *countingScorer) Batch(ctx context.Context, items []scoringsvc.BatchItem, now scoring.Time) []scoring.Score {
	c.batches++
	return c.inner.Batch(ctx, items, now)
}

func (c *countingScorer) Model() scoring.Model { return c.inner.Model() }
func (c *countingScorer) Close() error         { return nil }
