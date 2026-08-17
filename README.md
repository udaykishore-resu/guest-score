# Guest Score

Guest reputation scoring for short-term rental hosts. Look up a guest before you
accept their booking and get an explainable score, not a mystery number.

```bash
git clone <this-repo> && cd guest-score
make dev
```

Open http://localhost:5173. The backend seeds a realistic dataset on first run —
no database to provision, no credentials, no signup.

## Why it exists

A host accepting a booking from a stranger is making a decision with real
downside — property damage, a noise complaint that threatens their permit, a
guest who brings twelve people to a four-person listing — on almost no
information. Platform reviews are weak here because they are reciprocal: hosts
under-report bad guests to avoid retaliation.

Guest Score is a shared reputation layer. Hosts contribute structured post-stay
assessments; any host can query a guest before accepting.

## What makes the score trustworthy

Four stages, all inspectable at `/model` in the UI or `GET /api/scoring-model`:

1. **Weighted dimensions.** Five axes, unequally weighted — house rules 28%,
   property care 26%, communication 18%, noise 16%, booking accuracy 12% —
   because rule compliance and property damage are where the money and the
   permits are.
2. **Recency decay.** A review's weight halves every 365 days, on a continuous
   curve. Two-year-old behaviour counts a quarter as much as last month's.
3. **Limited-history adjustment.** The average is blended with a population
   prior of 3.9/5 weighted as three imaginary reviews, so one glowing review
   cannot read like twenty. This is why a guest with two perfect stays scores
   80 and a guest with twenty scores 96.
4. **Incident penalties.** Applied on the 0–100 scale, scaled by severity, faded
   with a 180-day half-life.

Three properties fall out of this design and are enforced by tests:

- **Every score is explained.** The API never returns a bare number — each score
  carries its dimension breakdown, the weights used, a confidence level, and a
  plain-language list of the factors that moved it, with their point impact.
- **Unrated is not zero.** A guest with no history returns an explicit `unrated`
  state and a recommendation to fall back on standard verification, rather than
  a fabricated score.
- **Scoring is a pure function.** `scoring.Compute(reviews, now, model)` performs
  no I/O and reads no clock — time is injected. Identical inputs give a
  bit-identical result, which is what makes a score auditable when someone asks
  why theirs is 68.

## Architecture

```
backend/                   Go 1.24, standard library only — no dependencies
  cmd/server/              wiring, graceful shutdown, SPA serving
  internal/domain/         entities + validation (imports nothing local)
  internal/scoring/        the pure engine
  internal/store/          Store interface, JSON-snapshot implementation, seed data
  internal/api/            handlers, router, middleware
frontend/                  React 19 + Vite + TypeScript, hand-rolled SVG charts
specs/                     spec-driven development docs
.specify/memory/           the project constitution
```

The backend's `go.mod` has no `require` block. That is deliberate and documented
in the constitution: it builds in air-gapped environments, deploys as one static
CGO-free binary, and needs no dependency audit. Storage sits behind a `Store`
interface so a SQL implementation can replace the JSON snapshot without the
domain, scoring, or API layers noticing.

Dependencies point strictly inward — `api → scoring → domain` — which is what
makes the purity rule enforceable rather than aspirational: the scoring package
has no access to anything that can perform I/O.

## API

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/health` | liveness |
| GET | `/api/guests` | directory; `q`, `band`, `incidents`, `sort`, `limit`, `offset` |
| POST | `/api/guests` | create a guest |
| GET | `/api/guests/{id}` | profile with full breakdown and review history |
| GET | `/api/guests/{id}/score` | score only |
| POST | `/api/reviews` | submit an assessment; returns the score before, after, and the delta |
| GET | `/api/reviews` | recent activity |
| GET | `/api/stats` | portfolio aggregates |
| GET | `/api/scoring-model` | the live weights, constants, and grade bands |

Errors return `{"error": {"code", "message", "fields": {...}}}` with field-level
detail on validation failures.

```bash
curl -X POST localhost:8080/api/reviews -H 'Content-Type: application/json' -d '{
  "guest_id": "g_001",
  "stay_id": "s_0500",
  "ratings": {"house_rules":5,"property_care":4,"communication":5,"noise":5,"accuracy":4},
  "incidents": [{"type":"late_checkout","severity":"minor"}],
  "comment": "Easy guest, left a little late."
}'
```

## Development

```bash
make dev       # backend :8080 + frontend :5173
make test      # go test ./... -race -cover
make lint      # go vet + tsc --noEmit
make build     # one binary that serves the API and the SPA
make run       # build, then serve everything from :8080
make reseed    # wipe stored data and regenerate the demo dataset
```

### Configuration

| Flag | Env | Default | Purpose |
|---|---|---|---|
| `-addr` | `ADDR` | `:8080` | listen address |
| `-data` | `DATA_PATH` | `./data/guest-score.json` | snapshot path |
| `-static` | `STATIC_DIR` | *(empty)* | built SPA to serve; empty disables |
| `-reseed` | — | `false` | wipe and regenerate demo data at startup |

## Deployment

```bash
make build
./bin/guest-score -static ./frontend/dist
```

Or `docker build -t guest-score . && docker run -p 8080:8080 guest-score`. The
image is a distroless static binary with the SPA baked in — one container, one
port, one volume for the snapshot.

Because it is a single dependency-free binary reading `ADDR`, it deploys to
Render, Fly.io, Railway, or Cloud Run without modification.

## Testing

37 tests, race-clean. The scoring engine is at 95% statement coverage and the
API layer at 77%. The constitution mandates specific cases rather than a
coverage target: the empty and single-review cases, the perfect and floor cases,
decay across year boundaries, incident penalty stacking, purity, concurrent
submissions, and the full URL of edge cases in the spec.

Handler tests run against the real router through `net/http/httptest` with a
pinned clock, so scores in tests are deterministic to the decimal.

## Status

Authentication is not implemented — this iteration is a single-tenant demo, and
the API is shaped so a host identity can be threaded through later without
reshaping the domain. Review authenticity and abuse of the review system are
out of scope and noted in the spec. The scoring constants are defensible but
unvalidated against real data; they are centralised in one struct and published
over the API precisely so they can be tuned.

## Licence

MIT. See [LICENSE](LICENSE).