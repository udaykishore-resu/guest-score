package scoringsvc

import (
	"context"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
	pb "github.com/udaykishore-resu/guest-score/backend/internal/gen/scoringv1"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoring"
)

// maxBatch bounds ComputeBatch.
//
// The request carries every review for every guest on the page, so an unbounded
// batch is an unbounded allocation driven by a caller. 500 is far above the
// directory's real page size and far below anything that would hurt.
const maxBatch = 500

// Server implements the ScoringService.
//
// It holds a Model and nothing else — no store, no cache, no clock of its own
// beyond the one it falls back to. That is what makes it trivially horizontally
// scalable and trivially testable: it is a pure function with a network
// interface bolted on.
type Server struct {
	pb.UnimplementedScoringServiceServer

	model scoring.Model
	log   *slog.Logger
}

// NewServer builds the service.
func NewServer(m scoring.Model, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{model: m, log: log}
}

func (s *Server) Compute(_ context.Context, req *pb.ComputeRequest) (*pb.ComputeResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	now := scoring.Now()
	if ts := req.GetEvaluatedAt(); ts != nil {
		now = scoring.At(ts.AsTime())
	}
	return &pb.ComputeResponse{
		GuestId: req.GetGuestId(),
		Score:   s.computeOne(req, now),
	}, nil
}

func (s *Server) computeOne(req *pb.ComputeRequest, now scoring.Time) *pb.Score {
	reviews := make([]domain.Review, 0, len(req.GetReviews()))
	for _, r := range req.GetReviews() {
		reviews = append(reviews, ReviewFromProto(r))
	}
	return ScoreToProto(scoring.Compute(reviews, now, s.model), now.Std())
}

func (s *Server) ComputeBatch(_ context.Context, req *pb.ComputeBatchRequest) (*pb.ComputeBatchResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if n := len(req.GetRequests()); n > maxBatch {
		return nil, status.Errorf(codes.InvalidArgument,
			"batch of %d exceeds the maximum of %d; page the request", n, maxBatch)
	}

	// One clock for the whole batch unless a request overrides it. Two guests on
	// the same page must not be ranked against different instants — recency
	// decay would make the comparison meaningless by a few microseconds, which
	// is invisible and therefore worse than being wrong loudly.
	batchNow := scoring.Now()
	if ts := req.GetEvaluatedAt(); ts != nil {
		batchNow = scoring.At(ts.AsTime())
	}

	out := &pb.ComputeBatchResponse{Responses: make([]*pb.ComputeResponse, 0, len(req.GetRequests()))}
	for _, r := range req.GetRequests() {
		now := batchNow
		if ts := r.GetEvaluatedAt(); ts != nil {
			now = scoring.At(ts.AsTime())
		}
		out.Responses = append(out.Responses, &pb.ComputeResponse{
			GuestId: r.GetGuestId(),
			Score:   s.computeOne(r, now),
		})
	}
	return out, nil
}

func (s *Server) DescribeModel(context.Context, *pb.DescribeModelRequest) (*pb.DescribeModelResponse, error) {
	return ModelToProto(s.model), nil
}
