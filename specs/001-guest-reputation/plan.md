# Implementation Plan: Hotel Guest Standing & Discounts

**Branch**: `001-guest-reputation` | **Spec**: [spec.md](./spec.md)

## Summary

A Go stdlib HTTP API computes explainable hotel guest standings from staff-filed
stay records, backed by a mutex-guarded in-memory store with JSON snapshot
persistence. A Vite + React SPA consumes it: guest directory, guest profile with
tier, discount and score breakdown, stay recording, portfolio dashboard.

## Technical Context

| | |
|---|---|
| **Backend language** | Go 1.24 |
| **Backend dependencies** | None — standard library only (Constitution II) |
| **HTTP routing** | `net/http.ServeMux` with Go 1.22 method+wildcard patterns |
| **Persistence** | `Store` interface; JSON-file implementation with atomic rename |
| **Frontend** | React 19 + Vite 7, TypeScript, no UI framework |
| **Charts** | Hand-rolled SVG — avoids a dependency for four simple visuals |
| **Testing** | `testing` + `net/http/httptest`, table-driven |
| **Deploy** | Single static binary serving the built SPA; Dockerfile + fly/render configs |

## Constitution Check

| Principle | Status | Note |
|---|---|---|
| I — Spec before code | PASS | spec.md written and reviewed before this plan |
| II — Zero backend deps | PASS | `go.mod` has no `require` block |
| III — Pure scoring | PASS | `scoring.Compute(reviews, now)` — no I/O, injected clock |
| IV — Explainable | PASS | `Score` value object carries breakdown, weights, factors |
| V — Test the math | PASS | table tests enumerate the six mandated cases |
| VI — Demoable | PASS | `make dev`; seed runs on empty store |

No exceptions requested.

## Architecture

```
backend/
  cmd/server/main.go        wiring, flags, graceful shutdown, static SPA serving
  internal/domain/          entities + validation. Zero dependencies on other pkgs.
  internal/scoring/         pure engine. Depends only on domain.
  internal/store/           Store interface, memory impl, JSON snapshot, seed data
  internal/api/             HTTP handlers, router, middleware, DTOs
frontend/src/
  lib/api.ts                typed fetch client
  pages/                    Directory, GuestProfile, Dashboard, SubmitReview
  components/               ScoreDial, DimensionBars, FactorList, GradeBadge, ...
```

**Dependency direction** is strictly inward: `api → scoring → domain` and
`api → store → domain`. The scoring package does not import store; the domain
package imports nothing local. This is what keeps Principle III enforceable —
the engine physically cannot perform I/O because it has no access to anything
that can.

## The Scoring Model

Composite score in [0, 100], computed in four stages.

**Stage 1 — Weighted dimension mean per review.** Each review yields a 1–5
quality value from its five dimensions:

| Dimension | Weight | Why |
|---|---|---|
| Hotel policy compliance | 0.28 | Strongest predictor of cost and complaint volume |
| Room condition | 0.26 | Direct financial exposure |
| Staff interaction | 0.18 | Determines the cost of the stay in staff attention |
| Noise / other guests | 0.16 | Externality; drives complaints from paying neighbours |
| Booking accuracy | 0.12 | Occupancy and purpose misrepresentation |

**Stage 2 — Time-decayed aggregation.** Each review carries weight
`exp(-ln(2) · ageDays / halfLifeDays)` with a half-life of 365 days: a review is
worth half as much after a year, a quarter after two. Continuous, as FR-004
requires, with no cliff at an arbitrary boundary.

**Stage 3 — Bayesian shrinkage.** The decayed mean is blended with a population
prior of 3.9/5 using a pseudo-count of 3.0:

```
adjusted = (prior·C + Σ(wᵢ·qᵢ)) / (C + Σwᵢ)
```

Two reviews cannot outrun twenty. This is what makes FR-005 true and what makes
the "low confidence" label honest rather than decorative.

**Stage 4 — Commendations, then incidents.** Both apply after the 0–100 rescale.
Commendations add `baseBonus × recencyFactor` (270-day half-life); incidents
deduct `basePenalty × severityMultiplier × recencyFactor` (180-day half-life).

Order matters and the obvious `base - penalty + bonus` is wrong. Applied
together, a pile of commendations pushes the raw total well above 100 where the
clamp silently absorbs any penalty — during implementation a guest with ten
commendations and a severe damage incident scored an unchanged 100.0. The engine
lifts and clamps first, then deducts:

```
lifted    = clamp(base + bonus, 0, 100)
composite = clamp(lifted - penalty, 0, 100)
```

Commendations can carry a guest to the ceiling; they cannot buy immunity.

**Rounding.** The tier is resolved from the *rounded* composite, not the raw
float. Resolving from the raw value produced a guest displaying 90.0 while
sitting in Premium, because 89.96 is below the VIP floor — and simultaneously
being told they were "0.0 points" away.

Confidence is derived from effective review count (the sum of decay weights):
< 1.5 low, < 4.0 medium, otherwise high.

**Tiers** carry over unchanged from the superseded implementation: VIP ≥ 90
(20% off), Premium ≥ 70 (15%), Regular ≥ 50 (10%), Watch List below (0%).

**Handling is separate from tier.** The tier is what the guest earns; handling is
what the front desk should do. A guest can hold Premium on accumulated history
and still warrant a flag at check-in after one recent severe incident. Collapsing
both into one number would hide exactly the thing staff need to see.

## API Surface

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/health` | liveness |
| GET | `/api/guests` | directory; `q`, `tier`, `incidents`, `sort` params |
| GET | `/api/guests/{id}` | profile with full score breakdown and reviews |
| POST | `/api/guests` | create guest |
| GET | `/api/guests/{id}/score` | score only |
| POST | `/api/reviews` | record a stay; returns score before/after and delta |
| GET | `/api/reviews` | recent reviews across portfolio |
| GET | `/api/stats` | dashboard aggregates |
| GET | `/api/scoring-model` | weights, constants, tiers, both catalogues — makes FR-007 self-documenting |

Errors return `{"error": {"code", "message", "fields": {...}}}` with field-level
detail on validation failures.

## Phasing

1. Domain types + validation, with tests.
2. Scoring engine + the six mandated table tests. **Gate: tests green before any HTTP code.**
3. Store interface, memory impl, JSON persistence, seed generator.
4. HTTP layer + handler tests via httptest.
5. Frontend: API client, then P1 pages (Directory, Profile, Submit), then P2 (Dashboard).
6. Static-embed build, Dockerfile, deploy configs, screenshots.

## Risks

| Risk | Mitigation |
|---|---|
| JSON-file store won't scale past a demo | `Store` is an interface; swap point documented in `store/README` |
| Hand-tuned scoring constants are unvalidated | Exposed via `/api/scoring-model` and centralized in one struct for tuning |
| Commendations could be used to launder a bad record | Bonuses are smaller than penalties, decay faster than ratings, and cannot mask a deduction (see stage 4) |
| No auth means the demo API is open | Documented explicitly; API shaped so host identity threads through later |
