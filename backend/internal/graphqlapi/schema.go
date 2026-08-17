// Package graphqlapi exposes the bureau over GraphQL.
//
// The REST surface stays exactly as it is; this is additive. The case for
// GraphQL here is specific rather than fashionable: the directory screen needs
// a guest, their score, their factor breakdown, their documents and their
// inquiry log, and over REST that is four requests whose responses the client
// stitches together. Worse, the guest profile needs a fifth. GraphQL lets the
// screen state its whole requirement once, which matters most on the mobile
// front-desk client where each round trip is a hotel wifi round trip.
//
// The batching below is the part that makes it honest. A naive resolver would
// compute a score per guest in the list, which is the N+1 problem with extra
// steps; the list resolver computes all of them in one Batch call before any
// field resolver runs, so a page of guests costs one scoring call whether it is
// served locally or over gRPC.
package graphqlapi

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/graphql-go/graphql"
	"github.com/graphql-go/handler"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoring"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoringsvc"
	"github.com/udaykishore-resu/guest-score/backend/internal/search"
	"github.com/udaykishore-resu/guest-score/backend/internal/store"
)

// Deps are what the resolvers need.
type Deps struct {
	Store  store.Store
	Scorer scoringsvc.Scorer
	Search search.Index
	Now    func() scoring.Time
}

// scoredGuest is the resolved shape of a guest plus their score.
type scoredGuest struct {
	Guest domain.Guest
	Score scoring.Score
}

// --- types -------------------------------------------------------------------

var dimensionScoreType = graphql.NewObject(graphql.ObjectConfig{
	Name: "DimensionScore",
	Fields: graphql.Fields{
		"dimension":   &graphql.Field{Type: graphql.String},
		"label":       &graphql.Field{Type: graphql.String},
		"average":     &graphql.Field{Type: graphql.Float},
		"weight":      &graphql.Field{Type: graphql.Float},
		"contributes": &graphql.Field{Type: graphql.Float},
	},
})

var factorType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "Factor",
	Description: "One human-readable reason the score is what it is.",
	Fields: graphql.Fields{
		"kind":        &graphql.Field{Type: graphql.String},
		"description": &graphql.Field{Type: graphql.String},
		"impact":      &graphql.Field{Type: graphql.Float},
	},
})

var scoreType = graphql.NewObject(graphql.ObjectConfig{
	Name: "Score",
	Fields: graphql.Fields{
		"rated":              &graphql.Field{Type: graphql.Boolean},
		"composite":          &graphql.Field{Type: graphql.Float},
		"tier":               &graphql.Field{Type: graphql.String},
		"tierNote":           &graphql.Field{Type: graphql.String, Resolve: field("TierNote")},
		"discountPercent":    &graphql.Field{Type: graphql.Int, Resolve: field("DiscountPercent")},
		"depositMultiplier":  &graphql.Field{Type: graphql.Float, Resolve: field("DepositMultiplier")},
		"flagged":            &graphql.Field{Type: graphql.Boolean},
		"nextTier":           &graphql.Field{Type: graphql.String, Resolve: field("NextTier")},
		"pointsToNextTier":   &graphql.Field{Type: graphql.Float, Resolve: field("PointsToNextTier")},
		"confidence":         &graphql.Field{Type: graphql.String},
		"handling":           &graphql.Field{Type: graphql.String},
		"headline":           &graphql.Field{Type: graphql.String},
		"stayCount":          &graphql.Field{Type: graphql.Int, Resolve: field("StayCount")},
		"disputedCount":      &graphql.Field{Type: graphql.Int, Resolve: field("DisputedCount")},
		"incidentCount":      &graphql.Field{Type: graphql.Int, Resolve: field("IncidentCount")},
		"commendationCount":  &graphql.Field{Type: graphql.Int, Resolve: field("CommendationCount")},
		"incidentPenalty":    &graphql.Field{Type: graphql.Float, Resolve: field("IncidentPenalty")},
		"commendationBonus":  &graphql.Field{Type: graphql.Float, Resolve: field("CommendationBonus")},
		"tenureBonus":        &graphql.Field{Type: graphql.Float, Resolve: field("TenureBonus")},
		"tenureYears":        &graphql.Field{Type: graphql.Float, Resolve: field("TenureYears")},
		"baseScore":          &graphql.Field{Type: graphql.Float, Resolve: field("BaseScore")},
		"adjustedAverage":    &graphql.Field{Type: graphql.Float, Resolve: field("AdjustedAverage")},
		"modelVersion": &graphql.Field{
			Type:        graphql.String,
			Description: "The model constants that produced this score. A score without it is not reproducible.",
			Resolve: func(graphql.ResolveParams) (any, error) {
				return scoringsvc.ModelVersion, nil
			},
		},
		"dimensions": &graphql.Field{Type: graphql.NewList(dimensionScoreType)},
		"factors":    &graphql.Field{Type: graphql.NewList(factorType)},
	},
})

