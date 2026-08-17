package scoringsvc

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
	pb "github.com/udaykishore-resu/guest-score/backend/internal/gen/scoringv1"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoring"
)

// Scorer is what the API depends on. Local and Remote both satisfy it, so no
// handler knows or cares whether a score crossed a network.
type Scorer interface {
	Score(ctx context.Context, guestID string, reviews []domain.Review, now scoring.Time) scoring.Score
	Batch(ctx context.Context, reqs []BatchItem, now scoring.Time) []scoring.Score
	Model() scoring.Model
	Close() error
}

// BatchItem is one guest's records in a batch request.
type BatchItem struct {
	GuestID string
	Reviews []domain.Review
}

// --- local -------------------------------------------------------------------

// Local calls the scoring function directly.
//
// This is the default and, for a single deployment, the right one: it is a pure
// function, so a network hop buys nothing but latency and a failure mode. The
// gRPC path exists for when the model needs to version and deploy separately,
// not because scoring is heavy.
type Local struct{ model scoring.Model }

// NewLocal builds the in-process scorer.
func NewLocal(m scoring.Model) *Local { return &Local{model: m} }

func (l *Local) Score(_ context.Context, _ string, reviews []domain.Review, now scoring.Time) scoring.Score {
	return scoring.Compute(reviews, now, l.model)
}

func (l *Local) Batch(_ context.Context, reqs []BatchItem, now scoring.Time) []scoring.Score {
	out := make([]scoring.Score, len(reqs))
	for i, r := range reqs {
		out[i] = scoring.Compute(r.Reviews, now, l.model)
	}
	return out
}

func (l *Local) Model() scoring.Model { return l.model }
func (l *Local) Close() error         { return nil }

// --- remote ------------------------------------------------------------------

// Remote calls the scoring service over gRPC, falling back to the local
// function when it cannot.
//
// The fallback is the interesting decision. Refusing to answer would be more
// honest about the deployment, but the score is not a lookup — it is a
// deterministic function of records the API already holds, and the API has the
// same code linked in. Failing a check-in because a sidecar is restarting, when
// the correct answer is computable locally, would be a self-inflicted outage.
//
// The cost is that a model deployed only to the scoring service would be
// silently bypassed during an outage. That is why fallbacks are counted and
// logged, and why ModelVersion exists: a score computed on the fallback path
// carries the API binary's version, so an audit can tell the two apart.
type Remote struct {
	conn    *grpc.ClientConn
	client  pb.ScoringServiceClient
	local   *Local
	log     *slog.Logger
	timeout time.Duration

	fallbacks atomic.Uint64
}

