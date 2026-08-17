package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/udaykishore-resu/guest-score/backend/internal/cache"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoring"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoringsvc"
	"github.com/udaykishore-resu/guest-score/backend/internal/search"
)

// This file wires the optional infrastructure — the scoring service, the cache,
// the search index and the health probes — into the HTTP surface. It is
// separate from api.go so that the handlers there stay about the domain, and so
// that the answer to "what happens with none of this configured?" is one file
// rather than a thread running through all of them.

// --- options -----------------------------------------------------------------

// WithScorer supplies the scoring implementation, local or gRPC.
func WithScorer(sc scoringsvc.Scorer) Option { return func(s *Server) { s.scorer = sc } }

// WithCache supplies a cache. Passing cache.Nop{} is equivalent to omitting it.
func WithCache(c cache.Cache) Option {
	return func(s *Server) {
		if c != nil {
			s.cache = c
		}
	}
}

// WithSearch supplies a search index.
func WithSearch(i search.Index) Option {
	return func(s *Server) {
		if i != nil {
			s.search = i
		}
	}
}

// WithGraphQL mounts a GraphQL handler at /graphql.
func WithGraphQL(h http.Handler) Option { return func(s *Server) { s.graphql = h } }

// WithHealthChecks registers dependency probes for /api/health.
func WithHealthChecks(checks ...HealthCheck) Option {
	return func(s *Server) { s.checks = append(s.checks, checks...) }
}

// --- health ------------------------------------------------------------------

// HealthCheck is one dependency probe.
//
// Critical distinguishes "this service cannot work" from "this service is
// slower or less capable than it could be". Getting that wrong is expensive in
// both directions: marking Redis critical means a cache restart takes the API
// out of the load balancer, and marking Postgres non-critical means Kubernetes
// keeps routing traffic to a replica that can only return 500s.
type HealthCheck struct {
	Name     string
	Critical bool
	Probe    func(context.Context) error
}

type checkResult struct {
	OK        bool    `json:"ok"`
	LatencyMS float64 `json:"latency_ms"`
	Critical  bool    `json:"critical"`
	Error     string  `json:"error,omitempty"`
}

// handleHealth reports per-dependency status.
//
// It answers 200 for both "ok" and "degraded", and 503 only when a critical
// dependency is down. A degraded service is still serving correct answers from
// a smaller feature set, and taking it out of rotation for that would turn a
// cache outage into a site outage.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	results := make(map[string]checkResult, len(s.checks))
	var mu sync.Mutex
	var wg sync.WaitGroup

	// Probes run concurrently: four sequential probes against four different
	// services would make the health endpoint the slowest thing in the system,
	// and a slow health endpoint gets a pod killed.
	for _, c := range s.checks {
		wg.Add(1)
		go func(c HealthCheck) {
			defer wg.Done()
			start := time.Now()
			err := c.Probe(ctx)
			res := checkResult{
				OK:        err == nil,
				LatencyMS: float64(time.Since(start).Microseconds()) / 1000.0,
				Critical:  c.Critical,
			}
			if err != nil {
				res.Error = err.Error()
			}
			mu.Lock()
			results[c.Name] = res
			mu.Unlock()
		}(c)
	}
	wg.Wait()

	status, code := "ok", http.StatusOK
	for _, res := range results {
		if res.OK {
			continue
		}
		if res.Critical {
			status, code = "unavailable", http.StatusServiceUnavailable
			break
		}
		status = "degraded"
	}

	writeJSON(w, code, map[string]any{
		"status":  status,
		"service": "guest-score",
		"time":    time.Now().UTC(),
		"checks":  results,
	})
}

// --- cached score ------------------------------------------------------------

// scoreCached returns a guest's score, using the cache when one is configured.
//
// The cached value is the computed Score, not the reviews: the reviews are
// cheap to fetch and the derivation is the expensive part, and caching the
// input would leave every replica recomputing the same answer.
func (s *Server) scoreCached(ctx context.Context, guestID string, now scoring.Time) (scoring.Score, error) {
	key := cache.ScoreKey(guestID)
	if b, err := s.cache.Get(ctx, key); err == nil {
		var sc scoring.Score
		if json.Unmarshal(b, &sc) == nil {
			return sc, nil
		}
		// A value that will not decode is a value written by an older build.
		// Drop it rather than serving a zero-valued score, which would read as
		// a guest with no standing at all.
		_ = s.cache.Delete(ctx, key)
	}

	reviews, err := s.store.ReviewsForGuest(guestID)
	if err != nil {
		return scoring.Score{}, err
	}
	sc := s.scorer.Score(ctx, guestID, reviews, now)

	if b, err := json.Marshal(sc); err == nil {
		_ = s.cache.Set(ctx, key, b, s.cacheTTL())
	}
	return sc, nil
}

func (s *Server) cacheTTL() time.Duration { return 60 * time.Second }

// invalidate drops everything a write about one guest could have staled.
func (s *Server) invalidate(ctx context.Context, guestID string) {
	if err := cache.InvalidateGuest(ctx, s.cache, guestID); err != nil {
		// A failed invalidation leaves a stale score visible, which is worse
		// than a failed read, so it is logged at error rather than swallowed.
		s.log.Error("cache invalidation failed; a stale score may be served",
			"guest_id", guestID, "err", err)
	}
}

