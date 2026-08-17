package scoringsvc_test

import (
	"context"
	"log/slog"
	"math"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
	pb "github.com/udaykishore-resu/guest-score/backend/internal/gen/scoringv1"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoring"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoringsvc"
)

var evalTime = time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

// richHistory is deliberately messy: a spread of dates so recency decay is
// active, an incident, a commendation, and a disputed record that must be
// excluded. A round trip that only ever sees clean input proves nothing.
func richHistory() []domain.Review {
	at := func(daysAgo int) time.Time { return evalTime.AddDate(0, 0, -daysAgo) }
	return []domain.Review{
		{
			ID: "r1", GuestID: "g1", HostID: "h1", MemberID: "m1", StayID: "s1",
			Ratings:     domain.Ratings{HouseRules: 5, PropertyCare: 4, Communication: 5, Noise: 4, Accuracy: 5},
			CheckIn:     at(400), CheckOut: at(397), SubmittedAt: at(396),
			Commendations: []domain.Commendation{{Type: domain.ComExceptionalCare, Note: "spotless"}},
		},
		{
			ID: "r2", GuestID: "g1", HostID: "h2", MemberID: "m2", StayID: "s2",
			Ratings:     domain.Ratings{HouseRules: 2, PropertyCare: 2, Communication: 3, Noise: 1, Accuracy: 4},
			CheckIn:     at(90), CheckOut: at(88), SubmittedAt: at(87),
			Incidents:   []domain.Incident{{Type: domain.IncNoiseComplaint, Severity: domain.SevSevere, Note: "3am"}},
		},
		{
			ID: "r3", GuestID: "g1", HostID: "h3", MemberID: "m3", StayID: "s3",
			Ratings:     domain.Ratings{HouseRules: 4, PropertyCare: 4, Communication: 4, Noise: 4, Accuracy: 4},
			SubmittedAt: at(20),
			// Under dispute, so the engine must hold it out entirely.
			Dispute: domain.Dispute{Status: domain.DisputeOpen, Reason: "was not my room"},
		},
	}
}

// TestConversionRoundTrip is the load-bearing test for the whole gRPC split.
//
// If the projection through the wire types loses or mangles a field, the score
// a caller sees depends on whether GS_SCORING_GRPC happened to be set — which
// would be an invisible, non-reproducible discrepancy in the one number this
// system exists to produce.
func TestConversionRoundTrip(t *testing.T) {
	t.Parallel()
	want := scoring.Compute(richHistory(), scoring.At(evalTime), scoring.DefaultModel)

	got := scoringsvc.ScoreFromProto(scoringsvc.ScoreToProto(want, evalTime))

	if !reflect.DeepEqual(want, got) {
		t.Fatalf("score changed crossing the wire types.\n want %+v\n  got %+v", want, got)
	}
}

// TestReviewProjectionPreservesScoringInputs pins what the wire format is
// allowed to drop.
//
// ReviewToProto is a projection, not a copy: property names, room numbers and
// free-text comments are not sent, because the scoring service has no use for
// them and a bureau should not ship a guest's comment history to a service that
// does not need it. What it may never drop is anything the engine reads — so
// the test asserts field-by-field on those, and then asserts the property that
// actually matters, which is that the score is unchanged.
func TestReviewProjectionPreservesScoringInputs(t *testing.T) {
	t.Parallel()
	originals := richHistory()

	trip := make([]domain.Review, 0, len(originals))
	for _, r := range originals {
		trip = append(trip, scoringsvc.ReviewFromProto(scoringsvc.ReviewToProto(r)))
	}

	for i, want := range originals {
		got := trip[i]
		if got.Ratings != want.Ratings {
			t.Errorf("%s: ratings %+v, want %+v", want.ID, got.Ratings, want.Ratings)
		}
		if !got.SubmittedAt.Equal(want.SubmittedAt) {
			t.Errorf("%s: submitted_at %v, want %v", want.ID, got.SubmittedAt, want.SubmittedAt)
		}
		// Compared by length and element, not DeepEqual: the conversion
		// deliberately normalises a nil slice to an empty one so the JSON
		// encoding is [] rather than null, and that normalisation must not
		// read as data loss.
		if len(got.Incidents) != len(want.Incidents) {
			t.Errorf("%s: %d incidents, want %d", want.ID, len(got.Incidents), len(want.Incidents))
		} else {
			for j := range want.Incidents {
				if !reflect.DeepEqual(got.Incidents[j], want.Incidents[j]) {
					t.Errorf("%s: incident %d is %+v, want %+v", want.ID, j, got.Incidents[j], want.Incidents[j])
				}
			}
		}
		if len(got.Commendations) != len(want.Commendations) {
			t.Errorf("%s: %d commendations, want %d", want.ID, len(got.Commendations), len(want.Commendations))
		} else {
			for j := range want.Commendations {
				if got.Commendations[j] != want.Commendations[j] {
					t.Errorf("%s: commendation %d is %+v, want %+v", want.ID, j, got.Commendations[j], want.Commendations[j])
				}
			}
		}
		if got.Dispute.Status != want.Dispute.Status {
			t.Errorf("%s: dispute status %q, want %q", want.ID, got.Dispute.Status, want.Dispute.Status)
		}
		if got.Scoreable() != want.Scoreable() {
			t.Errorf("%s: scoreable %v, want %v", want.ID, got.Scoreable(), want.Scoreable())
		}
	}

	now := scoring.At(evalTime)
	want := scoring.Compute(originals, now, scoring.DefaultModel)
	got := scoring.Compute(trip, now, scoring.DefaultModel)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("the projection changed the score.\n want %+v\n  got %+v", want, got)
	}
}