// NewRemote dials the scoring service.
//
// The dial is lazy — grpc.NewClient does not block — so a scoring service that
// is slower to start than the API does not prevent the API from serving. The
// first few requests take the fallback path and say so.
func NewRemote(target string, m scoring.Model, timeout time.Duration, log *slog.Logger) (*Remote, error) {
	if log == nil {
		log = slog.Default()
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	conn, err := grpc.NewClient(target,
		// Insecure because in both supported deployments — Compose and a
		// Kubernetes Service — this hop never leaves the pod network. Crossing
		// a trust boundary means mTLS, and that is a configuration change here
		// plus a certificate source, not a code change.
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultServiceConfig(`{
			"methodConfig": [{
				"name": [{"service": "scoring.v1.ScoringService"}],
				"retryPolicy": {
					"maxAttempts": 3,
					"initialBackoff": "0.05s",
					"maxBackoff": "0.5s",
					"backoffMultiplier": 2,
					"retryableStatusCodes": ["UNAVAILABLE", "RESOURCE_EXHAUSTED"]
				}
			}]
		}`),
	)
	if err != nil {
		return nil, err
	}
	return NewRemoteWithConn(conn, m, timeout, log), nil
}

// NewRemoteWithConn wraps an existing connection.
//
// This is what lets the tests run the real client against the real server over
// an in-memory listener: the equivalence between the local and remote paths is
// the one property worth proving, and proving it against a mock would prove
// nothing about the serialization that could break it.
func NewRemoteWithConn(conn *grpc.ClientConn, m scoring.Model, timeout time.Duration, log *slog.Logger) *Remote {
	if log == nil {
		log = slog.Default()
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	return &Remote{
		conn: conn, client: pb.NewScoringServiceClient(conn),
		local: NewLocal(m), log: log, timeout: timeout,
	}
}

// Fallbacks reports how many requests took the local path. Exposed for
// /api/health so a permanently-degraded deployment is visible rather than
// merely quiet.
func (r *Remote) Fallbacks() uint64 { return r.fallbacks.Load() }

func (r *Remote) fellBack(err error, op string) {
	n := r.fallbacks.Add(1)
	// Log the first failure and then powers of ten. A scoring service that is
	// down for an hour must not also fill the log with a line per request.
	if n == 1 || n%1000 == 0 {
		r.log.Warn("scoring service unavailable, computing locally",
			"op", op, "fallbacks", n, "err", err)
	}
}

func (r *Remote) Score(ctx context.Context, guestID string, reviews []domain.Review, now scoring.Time) scoring.Score {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	req := &pb.ComputeRequest{
		GuestId:     guestID,
		EvaluatedAt: tsOrNil(now.Std()),
	}
	for _, rev := range reviews {
		req.Reviews = append(req.Reviews, ReviewToProto(rev))
	}
	resp, err := r.client.Compute(ctx, req)
	if err != nil {
		r.fellBack(err, "Compute")
		return r.local.Score(ctx, guestID, reviews, now)
	}
	return ScoreFromProto(resp.GetScore())
}

func (r *Remote) Batch(ctx context.Context, items []BatchItem, now scoring.Time) []scoring.Score {
	if len(items) == 0 {
		return nil
	}
	// A batch carries every review for every guest on the page, so it gets more
	// room than a single call but still a bound.
	ctx, cancel := context.WithTimeout(ctx, r.timeout*3)
	defer cancel()

	req := &pb.ComputeBatchRequest{EvaluatedAt: tsOrNil(now.Std())}
	for _, it := range items {
		one := &pb.ComputeRequest{GuestId: it.GuestID}
		for _, rev := range it.Reviews {
			one.Reviews = append(one.Reviews, ReviewToProto(rev))
		}
		req.Requests = append(req.Requests, one)
	}

	resp, err := r.client.ComputeBatch(ctx, req)
	if err != nil {
		r.fellBack(err, "ComputeBatch")
		return r.local.Batch(ctx, items, now)
	}
	got := resp.GetResponses()
	if len(got) != len(items) {
		// A short or reordered batch would silently attach one guest's score to
		// another guest's row, which is the worst failure this system has. Do
		// not attempt to reconcile it; recompute the page locally.
		r.fellBack(errShortBatch{want: len(items), got: len(got)}, "ComputeBatch")
		return r.local.Batch(ctx, items, now)
	}
	out := make([]scoring.Score, len(got))
	for i, resp := range got {
		out[i] = ScoreFromProto(resp.GetScore())
	}
	return out
}

type errShortBatch struct{ want, got int }

func (e errShortBatch) Error() string {
	return "scoring service returned " + itoa(e.got) + " responses for " + itoa(e.want) + " requests"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Model returns the local model.
//
// It does not call DescribeModel, and that is deliberate: the model is needed
// synchronously to render /api/scoring-model, and a version fetched over a
// network that might be down would make a static document intermittently
// unavailable. The published document therefore describes the model this binary
// would apply — which, given the fallback above, is the one that will actually
// be applied if the service is unreachable.
func (r *Remote) Model() scoring.Model { return r.local.Model() }

// Ping checks the scoring service for /api/health.
func (r *Remote) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	_, err := r.client.DescribeModel(ctx, &pb.DescribeModelRequest{})
	return err
}

func (r *Remote) Close() error { return r.conn.Close() }
