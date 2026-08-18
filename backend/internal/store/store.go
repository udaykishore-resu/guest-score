// Package store provides persistence for guests and reviews.
//
// The Store interface is the seam the plan promises: the JSON-file
// implementation here is honest about being a demo-scale store, and a SQL
// implementation can replace it without the api or scoring packages noticing.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
)

// Sentinel errors the API layer maps onto HTTP status codes.
var (
	ErrNotFound  = errors.New("not found")
	ErrDuplicate = errors.New("duplicate")
)

// Query describes a guest directory lookup.
type Query struct {
	Search       string // case-insensitive substring over name and email
	Tier         string // "Excellent".."Poor", or "" for all
	HasIncidents bool
	Sort         string // "score" | "reviews" | "recent" | "name"
	Limit        int
	Offset       int
}

// Store is the persistence contract.
type Store interface {
	ListGuests() ([]domain.Guest, error)
	GetGuest(id string) (domain.Guest, error)
	CreateGuest(g domain.Guest) (domain.Guest, error)

	ResolveByDocument(hash string) (domain.Guest, bool)
	AttachDocument(guestID string, doc domain.IdentityDocument) error
	RecordInquiry(q domain.Inquiry)
	InquiriesFor(guestID string) []domain.Inquiry

	ReviewsForGuest(guestID string) ([]domain.Review, error)
	AllReviews() ([]domain.Review, error)
	CreateReview(r domain.Review) (domain.Review, error)

	Close() error
}

// FileStore keeps everything in memory behind an RWMutex and snapshots to a
// JSON file. Reads are lock-free-ish (RLock) and writes are serialized, which
// is what makes the concurrent-submission edge case in the spec safe: two
// simultaneous reviews both persist, neither is lost to a write race.
type FileStore struct {
	mu      sync.RWMutex
	guests  map[string]domain.Guest
	reviews map[string]domain.Review

	// stayKeys enforces FR-010 (one review per host per stay) as a set rather
	// than an O(n) scan on every write.
	stayKeys map[string]bool

	// docIndex maps a document hash to the profile it belongs to. This is the
	// cross-border lookup: any document, from any country, reaches one file.
	docIndex map[string]string

	// inquiries records every lookup, newest appended last.
	inquiries []domain.Inquiry

	path    string
	seq     atomic.Uint64
	dirty   bool
	closing chan struct{}
	wg      sync.WaitGroup
}

type snapshot struct {
	Version   int              `json:"version"`
	SavedAt   time.Time        `json:"saved_at"`
	Guests    []domain.Guest   `json:"guests"`
	Reviews   []domain.Review  `json:"reviews"`
	Inquiries []domain.Inquiry `json:"inquiries,omitempty"`
}

// NewFileStore opens (or creates) a store at path. Passing an empty path gives
// an ephemeral in-memory store, which is what the handler tests use.
func NewFileStore(path string) (*FileStore, error) {
	s := &FileStore{
		guests:   map[string]domain.Guest{},
		reviews:  map[string]domain.Review{},
		stayKeys: map[string]bool{},
		docIndex: map[string]string{},
		path:     path,
		closing:  make(chan struct{}),
	}
	if path != "" {
		if err := s.load(); err != nil {
			return nil, fmt.Errorf("loading store: %w", err)
		}
		s.wg.Add(1)
		go s.flushLoop()
	}
	return s, nil
}

func (s *FileStore) load() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // fresh store; the caller will seed it
	}
	if err != nil {
		return err
	}
	var snap snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return fmt.Errorf("corrupt snapshot at %s: %w", s.path, err)
	}
	for _, g := range snap.Guests {
		s.guests[g.ID] = g
		s.indexDocuments(g)
	}
	for _, r := range snap.Reviews {
		s.reviews[r.ID] = r
		s.stayKeys[stayKey(r.HostID, r.StayID)] = true
	}
	s.inquiries = append(s.inquiries, snap.Inquiries...)
	return nil
}

