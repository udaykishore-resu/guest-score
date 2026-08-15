# Tasks: Guest Reputation Scoring

**Input**: Design documents from `/specs/001-guest-reputation/`

**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md)

**Tests**: Included and mandatory. Constitution Principle V enumerates specific
cases that must exist; they are not optional and not a coverage target.

**Organization**: Grouped by user story so each is independently deliverable.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: US1–US4 per spec.md, or FOUND for shared prerequisites

## Path Conventions

Web app: `backend/` (Go, stdlib only), `frontend/` (React + Vite).

---

## Phase 1: Setup

- [x] T001 Initialise `backend/go.mod` with **no** require block (Constitution II)
- [x] T002 [P] Scaffold `frontend/` — Vite, React 19, TypeScript, strict mode
- [x] T003 [P] Add `.gitignore` covering `node_modules/`, `dist/`, `bin/`, `data/`
- [x] T004 [P] Add root `Makefile` with `dev`, `test`, `lint`, `build`, `run`, `reseed`

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Domain and persistence. The dependency graph must point strictly
inward — `api → scoring → domain` — because that is what makes Principle III
(scoring purity) enforceable rather than aspirational.

**⚠️ BLOCKS ALL USER STORIES**

- [x] T005 [FOUND] Define `Guest`, `Review`, `Ratings`, `Incident`, dimension and severity enums in `backend/internal/domain/domain.go`, importing nothing local
- [x] T006 [FOUND] Add `Validate()` returning `FieldErrors` for `Ratings`, `Review`, `Guest` (FR-009)
- [x] T007 [FOUND] Publish `IncidentCatalog` with base penalties and severity multipliers
- [x] T008 [FOUND] Define the `Store` interface in `backend/internal/store/store.go`
- [x] T009 [FOUND] Implement `FileStore` — RWMutex-guarded maps, atomic JSON snapshot via temp-file rename, background flush (FR-013)
- [x] T010 [FOUND] Enforce one-review-per-host-per-stay as a set rather than an O(n) scan (FR-010)
- [x] T011 [FOUND] Write the deterministic seed generator covering every grade band, unrated guests, aged evidence, and stacked incidents (FR-014)

**Checkpoint**: Domain types validate; data persists and reloads.

---

## Phase 3: US1 — Screen a guest before accepting a booking (P1)

**Goal**: An explainable score for any guest (FR-001 – FR-008).

**Independent test**: Open a profile; confirm score, grade, dimensions,
confidence, and recommendation all render from live API data.

### Scoring engine — gate

**Constitution V: these tests must pass before any HTTP code is written.**

- [x] T012 [US1] Define `Model` with weights, half-lives, prior, and grade bands as one inspectable struct in `backend/internal/scoring/scoring.go`
- [x] T013 [US1] Add the injected-clock type in `backend/internal/scoring/clock.go` so `Compute` cannot read a clock
- [x] T014 [US1] Implement stage 1–2: per-dimension weighted mean with exponential recency decay (FR-003, FR-004)
- [x] T015 [US1] Implement stage 3: Bayesian shrinkage toward the population prior (FR-005)
- [x] T016 [US1] Implement stage 4: incident penalties scaled by severity, faded by recency, floored at 0 (FR-006)
- [x] T017 [US1] Build the explanation — dimension breakdown, weights, confidence, factor list with point impacts (FR-007)
- [x] T018 [US1] Map score to grade band and booking recommendation; a recent severe incident must block a clean `accept` (FR-008)
- [x] T019 [US1] Return an explicit unrated state for zero reviews, never a fabricated zero (FR-002)
- [x] T020 [US1] Table test: the mandated cases — empty, single review, perfect, floor
- [x] T021 [US1] Test: composite never leaves [0,100] under stacked severe incidents
- [x] T022 [US1] Test: decay across year boundaries, including a future timestamp
- [x] T023 [US1] Test: time decay reduces influence and degrades confidence
- [x] T024 [US1] Test: incident penalties stack, and scale by both severity and age
- [x] T025 [US1] Test: `Compute` is pure — identical inputs, bit-identical output (SC-003)
- [x] T026 [US1] Test: weights sum to 1.0; grade bands are contiguous and descending

**Gate passed** → HTTP work may begin.

### API and UI

