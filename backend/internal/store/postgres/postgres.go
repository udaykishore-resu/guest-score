// Package postgres implements store.Store on PostgreSQL.
//
// The FileStore it replaces is correct, and for one process it is arguably
// better: it holds everything in memory behind an RWMutex, so a read never
// touches a disk. What it cannot do is hold its invariants across two
// processes. "One identity document belongs to exactly one file" is enforced
// there by a Go map guarded by a mutex; run a second replica and the guard is
// gone, and two front desks scanning the same passport at the same instant open
// two files for one person. That is the specific failure a bureau cannot
// tolerate, because the guest's history splits and the bad half can be outrun.
//
// So the three invariants live in the schema now — see migrations/0001_init.sql
// — and this file's job is mostly to translate constraint violations back into
// the sentinel errors the API layer already maps onto status codes.
//
// One wart, called out rather than hidden: store.Store's methods take no
// context, because the interface was written against an in-memory
// implementation where none was needed. Every query here therefore runs under a
// timeout derived from the store's base context rather than the caller's
// request context, which means an abandoned HTTP request does not cancel its
// query. Widening the interface to carry a context is the right fix and touches
// every caller; it is deliberately not bundled into this change.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
	"github.com/udaykishore-resu/guest-score/backend/internal/store"
)

// queryTimeout bounds every statement. See the package comment for why it is
// not the caller's deadline.
const queryTimeout = 5 * time.Second

// Store is the PostgreSQL implementation of store.Store.
type Store struct {
	pool *pgxpool.Pool
	log  *slog.Logger

	base   context.Context
	cancel context.CancelFunc

	seq atomic.Uint64
}

// Options configures the store.
type Options struct {
	DSN      string
	MaxConns int32
	Migrate  bool
	Log      *slog.Logger
}

// Open connects, optionally migrates, and verifies the connection.
func Open(ctx context.Context, opts Options) (*Store, error) {
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.MaxConns <= 0 {
		opts.MaxConns = 10
	}

	cfg, err := pgxpool.ParseConfig(opts.DSN)
	if err != nil {
		return nil, fmt.Errorf("parsing GS_POSTGRES_DSN: %w", err)
	}
	cfg.MaxConns = opts.MaxConns
	cfg.MinConns = 1
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 15 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	// A connection that cannot be established in five seconds is not going to
	// help this request, and holding the caller longer than that turns a
	// database blip into a stack of timed-out clients.
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connecting to postgres: %w", err)
	}

	pingCtx, cancelPing := context.WithTimeout(ctx, 10*time.Second)
	defer cancelPing()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres unreachable: %w", err)
	}

	if opts.Migrate {
		migCtx, cancelMig := context.WithTimeout(ctx, 2*time.Minute)
		defer cancelMig()
		if err := Migrate(migCtx, pool, opts.Log); err != nil {
			pool.Close()
			return nil, err
		}
	}

	base, cancel := context.WithCancel(context.WithoutCancel(ctx))
	return &Store{pool: pool, log: opts.Log, base: base, cancel: cancel}, nil
}

func (s *Store) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(s.base, queryTimeout)
}

// Pool exposes the connection pool for the health check and for packages that
// legitimately need their own tables in the same database, such as the MQTT
// ingest's deduplication log.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Ping reports database reachability for /api/health.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) Close() error {
	s.cancel()
	s.pool.Close()
	return nil
}

// --- error translation -------------------------------------------------------

// pgErr maps PostgreSQL constraint violations onto the store's sentinels, with
// a message that names the actual conflict rather than the index.
//
// This mapping is the whole reason the handlers did not need to change: a
// duplicate passport already produced a 409 when the FileStore's map caught it,
// and it produces the same 409 now that a unique index does.
func pgErr(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, store.ErrNotFound)
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "23505": // unique_violation
			switch pe.ConstraintName {
			case "identity_documents_pkey":
				return fmt.Errorf("this document is already on another profile: %w", store.ErrDuplicate)
			case "guests_email_lower_key":
				return fmt.Errorf("email already registered: %w", store.ErrDuplicate)
			case "reviews_host_stay_key":
				return fmt.Errorf("this member has already reviewed this stay: %w", store.ErrDuplicate)
			case "guests_pkey", "guests_global_id_key":
				return fmt.Errorf("guest already exists: %w", store.ErrDuplicate)
			}
			return fmt.Errorf("%s: %w", op, store.ErrDuplicate)
		case "23503": // foreign_key_violation
			// The only foreign key that can fail here points at guests, and the
			// spec is explicit that a review must not conjure a guest into
			// existence.
			return fmt.Errorf("%s: guest does not exist: %w", op, store.ErrNotFound)
		case "23514": // check_violation
			return fmt.Errorf("%s: value rejected by constraint %s", op, pe.ConstraintName)
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}