var documentType = graphql.NewObject(graphql.ObjectConfig{
	Name: "IdentityDocument",
	Description: "An identity document on file. The number is never stored or returned — " +
		"only a keyed hash, which is not exposed here, and the last four characters.",
	Fields: graphql.Fields{
		"country":   &graphql.Field{Type: graphql.String},
		"type":      &graphql.Field{Type: graphql.String, Resolve: field("Type")},
		"label": &graphql.Field{Type: graphql.String, Resolve: func(p graphql.ResolveParams) (any, error) {
			d, ok := p.Source.(domain.IdentityDocument)
			if !ok {
				return nil, nil
			}
			return d.Type.Label(), nil
		}},
		"last4":     &graphql.Field{Type: graphql.String, Resolve: field("Last4")},
		"verified":  &graphql.Field{Type: graphql.Boolean},
		"authority": &graphql.Field{Type: graphql.String},
		"portable": &graphql.Field{
			Type:        graphql.Boolean,
			Description: "Whether another country's members can resolve the file with this document.",
			Resolve: func(p graphql.ResolveParams) (any, error) {
				d, ok := p.Source.(domain.IdentityDocument)
				if !ok {
					return false, nil
				}
				return d.Portable(), nil
			},
		},
		"addedAt": &graphql.Field{Type: graphql.DateTime, Resolve: field("AddedAt")},
	},
})

var incidentType = graphql.NewObject(graphql.ObjectConfig{
	Name: "Incident",
	Fields: graphql.Fields{
		"type":     &graphql.Field{Type: graphql.String},
		"severity": &graphql.Field{Type: graphql.String},
		"note":     &graphql.Field{Type: graphql.String},
		"label": &graphql.Field{Type: graphql.String, Resolve: func(p graphql.ResolveParams) (any, error) {
			i, ok := p.Source.(domain.Incident)
			if !ok {
				return nil, nil
			}
			return i.Type.Label(), nil
		}},
	},
})

var commendationType = graphql.NewObject(graphql.ObjectConfig{
	Name: "Commendation",
	Fields: graphql.Fields{
		"type": &graphql.Field{Type: graphql.String},
		"note": &graphql.Field{Type: graphql.String},
		"label": &graphql.Field{Type: graphql.String, Resolve: func(p graphql.ResolveParams) (any, error) {
			c, ok := p.Source.(domain.Commendation)
			if !ok {
				return nil, nil
			}
			return c.Type.Label(), nil
		}},
	},
})

var disputeType = graphql.NewObject(graphql.ObjectConfig{
	Name: "Dispute",
	Fields: graphql.Fields{
		"status": &graphql.Field{Type: graphql.String},
		"reason": &graphql.Field{Type: graphql.String},
		"label": &graphql.Field{Type: graphql.String, Resolve: func(p graphql.ResolveParams) (any, error) {
			d, ok := p.Source.(domain.Dispute)
			if !ok {
				return nil, nil
			}
			return d.Status.Label(), nil
		}},
		"countsTowardScore": &graphql.Field{Type: graphql.Boolean, Resolve: func(p graphql.ResolveParams) (any, error) {
			d, ok := p.Source.(domain.Dispute)
			if !ok {
				return true, nil
			}
			return d.Status.CountsTowardScore(), nil
		}},
	},
})

