# Feature Specification: Guest Reputation Scoring

**Feature Branch**: `001-guest-reputation`

**Created**: 2026-08-14

**Status**: Draft

**Input**: Short-term rental hosts need a way to assess an unknown guest's
reliability before accepting a booking, and a way to contribute what they learned
after the stay ends.

---

## Problem

A host accepting a booking request is making a decision with real downside — a
damaged property, a noise complaint that threatens their permit, a guest who
brings twelve people to a four-person listing — on the basis of almost no
information. Platform review systems are weak here: reviews are reciprocal, so
hosts under-report bad guests to avoid retaliation, and a guest's history is
scattered across platforms that do not talk to each other.

Guest Score is a shared reputation layer. Hosts contribute structured
post-stay assessments; any host can query a guest's aggregate score before
accepting. The score is explainable, decays over time so old behavior stops
dominating, and is honest about its own confidence when the evidence is thin.

---

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Screen a guest before accepting a booking (Priority: P1)

A host receives a booking request from a guest they have never hosted. Before
accepting, they look the guest up and see a single composite score, a letter
grade, a per-dimension breakdown, and a plain-language recommendation of whether
to accept, accept with conditions, or decline.

**Why this priority**: This is the moment of decision and the entire reason the
product exists. Everything else in the system exists to make this screen
trustworthy. Shipped alone, it is already a viable product for any host with
access to a seeded dataset.

**Independent Test**: Search for a guest by name, open their profile, and confirm
the composite score, grade, dimension bars, confidence indicator, and
recommendation all render from live API data.

**Acceptance Scenarios**:

1. **Given** a guest with eight completed stays and consistently high ratings,
   **When** a host opens their profile, **Then** the composite score is at or
   above 85, the grade is A, confidence reads "high", and the recommendation is
   "accept".
2. **Given** a guest with two stays, both rated well, **When** a host opens their
   profile, **Then** the score is visibly pulled toward the population mean and
   confidence reads "low", with copy explaining that limited history widens the
   uncertainty.
3. **Given** a guest with a verified property-damage incident in the last six
   months, **When** a host opens their profile, **Then** the incident appears in
   the factor list with its point penalty stated, and the recommendation is not
   "accept".
4. **Given** a guest with no reviews at all, **When** a host opens their profile,
   **Then** the system reports "unrated" rather than inventing a score, and
   recommends standard verification steps instead.

---

### User Story 2 — Submit a post-stay assessment (Priority: P1)

After a stay ends, the host rates the guest across five dimensions, optionally
flags incidents, and leaves a short written note. The guest's score updates
immediately and the host can see exactly how much their review moved it.

**Why this priority**: A reputation system with no inflow is a static dataset.
Contribution and consumption are the two halves of the same loop, and the loop
must close inside a demo for the product to be convincing.

**Independent Test**: Submit a review through the form and confirm the target
guest's score changes, the review appears in their history, and the delta is
reported back.

**Acceptance Scenarios**:

1. **Given** a completed stay, **When** the host submits ratings on all five
   dimensions, **Then** the review is stored and the guest's composite score is
   recomputed and returned in the same response.
2. **Given** a review form with a dimension rating of 0 or 6, **When** the host
   submits, **Then** the API rejects it with a field-level validation error and
   stores nothing.
3. **Given** a host submits a review for a guest they have already reviewed for
   the same stay, **When** the request is processed, **Then** it is rejected as a
   duplicate.
4. **Given** a review that flags a "noise complaint" incident, **When** it is
   saved, **Then** the guest's score reflects the incident penalty and the
   incident is listed as a distinct factor, separate from the dimension ratings.

---

### User Story 3 — Portfolio dashboard (Priority: P2)

A host with multiple properties sees their review activity at a glance: how many
guests they have rated, the distribution of scores across guests they have
hosted, their average ratings by dimension, and recent incidents across the
portfolio.

**Why this priority**: Turns a lookup utility into something a host opens
regularly. Valuable, but the product works without it.

**Independent Test**: Open the dashboard and confirm every statistic is computed
from stored reviews rather than hardcoded.

**Acceptance Scenarios**:

1. **Given** a host with reviews across several guests, **When** they open the
   dashboard, **Then** total reviews, average composite of guests hosted, and a
   score-band distribution are displayed and reconcile with the underlying data.
2. **Given** no reviews yet, **When** the dashboard loads, **Then** it shows an
   empty state rather than zeros presented as if they were measurements.

---

### User Story 4 — Guest directory with search and filter (Priority: P2)

Hosts browse and search the guest directory by name or email, filter by score
band and by whether the guest has incidents on record, and sort by score, review
count, or most recent stay.

**Why this priority**: Necessary for navigating a dataset of any real size, but
the P1 screening flow can be reached by direct link without it.

