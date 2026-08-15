# Guest Score Constitution

**Version**: 1.0.0
**Ratified**: 2026-08-14

The non-negotiable engineering principles for this project. Every spec, plan, and
implementation task is checked against this document. A plan that violates a
principle must either be revised or must record an explicit, justified exception
in its Complexity Tracking section.

---

## Principle I — Spec Before Code

No implementation work begins without a written specification that states user
value in plain language and defines acceptance scenarios in Given/When/Then form.
Specs describe *what* and *why*; plans describe *how*. A spec that names a
library, a framework, or a database schema has leaked implementation detail and
must be rewritten.

## Principle II — Zero Runtime Dependencies in the Backend

The Go backend compiles and runs using only the standard library. No third-party
modules in `go.mod`.

Rationale: this project must build in air-gapped and proxy-restricted
environments, deploy as a single static binary with no CGO, and survive supply
chain review without a dependency audit. The constraint is a feature, not a
limitation — it forces explicit, readable code at the boundaries.

Consequence: persistence, routing, JSON handling, and testing all use stdlib
primitives. Storage sits behind a `Store` interface so a SQL-backed
implementation can be added later without touching the domain or API layers.

## Principle III — The Scoring Engine Is Pure

Score computation is a pure function of its inputs: given the same reviews and
the same evaluation time, it returns the same score. It performs no I/O, reads no
clocks, and mutates no state. Time is injected as a parameter.

Rationale: reputation scores affect real decisions about real people. A score
must be reproducible, auditable, and explainable. Purity is what makes it
testable to the decimal place.

## Principle IV — Every Score Is Explainable

The API never returns a bare number. Every score is accompanied by its component
breakdown, the weights applied, the confidence level, and a human-readable list
of the factors that moved it. A user who asks "why is my score 68?" gets a real
answer from the API itself, not from a support ticket.

## Principle V — Test the Math, Test the Edges

The scoring engine carries table-driven unit tests covering: the empty case, the
single-review case, the perfect-score case, the floor case, time decay across
year boundaries, and incident penalty stacking. HTTP handlers are tested through
`net/http/httptest` against the real router. Coverage is not a target; the listed
cases are mandatory.

## Principle VI — Demoable at Every Commit

`make dev` starts the full stack. The server seeds a realistic dataset on first
run. There is no step in any README that reads "obtain credentials" or "provision
a database" before a developer can see the product work.

---

## Governance

Amendments require a version bump to this document and a note in the changelog
of the affected specs. Principles II and III are load-bearing for the project's
deployment story and testability respectively; amending either requires
rewriting the affected plans, not just the code.
