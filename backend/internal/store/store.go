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
	Band         string // "A".."F", or "" for all
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

	path    string
	seq     atomic.Uint64
	dirty   bool
	closing chan struct{}
	wg      sync.WaitGroup
}

type snapshot struct {
	Version int             `json:"version"`
	SavedAt time.Time       `json:"saved_at"`
	Guests  []domain.Guest  `json:"guests"`
	Reviews []domain.Review `json:"reviews"`
}

// NewFileStore opens (or creates) a store at path. Passing an empty path gives
// an ephemeral in-memory store, which is what the handler tests use.
func NewFileStore(path string) (*FileStore, error) {
	s := &FileStore{
		guests:   map[string]domain.Guest{},
		reviews:  map[string]domain.Review{},
		stayKeys: map[string]bool{},
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
	}
	for _, r := range snap.Reviews {
		s.reviews[r.ID] = r
		s.stayKeys[stayKey(r.HostID, r.StayID)] = true
	}
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
	}
	for _, r := range reviews {
		if r.Incidents == nil {
			r.Incidents = []domain.Incident{}
		}
		s.reviews[r.ID] = r
		s.stayKeys[stayKey(r.HostID, r.StayID)] = true
	}
	s.dirty = true
}