func (s *Store) nextID(prefix string) string {
	// Same shape as the FileStore's identifiers so a snapshot migrated across
	// stays legible, with nanosecond precision because two replicas generate
	// these independently and only the primary key would catch a collision.
	n := s.seq.Add(1)
	return fmt.Sprintf("%s_%d_%03d", prefix, time.Now().UTC().UnixNano()%1_000_000_000, n%1000)
}

// --- guests ------------------------------------------------------------------

const guestColumns = `g.id, g.global_id, g.name, g.email, g.phone, g.city,
	g.nationality, g.verified, g.joined_at, g.avatar_seed`

// scanGuest reads one guest row. Documents are attached separately.
func scanGuest(row pgx.Row) (domain.Guest, error) {
	var g domain.Guest
	var globalID, nationality *string
	err := row.Scan(&g.ID, &globalID, &g.Name, &g.Email, &g.Phone, &g.City,
		&nationality, &g.Verified, &g.JoinedAt, &g.AvatarSeed)
	if err != nil {
		return domain.Guest{}, err
	}
	if globalID != nil {
		g.GlobalID = domain.GlobalID(*globalID)
	}
	if nationality != nil {
		g.Nationality = domain.Country(*nationality)
	}
	g.Documents = []domain.IdentityDocument{}
	return g, nil
}

// ListGuests returns every guest with their documents attached.
//
// Documents are fetched in a second query and joined in memory rather than with
// a LEFT JOIN, because a join would multiply each guest row by its document
// count and the deduplication would cost more than the extra round trip. Two
// queries, not N+1.
func (s *Store) ListGuests() ([]domain.Guest, error) {
	ctx, cancel := s.ctx()
	defer cancel()

	rows, err := s.pool.Query(ctx, `SELECT `+guestColumns+` FROM guests g ORDER BY g.name, g.id`)
	if err != nil {
		return nil, pgErr("listing guests", err)
	}
	defer rows.Close()

	guests := []domain.Guest{}
	index := map[string]int{}
	for rows.Next() {
		g, err := scanGuest(rows)
		if err != nil {
			return nil, pgErr("listing guests", err)
		}
		index[g.ID] = len(guests)
		guests = append(guests, g)
	}
	if err := rows.Err(); err != nil {
		return nil, pgErr("listing guests", err)
	}
	if len(guests) == 0 {
		return guests, nil
	}

	docRows, err := s.pool.Query(ctx, `
		SELECT guest_id, country, doc_type, hash, last4, verified, authority, added_at, verified_at
		FROM identity_documents ORDER BY added_at, hash`)
	if err != nil {
		return nil, pgErr("listing documents", err)
	}
	defer docRows.Close()
	for docRows.Next() {
		var guestID string
		d, err := scanDocument(docRows, &guestID)
		if err != nil {
			return nil, pgErr("listing documents", err)
		}
		if i, ok := index[guestID]; ok {
			guests[i].Documents = append(guests[i].Documents, d)
		}
	}
	return guests, pgErr("listing documents", docRows.Err())
}

func scanDocument(row pgx.Row, guestID *string) (domain.IdentityDocument, error) {
	var d domain.IdentityDocument
	var country, docType string
	err := row.Scan(guestID, &country, &docType, &d.Hash, &d.Last4,
		&d.Verified, &d.Authority, &d.AddedAt, &d.VerifiedAt)
	if err != nil {
		return domain.IdentityDocument{}, err
	}
	d.Country = domain.Country(country)
	d.Type = domain.DocumentType(docType)
	return d, nil
}

func (s *Store) GetGuest(id string) (domain.Guest, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	return s.getGuest(ctx, id)
}