// TestLocalAndRemoteAgree runs a real gRPC server over an in-memory listener
// and checks the two scorers produce identical results.
func TestLocalAndRemoteAgree(t *testing.T) {
	t.Parallel()
	remote, stop := startServer(t)
	defer stop()

	local := scoringsvc.NewLocal(scoring.DefaultModel)
	now := scoring.At(evalTime)
	reviews := richHistory()

	want := local.Score(context.Background(), "g1", reviews, now)
	got := remote.Score(context.Background(), "g1", reviews, now)

	if !reflect.DeepEqual(want, got) {
		t.Fatalf("remote scorer disagrees with local.\n want %+v\n  got %+v", want, got)
	}
	if remote.Fallbacks() != 0 {
		t.Fatalf("expected no fallbacks against a healthy server, got %d", remote.Fallbacks())
	}
}

func TestBatchMatchesIndividual(t *testing.T) {
	t.Parallel()
	remote, stop := startServer(t)
	defer stop()

	now := scoring.At(evalTime)
	items := []scoringsvc.BatchItem{
		{GuestID: "g1", Reviews: richHistory()},
		{GuestID: "g2", Reviews: nil},
		{GuestID: "g3", Reviews: richHistory()[:1]},
	}

	batch := remote.Batch(context.Background(), items, now)
	if len(batch) != len(items) {
		t.Fatalf("batch returned %d scores for %d guests", len(batch), len(items))
	}
	for i, it := range items {
		want := remote.Score(context.Background(), it.GuestID, it.Reviews, now)
		if !reflect.DeepEqual(want, batch[i]) {
			t.Errorf("%s: batch score differs from individual\n want %+v\n  got %+v",
				it.GuestID, want, batch[i])
		}
	}
}

// TestFallbackWhenServerDown is the behaviour that keeps a check-in working
// while the scoring service is restarting.
func TestFallbackWhenServerDown(t *testing.T) {
	t.Parallel()
	// A target that resolves but never answers.
	remote, err := scoringsvc.NewRemote("127.0.0.1:1", scoring.DefaultModel,
		200*time.Millisecond, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()

	now := scoring.At(evalTime)
	want := scoring.Compute(richHistory(), now, scoring.DefaultModel)
	got := remote.Score(context.Background(), "g1", richHistory(), now)

	if !reflect.DeepEqual(want, got) {
		t.Fatalf("fallback did not produce the locally-computed score")
	}
	if remote.Fallbacks() == 0 {
		t.Fatal("fallback was not counted; a permanently degraded deployment would be invisible")
	}
}

func TestDescribeModelMatchesDefaults(t *testing.T) {
	t.Parallel()
	_, stop := startServer(t)
	defer stop()

	got := scoringsvc.ModelToProto(scoring.DefaultModel)
	m := scoring.DefaultModel

	if got.GetScoreMax() != m.ScoreMax || got.GetScoreMin() != m.ScoreMin {
		t.Errorf("range %v..%v, want %v..%v",
			got.GetScoreMin(), got.GetScoreMax(), m.ScoreMin, m.ScoreMax)
	}
	if got.GetNewGuestScore() != m.NewGuestScore {
		t.Errorf("new guest score %v, want %v", got.GetNewGuestScore(), m.NewGuestScore)
	}
	if len(got.GetTiers()) != len(m.Tiers) {
		t.Fatalf("published %d tiers, model has %d", len(got.GetTiers()), len(m.Tiers))
	}
	// Dimension weights must be published in the canonical order, not map order,
	// or the UI renders them differently on every request.
	if len(got.GetDimensions()) != len(domain.AllDimensions) {
		t.Fatalf("published %d dimensions, want %d", len(got.GetDimensions()), len(domain.AllDimensions))
	}
	var sum float64
	for i, d := range got.GetDimensions() {
		if d.GetDimension() != string(domain.AllDimensions[i]) {
			t.Errorf("dimension %d is %q, want %q", i, d.GetDimension(), domain.AllDimensions[i])
		}
		sum += d.GetWeight()
	}
	if math.Abs(sum-1.0) > 1e-9 {
		t.Errorf("published weights sum to %v, want 1.0", sum)
	}
}

func TestBatchLimitIsEnforced(t *testing.T) {
	t.Parallel()
	srv := scoringsvc.NewServer(scoring.DefaultModel, slog.Default())
	req := &pb.ComputeBatchRequest{Requests: make([]*pb.ComputeRequest, 501)}
	for i := range req.Requests {
		req.Requests[i] = &pb.ComputeRequest{GuestId: "g"}
	}
	if _, err := srv.ComputeBatch(context.Background(), req); err == nil {
		t.Fatal("an oversized batch was accepted; the request size is caller-controlled")
	}
}

// startServer runs the service over an in-memory listener, so the test
// exercises real gRPC serialization without binding a port.
func startServer(t *testing.T) (*scoringsvc.Remote, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterScoringServiceServer(grpcSrv, scoringsvc.NewServer(scoring.DefaultModel, slog.Default()))
	go func() { _ = grpcSrv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}

	remote := scoringsvc.NewRemoteWithConn(conn, scoring.DefaultModel, 5*time.Second, slog.Default())
	return remote, func() {
		_ = remote.Close()
		grpcSrv.Stop()
		_ = lis.Close()
	}
}