**Independent Test**: Apply each filter and sort independently and confirm the
result set changes correctly.

**Acceptance Scenarios**:

1. **Given** a partial name typed into search, **When** results return, **Then**
   only guests whose name or email contains that substring, case-insensitively,
   are listed.
2. **Given** the "has incidents" filter is active, **When** results return,
   **Then** every listed guest has at least one incident on record.

---

### Edge Cases

- A guest with zero reviews returns an explicit "unrated" state, never a
  fabricated default score.
- A guest whose reviews are all older than the decay horizon retains a score, but
  confidence degrades and the age of the evidence is surfaced.
- All five dimension ratings at the minimum plus multiple severe incidents cannot
  push the composite below zero; the score floors at zero.
- All ratings at maximum with zero incidents cannot exceed one hundred.
- Two reviews submitted for the same guest at the same instant must both persist;
  neither may be lost to a write race.
- Search input containing regex or SQL metacharacters is treated as a literal
  substring.
- A review referencing a guest ID that does not exist is rejected, not silently
  creating a guest.

---

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: System MUST compute a composite reputation score from 0 to 100 for
  any guest with at least one review.
- **FR-002**: System MUST return the state "unrated" for guests with no reviews,
  distinct from a score of zero.
- **FR-003**: System MUST weight the five assessment dimensions unequally,
  according to a published, inspectable weight table.
- **FR-004**: System MUST reduce the influence of older reviews relative to
  recent ones, on a continuous decay curve rather than a hard cutoff.
- **FR-005**: System MUST pull scores derived from few reviews toward the
  population mean, so that a single glowing review does not produce the same
  score as twenty of them.
- **FR-006**: System MUST apply additional penalties for flagged incidents,
  scaled by incident severity and reduced by the incident's age.
- **FR-007**: System MUST return, alongside every score, the per-dimension
  contributions, the weights used, a confidence level, and a human-readable list
  of contributing factors.
- **FR-008**: System MUST classify each score into a letter grade band and a
  booking recommendation.
- **FR-009**: System MUST validate that every dimension rating is an integer from
  1 to 5 and reject the entire submission if any is out of range.
- **FR-010**: System MUST reject a second review from the same host for the same
  stay.
- **FR-011**: System MUST support case-insensitive substring search over guest
  name and email, and filtering by score band and incident presence.
- **FR-012**: System MUST expose aggregate portfolio statistics computed from
  stored data.
- **FR-013**: System MUST persist data across process restarts.
- **FR-014**: System MUST seed a realistic demonstration dataset when started
  against empty storage.

### Key Entities

- **Guest** — a person who stays at properties. Has an identity (name, email,
  optional verification markers), a join date, and a derived reputation score.
  The score is never stored as a field; it is always computed from reviews.
- **Review** — one host's structured assessment of one guest for one stay.
  Carries five dimension ratings, zero or more incidents, a written comment, the
  stay dates, and the submission timestamp.
- **Incident** — a discrete negative event attached to a review, with a type
  (property damage, noise complaint, unauthorized guests, house rules violation,
  late checkout, payment issue) and a severity.
- **Score** — a computed, non-persisted value object: composite, grade, band,
  confidence, per-dimension breakdown, factor list, recommendation.

---

## Success Criteria *(mandatory)*

- **SC-001**: A host can go from opening the app to a reasoned accept/decline
  judgment on an unknown guest in under thirty seconds.
- **SC-002**: Every score returned by the API is accompanied by enough detail to
  reconstruct how it was reached, with no reference to external documentation.
- **SC-003**: Recomputing a score with unchanged inputs and a fixed evaluation
  time yields a bit-identical result.
- **SC-004**: The full stack runs from a fresh clone with two commands and no
  credentials, secrets, or provisioned services.
- **SC-005**: Score lookup for a guest with fifty reviews returns in under 50ms
  on commodity hardware.

---

## Assumptions

Recorded because the source repository was not reachable at specification time;
these are the interpretations this build proceeds on.

1. The domain is short-term rental hosting; "guest" means a person staying at a
   host's property.
2. Hosts are trusted contributors. Review authenticity, host identity
   verification, and abuse of the review system are out of scope for this
   iteration and are noted as future work.
3. Single-tenant demo posture: no authentication in this iteration. The API is
   designed so that a host identity can be threaded through later without
   reshaping the domain.
4. Scoring weights and decay constants are product decisions, chosen here to be
   defensible and legible, and are expected to be tuned against real data.

## Out of Scope

Authentication and authorization; guest-facing score disputes; multi-tenancy;
integration with Airbnb/VRBO APIs; notification delivery; payment handling;
GDPR erasure workflows and the audit trail they require.
