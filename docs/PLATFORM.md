# The platform layer

Guest Score now speaks to PostgreSQL, Redis, Elasticsearch, MQTT and gRPC, and
exposes GraphQL alongside the REST API. This document explains what each one is
for, how to turn it on, and — the part that matters most — what happens when it
is off.

## The governing rule: everything is optional

There is no configuration you must supply. With an empty environment the
service starts on the JSON file store, computes scores in-process, caches
nothing, searches by substring and ingests no events. That is the same demo this
repository has always run, and it is what keeps `go test ./...` container-free.

| Unset environment variable | What you get instead                              |
|----------------------------|---------------------------------------------------|
| `GS_POSTGRES_DSN`          | the JSON `FileStore` at `GS_DATA_PATH`             |
| `GS_REDIS_ADDR`            | a no-op cache: every read is a miss                |
| `GS_ELASTIC_URL`           | in-process substring search, labelled as such      |
| `GS_MQTT_URL`              | event ingest disabled                              |
| `GS_SCORING_GRPC`          | scoring linked in-process, no network hop          |
| `GS_GRAPHQL=false`         | REST only                                          |

The boot log states how every one resolved, in one line:

```
level=INFO msg="guest-score starting" addr=:8090 store=postgres cache=redis:redis:6379
  search=elasticsearch:http://elasticsearch:9200 events=mqtt:tcp://mosquitto:1883
  scoring=grpc:guest-score-scoring:9090 graphql=enabled+graphiql
```

If you are ever unsure whether you are looking at real infrastructure or a
fallback, that line is the answer.

## PostgreSQL — the system of record

The `FileStore` is correct and, for one process, fast. What it cannot do is hold
its invariants across two processes: "one identity document belongs to exactly
one file" is enforced there by a Go map behind a mutex, and a second replica
makes that guard meaningless. Two front desks scanning the same passport at the
same instant then open two files for one person — the specific failure a bureau
cannot tolerate, because the guest's history splits and the bad half can be
outrun.

So three invariants moved into the schema:

| Invariant                                   | Constraint                       |
|---------------------------------------------|----------------------------------|
| one document, one file                       | `PRIMARY KEY (hash)`             |
| one member reviews one stay at most once     | `UNIQUE (lower(host_id), lower(stay_id))` |
| one email opens at most one file             | `UNIQUE (lower(email))`          |

Two further schema decisions worth knowing:

- **Ratings are five columns with `CHECK (BETWEEN 1 AND 5)`**, not a JSONB blob.
  The rule is already enforced in `domain.Ratings.Validate`; duplicating it is
  deliberate, because a bad rating that reaches the table silently changes
  *everyone's* score through the population prior, and afterwards there is no
  way to tell which rows were wrong.
- **The inquiry log is append-only**, enforced by a trigger that rejects `UPDATE`
  and `DELETE`. A guest is entitled to know who pulled their file, which makes
  that table evidence, and evidence that can be quietly amended is not evidence.
  A trigger rather than `REVOKE` because the application role owns the table and
  an owner can grant its own privileges back.

Migrations are embedded, applied on boot under a `pg_advisory_lock` so
simultaneous replicas cannot race, and checksummed — editing an applied
migration is refused rather than silently skipped.

**Known wart, not hidden:** `store.Store`'s methods take no `context`, because
the interface was written against an in-memory implementation. Every query
therefore runs under the store's own timeout rather than the caller's request
deadline, so an abandoned HTTP request does not cancel its query. Widening the
interface is the right fix and touches every caller; it was deliberately not
bundled into this change.

## Redis — caching the derivation, not the data

Scoring is pure and fast, but a directory page recomputes it for every guest
from their full history, and `/api/stats` does it for the whole population on
every request. That read amplification is what is cached — the computed `Score`,
not the reviews.

Invalidation is deliberately blunt: a write about one guest drops that guest's
score plus every cached directory page, search page and the stats blob. Working
out which cached pages contain a given guest would cost more than recomputing
them, and over-invalidating costs a recompute while under-invalidating serves a
wrong score.

A failing Redis degrades to a miss, never to an error. A failing *invalidation*
is logged at error level, because that one leaves a stale score visible.

## Elasticsearch — finding a file that was keyed by another desk

The substring match is kept as the fallback but cannot do what this directory
needs. Files opened in Mumbai and pulled in Lisbon have names keyed by different
desks, in different scripts, with different transliterations and ordinary typos.
"Muller" must find "Müller" and "Rohan Mehta" must find "Rohit Mehtaa", or the
agent concludes there is no file and opens a duplicate.

The index therefore folds diacritics and matches names with `fuzziness: AUTO`,
while boosting an exact hit so a correctly typed name is never outranked by a
fuzzy hit on someone else. A `global_id` is matched as an exact term with a
large boost and **never fuzzily** — fuzzy-matching an identifier resolves to the
wrong person's file, which is worse than finding nothing.

Two things this index never contains: **document numbers, and the hashes derived
from them.** The mapping is `dynamic: strict`, so a field added upstream is
rejected rather than silently indexed, and there is a test that fails if a hash
or a last-4 ever appears in an indexed document.