func (s *Store) getGuest(ctx context.Context, id string) (domain.Guest, error) {
	g, err := scanGuest(s.pool.QueryRow(ctx, `SELECT `+guestColumns+` FROM guests g WHERE g.id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Guest{}, fmt.Errorf("guest %q: %w", id, store.ErrNotFound)
		}
		return domain.Guest{}, pgErr("getting guest", err)
	}
	docs, err := s.documentsFor(ctx, id)
	if err != nil {
		return domain.Guest{}, err
	}
	g.Documents = docs
	return g, nil
}

func (s *Store) documentsFor(ctx context.Context, guestID string) ([]domain.IdentityDocument, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT guest_id, country, doc_type, hash, last4, verified, authority, added_at, verified_at
		FROM identity_documents WHERE guest_id = $1 ORDER BY added_at, hash`, guestID)
	if err != nil {
		return nil, pgErr("loading documents", err)
	}
	defer rows.Close()
	docs := []domain.IdentityDocument{}
	for rows.Next() {
		var owner string
		d, err := scanDocument(rows, &owner)
		if err != nil {
			return nil, pgErr("loading documents", err)
		}
		docs = append(docs, d)
	}
	return docs, pgErr("loading documents", rows.Err())
}

// CreateGuest inserts a guest and any documents presented with them, in one
// transaction.
//
// Atomicity matters more here than it looks: a guest row without its opening
// document is a file nobody can resolve, and a document row without its guest
// is a hash that permanently blocks the real person from opening a file.
func (s *Store) CreateGuest(g domain.Guest) (domain.Guest, error) {
	ctx, cancel := s.ctx()
	defer cancel()

	if g.ID == "" {
		g.ID = s.nextID("g")
	}
	if g.JoinedAt.IsZero() {
		g.JoinedAt = time.Now().UTC()
	}
	if g.AvatarSeed == "" {
		g.AvatarSeed = g.ID
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Guest{}, pgErr("creating guest", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var globalID *string
	if g.GlobalID != "" {
		v := string(g.GlobalID)
		globalID = &v
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO guests (id, global_id, name, email, phone, city, nationality, verified, joined_at, avatar_seed)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		g.ID, globalID, g.Name, g.Email, g.Phone, g.City, string(g.Nationality),
		g.Verified, g.JoinedAt, g.AvatarSeed)
	if err != nil {
		return domain.Guest{}, pgErr("creating guest", err)
	}

	for _, d := range g.Documents {
		if err := insertDocument(ctx, tx, g.ID, d); err != nil {
			return domain.Guest{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.Guest{}, pgErr("creating guest", err)
	}
	return g, nil
}

func insertDocument(ctx context.Context, tx pgx.Tx, guestID string, d domain.IdentityDocument) error {
	if d.AddedAt.IsZero() {
		d.AddedAt = time.Now().UTC()
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO identity_documents
			(hash, guest_id, country, doc_type, last4, verified, authority, added_at, verified_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		d.Hash, guestID, string(d.Country), string(d.Type), d.Last4,
		d.Verified, d.Authority, d.AddedAt, d.VerifiedAt)
	return pgErr("attaching document", err)
}

// --- identity resolution -----------------------------------------------------

// ResolveByDocument finds the file a document hash belongs to.
//
// The signature returns no error, which is inherited from the in-memory
// implementation where a lookup could not fail. A database lookup can, so a
// genuine failure is logged and reported as "not found" — the caller then opens
// a new file. That is the wrong outcome under a database outage, and it is
// recorded here as a known limitation rather than papered over: fixing it means
// widening the interface to return an error, which is the same change the
// context wart needs.
func (s *Store) ResolveByDocument(hash string) (domain.Guest, bool) {
	ctx, cancel := s.ctx()
	defer cancel()

	var guestID string
	err := s.pool.QueryRow(ctx,
		`SELECT guest_id FROM identity_documents WHERE hash = $1`, hash).Scan(&guestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Guest{}, false
	}
	if err != nil {
		s.log.Error("document resolution failed; treating as no match, which may open a duplicate file",
			"err", err)
		return domain.Guest{}, false
	}
	g, err := s.getGuest(ctx, guestID)
	if err != nil {
		s.log.Error("document resolved to a missing guest", "guest_id", guestID, "err", err)
		return domain.Guest{}, false
	}
	return g, true
}

// AttachDocument adds a document to an existing file.
//
// Re-presenting a document already on the same file is a no-op, matching the
// in-memory behaviour: the desk should not have to remember whether it has
// scanned this passport before. Presenting one that belongs to somebody else is
// a conflict, and that is the guard against silently merging two people's
// histories.
func (s *Store) AttachDocument(guestID string, doc domain.IdentityDocument) error {
	ctx, cancel := s.ctx()
	defer cancel()

	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM guests WHERE id = $1)`, guestID).
		Scan(&exists); err != nil {
		return pgErr("attaching document", err)
	}
	if !exists {
		return fmt.Errorf("guest %q: %w", guestID, store.ErrNotFound)
	}

	if doc.AddedAt.IsZero() {
		doc.AddedAt = time.Now().UTC()
	}

	// ON CONFLICT DO NOTHING then check the owner: this resolves the
	// already-attached case and the owned-by-someone-else case in one statement
	// without a read-then-write race between two desks.
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO identity_documents
			(hash, guest_id, country, doc_type, last4, verified, authority, added_at, verified_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (hash) DO NOTHING`,
		doc.Hash, guestID, string(doc.Country), string(doc.Type), doc.Last4,
		doc.Verified, doc.Authority, doc.AddedAt, doc.VerifiedAt)
	if err != nil {
		return pgErr("attaching document", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	var owner string
	if err := s.pool.QueryRow(ctx,
		`SELECT guest_id FROM identity_documents WHERE hash = $1`, doc.Hash).Scan(&owner); err != nil {
		return pgErr("attaching document", err)
	}
	if owner == guestID {
		return nil // already attached
	}
	return fmt.Errorf("this document is already on another profile: %w", store.ErrDuplicate)
}

// --- inquiries ---------------------------------------------------------------

// RecordInquiry appends to the access log.
//
// The signature returns nothing, again inherited from the in-memory store. A
// dropped inquiry is a real problem — it is the record a guest is entitled to
// see — so a failure is logged at error level rather than swallowed.
func (s *Store) RecordInquiry(q domain.Inquiry) {
	ctx, cancel := s.ctx()
	defer cancel()

	if q.At.IsZero() {
		q.At = time.Now().UTC()
	}
	if q.Purpose == "" {
		q.Purpose = domain.InquiryCheckIn
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO inquiries (guest_id, member_id, member_name, purpose, at)
		VALUES ($1,$2,$3,$4,$5)`,
		q.GuestID, q.MemberID, q.MemberName, string(q.Purpose), q.At)
	if err != nil {
		s.log.Error("failed to record inquiry; the guest's access log is now incomplete",
			"guest_id", q.GuestID, "member_id", q.MemberID, "err", err)
	}
}

func (s *Store) InquiriesFor(guestID string) []domain.Inquiry {
	ctx, cancel := s.ctx()
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		SELECT id, guest_id, member_id, member_name, purpose, at
		FROM inquiries WHERE guest_id = $1 ORDER BY at DESC, id DESC LIMIT 500`, guestID)
	if err != nil {
		s.log.Error("failed to read inquiries", "guest_id", guestID, "err", err)
		return []domain.Inquiry{}
	}
	defer rows.Close()

	out := []domain.Inquiry{}
	for rows.Next() {
		var q domain.Inquiry
		var id int64
		var purpose string
		if err := rows.Scan(&id, &q.GuestID, &q.MemberID, &q.MemberName, &purpose, &q.At); err != nil {
			s.log.Error("failed to scan inquiry", "guest_id", guestID, "err", err)
			return out
		}
		q.ID = fmt.Sprintf("iq_%d", id)
		q.Purpose = domain.InquiryPurpose(purpose)
		out = append(out, q)
	}
	return out
}

// --- reviews -----------------------------------------------------------------

const reviewColumns = `id, guest_id, host_id, host_name, property_id, property_name,
	member_id, member_name, stay_id, room_number,
	house_rules, property_care, communication, noise, accuracy,
	incidents, commendations, comment, check_in, check_out, submitted_at,
	dispute_status, dispute_reason, dispute_resolution, dispute_raised_at, dispute_resolved_at`

func scanReview(row pgx.Row) (domain.Review, error) {
	var r domain.Review
	var incidents, commendations []byte
	var checkIn, checkOut *time.Time
	var disputeStatus string

	err := row.Scan(&r.ID, &r.GuestID, &r.HostID, &r.HostName, &r.PropertyID, &r.PropertyName,
		&r.MemberID, &r.MemberName, &r.StayID, &r.RoomNumber,
		&r.Ratings.HouseRules, &r.Ratings.PropertyCare, &r.Ratings.Communication,
		&r.Ratings.Noise, &r.Ratings.Accuracy,
		&incidents, &commendations, &r.Comment, &checkIn, &checkOut, &r.SubmittedAt,
		&disputeStatus, &r.Dispute.Reason, &r.Dispute.Resolution,
		&r.Dispute.RaisedAt, &r.Dispute.ResolvedAt)
	if err != nil {
		return domain.Review{}, err
	}

	r.Incidents = []domain.Incident{}
	if len(incidents) > 0 {
		if err := json.Unmarshal(incidents, &r.Incidents); err != nil {
			return domain.Review{}, fmt.Errorf("review %s: decoding incidents: %w", r.ID, err)
		}
	}
	r.Commendations = []domain.Commendation{}
	if len(commendations) > 0 {
		if err := json.Unmarshal(commendations, &r.Commendations); err != nil {
			return domain.Review{}, fmt.Errorf("review %s: decoding commendations: %w", r.ID, err)
		}
	}
	if checkIn != nil {
		r.CheckIn = *checkIn
	}
	if checkOut != nil {
		r.CheckOut = *checkOut
	}
	r.Dispute.Status = domain.DisputeStatus(disputeStatus)
	return r, nil
}

func (s *Store) queryReviews(ctx context.Context, where string, args ...any) ([]domain.Review, error) {
	sql := `SELECT ` + reviewColumns + ` FROM reviews`
	if where != "" {
		sql += ` WHERE ` + where
	}
	// Ordering in SQL rather than in Go so a LIMIT can be pushed down later
	// without silently changing which reviews are returned.
	sql += ` ORDER BY submitted_at DESC, id DESC`

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, pgErr("loading reviews", err)
	}
	defer rows.Close()

	out := []domain.Review{}
	for rows.Next() {
		r, err := scanReview(rows)
		if err != nil {
			return nil, pgErr("loading reviews", err)
		}
		out = append(out, r)
	}
	return out, pgErr("loading reviews", rows.Err())
}

func (s *Store) ReviewsForGuest(guestID string) ([]domain.Review, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	return s.queryReviews(ctx, `guest_id = $1`, guestID)
}

func (s *Store) AllReviews() ([]domain.Review, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	return s.queryReviews(ctx, "")
}

func (s *Store) CreateReview(r domain.Review) (domain.Review, error) {
	ctx, cancel := s.ctx()
	defer cancel()

	if r.ID == "" {
		r.ID = s.nextID("r")
	}
	if r.SubmittedAt.IsZero() {
		r.SubmittedAt = time.Now().UTC()
	}
	if r.Incidents == nil {
		r.Incidents = []domain.Incident{}
	}
	if r.Commendations == nil {
		r.Commendations = []domain.Commendation{}
	}

	incidents, err := json.Marshal(r.Incidents)
	if err != nil {
		return domain.Review{}, fmt.Errorf("encoding incidents: %w", err)
	}
	commendations, err := json.Marshal(r.Commendations)
	if err != nil {
		return domain.Review{}, fmt.Errorf("encoding commendations: %w", err)
	}

	var checkIn, checkOut *time.Time
	if !r.CheckIn.IsZero() {
		checkIn = &r.CheckIn
	}
	if !r.CheckOut.IsZero() {
		checkOut = &r.CheckOut
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO reviews (`+reviewColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)`,
		r.ID, r.GuestID, r.HostID, r.HostName, r.PropertyID, r.PropertyName,
		r.MemberID, r.MemberName, r.StayID, r.RoomNumber,
		r.Ratings.HouseRules, r.Ratings.PropertyCare, r.Ratings.Communication,
		r.Ratings.Noise, r.Ratings.Accuracy,
		incidents, commendations, r.Comment, checkIn, checkOut, r.SubmittedAt,
		string(r.Dispute.Status), r.Dispute.Reason, r.Dispute.Resolution,
		r.Dispute.RaisedAt, r.Dispute.ResolvedAt)
	if err != nil {
		return domain.Review{}, pgErr("creating review", err)
	}
	return r, nil
}

// --- seeding -----------------------------------------------------------------

// IsEmpty reports whether the store holds no guests, which is the signal to
// seed. Mirrors FileStore.IsEmpty so main can treat the two identically.
func (s *Store) IsEmpty() bool {
	ctx, cancel := s.ctx()
	defer cancel()
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM guests)`).Scan(&exists); err != nil {
		s.log.Error("failed to check whether the store is empty", "err", err)
		return false // do not seed on top of a database we cannot read
	}
	return !exists
}

// LoadSeed bulk-inserts a dataset in one transaction.
//
// ON CONFLICT DO NOTHING throughout: seeding is replayable, and two replicas
// booting into an empty database at the same time must not turn a race into a
// crash loop.
func (s *Store) LoadSeed(guests []domain.Guest, reviews []domain.Review) {
	ctx, cancel := context.WithTimeout(s.base, 60*time.Second)
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		s.log.Error("seeding failed to begin", "err", err)
		return
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	for _, g := range guests {
		var globalID *string
		if g.GlobalID != "" {
			v := string(g.GlobalID)
			globalID = &v
		}
		if g.AvatarSeed == "" {
			g.AvatarSeed = g.ID
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO guests (id, global_id, name, email, phone, city, nationality, verified, joined_at, avatar_seed)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (id) DO NOTHING`,
			g.ID, globalID, g.Name, g.Email, g.Phone, g.City, string(g.Nationality),
			g.Verified, g.JoinedAt, g.AvatarSeed); err != nil {
			s.log.Error("seeding guest failed", "id", g.ID, "err", err)
			return
		}
		for _, d := range g.Documents {
			if _, err := tx.Exec(ctx, `
				INSERT INTO identity_documents
					(hash, guest_id, country, doc_type, last4, verified, authority, added_at, verified_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (hash) DO NOTHING`,
				d.Hash, g.ID, string(d.Country), string(d.Type), d.Last4,
				d.Verified, d.Authority, d.AddedAt, d.VerifiedAt); err != nil {
				s.log.Error("seeding document failed", "guest", g.ID, "err", err)
				return
			}
		}
	}

	for _, r := range reviews {
		if r.Incidents == nil {
			r.Incidents = []domain.Incident{}
		}
		if r.Commendations == nil {
			r.Commendations = []domain.Commendation{}
		}
		incidents, _ := json.Marshal(r.Incidents)
		commendations, _ := json.Marshal(r.Commendations)
		var checkIn, checkOut *time.Time
		if !r.CheckIn.IsZero() {
			checkIn = &r.CheckIn
		}
		if !r.CheckOut.IsZero() {
			checkOut = &r.CheckOut
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO reviews (`+reviewColumns+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)
			ON CONFLICT (id) DO NOTHING`,
			r.ID, r.GuestID, r.HostID, r.HostName, r.PropertyID, r.PropertyName,
			r.MemberID, r.MemberName, r.StayID, r.RoomNumber,
			r.Ratings.HouseRules, r.Ratings.PropertyCare, r.Ratings.Communication,
			r.Ratings.Noise, r.Ratings.Accuracy,
			incidents, commendations, r.Comment, checkIn, checkOut, r.SubmittedAt,
			string(r.Dispute.Status), r.Dispute.Reason, r.Dispute.Resolution,
			r.Dispute.RaisedAt, r.Dispute.ResolvedAt); err != nil {
			s.log.Error("seeding review failed", "id", r.ID, "err", err)
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		s.log.Error("seeding failed to commit", "err", err)
		return
	}
	s.log.Info("seeded demo dataset", "guests", len(guests), "reviews", len(reviews))
}

// MarkEventProcessed records an MQTT event id, reporting whether this is the
// first time it has been seen.
//
// MQTT QoS 1 is at-least-once, so a redelivery after a broker reconnect is
// routine. Without this the same incident is filed twice and the guest is
// penalised twice for one event.
func (s *Store) MarkEventProcessed(ctx context.Context, eventID, propertyID, kind, result string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO processed_events (event_id, property_id, kind, result)
		VALUES ($1,$2,$3,$4) ON CONFLICT (event_id) DO NOTHING`,
		eventID, propertyID, kind, result)
	if err != nil {
		return false, pgErr("recording event", err)
	}
	return tag.RowsAffected() == 1, nil
}

// compile-time proof that the contract is satisfied.
var _ store.Store = (*Store)(nil)