// flushLoop persists dirty state on a timer rather than on every write, so a
// burst of submissions costs one fsync instead of N.
func (s *FileStore) flushLoop() {
	defer s.wg.Done()
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := s.Flush(); err != nil {
				fmt.Fprintf(os.Stderr, "guest-score: snapshot failed: %v\n", err)
			}
		case <-s.closing:
			return
		}
	}
}

// Flush writes a snapshot if anything changed since the last one. The write is
// atomic: a temp file in the same directory followed by a rename, so a crash
// mid-write cannot leave a truncated snapshot behind.
func (s *FileStore) Flush() error {
	s.mu.Lock()
	if !s.dirty || s.path == "" {
		s.mu.Unlock()
		return nil
	}
	snap := snapshot{Version: 1, SavedAt: time.Now().UTC()}
	for _, g := range s.guests {
		snap.Guests = append(snap.Guests, g)
	}
	for _, r := range s.reviews {
		snap.Reviews = append(snap.Reviews, r)
	}
	snap.Inquiries = append(snap.Inquiries, s.inquiries...)
	s.dirty = false
	s.mu.Unlock()

	// Deterministic ordering keeps the snapshot diffable in git.
	sort.Slice(snap.Guests, func(i, j int) bool { return snap.Guests[i].ID < snap.Guests[j].ID })
	sort.Slice(snap.Reviews, func(i, j int) bool { return snap.Reviews[i].ID < snap.Reviews[j].ID })

	b, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Close stops the flush loop and writes a final snapshot.
func (s *FileStore) Close() error {
	if s.path == "" {
		return nil
	}
	select {
	case <-s.closing: // already closed
	default:
		close(s.closing)
	}
	s.wg.Wait()
	return s.Flush()
}

// --- Guests ------------------------------------------------------------------

func (s *FileStore) ListGuests() ([]domain.Guest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Guest, 0, len(s.guests))
	for _, g := range s.guests {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *FileStore) GetGuest(id string) (domain.Guest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.guests[id]
	if !ok {
		return domain.Guest{}, fmt.Errorf("guest %q: %w", id, ErrNotFound)
	}
	return g, nil
}

func (s *FileStore) CreateGuest(g domain.Guest) (domain.Guest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g.ID == "" {
		g.ID = s.nextID("g")
	}
	if _, exists := s.guests[g.ID]; exists {
		return domain.Guest{}, fmt.Errorf("guest %q: %w", g.ID, ErrDuplicate)
	}
	for _, existing := range s.guests {
		if strings.EqualFold(existing.Email, g.Email) {
			return domain.Guest{}, fmt.Errorf("email %q already registered: %w", g.Email, ErrDuplicate)
		}
	}
	if g.JoinedAt.IsZero() {
		g.JoinedAt = time.Now().UTC()
	}
	if g.AvatarSeed == "" {
		g.AvatarSeed = g.ID
	}
	s.guests[g.ID] = g
	s.indexDocuments(g)
	s.dirty = true
	return g, nil
}

// --- Reviews -----------------------------------------------------------------

func (s *FileStore) ReviewsForGuest(guestID string) ([]domain.Review, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []domain.Review{}
	for _, r := range s.reviews {
		if r.GuestID == guestID {
			out = append(out, r)
		}
	}
	domain.SortReviewsByRecency(out)
	return out, nil
}

func (s *FileStore) AllReviews() ([]domain.Review, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Review, 0, len(s.reviews))
	for _, r := range s.reviews {
		out = append(out, r)
	}
	domain.SortReviewsByRecency(out)
	return out, nil
}

func (s *FileStore) CreateReview(r domain.Review) (domain.Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// A review must not conjure a guest into existence (spec edge case).
	if _, ok := s.guests[r.GuestID]; !ok {
		return domain.Review{}, fmt.Errorf("guest %q: %w", r.GuestID, ErrNotFound)
	}
	key := stayKey(r.HostID, r.StayID)
	if s.stayKeys[key] {
		return domain.Review{}, fmt.Errorf("host %q already reviewed stay %q: %w", r.HostID, r.StayID, ErrDuplicate)
	}
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
	s.reviews[r.ID] = r
	s.stayKeys[key] = true
	s.dirty = true
	return r, nil
}

