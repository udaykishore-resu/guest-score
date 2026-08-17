// Package scoringsvc puts the scoring engine behind a gRPC boundary.
//
// The engine itself does not change and does not learn about gRPC: Compute is
// still a pure function over domain records with the clock injected. This
// package is only translation — domain types to wire types and back — plus a
// server, a client, and the decision of which one the API uses.
//
// The reason for the boundary is regulatory rather than architectural. The
// model is the part of a bureau that gets audited: someone will eventually ask
// which version produced a given score and whether it can be reproduced. A
// separate service with a versioned contract makes "the model changed on
// Tuesday" a deployable, reviewable fact instead of a line in a diff of the
// whole API.
//
// What it must not do is change the answer. The conversions below are total and
// lossless in both directions, and scoringsvc_test.go round-trips a populated
// Score through the wire types to prove the local and remote paths cannot
// disagree.
package scoringsvc

import (
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
	pb "github.com/udaykishore-resu/guest-score/backend/internal/gen/scoringv1"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoring"
)

// ModelVersion identifies the constants in force.
//
// It is a hand-maintained string rather than a hash of the Model, deliberately:
// a hash changes when an unrelated field is reordered and says nothing to a
// human reading an audit trail. Bump it whenever a change would move a score,
// and only then.
const ModelVersion = "2026.08-anchored-1000"