// reindexGuest keeps the search index in step with a write.
//
// Failures are logged and not surfaced: the index is an accelerator, and a
// review that was accepted must not report failure because a secondary system
// was briefly unavailable. The reindex endpoint below repairs the drift.
func (s *Server) reindexGuest(ctx context.Context, guestID string) {
	if _, ok := s.search.(search.Nop); ok {
		return
	}
	g, err := s.store.GetGuest(guestID)
	if err != nil {
		return
	}
	reviews, err := s.store.ReviewsForGuest(guestID)
	if err != nil {
		return
	}
	sc := s.scorer.Score(ctx, guestID, reviews, s.now())
	doc := search.DocFor(g, sc.Tier, sc.Composite, sc.Flagged, sc.IncidentCount, sc.StayCount,
		time.Now().UTC().Format(time.RFC3339))
	if err := s.search.Put(ctx, doc); err != nil {
		s.log.Warn("search index update failed; the directory may be stale until reindex",
			"guest_id", guestID, "err", err)
	}
}

// --- search ------------------------------------------------------------------

type searchHitResponse struct {
	Guest     GuestSummary `json:"guest"`
	Relevance float64      `json:"relevance"`
}

// handleSearch is the fuzzy directory lookup.
//
// It returns the engine that answered, which matters operationally: with
// Elasticsearch, "no results" means no file plausibly matches; without it, it
// only means no name contains that exact substring. A desk agent who cannot
// tell the two apart will open a duplicate file, which is the failure this
// index exists to prevent.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	text := strings.TrimSpace(q.Get("q"))
	tier := strings.TrimSpace(q.Get("tier"))
	country := strings.ToUpper(strings.TrimSpace(q.Get("country")))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 25
	}

	ctx := r.Context()
	now := s.now()

	res, err := s.search.Search(ctx, search.Query{
		Text: text, Tier: tier, Country: country, Limit: limit,
	})
	if err != nil {
		// A search backend that errors falls back rather than failing the
		// request: an exact-substring answer is far better than none.
		s.log.Warn("search backend failed, falling back to in-process matching", "err", err)
		res = search.Results{Engine: search.EngineInProcess}
	}

	out := struct {
		Engine string              `json:"engine"`
		Total  int                 `json:"total"`
		Hits   []searchHitResponse `json:"hits"`
		Note   string              `json:"note,omitempty"`
	}{Engine: res.Engine, Hits: []searchHitResponse{}}

	if res.Engine == search.EngineElastic {
		out.Total = res.Total
		for _, h := range res.Hits {
			// Hydrate from the store: the index is an accelerator and never the
			// source of truth for what is displayed, so a stale indexed score
			// is never shown as the guest's standing.
			g, err := s.store.GetGuest(h.Doc.ID)
			if err != nil {
				continue
			}
			sc, err := s.scoreCached(ctx, g.ID, now)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			out.Hits = append(out.Hits, searchHitResponse{
				Guest: GuestSummary{Guest: g, Score: sc}, Relevance: h.Relevance,
			})
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	out.Note = "Elasticsearch is not configured, so this is an exact substring match. " +
		"Misspellings and alternate transliterations will not be found."
	guests, err := s.store.ListGuests()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, g := range guests {
		if !search.MatchesInProcess(g, text) {
			continue
		}
		if country != "" && !strings.EqualFold(string(g.Nationality), country) {
			continue
		}
		sc, err := s.scoreCached(ctx, g.ID, now)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if tier != "" && !strings.EqualFold(sc.Tier, tier) {
			continue
		}
		out.Hits = append(out.Hits, searchHitResponse{
			Guest: GuestSummary{Guest: g, Score: sc}, Relevance: 1,
		})
	}
	sort.SliceStable(out.Hits, func(i, j int) bool {
		return out.Hits[i].Guest.Score.Composite > out.Hits[j].Guest.Score.Composite
	})
	if len(out.Hits) > limit {
		out.Hits = out.Hits[:limit]
	}
	out.Total = len(out.Hits)
	writeJSON(w, http.StatusOK, out)
}

// handleReindex rebuilds the search index from the store.
//
// This is the repair path for the best-effort indexing above, and the way a
// mapping change is rolled out. It is deliberately synchronous and reports what
// it did: a reindex that silently half-finished is worse than one that failed.
func (s *Server) handleReindex(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.search.(search.Nop); ok {
		writeError(w, http.StatusPreconditionFailed, "search_disabled",
			"Elasticsearch is not configured; set GS_ELASTIC_URL to enable the index.", nil)
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
	defer cancel()

	if err := s.search.Ensure(ctx); err != nil {
		s.fail(w, r, fmt.Errorf("ensuring index: %w", err))
		return
	}
	guests, err := s.store.ListGuests()
	if err != nil {
		s.fail(w, r, err)
		return
	}

	now := s.now()
	stamp := time.Now().UTC().Format(time.RFC3339)
	docs := make([]search.Doc, 0, len(guests))
	for _, g := range guests {
		reviews, err := s.store.ReviewsForGuest(g.ID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		sc := s.scorer.Score(ctx, g.ID, reviews, now)
		docs = append(docs, search.DocFor(g, sc.Tier, sc.Composite, sc.Flagged,
			sc.IncidentCount, sc.StayCount, stamp))
	}

	// Batched so a large directory is a handful of bulk requests rather than
	// one enormous body Elasticsearch may reject outright.
	const batch = 200
	indexed := 0
	for i := 0; i < len(docs); i += batch {
		end := min(i+batch, len(docs))
		if err := s.search.PutBatch(ctx, docs[i:end]); err != nil {
			writeError(w, http.StatusBadGateway, "reindex_failed",
				fmt.Sprintf("Indexed %d of %d guests before failing: %v", indexed, len(docs), err), nil)
			return
		}
		indexed = end
	}

	s.log.Info("search index rebuilt", "guests", indexed)
	writeJSON(w, http.StatusOK, map[string]any{
		"indexed": indexed,
		"engine":  search.EngineElastic,
	})
}