var reviewType = graphql.NewObject(graphql.ObjectConfig{
	Name: "StayRecord",
	Fields: graphql.Fields{
		"id":            &graphql.Field{Type: graphql.String, Resolve: field("ID")},
		"guestId":       &graphql.Field{Type: graphql.String, Resolve: field("GuestID")},
		"memberId":      &graphql.Field{Type: graphql.String, Resolve: field("MemberID")},
		"memberName":    &graphql.Field{Type: graphql.String, Resolve: field("MemberName")},
		"propertyName":  &graphql.Field{Type: graphql.String, Resolve: field("PropertyName")},
		"stayId":        &graphql.Field{Type: graphql.String, Resolve: field("StayID")},
		"roomNumber":    &graphql.Field{Type: graphql.String, Resolve: field("RoomNumber")},
		"comment":       &graphql.Field{Type: graphql.String},
		"checkIn":       &graphql.Field{Type: graphql.DateTime, Resolve: field("CheckIn")},
		"checkOut":      &graphql.Field{Type: graphql.DateTime, Resolve: field("CheckOut")},
		"submittedAt":   &graphql.Field{Type: graphql.DateTime, Resolve: field("SubmittedAt")},
		"nights":        &graphql.Field{Type: graphql.Int, Resolve: func(p graphql.ResolveParams) (any, error) {
			r, ok := p.Source.(domain.Review)
			if !ok {
				return 0, nil
			}
			return r.Nights(), nil
		}},
		"incidents":     &graphql.Field{Type: graphql.NewList(incidentType)},
		"commendations": &graphql.Field{Type: graphql.NewList(commendationType)},
		"dispute":       &graphql.Field{Type: disputeType},
	},
})

var inquiryType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "Inquiry",
	Description: "A record that a member pulled this guest's file. Inquiries never affect the score.",
	Fields: graphql.Fields{
		"id":         &graphql.Field{Type: graphql.String, Resolve: field("ID")},
		"memberId":   &graphql.Field{Type: graphql.String, Resolve: field("MemberID")},
		"memberName": &graphql.Field{Type: graphql.String, Resolve: field("MemberName")},
		"purpose":    &graphql.Field{Type: graphql.String},
		"at":         &graphql.Field{Type: graphql.DateTime},
	},
})

// field builds a resolver that reads a Go struct field whose name differs from
// the GraphQL field name. graphql-go matches case-insensitively on the exact
// name, which misses ID/URL-style initialisms and camelCase renames.
func field(name string) graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (any, error) {
		return graphql.DefaultResolveFn(graphql.ResolveParams{
			Source: p.Source, Args: p.Args, Info: graphql.ResolveInfo{FieldName: name}, Context: p.Context,
		})
	}
}

// --- schema ------------------------------------------------------------------