- [x] T027 [US1] Router on `net/http.ServeMux` with Go 1.22 method+wildcard patterns
- [x] T028 [US1] `GET /api/guests/{id}` returning profile, score, and review history
- [x] T029 [US1] `GET /api/guests/{id}/score`
- [x] T030 [US1] `GET /api/scoring-model` publishing weights and constants so the client hardcodes nothing (FR-007)
- [x] T031 [P] [US1] Typed API client in `frontend/src/lib/api.ts`
- [x] T032 [P] [US1] `ScoreDial`, `DimensionBars`, `ScoreComposition` in `frontend/src/components/charts.tsx`
- [x] T033 [P] [US1] `GradeBadge`, `RecommendationBadge`, `ConfidenceChip` in `frontend/src/components/ui.tsx`
- [x] T034 [US1] Guest profile page wiring score, factors, and stay history
- [x] T035 [US1] Handler test: unrated is not zero
- [x] T036 [US1] Handler test: every rated guest returns a complete explanation

**Checkpoint**: A host can reach a reasoned accept/decline judgment (SC-001).

---

## Phase 4: US2 — Submit a post-stay assessment (P1)

**Goal**: Close the contribution loop.

- [x] T037 [US2] `POST /api/reviews` returning score before, after, and the delta
- [x] T038 [US2] Reject out-of-range ratings with field-level errors, storing nothing (FR-009)
- [x] T039 [US2] Reject a duplicate host+stay review with 409 (FR-010)
- [x] T040 [US2] Reject a review for an unknown guest rather than creating one
- [x] T041 [US2] Review submission form with per-dimension weights shown, incident flags with severity, and character counter
- [x] T042 [US2] Success state showing the exact before/after/delta the review caused
- [x] T043 [US2] Handler test: happy path, and the review is readable back
- [x] T044 [US2] Handler test: ratings of 0, 6, −1, 99 all rejected; nothing persisted
- [x] T045 [US2] Handler test: duplicate stay returns 409
- [x] T046 [US2] Handler test: a flagged incident appears as a distinct penalty factor
- [x] T047 [US2] Handler test: 24 concurrent submissions all persist with distinct IDs (write-race edge case)

---

## Phase 5: US3 — Portfolio dashboard (P2)

- [x] T048 [US3] `GET /api/stats` computing band distribution, dimension averages, and a 12-month timeline
- [x] T049 [US3] `GET /api/reviews` activity feed, denormalising guest names to avoid N+1
- [x] T050 [P] [US3] `BandDistribution` and `ReviewTimeline` charts — single-hue, direct-labelled, table view behind each
- [x] T051 [US3] Dashboard page with stat tiles and charts
- [x] T052 [US3] Empty state rather than zeros presented as measurements
- [x] T053 [US3] Handler test: every statistic reconciles with the underlying data
- [x] T054 [US3] Handler test: an empty store reports `empty: true`

---

## Phase 6: US4 — Directory with search and filter (P2)

- [x] T055 [US4] `GET /api/guests` with `q`, `band`, `incidents`, `sort`, `limit`, `offset` (FR-011)
- [x] T056 [US4] Case-insensitive literal substring matching — never compiled as a pattern
- [x] T057 [US4] Sort with unrated guests last rather than tying at zero
- [x] T058 [US4] Directory page with debounced search and filter controls
- [x] T059 [US4] Handler test: regex and SQL metacharacters are treated as literals
- [x] T060 [US4] Handler test: band and incident filters return only matching guests

---

## Phase 7: Polish & Deployment

- [x] T061 [P] Scoring-model page rendered entirely from the API, hardcoding no constants
- [x] T062 [P] Light and dark palettes, each selected for its own surface rather than machine-flipped
- [x] T063 [P] Error middleware, panic recovery, request logging, CORS
- [x] T064 Serve the built SPA from the Go binary with deep-link fallback and correct asset caching
- [x] T065 [P] Graceful shutdown flushing a final snapshot
- [x] T066 [P] Distroless `Dockerfile` building frontend and backend in one image
- [x] T067 [P] `README.md` documenting the model, the API, and deployment
- [x] T068 Verify: `go test ./... -race -cover`, `go vet`, frontend build, screenshots, end-to-end review submission

---

## Dependencies

```
Setup (T001-T004)
   ↓
Foundational (T005-T011)   ← blocks everything
   ↓
US1 scoring gate (T012-T026)   ← Constitution V: blocks all HTTP work
   ↓
   ├── US1 API/UI (T027-T036)
   ├── US2 (T037-T047)  ── needs US1's score computation for the delta
   ├── US3 (T048-T054)  ── independent of US2
   └── US4 (T055-T060)  ── independent of US2 and US3
   ↓
Polish (T061-T068)
```

US3 and US4 are genuinely parallel once US1's API exists. US2 depends on US1
because the submission response reports a score delta.

## Status

All 68 tasks complete. 37 tests, race-clean; 95% statement coverage on
`internal/scoring`, 77% on `internal/api`.