// nextID returns a monotonic, human-legible identifier. Callers must hold the
// write lock; the atomic counter guards the ephemeral no-path case where two
// goroutines could otherwise race on the sequence itself.
func (s *FileStore) nextID(prefix string) string {
	n := s.seq.Add(1)
	return fmt.Sprintf("%s_%d_%03d", prefix, time.Now().UTC().Unix()%1000000, n)
}

func stayKey(hostID, stayID string) string {
	return strings.ToLower(hostID) + "\x00" + strings.ToLower(stayID)
}

// IsEmpty reports whether the store holds no guests, which is the signal to
// seed (FR-014).
func (s *FileStore) IsEmpty() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.guests) == 0
}

// LoadSeed bulk-inserts a dataset, bypassing the duplicate-email check so a
// seed file can be replayed into a fresh store deterministically.
func (s *FileStore) LoadSeed(guests []domain.Guest, reviews []domain.Review) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range guests {
		s.guests[g.ID] = g
		s.indexDocuments(g)
	}
	for _, r := range reviews {
		if r.Incidents == nil {
			r.Incidents = []domain.Incident{}
		}
		if r.Commendations == nil {
			r.Commendations = []domain.Commendation{}
		}
		s.reviews[r.ID] = r
		s.stayKeys[stayKey(r.HostID, r.StayID)] = true
	}
	s.dirty = true
}

// --- Identity resolution -----------------------------------------------------

// ResolveByDocument finds the profile a document belongs to.
//
// This is the mechanism that makes the file global. A member in Lisbon scans a
// passport it has never seen; the hash matches one already attached to a file
// opened in Mumbai, and the same standing comes back. Without this, each
// country would accumulate its own disconnected profiles and a guest could
// outrun a bad record by crossing a border.
func (s *FileStore) ResolveByDocument(hash string) (domain.Guest, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.docIndex[hash]
	if !ok {
		return domain.Guest{}, false
	}
	g, ok := s.guests[id]
	return g, ok
}

// AttachDocument adds a document to an existing profile.
//
// Attaching a second document is how a domestic-only file becomes portable: a
// guest who opened on an Aadhaar adds a passport, and from then on any country
// can reach them. Re-presenting a document already on file is a no-op rather
// than an error — the desk should not have to care whether it has seen this
// passport before.
func (s *FileStore) AttachDocument(guestID string, doc domain.IdentityDocument) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.guests[guestID]
	if !ok {
		return fmt.Errorf("guest %q: %w", guestID, ErrNotFound)
	}
	if owner, taken := s.docIndex[doc.Hash]; taken {
		if owner == guestID {
			return nil // already attached
		}
		// The same document cannot belong to two people. This is the guard
		// against silently merging or hijacking a file.
		return fmt.Errorf("this document is already on another profile: %w", ErrDuplicate)
	}
	g.Documents = append(g.Documents, doc)
	s.guests[guestID] = g
	s.docIndex[doc.Hash] = guestID
	s.dirty = true
	return nil
}

// indexDocuments rebuilds the document index for one guest.
func (s *FileStore) indexDocuments(g domain.Guest) {
	for _, d := range g.Documents {
		s.docIndex[d.Hash] = g.ID
	}
}

// RecordInquiry logs that a member pulled a guest's file.
func (s *FileStore) RecordInquiry(q domain.Inquiry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if q.ID == "" {
		q.ID = s.nextID("q")
	}
	if q.At.IsZero() {
		q.At = time.Now().UTC()
	}
	s.inquiries = append(s.inquiries, q)
	s.dirty = true
}

// InquiriesFor returns a guest's inquiry history, newest first.
func (s *FileStore) InquiriesFor(guestID string) []domain.Inquiry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []domain.Inquiry{}
	for i := len(s.inquiries) - 1; i >= 0; i-- {
		if s.inquiries[i].GuestID == guestID {
			out = append(out, s.inquiries[i])
		}
	}
	return out
}