// New builds the executable schema.
func New(d Deps) (graphql.Schema, error) {
	if d.Now == nil {
		d.Now = scoring.Now
	}

	guestType := graphql.NewObject(graphql.ObjectConfig{
		Name:        "Guest",
		Description: "A guest's bureau file.",
		Fields: graphql.Fields{
			"id":       &graphql.Field{Type: graphql.NewNonNull(graphql.String), Resolve: guestField("ID")},
			"globalId": &graphql.Field{Type: graphql.String, Resolve: guestField("GlobalID"),
				Description: "The permanent, portable identifier. The same file resolves under it in any country."},
			"name":        &graphql.Field{Type: graphql.String, Resolve: guestField("Name")},
			"email":       &graphql.Field{Type: graphql.String, Resolve: guestField("Email")},
			"city":        &graphql.Field{Type: graphql.String, Resolve: guestField("City")},
			"nationality": &graphql.Field{Type: graphql.String, Resolve: guestField("Nationality")},
			"verified":    &graphql.Field{Type: graphql.Boolean, Resolve: guestField("Verified")},
			"joinedAt":    &graphql.Field{Type: graphql.DateTime, Resolve: guestField("JoinedAt")},
			"documents": &graphql.Field{Type: graphql.NewList(documentType), Resolve: func(p graphql.ResolveParams) (any, error) {
				g, ok := p.Source.(scoredGuest)
				if !ok {
					return nil, nil
				}
				return g.Guest.Documents, nil
			}},
			"portable": &graphql.Field{
				Type:        graphql.Boolean,
				Description: "Whether any document on file lets another country resolve this guest.",
				Resolve: func(p graphql.ResolveParams) (any, error) {
					g, ok := p.Source.(scoredGuest)
					if !ok {
						return false, nil
					}
					for _, d := range g.Guest.Documents {
						if d.Portable() {
							return true, nil
						}
					}
					return false, nil
				},
			},
			"score": &graphql.Field{Type: scoreType, Resolve: func(p graphql.ResolveParams) (any, error) {
				g, ok := p.Source.(scoredGuest)
				if !ok {
					return nil, nil
				}
				return g.Score, nil
			}},
			"stays": &graphql.Field{
				Type:        graphql.NewList(reviewType),
				Description: "Stay records filed by members, newest first.",
				Resolve: func(p graphql.ResolveParams) (any, error) {
					g, ok := p.Source.(scoredGuest)
					if !ok {
						return nil, nil
					}
					return d.Store.ReviewsForGuest(g.Guest.ID)
				},
			},
			"inquiries": &graphql.Field{
				Type:        graphql.NewList(inquiryType),
				Description: "Who has pulled this file.",
				Resolve: func(p graphql.ResolveParams) (any, error) {
					g, ok := p.Source.(scoredGuest)
					if !ok {
						return nil, nil
					}
					return d.Store.InquiriesFor(g.Guest.ID), nil
				},
			},
		},
	})

	searchHitType := graphql.NewObject(graphql.ObjectConfig{
		Name: "SearchHit",
		Fields: graphql.Fields{
			"guest":     &graphql.Field{Type: guestType},
			"relevance": &graphql.Field{Type: graphql.Float},
		},
	})

	searchResultType := graphql.NewObject(graphql.ObjectConfig{
		Name: "SearchResult",
		Fields: graphql.Fields{
			"total": &graphql.Field{Type: graphql.Int},
			"engine": &graphql.Field{
				Type: graphql.String,
				Description: "Which engine answered: \"elasticsearch\" for fuzzy matching, " +
					"\"in-process\" for exact substring only. \"No file found\" means " +
					"something different in each case.",
			},
			"hits": &graphql.Field{Type: graphql.NewList(searchHitType)},
		},
	})

	tierType := graphql.NewObject(graphql.ObjectConfig{
		Name: "Tier",
		Fields: graphql.Fields{
			"min":               &graphql.Field{Type: graphql.Float},
			"name":              &graphql.Field{Type: graphql.String},
			"discountPercent":   &graphql.Field{Type: graphql.Int, Resolve: field("Discount")},
			"depositMultiplier": &graphql.Field{Type: graphql.Float, Resolve: field("DepositMultiplier")},
			"flagged":           &graphql.Field{Type: graphql.Boolean},
			"description":       &graphql.Field{Type: graphql.String},
		},
	})

	modelType := graphql.NewObject(graphql.ObjectConfig{
		Name:        "ScoringModel",
		Description: "Every constant the engine applies. Published so a score can be checked from outside the binary.",
		Fields: graphql.Fields{
			"modelVersion":  &graphql.Field{Type: graphql.String, Resolve: func(graphql.ResolveParams) (any, error) { return scoringsvc.ModelVersion, nil }},
			"scoreMin":      &graphql.Field{Type: graphql.Float, Resolve: field("ScoreMin")},
			"scoreMax":      &graphql.Field{Type: graphql.Float, Resolve: field("ScoreMax")},
			"newGuestScore": &graphql.Field{Type: graphql.Float, Resolve: field("NewGuestScore")},
			"priorMean":     &graphql.Field{Type: graphql.Float, Resolve: field("PriorMean")},
			"priorStrength": &graphql.Field{Type: graphql.Float, Resolve: field("PriorStrength")},
			"reviewHalfLifeDays":   &graphql.Field{Type: graphql.Float, Resolve: field("ReviewHalfLife")},
			"incidentHalfLifeDays": &graphql.Field{Type: graphql.Float, Resolve: field("IncidentHalfLife")},
			"tenurePointsPerYear":  &graphql.Field{Type: graphql.Float, Resolve: field("TenurePointsPerYear")},
			"tenureMaxPoints":      &graphql.Field{Type: graphql.Float, Resolve: field("TenureMaxPoints")},
			"tiers":                &graphql.Field{Type: graphql.NewList(tierType)},
		},
	})

	query := graphql.NewObject(graphql.ObjectConfig{
		Name: "Query",
		Fields: graphql.Fields{
			"guest": &graphql.Field{
				Type: guestType,
				Args: graphql.FieldConfigArgument{
					"id": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
				},
				Resolve: func(p graphql.ResolveParams) (any, error) {
					id, _ := p.Args["id"].(string)
					g, err := d.Store.GetGuest(id)
					if err != nil {
						return nil, err
					}
					reviews, err := d.Store.ReviewsForGuest(id)
					if err != nil {
						return nil, err
					}
					now := d.Now()
					return scoredGuest{Guest: g, Score: d.Scorer.Score(p.Context, id, reviews, now)}, nil
				},
			},

			"guests": &graphql.Field{
				Type:        graphql.NewList(guestType),
				Description: "The directory. Scores for the whole page are computed in one batch.",
				Args: graphql.FieldConfigArgument{
					"search": &graphql.ArgumentConfig{Type: graphql.String},
					"tier":   &graphql.ArgumentConfig{Type: graphql.String},
					"limit":  &graphql.ArgumentConfig{Type: graphql.Int, DefaultValue: 50},
					"offset": &graphql.ArgumentConfig{Type: graphql.Int, DefaultValue: 0},
				},
				Resolve: func(p graphql.ResolveParams) (any, error) {
					return resolveDirectory(p, d)
				},
			},

			"searchGuests": &graphql.Field{
				Type:        searchResultType,
				Description: "Fuzzy directory search. Falls back to exact substring matching when Elasticsearch is not configured.",
				Args: graphql.FieldConfigArgument{
					"query":   &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
					"tier":    &graphql.ArgumentConfig{Type: graphql.String},
					"country": &graphql.ArgumentConfig{Type: graphql.String},
					"limit":   &graphql.ArgumentConfig{Type: graphql.Int, DefaultValue: 25},
				},
				Resolve: func(p graphql.ResolveParams) (any, error) {
					return resolveSearch(p, d)
				},
			},

			"scoringModel": &graphql.Field{
				Type: modelType,
				Resolve: func(graphql.ResolveParams) (any, error) {
					return d.Scorer.Model(), nil
				},
			},

			"health": &graphql.Field{
				Type: graphql.String,
				Resolve: func(graphql.ResolveParams) (any, error) {
					return "ok", nil
				},
			},
		},
	})

	return graphql.NewSchema(graphql.SchemaConfig{Query: query})
}

