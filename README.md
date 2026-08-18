# Guest Score

[![CI](https://github.com/udaykishore-resu/guest-score/actions/workflows/ci.yml/badge.svg)](https://github.com/udaykishore-resu/guest-score/actions/workflows/ci.yml)
[![Release](https://github.com/udaykishore-resu/guest-score/actions/workflows/release.yml/badge.svg)](https://github.com/udaykishore-resu/guest-score/actions/workflows/release.yml)
![Go](https://img.shields.io/badge/Go-1.24-00ADD8?logo=go&logoColor=white)
![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-17-4169E1?logo=postgresql&logoColor=white)
![Redis](https://img.shields.io/badge/Redis-7-DC382D?logo=redis&logoColor=white)
![Elasticsearch](https://img.shields.io/badge/Elasticsearch-8-005571?logo=elasticsearch&logoColor=white)
![gRPC](https://img.shields.io/badge/gRPC-proto3-244C5A?logo=grpc&logoColor=white)
![GraphQL](https://img.shields.io/badge/GraphQL-E10098?logo=graphql&logoColor=white)
![MQTT](https://img.shields.io/badge/MQTT-3.1.1-660066?logo=mqtt&logoColor=white)
![Kubernetes](https://img.shields.io/badge/Kubernetes-Kustomize-326CE5?logo=kubernetes&logoColor=white)

A credit bureau for hotel guests. One portable file per person, opened with
whatever identity document their country issues, resolvable by any member hotel
in any country — so a guest's standing follows them across borders instead of
resetting at each one.

```bash
git clone https://github.com/udaykishore-resu/guest-score && cd guest-score
make dev
```

Open <http://localhost:5173>. It seeds a realistic dataset on first run: no
database to provision, no credentials, no signup. Every piece of infrastructure
this repo can use is optional — see [Running against the real stack](#running-against-the-real-stack).

---

## Contents

- [Why it exists](#why-it-exists)
- [Architecture](#architecture)
- [How a file crosses a border](#how-a-file-crosses-a-border)
- [How the score is computed](#how-the-score-is-computed)
- [What makes the score defensible](#what-makes-the-score-defensible)
- [Identity and privacy](#identity-and-privacy)
- [API](#api)
- [Running against the real stack](#running-against-the-real-stack)
- [Development](#development)
- [Deployment](#deployment)
- [Testing](#testing)
- [Known limitations](#known-limitations)

---

## Why it exists

A hotel checking in a stranger is making a commercial decision — what deposit to
take, whether to upgrade, whether to accept the booking at all — on almost no
information. Its own history with that guest is one property's worth, and a
guest who behaves badly simply books elsewhere next time.

Consumer credit solved the equivalent problem with a bureau: members report,
members query, and the file belongs to the person rather than to any one lender.
Guest Score is that shape applied to hospitality. Members file structured stay
records; any member can pull any guest's standing; the guest can see who has
been asking and dispute anything they think is wrong.

The part that makes it work is the **portable file**. A file opened in Mumbai on
an Aadhaar has to be reachable in Lisbon from a passport, or the bureau is just
a set of national silos and crossing a border erases the record.

## Architecture

[![Architecture](docs/diagrams/architecture.svg)](docs/diagrams/architecture.svg)

<sub>Download: [SVG](docs/diagrams/architecture.svg) · [PNG](docs/diagrams/architecture.png) · [Mermaid source](docs/diagrams/architecture.mmd)</sub>

Five external systems, and **every one of them is optional**. An unset
environment variable is not an error — it selects a fallback, and the boot log
says which:

| Unset                | Falls back to                              |
|----------------------|--------------------------------------------|
| `GS_POSTGRES_DSN`    | JSON `FileStore` at `GS_DATA_PATH`          |
| `GS_REDIS_ADDR`      | no-op cache — every read is a miss          |
| `GS_ELASTIC_URL`     | in-process substring search, labelled as such |
| `GS_MQTT_URL`        | event ingest disabled                       |
| `GS_SCORING_GRPC`    | scoring linked in-process, no network hop   |

```
level=INFO msg="guest-score starting" addr=:8090 store=postgres cache=redis:redis:6379
  search=elasticsearch:http://elasticsearch:9200 events=mqtt:tcp://mosquitto:1883
  scoring=grpc:guest-score-scoring:9090 graphql=enabled+graphiql
```

That line is the answer to "am I looking at real infrastructure or a fallback?",
and it is printed before anything can fail. [docs/PLATFORM.md](docs/PLATFORM.md)
explains each integration and why it is there.

## How a file crosses a border

[![Cross-border identity resolution](docs/diagrams/sequence-cross-border.svg)](docs/diagrams/sequence-cross-border.svg)

<sub>Download: [SVG](docs/diagrams/sequence-cross-border.svg) · [PNG](docs/diagrams/sequence-cross-border.png) · [Mermaid source](docs/diagrams/sequence-cross-border.mmd)</sub>

Six countries are registered today (IN, US, GB, AE, SG, DE), each with the
documents its authorities actually issue and the checksum each one carries —
Verhoeff for Aadhaar, Luhn for a US driver's licence, the NRIC algorithm for
Singapore. A document is validated locally before anything is sent anywhere: a
typo caught at the desk costs nothing, while a typo sent upstream costs a
request and writes a near-miss identity number into somebody's log.

Only some documents are **portable**. A domestic driving licence identifies its
holder at home and nowhere else, so a file opened on one carries a notice until
a passport is attached. That is the step that makes the bureau global in
practice rather than in principle.

## How the score is computed

[![Scoring pipeline](docs/diagrams/flow-scoring.svg)](docs/diagrams/flow-scoring.svg)

<sub>Download: [SVG](docs/diagrams/flow-scoring.svg) · [PNG](docs/diagrams/flow-scoring.png) · [Mermaid source](docs/diagrams/flow-scoring.mmd)</sub>

A 0–1000 scale on which a new guest opens at 500. Every constant is published at
`GET /api/scoring-model` and rendered at `/model` in the UI, so the model can be
checked from outside the binary.

| Tier      | From | Discount | Deposit | Notes                          |
|-----------|-----:|---------:|--------:|--------------------------------|
| Excellent |  800 |      20% |   0.25× |                                |
| Good      |  700 |      10% |    0.5× |                                |
| Fair      |  500 |        — |   1.0×  | standard terms                 |
| Poor      |    0 |        — |    2.0× | flagged; a human decides       |

The system never bans anyone. It surfaces a flag and leaves the decision, and
the appeal, to people.

## What makes the score defensible

Four properties, each with a test that fails if it regresses:

**It is pure.** `scoring.Compute` takes the evaluation instant as a parameter,
reads no clock, performs no I/O and mutates nothing. Same records in, same score
out — which is what lets a dispute review reproduce a score exactly as it stood
on a given day.

**Incidents always move it down.** The obvious `clamp(base − penalty + bonus)` is
wrong, and was a real bug here: applying both at once let ten commendations push
the raw total far above the ceiling, where the clamp silently absorbed a severe
damage incident and the score never moved. Lifting and saturating first, then
deducting, means a pile of commendations can carry a guest to the top of the
scale but can never buy immunity.

**The top of the scale still ranks.** A hard clamp put every exceptional guest on
exactly 1000.0. Above 900 the scale now saturates exponentially, approaching the
maximum without reaching it, so the best guests stay distinguishable and
continued good behaviour still pays. Tier thresholds all sit below the
compressed region, so a tier means exactly what the published model says.

**A disputed record is held out.** An open or upheld dispute excludes the record
from the maths entirely. Scoring a record the guest is actively contesting —
which the review may overturn — is the thing a dispute process exists to
prevent.

## Identity and privacy

**A document number is never stored.** Not in Postgres, not in Elasticsearch,
not in a log. What is stored is an HMAC-SHA256 keyed hash and the last four
characters: enough to resolve a file, enough for a desk agent to confirm they
scanned the right card, and not enough to reconstruct the document. That is what
makes holding an Aadhaar-derived value defensible under the Aadhaar Act at all.

The search index is `dynamic: strict`, so a field added upstream is rejected
rather than silently indexed, and a test walks the GraphQL schema to prove no
field can expose a hash.

Because only hashes and last-4 ever leave the domain layer, a deployment can
keep a small global hash index replicated worldwide while PII stays in a
regional database — which is how this satisfies data-residency rules without
giving up cross-border resolution.

`GS_IDENTITY_KEY` derives every hash. Rotating it orphans every stored document,
so it belongs in a KMS, not an environment variable. The service warns loudly
when it is unset and running on the development default.

## API

| Method | Path | Purpose |
|--------|------|---------|
| `GET`  | `/api/health` | per-dependency status and latency |
| `GET`  | `/api/scoring-model` | every constant the engine applies |
| `GET`  | `/api/guests` | directory, filterable by tier and incidents |
| `POST` | `/api/guests` | open a file directly |
| `GET`  | `/api/guests/{id}` | file plus full stay history |
| `GET`  | `/api/guests/{id}/score` | score alone |
| `GET`  | `/api/guests/{id}/inquiries` | who has pulled this file |
| `POST` | `/api/guests/{id}/documents` | attach a document; makes a file portable |
| `GET`  | `/api/reviews` · `POST` `/api/reviews` | stay records |
| `GET`  | `/api/stats` | portfolio aggregates |
| `GET`  | `/api/identity/countries` | accepted documents per country |
| `POST` | `/api/identity/resolve` | scan an ID — resolve or open a file |
| `GET`  | `/api/search` | fuzzy directory search; reports which engine answered |
| `POST` | `/api/admin/reindex` | rebuild the search index from the store |
| `POST` | `/graphql` · `GET` `/graphiql` | GraphQL, explorer in dev only |

A guest profile is five REST calls or one GraphQL query:

```graphql
{
  guest(id: "g_001") {
    name globalId portable
    score { composite tier discountPercent depositMultiplier modelVersion
            factors { kind description impact } }
    documents { label last4 portable }
    stays { memberName nights dispute { status countsTowardScore } }
    inquiries { memberName purpose at }
  }
}
```

The directory scores an entire page in one batched call, whether that is served
in-process or over gRPC. There is a test that fails if it ever regresses to N+1.

## Running against the real stack

```bash
cd ../dev-stack && make up      # Postgres, Redis, Elasticsearch, Mosquitto
cd ../guest-score && make dev-split
```

`make dev-split` runs the API against the gRPC scoring service, the way the
stack deploys it. Or set the variables yourself:

```bash
export GS_POSTGRES_DSN='postgres://guestscore:guestscore@localhost:5432/guestscore?sslmode=disable'
export GS_REDIS_ADDR=localhost:6379
export GS_ELASTIC_URL=http://localhost:9200
export GS_MQTT_URL=tcp://localhost:1883
export GS_SCORING_GRPC=localhost:9090
```

Watch a property file an incident and the score move:

```bash
go run ./cmd/propertysim -guest g_001 -type incident -severity moderate
mosquitto_sub -h localhost -t 'guestscore/_bureau/acks' -v
```

Send the same `-event-id` twice and the second is acked as a duplicate rather
than filed again — MQTT QoS 1 is at-least-once, so that is routine operation.

Full variable list: [`../dev-stack/.env.example`](../dev-stack/.env.example).

## Development

```
make help          list every target
make dev           API :8090 + SPA :5173, no infrastructure needed
make dev-split     API against the gRPC scoring service
make test          go test ./... -race -cover
make lint          go vet + golangci-lint + frontend typecheck
make proto         regenerate the gRPC stubs
make docker        build all three images
```

Layout:

```
backend/
  cmd/            server · scoringd · propertysim
  internal/
    domain/       entities, validation, identity documents and checksums
    scoring/      the pure engine — no I/O, no clock
    store/        Store interface, FileStore, postgres/ implementation
    api/          REST handlers, health, search
    graphqlapi/   schema and resolvers
    scoringsvc/   gRPC server, client, and the local/remote equivalence tests
    cache/        Cache interface, Redis, no-op
    search/       Index interface, Elasticsearch over net/http, no-op
    events/       MQTT ingest, deduplication, property simulator
    config/       every environment variable, in one place
  proto/          scoring.proto — the versioned model contract
frontend/         React 19 · Vite · TypeScript, hand-rolled SVG charts
docs/             PLATFORM.md and the diagrams above
```

## Deployment

Three images, all distroless and non-root:

```bash
docker build -t guest-score-api:dev     -f Dockerfile .
docker build -t guest-score-scoring:dev -f Dockerfile.scoring .
docker build -t guest-score-web:dev     -f Dockerfile.web .
```

Kubernetes manifests, a kind cluster and Compose for the whole platform live in
the sibling [`dev-stack`](../dev-stack) repository:

```bash
cd ../dev-stack
make kind-up && make kind-load && make k8s-apply
make smoke
```

Only Postgres is a critical dependency. `/api/health` returns `degraded` with a
200 when a non-critical dependency is down, because a degraded service is still
serving correct answers from a smaller feature set — taking it out of the load
balancer would turn a cache outage into a site outage.

## Testing

```bash
make test                    # unit and handler tests, race detector on
cd ../dev-stack && make up
make test-int                # adds the Postgres, Redis and Elasticsearch paths
```

CI additionally stands up Postgres, Redis, Elasticsearch and Mosquitto and
asserts the service really talks to all of them: that the append-only trigger on
the inquiry log rejects a `DELETE`, that a misspelled name still finds its file,
that a redelivered MQTT event is filed exactly once, and that Redis actually
receives keys. Those are the claims a reader cannot otherwise verify without
doing the work themselves.

## Known limitations

Stated rather than hidden:

- **`store.Store` takes no `context`.** The interface was written against an
  in-memory implementation. Postgres queries therefore run under the store's own
  timeout rather than the caller's deadline, so an abandoned request does not
  cancel its query. Widening the interface touches every caller and was not
  bundled into this change.
- **`ResolveByDocument` cannot report an error.** Same cause. A database failure
  is logged and reported as "no match", which under an outage means the desk
  opens a duplicate file — the wrong outcome, and the reason the fix above
  matters.
- **Document verification is a fixture.** Real verifiers (UIDAI, a state DMV, a
  passport office) need credentials, contracts and in most cases a legal basis
  to call. Every simulated result is labelled `"simulated"` so an agent is never
  misled into believing an authority confirmed something.
- **An unrated MQTT record uses a neutral rating of 4.** A mid-stay incident
  carries no assessment of overall conduct, so the record sits at the population
  prior. The residual bias is +0.1 on one dimension; the exact fix is a flag
  marking a record unrated, which is a domain change.
- **Not yet built** from the invention disclosure: evidence photo upload,
  automated guest notifications, and role-based access control. `Member` exists
  in the domain but is not yet wired through the API.

## Repository topics

The topics that describe this repository are kept in
[`scripts/set-topics.sh`](scripts/set-topics.sh) so the intended set is
reviewable in the diff, even though topics themselves live in GitHub metadata
rather than in the tree:

```bash
./scripts/set-topics.sh --print    # show the list
./scripts/set-topics.sh            # apply it (needs the gh CLI, authenticated)
```

## Licence

[MIT](LICENSE).
