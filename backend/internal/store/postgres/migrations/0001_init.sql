-- Guest Score: initial bureau schema.
--
-- Three invariants that the Go code currently holds in a map behind an RWMutex
-- are moved into the database here, because a mutex only protects one process
-- and this service is meant to run more than one:
--
--   1. one identity document belongs to exactly one file  -> PRIMARY KEY (hash)
--   2. one member reviews one stay at most once           -> UNIQUE (host_id, stay_id)
--   3. one email opens at most one file                   -> UNIQUE (lower(email))
--
-- Without these as constraints, two front desks scanning the same passport at
-- the same instant can open two files for one person, which is the exact
-- failure that destroys a bureau: the guest's history splits and the bad half
-- can be outrun.

CREATE TABLE IF NOT EXISTS guests (
    id          TEXT        PRIMARY KEY,
    global_id   TEXT        UNIQUE,
    name        TEXT        NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 120),
    email       TEXT        NOT NULL,
    phone       TEXT        NOT NULL DEFAULT '',
    city        TEXT        NOT NULL DEFAULT '',
    nationality TEXT        NOT NULL DEFAULT '',
    verified    BOOLEAN     NOT NULL DEFAULT FALSE,
    joined_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    avatar_seed TEXT        NOT NULL DEFAULT ''
);

-- Case-insensitive uniqueness without the citext extension, which is not
-- available on every managed Postgres.
CREATE UNIQUE INDEX IF NOT EXISTS guests_email_lower_key ON guests (lower(email));

