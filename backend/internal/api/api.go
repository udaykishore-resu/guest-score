// Package api exposes the HTTP surface described in plan.md.
//
// Routing uses net/http.ServeMux with Go 1.22 method+wildcard patterns, so
// there is no third-party router (Constitution Principle II).
package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoring"
	"github.com/udaykishore-resu/guest-score/backend/internal/store"
)

// Server holds the API dependencies.
type Server struct {
	store store.Store
	model scoring.Model
	log   *slog.Logger

	// now is injected so handler tests can pin the evaluation instant and get
	// the same determinism the scoring tests rely on.
	now func() scoring.Time
}

// Option configures a Server.
type Option func(*Server)

// WithClock overrides the evaluation clock. Test-only in practice.
func WithClock(f func() scoring.Time) Option {
	return func(s *Server) { s.now = f }
}

// New builds a Server.
func New(st store.Store, log *slog.Logger, opts ...Option) *Server {
	s := &Server{
		store: st,
		model: scoring.DefaultModel,
		log:   log,
		now:   scoring.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Routes returns the fully wired API mux, already wrapped in middleware.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/scoring-model", s.handleScoringModel)
	mux.HandleFunc("GET /api/guests", s.handleListGuests)
	mux.HandleFunc("POST /api/guests", s.handleCreateGuest)
	mux.HandleFunc("GET /api/guests/{id}", s.handleGetGuest)
	mux.HandleFunc("GET /api/guests/{id}/score", s.handleGetScore)
	mux.HandleFunc("GET /api/reviews", s.handleListReviews)
	mux.HandleFunc("POST /api/reviews", s.handleCreateReview)
	mux.HandleFunc("GET /api/stats", s.handleStats)

	return withRecover(s.log, withCORS(withLogging(s.log, mux)))
}

// --- DTOs --------------------------------------------------------------------

// GuestSummary is a directory row: identity plus the computed score.
type GuestSummary struct {
	domain.Guest
	Score      scoring.Score `json:"score"`
	LastStayAt *time.Time    `json:"last_stay_at,omitempty"`
}

// GuestDetail adds the full review history.
type GuestDetail struct {
	GuestSummary
	Reviews []domain.Review `json:"reviews"`
}

type listGuestsResponse struct {
	Guests []GuestSummary `json:"guests"`
	Total  int            `json:"total"`
}

type createReviewResponse struct {
	Review        domain.Review `json:"review"`
	ScoreBefore   scoring.Score `json:"score_before"`
	ScoreAfter    scoring.Score `json:"score_after"`
	CompositeDelta float64      `json:"composite_delta"`
}

// --- Handlers ----------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   time.Now().UTC(),
		"service": "guest-score",
	})
}

// handleScoringModel publishes the weights and constants. This is what makes
// FR-007 self-documenting: a client can render the exact model the score used
// without the numbers being duplicated in the frontend.
func (s *Server) handleScoringModel(w http.ResponseWriter, r *http.Request) {
	type dimInfo struct {
		Dimension domain.Dimension `json:"dimension"`
		Label     string           `json:"label"`
		Weight    float64          `json:"weight"`
	}
	dims := make([]dimInfo, 0, len(domain.AllDimensions))
	for _, d := range domain.AllDimensions {
		dims = append(dims, dimInfo{d, d.Label(), s.model.Weights[d]})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dimensions":              dims,
		"review_half_life_days":   s.model.ReviewHalfLife,
		"incident_half_life_days": s.model.IncidentHalfLife,
		"prior_mean":              s.model.PriorMean,
		"prior_strength":          s.model.PriorStrength,
		"grade_bands":             s.model.GradeBands,
		"incident_catalog":        domain.IncidentCatalog,
		"severity_multipliers": map[string]float64{
			string(domain.SevMinor):    domain.SevMinor.Multiplier(),
			string(domain.SevModerate): domain.SevModerate.Multiplier(),
			string(domain.SevSevere):   domain.SevSevere.Multiplier(),
		},
	})
}