func tsOrNil(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func timeOrZero(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

func ptrTimeOrNil(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}
	t := ts.AsTime()
	return &t
}

// --- domain -> wire ----------------------------------------------------------

// ReviewToProto converts one stay record.
func ReviewToProto(r domain.Review) *pb.Review {
	out := &pb.Review{
		Id:       r.ID,
		GuestId:  r.GuestID,
		HostId:   r.HostID,
		MemberId: r.MemberID,
		StayId:   r.StayID,
		Ratings: &pb.Ratings{
			HouseRules:    int32(r.Ratings.HouseRules),
			PropertyCare:  int32(r.Ratings.PropertyCare),
			Communication: int32(r.Ratings.Communication),
			Noise:         int32(r.Ratings.Noise),
			Accuracy:      int32(r.Ratings.Accuracy),
		},
		CheckIn:     tsOrNil(r.CheckIn),
		CheckOut:    tsOrNil(r.CheckOut),
		SubmittedAt: tsOrNil(r.SubmittedAt),
		Dispute: &pb.Dispute{
			Status:     string(r.Dispute.Status),
			Reason:     r.Dispute.Reason,
			Resolution: r.Dispute.Resolution,
		},
	}
	if r.Dispute.RaisedAt != nil {
		out.Dispute.RaisedAt = timestamppb.New(*r.Dispute.RaisedAt)
	}
	if r.Dispute.ResolvedAt != nil {
		out.Dispute.ResolvedAt = timestamppb.New(*r.Dispute.ResolvedAt)
	}
	for _, i := range r.Incidents {
		out.Incidents = append(out.Incidents, &pb.Incident{
			Type: string(i.Type), Severity: string(i.Severity), Note: i.Note, Evidence: i.Evidence,
		})
	}
	for _, c := range r.Commendations {
		out.Commendations = append(out.Commendations, &pb.Commendation{
			Type: string(c.Type), Note: c.Note,
		})
	}
	return out
}

// ReviewFromProto is the inverse of ReviewToProto.
func ReviewFromProto(p *pb.Review) domain.Review {
	if p == nil {
		return domain.Review{}
	}
	r := domain.Review{
		ID:            p.GetId(),
		GuestID:       p.GetGuestId(),
		HostID:        p.GetHostId(),
		MemberID:      p.GetMemberId(),
		StayID:        p.GetStayId(),
		CheckIn:       timeOrZero(p.GetCheckIn()),
		CheckOut:      timeOrZero(p.GetCheckOut()),
		SubmittedAt:   timeOrZero(p.GetSubmittedAt()),
		Incidents:     []domain.Incident{},
		Commendations: []domain.Commendation{},
	}
	if rt := p.GetRatings(); rt != nil {
		r.Ratings = domain.Ratings{
			HouseRules:    int(rt.GetHouseRules()),
			PropertyCare:  int(rt.GetPropertyCare()),
			Communication: int(rt.GetCommunication()),
			Noise:         int(rt.GetNoise()),
			Accuracy:      int(rt.GetAccuracy()),
		}
	}
	for _, i := range p.GetIncidents() {
		r.Incidents = append(r.Incidents, domain.Incident{
			Type:     domain.IncidentType(i.GetType()),
			Severity: domain.Severity(i.GetSeverity()),
			Note:     i.GetNote(),
			Evidence: i.GetEvidence(),
		})
	}
	for _, c := range p.GetCommendations() {
		r.Commendations = append(r.Commendations, domain.Commendation{
			Type: domain.CommendationType(c.GetType()), Note: c.GetNote(),
		})
	}
	if d := p.GetDispute(); d != nil {
		r.Dispute = domain.Dispute{
			Status:     domain.DisputeStatus(d.GetStatus()),
			Reason:     d.GetReason(),
			Resolution: d.GetResolution(),
			RaisedAt:   ptrTimeOrNil(d.GetRaisedAt()),
			ResolvedAt: ptrTimeOrNil(d.GetResolvedAt()),
		}
	}
	return r
}

// ScoreToProto converts a computed score for the wire.
func ScoreToProto(s scoring.Score, evaluatedAt time.Time) *pb.Score {
	out := &pb.Score{
		Rated:              s.Rated,
		Composite:          s.Composite,
		Tier:               s.Tier,
		DiscountPercent:    int32(s.DiscountPercent),
		DepositMultiplier:  s.DepositMultiplier,
		TierNote:           s.TierNote,
		Flagged:            s.Flagged,
		PointsToNextTier:   s.PointsToNextTier,
		NextTier:           s.NextTier,
		Confidence:         string(s.Confidence),
		Handling:           string(s.Handling),
		Headline:           s.Headline,
		StayCount:          int32(s.StayCount),
		DisputedCount:      int32(s.DisputedCount),
		EffectiveStayCount: s.EffectiveStayCount,
		IncidentCount:      int32(s.IncidentCount),
		CommendationCount:  int32(s.CommendationCount),
		RawAverage:         s.RawAverage,
		AdjustedAverage:    s.AdjustedAverage,
		BaseScore:          s.BaseScore,
		IncidentPenalty:    s.IncidentPenalty,
		CommendationBonus:  s.CommendationBonus,
		TenureBonus:        s.TenureBonus,
		TenureYears:        s.TenureYears,
		ModelVersion:       ModelVersion,
		EvaluatedAt:        tsOrNil(evaluatedAt),
	}
	for _, d := range s.Dimensions {
		out.Dimensions = append(out.Dimensions, &pb.DimensionScore{
			Dimension: string(d.Dimension), Label: d.Label,
			Average: d.Average, Weight: d.Weight, Contributes: d.Contributes,
		})
	}
	for _, f := range s.Factors {
		out.Factors = append(out.Factors, &pb.Factor{
			Kind: f.Kind, Description: f.Description, Impact: f.Impact,
		})
	}
	return out
}

// ScoreFromProto is the inverse of ScoreToProto.
func ScoreFromProto(p *pb.Score) scoring.Score {
	if p == nil {
		return scoring.Score{}
	}
	s := scoring.Score{
		Rated:              p.GetRated(),
		Composite:          p.GetComposite(),
		Tier:               p.GetTier(),
		DiscountPercent:    int(p.GetDiscountPercent()),
		DepositMultiplier:  p.GetDepositMultiplier(),
		TierNote:           p.GetTierNote(),
		Flagged:            p.GetFlagged(),
		PointsToNextTier:   p.GetPointsToNextTier(),
		NextTier:           p.GetNextTier(),
		Confidence:         scoring.Confidence(p.GetConfidence()),
		Handling:           scoring.Handling(p.GetHandling()),
		Headline:           p.GetHeadline(),
		StayCount:          int(p.GetStayCount()),
		DisputedCount:      int(p.GetDisputedCount()),
		EffectiveStayCount: p.GetEffectiveStayCount(),
		IncidentCount:      int(p.GetIncidentCount()),
		CommendationCount:  int(p.GetCommendationCount()),
		RawAverage:         p.GetRawAverage(),
		AdjustedAverage:    p.GetAdjustedAverage(),
		BaseScore:          p.GetBaseScore(),
		IncidentPenalty:    p.GetIncidentPenalty(),
		CommendationBonus:  p.GetCommendationBonus(),
		TenureBonus:        p.GetTenureBonus(),
		TenureYears:        p.GetTenureYears(),
	}
	for _, d := range p.GetDimensions() {
		s.Dimensions = append(s.Dimensions, scoring.DimensionScore{
			Dimension: domain.Dimension(d.GetDimension()), Label: d.GetLabel(),
			Average: d.GetAverage(), Weight: d.GetWeight(), Contributes: d.GetContributes(),
		})
	}
	for _, f := range p.GetFactors() {
		s.Factors = append(s.Factors, scoring.Factor{
			Kind: f.GetKind(), Description: f.GetDescription(), Impact: f.GetImpact(),
		})
	}
	return s
}

// ModelToProto publishes the model constants.
func ModelToProto(m scoring.Model) *pb.DescribeModelResponse {
	out := &pb.DescribeModelResponse{
		ModelVersion:              ModelVersion,
		ReviewHalfLifeDays:        m.ReviewHalfLife,
		IncidentHalfLifeDays:      m.IncidentHalfLife,
		CommendationHalfLifeDays:  m.CommendationHalfLife,
		PriorMean:                 m.PriorMean,
		PriorStrength:             m.PriorStrength,
		ScoreMin:                  m.ScoreMin,
		ScoreMax:                  m.ScoreMax,
		NewGuestScore:             m.NewGuestScore,
		AnchorQuality:             m.AnchorQuality,
		AnchorScore:               m.AnchorScore,
		PointsPerQualityPointUp:   m.PointsPerUp,
		PointsPerQualityPointDown: m.PointsPerDown,
		TenurePointsPerYear:       m.TenurePointsPerYear,
		TenureMaxPoints:           m.TenureMaxPoints,
		SeverityMultipliers: map[string]float64{
			string(domain.SevMinor):    domain.SevMinor.Multiplier(),
			string(domain.SevModerate): domain.SevModerate.Multiplier(),
			string(domain.SevSevere):   domain.SevSevere.Multiplier(),
		},
	}
	// AllDimensions rather than ranging the weights map: a map has no order,
	// and the UI renders these in a fixed sequence.
	for _, d := range domain.AllDimensions {
		out.Dimensions = append(out.Dimensions, &pb.DimensionWeight{
			Dimension: string(d), Label: d.Label(), Weight: m.Weights[d],
		})
	}
	for _, t := range m.Tiers {
		out.Tiers = append(out.Tiers, &pb.Tier{
			Min: t.Min, Name: t.Name, DiscountPercent: int32(t.Discount),
			DepositMultiplier: t.DepositMultiplier, Flagged: t.Flagged, Note: t.Description,
		})
	}
	for _, e := range domain.IncidentCatalog {
		out.IncidentCatalog = append(out.IncidentCatalog, &pb.IncidentCatalogEntry{
			Type: string(e.Type), Label: e.Label, BasePenalty: e.BasePenalty, Description: e.Description,
		})
	}
	for _, e := range domain.CommendationCatalog {
		out.CommendationCatalog = append(out.CommendationCatalog, &pb.CommendationCatalogEntry{
			Type: string(e.Type), Label: e.Label, BaseBonus: e.BaseBonus, Description: e.Description,
		})
	}
	return out
}