// guestField reads a field off the embedded domain.Guest.
func guestField(name string) graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (any, error) {
		g, ok := p.Source.(scoredGuest)
		if !ok {
			return nil, nil
		}
		return graphql.DefaultResolveFn(graphql.ResolveParams{
			Source: g.Guest, Info: graphql.ResolveInfo{FieldName: name}, Context: p.Context,
		})
	}
}

// resolveDirectory lists guests and scores the whole page in one call.
//
// This is the N+1 fix. Reviews are still fetched per guest — the store has no
// bulk read and adding one is a larger change — but scoring, which is the part
// that may cross a network, happens exactly once per page.
func resolveDirectory(p graphql.ResolveParams, d Deps) (any, error) {
	guests, err := d.Store.ListGuests()
	if err != nil {
		return nil, err
	}
	searchText, _ := p.Args["search"].(string)
	tier, _ := p.Args["tier"].(string)
	limit, _ := p.Args["limit"].(int)
	offset, _ := p.Args["offset"].(int)

	filtered := guests[:0:0]
	for _, g := range guests {
		if searchText != "" && !search.MatchesInProcess(g, searchText) {
			continue
		}
		filtered = append(filtered, g)
	}

	items := make([]scoringsvc.BatchItem, 0, len(filtered))
	for _, g := range filtered {
		reviews, err := d.Store.ReviewsForGuest(g.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, scoringsvc.BatchItem{GuestID: g.ID, Reviews: reviews})
	}

	scores := d.Scorer.Batch(p.Context, items, d.Now())
	out := make([]scoredGuest, 0, len(filtered))
	for i, g := range filtered {
		sc := scoring.Score{}
		if i < len(scores) {
			sc = scores[i]
		}
		if tier != "" && !strings.EqualFold(sc.Tier, tier) {
			continue
		}
		out = append(out, scoredGuest{Guest: g, Score: sc})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score.Composite != out[j].Score.Composite {
			return out[i].Score.Composite > out[j].Score.Composite
		}
		return out[i].Guest.Name < out[j].Guest.Name
	})

	if offset > 0 {
		if offset >= len(out) {
			return []scoredGuest{}, nil
		}
		out = out[offset:]
	}
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