-- Identity documents.
--
-- The number itself is never stored, in any column, in any form. What is stored
-- is an HMAC-SHA256 keyed hash and the last four characters, which is enough to
-- resolve a file and enough for a desk agent to confirm they scanned the right
-- card, and not enough to reconstruct the document. That is what makes holding
-- an Aadhaar-derived value defensible under the Aadhaar Act at all; see
-- internal/domain/identity.go.
CREATE TABLE IF NOT EXISTS identity_documents (
    hash        TEXT        PRIMARY KEY,
    guest_id    TEXT        NOT NULL REFERENCES guests (id) ON DELETE CASCADE,
    country     TEXT        NOT NULL,
    doc_type    TEXT        NOT NULL,
    last4       TEXT        NOT NULL,
    verified    BOOLEAN     NOT NULL DEFAULT FALSE,
    authority   TEXT        NOT NULL DEFAULT '',
    added_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    verified_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS identity_documents_guest_idx ON identity_documents (guest_id);

-- Stay records.
--
-- Ratings are five columns with CHECK constraints rather than one JSONB blob.
-- The 1-5 rule is already enforced in domain.Ratings.Validate; duplicating it
-- here is deliberate defence in depth, because a bad rating that reaches the
-- table silently changes everyone's score through the population prior, and
-- there is no way to tell afterwards which rows were wrong.
--
-- Incidents and commendations stay as JSONB: they are value objects read as a
-- unit with their review and never filtered on individually in the hot path.
-- If cross-guest incident analytics ever matter, they normalise into their own
-- tables and the GIN indexes below come out.
CREATE TABLE IF NOT EXISTS reviews (
    id            TEXT        PRIMARY KEY,
    guest_id      TEXT        NOT NULL REFERENCES guests (id) ON DELETE CASCADE,
    host_id       TEXT        NOT NULL,
    host_name     TEXT        NOT NULL DEFAULT '',
    property_id   TEXT        NOT NULL DEFAULT '',
    property_name TEXT        NOT NULL DEFAULT '',
    member_id     TEXT        NOT NULL DEFAULT '',
    member_name   TEXT        NOT NULL DEFAULT '',
    stay_id       TEXT        NOT NULL,
    room_number   TEXT        NOT NULL DEFAULT '',

    house_rules    SMALLINT   NOT NULL CHECK (house_rules    BETWEEN 1 AND 5),
    property_care  SMALLINT   NOT NULL CHECK (property_care  BETWEEN 1 AND 5),
    communication  SMALLINT   NOT NULL CHECK (communication  BETWEEN 1 AND 5),
    noise          SMALLINT   NOT NULL CHECK (noise          BETWEEN 1 AND 5),
    accuracy       SMALLINT   NOT NULL CHECK (accuracy       BETWEEN 1 AND 5),

    incidents     JSONB       NOT NULL DEFAULT '[]'::jsonb,
    commendations JSONB       NOT NULL DEFAULT '[]'::jsonb,
    comment       TEXT        NOT NULL DEFAULT '' CHECK (length(comment) <= 2000),

    check_in      TIMESTAMPTZ,
    check_out     TIMESTAMPTZ,
    submitted_at  TIMESTAMPTZ NOT NULL DEFAULT now(),

    dispute_status      TEXT NOT NULL DEFAULT ''
        CHECK (dispute_status IN ('', 'open', 'upheld', 'rejected')),
    dispute_reason      TEXT NOT NULL DEFAULT '',
    dispute_resolution  TEXT NOT NULL DEFAULT '',
    dispute_raised_at   TIMESTAMPTZ,
    dispute_resolved_at TIMESTAMPTZ,

    CONSTRAINT reviews_stay_dates CHECK (
        check_in IS NULL OR check_out IS NULL OR check_out >= check_in
    )
);

-- FR-010: one member, one review, one stay.
CREATE UNIQUE INDEX IF NOT EXISTS reviews_host_stay_key
    ON reviews (lower(host_id), lower(stay_id));

CREATE INDEX IF NOT EXISTS reviews_guest_submitted_idx
    ON reviews (guest_id, submitted_at DESC);

CREATE INDEX IF NOT EXISTS reviews_incidents_gin ON reviews USING gin (incidents);

-- Inquiry log: append-only by construction.
--
-- The guest is entitled to know who pulled their file, which makes this table
-- evidence. Evidence that can be quietly amended is not evidence, so a trigger
-- below rejects UPDATE and DELETE outright.
--
-- A trigger rather than REVOKE because the application role owns these tables,
-- and an owner can grant its own privileges back. The trigger binds regardless
-- of who is connected, including a human with psql open.
CREATE TABLE IF NOT EXISTS inquiries (
    id          BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    guest_id    TEXT        NOT NULL REFERENCES guests (id) ON DELETE CASCADE,
    member_id   TEXT        NOT NULL,
    member_name TEXT        NOT NULL DEFAULT '',
    purpose     TEXT        NOT NULL
        CHECK (purpose IN ('check_in', 'booking', 'manual_review')),
    at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS inquiries_guest_at_idx ON inquiries (guest_id, at DESC);

-- Event deduplication for the MQTT ingest.
--
-- MQTT QoS 1 is at-least-once, so a redelivery after a broker restart is normal
-- operation, not an anomaly. Without this table a redelivered incident is filed
-- twice and the guest is penalised twice for one event.
CREATE TABLE IF NOT EXISTS processed_events (
    event_id    TEXT        PRIMARY KEY,
    property_id TEXT        NOT NULL DEFAULT '',
    kind        TEXT        NOT NULL DEFAULT '',
    result      TEXT        NOT NULL DEFAULT '',
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS processed_events_at_idx ON processed_events (processed_at DESC);

-- Append-only enforcement for the inquiry log.
--
-- The guest can ask who pulled their file, and that answer has to be one nobody
-- could have edited afterwards. A trigger binds regardless of role, so it also
-- catches a well-meaning operator with psql open.
CREATE OR REPLACE FUNCTION inquiries_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION
        'inquiries is append-only: % is not permitted. The inquiry log is the record shown to guests under access-transparency obligations.',
        TG_OP;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS inquiries_no_mutate ON inquiries;
CREATE TRIGGER inquiries_no_mutate
    BEFORE UPDATE OR DELETE ON inquiries
    FOR EACH ROW EXECUTE FUNCTION inquiries_append_only();