Search responses always report which engine answered. That is operational, not
decorative: with Elasticsearch, "no results" means no file plausibly matches;
without it, only that no name contains that exact substring.

There is no `go-elasticsearch` dependency. This makes four calls, and the
official client is a generated surface two orders of magnitude larger. See the
comment at the top of `internal/search/elastic.go`.

## MQTT — properties reporting from unreliable networks

Publishers are property-side systems on hotel networks: a PMS, a front-desk
terminal, a housekeeping tablet. They are the wrong place to implement retry.
MQTT gives them a session with QoS 1, so an incident filed while the uplink is
down is queued and delivered when it returns. A retained last-will on the status
topic also makes "this property stopped reporting" observable, which an HTTP
endpoint cannot express at all — silence and health look identical.

```
guestscore/{property_id}/events    properties publish here
guestscore/{property_id}/status    retained: "online" / "offline"
guestscore/_bureau/acks            the bureau publishes an outcome per event
```

QoS 1 is at-least-once, so **every event carries an `event_id` and is
deduplicated**. Without that, a broker reconnect files the same incident twice
and the guest is penalised twice for one event. With Postgres the dedup table is
durable; without it, an in-memory deduper is used and the boot log warns that a
redelivery across a restart will be applied twice.

Permanent rejections are acked with a reason so the publisher can fix them.
Transient failures are deliberately *not* acked, so the broker redelivers.

**Unrated records.** A mid-stay incident carries no assessment of overall
conduct, so an omitted rating becomes 4 — the nearest integer to the model's
3.9 population prior — making the record score-neutral on quality and moving the
score only by the incident penalty. The residual bias is +0.1 on one dimension,
damped further by shrinkage. The exact fix is a flag marking a record unrated so
the quality stage skips it, which is a domain change touching the scoring engine
and its tests, and so is not bundled into the transport layer.

Try it:

```sh
go run ./cmd/propertysim -guest g_001 -type incident -severity moderate
go run ./cmd/propertysim -guest g_001 -type incident -event-id evt_same -repeat 2   # deduplication
mosquitto_sub -h localhost -t 'guestscore/_bureau/acks' -v
```

## gRPC — the model as a separately deployable thing

Scoring is a pure function, so a network hop buys nothing but latency. The
boundary is regulatory rather than architectural: the model is the part that
gets audited, and someone will eventually ask which version produced a score and
whether it can be reproduced. A separate service with a versioned contract makes
"the model changed on Tuesday" a deployable, reviewable fact.

The API falls back to computing locally when the service is unreachable. That is
a considered trade: the score is a deterministic function of records the API
already holds, and failing a check-in because a sidecar is restarting would be a
self-inflicted outage. The cost is that a model deployed only to the scoring
service is bypassed during an outage — which is why fallbacks are counted,
logged, and surfaced in `/api/health`, and why every score carries a
`model_version`.

`internal/scoringsvc/scoringsvc_test.go` runs the real client against the real
server over an in-memory listener and asserts the two paths produce identical
`Score` values. If the wire projection ever loses a field, that test fails
rather than the discrepancy showing up as an unreproducible number.

## GraphQL — one request for a screen

The guest profile needs identity, score, factor breakdown, documents, stays and
the inquiry log: five REST calls the client stitches together, over hotel wifi.
GraphQL lets the screen state its whole requirement once.

The directory resolver scores the entire page in a single `Batch` call before any
field resolver runs, so a page costs one scoring call regardless of size —
locally or over gRPC. There is a test that fails if that ever regresses into
N+1.

GraphiQL is on by default in development and must be off elsewhere: it is an
unauthenticated introspection console. `GS_GRAPHIQL=false`.

## Health

`/api/health` probes every configured dependency concurrently and reports
per-dependency latency:

```json
{
  "status": "degraded",
  "checks": {
    "postgres":      {"ok": true,  "latency_ms": 1.4, "critical": true},
    "redis":         {"ok": true,  "latency_ms": 0.3, "critical": false},
    "elasticsearch": {"ok": true,  "latency_ms": 7.1, "critical": false},
    "scoring-grpc":  {"ok": false, "latency_ms": 3.0, "critical": false,
                      "error": "connection refused"}
  }
}
```

Only Postgres is critical. `degraded` returns 200, because a degraded service is
still serving correct answers from a smaller feature set — taking it out of the
load balancer would turn a cache outage into a site outage. Only a critical
failure returns 503.

## Running against the real stack

```sh
cd ../dev-stack && make up        # Postgres, Redis, Elasticsearch, Mosquitto
cd ../guest-score && make dev-split
```

Or export the variables yourself:

```sh
export GS_POSTGRES_DSN='postgres://guestscore:guestscore@localhost:5432/guestscore?sslmode=disable'
export GS_REDIS_ADDR=localhost:6379
export GS_ELASTIC_URL=http://localhost:9200
export GS_MQTT_URL=tcp://localhost:1883
export GS_SCORING_GRPC=localhost:9090
```

Full variable list: `../dev-stack/.env.example`.