func (s *Server) handleListGuests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	search := strings.ToLower(strings.TrimSpace(q.Get("q")))
	band := strings.ToUpper(strings.TrimSpace(q.Get("band")))
	onlyIncidents := q.Get("incidents") == "true"
	sortBy := q.Get("sort")
	if sortBy == "" {
		sortBy = "score"
	}

	guests, err := s.store.ListGuests()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	now := s.now()

	out := make([]GuestSummary, 0, len(guests))
	for _, g := range guests {
		// Substring match is done on lowercased literals, never compiled as a
		// pattern, so regex metacharacters in the query are inert.
		if search != "" &&
			!strings.Contains(strings.ToLower(g.Name), search) &&
			!strings.Contains(strings.ToLower(g.Email), search) {
			continue
		}
		reviews, err := s.store.ReviewsForGuest(g.ID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		sc := scoring.Compute(reviews, now, s.model)

		if band != "" && sc.Grade != band {
			continue
		}
		if onlyIncidents && sc.IncidentCount == 0 {
			continue
		}

		sum := GuestSummary{Guest: g, Score: sc}
		if len(reviews) > 0 {
			latest := reviews[0].CheckOut
			for _, rv := range reviews {
				if rv.CheckOut.After(latest) {
					latest = rv.CheckOut
				}
			}
			sum.LastStayAt = &latest
		}
		out = append(out, sum)
	}

	sortSummaries(out, sortBy)

	total := len(out)
	if off, _ := strconv.Atoi(q.Get("offset")); off > 0 {
		if off >= len(out) {
			out = nil
		} else {
			out = out[off:]
		}
	}
	if lim, _ := strconv.Atoi(q.Get("limit")); lim > 0 && lim < len(out) {
		out = out[:lim]
	}

	writeJSON(w, http.StatusOK, listGuestsResponse{Guests: out, Total: total})
}

func sortSummaries(out []GuestSummary, by string) {
	switch by {
	case "name":
		sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	case "reviews":
		sort.SliceStable(out, func(i, j int) bool {
			return out[i].Score.ReviewCount > out[j].Score.ReviewCount
		})
	case "recent":
		sort.SliceStable(out, func(i, j int) bool {
			// Guests with no stays sort last rather than sorting as epoch-zero.
			if out[i].LastStayAt == nil {
				return false
			}
			if out[j].LastStayAt == nil {
				return true
			}
			return out[i].LastStayAt.After(*out[j].LastStayAt)
		})
	default: // "score"
		sort.SliceStable(out, func(i, j int) bool {
			// Unrated guests sort below every rated guest instead of tying at 0.
			if out[i].Score.Rated != out[j].Score.Rated {
				return out[i].Score.Rated
			}
			return out[i].Score.Composite > out[j].Score.Composite
		})
	}
}

func (s *Server) handleGetGuest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	g, err := s.store.GetGuest(id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	reviews, err := s.store.ReviewsForGuest(id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	sc := scoring.Compute(reviews, s.now(), s.model)

	detail := GuestDetail{
		GuestSummary: GuestSummary{Guest: g, Score: sc},
		Reviews:      reviews,
	}
	if len(reviews) > 0 {
		latest := reviews[0].CheckOut
		for _, rv := range reviews {
			if rv.CheckOut.After(latest) {
				latest = rv.CheckOut
			}
		}
		detail.LastStayAt = &latest
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleGetScore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetGuest(id); err != nil {
		s.fail(w, r, err)
		return
	}
	reviews, err := s.store.ReviewsForGuest(id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, scoring.Compute(reviews, s.now(), s.model))
}

func (s *Server) handleCreateGuest(w http.ResponseWriter, r *http.Request) {
	var g domain.Guest
	if err := decodeJSON(r, &g); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error(), nil)
		return
	}
	g.Name = strings.TrimSpace(g.Name)
	g.Email = strings.TrimSpace(g.Email)
	if errs := g.Validate(); errs.Any() {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "The guest could not be created.", errs)
		return
	}
	created, err := s.store.CreateGuest(g)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, GuestSummary{
		Guest: created,
		Score: scoring.Compute(nil, s.now(), s.model),
	})
}