type searchHit struct {
	Guest     scoredGuest
	Relevance float64
}

type searchResult struct {
	Total  int
	Engine string
	Hits   []searchHit
}

func resolveSearch(p graphql.ResolveParams, d Deps) (any, error) {
	q, _ := p.Args["query"].(string)
	tier, _ := p.Args["tier"].(string)
	country, _ := p.Args["country"].(string)
	limit, _ := p.Args["limit"].(int)

	res, err := d.Search.Search(p.Context, search.Query{
		Text: q, Tier: tier, Country: country, Limit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	// Elasticsearch answered: hydrate each hit from the store, because the index
	// is a search accelerator and never the source of truth for what is
	// displayed. A stale indexed score must not be shown as the guest's
	// standing.
	if res.Engine == search.EngineElastic {
		out := searchResult{Total: res.Total, Engine: res.Engine}
		for _, h := range res.Hits {
			g, err := d.Store.GetGuest(h.Doc.ID)
			if err != nil {
				continue // indexed but since deleted; skip rather than fail the query
			}
			reviews, err := d.Store.ReviewsForGuest(g.ID)
			if err != nil {
				return nil, err
			}
			out.Hits = append(out.Hits, searchHit{
				Guest:     scoredGuest{Guest: g, Score: d.Scorer.Score(p.Context, g.ID, reviews, d.Now())},
				Relevance: h.Relevance,
			})
		}
		return out, nil
	}

	// No search backend. Fall back to the substring scan and say so, so the
	// caller knows fuzzy matching was not attempted.
	guests, err := d.Store.ListGuests()
	if err != nil {
		return nil, err
	}
	out := searchResult{Engine: search.EngineInProcess}
	for _, g := range guests {
		if !search.MatchesInProcess(g, q) {
			continue
		}
		if country != "" && !strings.EqualFold(string(g.Nationality), country) {
			continue
		}
		reviews, err := d.Store.ReviewsForGuest(g.ID)
		if err != nil {
			return nil, err
		}
		sc := d.Scorer.Score(p.Context, g.ID, reviews, d.Now())
		if tier != "" && !strings.EqualFold(sc.Tier, tier) {
			continue
		}
		out.Hits = append(out.Hits, searchHit{Guest: scoredGuest{Guest: g, Score: sc}, Relevance: 1})
		if limit > 0 && len(out.Hits) >= limit {
			break
		}
	}
	out.Total = len(out.Hits)
	return out, nil
}

// Handler builds the HTTP handler.
func Handler(schema graphql.Schema, graphiql bool) http.Handler {
	return handler.New(&handler.Config{
		Schema: &schema,
		Pretty: true,
		// GraphiQL is an unauthenticated introspection console. It is a genuine
		// aid in development and a genuine liability anywhere else, which is
		// why it is a separate flag from GraphQL itself.
		GraphiQL:   graphiql,
		Playground: false,
	})
}

// WithTimeout bounds a GraphQL request.
//
// A single query can legitimately ask for the whole directory with every
// guest's stays and inquiries, which is a lot of store calls. Without a
// deadline, one expensive query holds a connection indefinitely.
func WithTimeout(h http.Handler, d time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}
