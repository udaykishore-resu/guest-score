# Feature Specification: Hotel Guest Standing & Discounts

**Feature Branch**: `001-guest-reputation`

**Created**: 2026-08-14

**Status**: Draft

**Input**: A hotel group needs a Guest Score that both rewards good guests with
a loyalty tier and discount, and flags risky ones to the front desk — computed
from recorded stays rather than a manually maintained list.

---

## Problem

A hotel group knows a great deal about how each guest actually behaves — how the
room was left, whether policy was followed, whether housekeeping or the night
manager had to get involved — and does almost nothing with it. The information
sits in individual staff memories and per-property notes. Two things are lost as
a result: the best guests are never rewarded for being easy to host, and the
handful who cause real problems arrive unflagged at the next property.

Guest Score turns recorded stays into a single standing. That standing does two
jobs at once. For the guest it is a loyalty tier — VIP, Premium, Regular — and a
discount they earn. For the front desk it is a risk signal, up to and including
a Watch List flag. Both come from the same evidence, and both are explainable
down to the individual point.

### Relationship to the previous implementation

This supersedes a frontend-only prototype that rendered a score ring, discount,
and category from hardcoded mock data with no backend. The tier thresholds and
discount percentages defined there (≥90 VIP/20%, ≥70 Premium/15%, ≥50
Regular/10%, otherwise Watch List/0%) are carried over unchanged, so a guest's
standing does not silently change meaning between versions. What is new is that
the number is now computed from real recorded stays, persisted, and explainable.

---

## User Scenarios & Testing *(mandatory)*

### User Story 1 — See a guest's standing and what it earns (Priority: P1)

A front-desk agent looks up an arriving guest and sees one composite score, the
loyalty tier it places them in, the discount that tier earns, a per-dimension
breakdown, and plain-language guidance on how to handle the check-in.

**Why this priority**: This is the screen the product exists for, and it serves
both audiences at once — the guest's reward and the hotel's risk signal come
from the same number. Shipped alone it is already useful against a seeded
dataset.

**Independent Test**: Open a guest profile and confirm score, tier, discount,
dimension bars, confidence, and handling guidance all render from live API data.

**Acceptance Scenarios**:

1. **Given** a guest with a long, spotless history and several commendations,
   **When** their profile is opened, **Then** the tier is VIP, the discount reads
   20%, confidence is "high", and handling is "VIP treatment".
2. **Given** a guest with two stays, both rated well, **When** their profile is
   opened, **Then** the score is visibly pulled toward the population mean and
   confidence reads "low", with copy explaining that limited history widens the
   uncertainty.
3. **Given** a guest with a verified property-damage incident in the last six
   months, **When** their profile is opened, **Then** the incident appears in the
   factor list with its point penalty stated, and handling is not "VIP
   treatment" regardless of the tier they hold.
4. **Given** a guest with no recorded stays, **When** their profile is opened,
   **Then** the system reports "Unrated" rather than inventing a score, and
   frames it as a neutral starting point rather than a negative signal.
5. **Given** any rated guest below the top tier, **When** their profile is
   opened, **Then** the next tier and the exact points remaining to reach it are
   shown.

---

### User Story 2 — Record a stay (Priority: P1)

After checkout, staff rate the guest across five dimensions, flag any incidents,
record any commendations, and leave a short note. The guest's standing updates
immediately and the person filing it sees exactly how much it moved, including
any tier change.

**Why this priority**: A reputation system with no inflow is a static dataset.
Contribution and consumption are the two halves of the same loop, and the loop
must close inside a demo for the product to be convincing.

**Independent Test**: Submit a stay through the form and confirm the guest's
score changes, the record appears in their history, and the delta is reported
back.

**Acceptance Scenarios**:

1. **Given** a completed stay, **When** staff submit ratings on all five
   dimensions, **Then** the record is stored and the guest's composite score is
   recomputed and returned in the same response.
2. **Given** a stay form with a dimension rating of 0 or 6, **When** staff
   submit, **Then** the API rejects it with a field-level validation error and
   stores nothing.
3. **Given** staff submit a second record for a stay already recorded, **When** the request is processed, **Then** it is rejected as a
   duplicate.
4. **Given** a stay record that flags a "noise complaint" incident, **When** it is
   saved, **Then** the score reflects the incident penalty and the incident is
   listed as a distinct factor, separate from the dimension ratings.
5. **Given** a stay record with a commendation, **When** it is saved, **Then** the
   score rises and the commendation appears as a distinct positive factor with
   its point value.

---

### User Story 3 — Portfolio dashboard (Priority: P2)

A manager sees activity across the group at a glance: how many stays have been
recorded, how guests distribute across loyalty tiers, the average discount being
earned, average ratings by dimension, and recent incidents.

**Why this priority**: Turns a lookup utility into something a manager opens
regularly. Valuable, but the product works without it.

**Independent Test**: Open the dashboard and confirm every statistic is computed
from stored stay records rather than hardcoded.

**Acceptance Scenarios**:

1. **Given** recorded stays across several guests, **When** the dashboard is
   opened, **Then** total stays, average composite, average discount earned, and
   the tier distribution are displayed and reconcile with the underlying data.
2. **Given** no stays recorded yet, **When** the dashboard loads, **Then** it shows an
   empty state rather than zeros presented as if they were measurements.

---

### User Story 4 — Guest directory with search and filter (Priority: P2)

Staff browse and search the guest directory by name or email, filter by loyalty
tier and by whether the guest has incidents on record, and sort by score, stay
count, or most recent stay.

**Why this priority**: Necessary for navigating a dataset of any real size, but
the P1 lookup can be reached by direct link without it.

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

- A guest with zero recorded stays returns an explicit "unrated" state, never a
  fabricated default score.
- Commendations cannot lift a score past the ceiling in a way that hides a
  penalty; an incident always reduces the final number.
- A score that displays as exactly a tier threshold is placed in that tier, not
  the one below it.
- A guest whose stays are all older than the decay horizon retains a score, but
  confidence degrades and the age of the evidence is surfaced.
- All five dimension ratings at the minimum plus multiple severe incidents cannot
  push the composite below zero; the score floors at zero.
- All ratings at maximum with zero incidents cannot exceed one hundred.
- Two stay records submitted for the same guest at the same instant must both persist;
  neither may be lost to a write race.
- Search input containing regex or SQL metacharacters is treated as a literal
  substring.
- A stay record referencing a guest ID that does not exist is rejected, not
  silently creating a guest.

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
- **FR-006a**: System MUST apply bonuses for recorded commendations, reduced by
  age, with per-event values smaller than the corresponding penalties.
- **FR-006b**: Commendations MUST NOT be able to mask an incident: a penalty
  must always reduce the final score, even for a guest already at the ceiling.
- **FR-007**: System MUST return, alongside every score, the per-dimension
  contributions, the weights used, a confidence level, and a human-readable list
  of contributing factors.
- **FR-008**: System MUST classify each score into a loyalty tier carrying a
  discount percentage, and separately into front-desk handling guidance. A
  recent severe incident MUST prevent "VIP treatment" handling regardless of the
  tier held.
- **FR-008a**: Tier thresholds and discounts MUST match the superseded
  implementation exactly: ≥90 VIP/20%, ≥70 Premium/15%, ≥50 Regular/10%,
  otherwise Watch List/0%.
- **FR-008b**: System MUST report the next tier and the points remaining to
  reach it for any guest below the top tier.
- **FR-008c**: The tier MUST be derived from the same rounded score shown to the
  user, so a guest displaying 90.0 is never placed below the 90 threshold.
- **FR-009**: System MUST validate that every dimension rating is an integer from
  1 to 5 and reject the entire submission if any is out of range.
- **FR-010**: System MUST reject a second review from the same host for the same
  stay.
- **FR-011**: System MUST support case-insensitive substring search over guest
  name and email, and filtering by loyalty tier and incident presence.
- **FR-012**: System MUST expose aggregate portfolio statistics computed from
  stored data.
- **FR-013**: System MUST persist data across process restarts.
- **FR-014**: System MUST seed a realistic demonstration dataset when started
  against empty storage.

### Key Entities

- **Guest** — a person who stays at the group's properties. Has an identity
  (name, email, optional verification markers), a join date, and a derived
  standing. The standing is never stored as a field; it is always computed from
  stay records.
- **Stay record** — one structured staff assessment of one guest for one stay.
  Carries five dimension ratings, zero or more incidents, zero or more
  commendations, a written note, the stay dates, and the submission timestamp.
- **Incident** — a discrete negative event attached to a stay record, with a type
  (property damage, noise complaint, unauthorized occupants, policy violation,
  late checkout, payment issue) and a severity.
- **Commendation** — a discrete positive event attached to a stay record
  (exceptional room care, staff commendation, repeat stay, accommodating,
  referral). The upward counterpart to an incident.
- **Score** — a computed, non-persisted value object: composite, tier, discount,
  points to next tier, confidence, per-dimension breakdown, factor list,
  handling guidance.

---

## Success Criteria *(mandatory)*

- **SC-001**: An agent can go from opening the app to knowing a guest's tier,
  discount, and any handling flag in under thirty seconds.
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

Revised after the superseded implementation became readable. The first draft of
this spec assumed short-term rentals and framed the product as booking
screening; both were wrong, and the tier/discount model below replaces them.

1. The domain is hotel hospitality; "guest" means a person staying at one of the
   group's properties, and stay records are filed by staff.
2. Staff are trusted contributors. Record authenticity and abuse of the scoring
   system are out of scope for this iteration and noted as future work.
3. Single-tenant demo posture: no authentication in this iteration. The API is
   designed so that a host identity can be threaded through later without
   reshaping the domain.
4. Scoring weights and decay constants are product decisions, chosen here to be
   defensible and legible, and are expected to be tuned against real data.

## Out of Scope

Authentication and authorization; guest-facing score disputes; multi-tenancy;
PMS integration; applying the discount at booking time; notification delivery;
payment handling; GDPR erasure workflows and the audit trail they require.