func (s *Server) handleCreateReview(w http.ResponseWriter, r *http.Request) {
	var rev domain.Review
	if err := decodeJSON(r, &rev); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error(), nil)
		return
	}
	if rev.HostID == "" {
		rev.HostID = "h_001" // single-tenant demo posture; see spec assumption 3
	}
	if rev.HostName == "" {
		rev.HostName = "You"
	}
	if rev.StayID == "" {
		rev.StayID = fmt.Sprintf("s_manual_%d", time.Now().UnixNano())
	}
	if rev.SubmittedAt.IsZero() {
		rev.SubmittedAt = time.Now().UTC()
	}

	if errs := rev.Validate(); errs.Any() {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed",
			"The review could not be submitted.", errs)
		return
	}

	// Capture the score before the write so the response can report the delta,
	// which is what makes User Story 2's "see how much your review moved it"
	// acceptance criterion observable.
	before, err := s.store.ReviewsForGuest(rev.GuestID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	now := s.now()
	scoreBefore := scoring.Compute(before, now, s.model)

	created, err := s.store.CreateReview(rev)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	after, err := s.store.ReviewsForGuest(rev.GuestID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	scoreAfter := scoring.Compute(after, now, s.model)

	delta := scoreAfter.Composite
	if scoreBefore.Rated {
		delta = scoreAfter.Composite - scoreBefore.Composite
	}

	writeJSON(w, http.StatusCreated, createReviewResponse{
		Review:         created,
		ScoreBefore:    scoreBefore,
		ScoreAfter:     scoreAfter,
		CompositeDelta: round1(delta),
	})
}

func (s *Server) handleListReviews(w http.ResponseWriter, r *http.Request) {
	reviews, err := s.store.AllReviews()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	limit := 25
	if l, _ := strconv.Atoi(r.URL.Query().Get("limit")); l > 0 {
		limit = l
	}
	if len(reviews) > limit {
		reviews = reviews[:limit]
	}

	// Denormalize the guest name so the activity feed is one request, not N+1.
	type row struct {
		domain.Review
		GuestName string `json:"guest_name"`
	}
	out := make([]row, 0, len(reviews))
	for _, rv := range reviews {
		name := ""
		if g, err := s.store.GetGuest(rv.GuestID); err == nil {
			name = g.Name
		}
		out = append(out, row{Review: rv, GuestName: name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"reviews": out})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	guests, err := s.store.ListGuests()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	reviews, err := s.store.AllReviews()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	now := s.now()

	bands := map[string]int{"A": 0, "B": 0, "C": 0, "D": 0, "F": 0}
	dimTotals := map[domain.Dimension]float64{}
	dimCounts := map[domain.Dimension]int{}
	var scoreSum float64
	rated, unrated, withIncidents, incidentTotal := 0, 0, 0, 0

	for _, g := range guests {
		rs, err := s.store.ReviewsForGuest(g.ID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		sc := scoring.Compute(rs, now, s.model)
		if !sc.Rated {
			unrated++
			continue
		}
		rated++
		scoreSum += sc.Composite
		bands[sc.Grade]++
		if sc.IncidentCount > 0 {
			withIncidents++
			incidentTotal += sc.IncidentCount
		}
	}

	for _, rv := range reviews {
		for _, d := range domain.AllDimensions {
			dimTotals[d] += float64(rv.Ratings.Get(d))
			dimCounts[d]++
		}
	}

	type dimAvg struct {
		Dimension domain.Dimension `json:"dimension"`
		Label     string           `json:"label"`
		Average   float64          `json:"average"`
	}
	dimAverages := make([]dimAvg, 0, len(domain.AllDimensions))
	for _, d := range domain.AllDimensions {
		avg := 0.0
		if dimCounts[d] > 0 {
			avg = round2(dimTotals[d] / float64(dimCounts[d]))
		}
		dimAverages = append(dimAverages, dimAvg{d, d.Label(), avg})
	}

	// An empty portfolio reports an explicit empty state rather than zeros
	// presented as if they were measurements (acceptance scenario 3.2).
	avgScore := 0.0
	if rated > 0 {
		avgScore = round1(scoreSum / float64(rated))
	}

	// Review volume over the last 12 months, oldest bucket first.
	type bucket struct {
		Month string `json:"month"`
		Count int    `json:"count"`
	}
	counts := map[string]int{}
	for _, rv := range reviews {
		counts[rv.SubmittedAt.Format("2006-01")]++
	}
	timeline := make([]bucket, 0, 12)
	cursor := now.Std().AddDate(0, -11, 0)
	for i := 0; i < 12; i++ {
		key := cursor.Format("2006-01")
		timeline = append(timeline, bucket{Month: key, Count: counts[key]})
		cursor = cursor.AddDate(0, 1, 0)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"empty":                 len(reviews) == 0,
		"total_guests":          len(guests),
		"total_reviews":         len(reviews),
		"rated_guests":          rated,
		"unrated_guests":        unrated,
		"average_score":         avgScore,
		"guests_with_incidents": withIncidents,
		"total_incidents":       incidentTotal,
		"band_distribution":     bands,
		"dimension_averages":    dimAverages,
		"review_timeline":       timeline,
	})
}

// --- Errors ------------------------------------------------------------------

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error(), nil)
	case errors.Is(err, store.ErrDuplicate):
		writeError(w, http.StatusConflict, "duplicate", err.Error(), nil)
	default:
		s.log.Error("request failed", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.", nil)
	}
}
