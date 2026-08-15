# Implementation Plan: Guest Reputation Scoring

**Branch**: `001-guest-reputation` | **Spec**: [spec.md](./spec.md)

## Summary

A Go stdlib HTTP API computes explainable guest reputation scores from
host-submitted reviews, backed by a mutex-guarded in-memory store with
JSON snapshot persistence. A Vite + React SPA consumes it: guest directory,
guest profile with score breakdown, review submission, portfolio dashboard.

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
| House rules compliance | 0.28 | Strongest predictor of host risk; drives permits and complaints |
| Property care | 0.26 | Direct financial exposure |
| Communication | 0.18 | Determines cost of the stay in host attention |
| Noise / neighbor impact | 0.16 | Externality; threatens the listing itself |
| Accuracy of booking details | 0.12 | Party size and purpose misrepresentation |

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

**Stage 4 — Incident penalties.** Applied after the 0–100 rescale, each incident
deducts `basePenalty × severityMultiplier × recencyFactor`, where recency uses a
180-day half-life. Penalties stack additively; the result floors at 0.

Confidence is derived from effective review count (the sum of decay weights):
< 1.5 low, < 4.0 medium, otherwise high.

Grades: A ≥ 85, B ≥ 70, C ≥ 55, D ≥ 40, F below. Recommendations map from grade
and incident presence — an A guest with a severe recent incident does not return
"accept".

## API Surface

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/health` | liveness |
| GET | `/api/guests` | directory; `q`, `band`, `incidents`, `sort` params |
| GET | `/api/guests/{id}` | profile with full score breakdown and reviews |
| POST | `/api/guests` | create guest |
| GET | `/api/guests/{id}/score` | score only |
| POST | `/api/reviews` | submit review; returns score before/after and delta |
| GET | `/api/reviews` | recent reviews across portfolio |
| GET | `/api/stats` | dashboard aggregates |
| GET | `/api/scoring-model` | weights, constants, grade bands — makes FR-007 self-documenting |

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
| No auth means the demo API is open | Documented explicitly; API shaped so host identity threads through later |
