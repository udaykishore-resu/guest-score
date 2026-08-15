// Package domain holds the core entities of Guest Score and their validation
// rules. It imports nothing from the rest of the codebase, which keeps the
// dependency graph pointing inward and makes the scoring engine trivially
// testable in isolation.
package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Dimension is one axis of a post-stay assessment.
type Dimension string

const (
	DimHouseRules    Dimension = "house_rules"
	DimPropertyCare  Dimension = "property_care"
	DimCommunication Dimension = "communication"
	DimNoise         Dimension = "noise"
	DimAccuracy      Dimension = "accuracy"
)

// AllDimensions is the canonical ordering used everywhere a breakdown is
// rendered, so the UI never has to sort a map.
var AllDimensions = []Dimension{
	DimHouseRules,
	DimPropertyCare,
	DimCommunication,
	DimNoise,
	DimAccuracy,
}

// Label returns the human-readable name of a dimension.
func (d Dimension) Label() string {
	switch d {
	case DimHouseRules:
		return "House rules compliance"
	case DimPropertyCare:
		return "Property care"
	case DimCommunication:
		return "Communication"
	case DimNoise:
		return "Noise & neighbor impact"
	case DimAccuracy:
		return "Booking accuracy"
	}
	return string(d)
}

// Ratings holds one review's score on each dimension. Each value is an integer
// from 1 to 5 inclusive.
type Ratings struct {
	HouseRules    int `json:"house_rules"`
	PropertyCare  int `json:"property_care"`
	Communication int `json:"communication"`
	Noise         int `json:"noise"`
	Accuracy      int `json:"accuracy"`
}

// Get returns the rating for a dimension.
func (r Ratings) Get(d Dimension) int {
	switch d {
	case DimHouseRules:
		return r.HouseRules
	case DimPropertyCare:
		return r.PropertyCare
	case DimCommunication:
		return r.Communication
	case DimNoise:
		return r.Noise
	case DimAccuracy:
		return r.Accuracy
	}
	return 0
}

// Validate enforces FR-009: every dimension is an integer in [1,5], and the
// whole submission is rejected if any single value is out of range.
func (r Ratings) Validate() FieldErrors {
	errs := FieldErrors{}
	check := func(field string, v int) {
		if v < 1 || v > 5 {
			errs[field] = fmt.Sprintf("must be an integer from 1 to 5, got %d", v)
		}
	}
	check("ratings.house_rules", r.HouseRules)
	check("ratings.property_care", r.PropertyCare)
	check("ratings.communication", r.Communication)
	check("ratings.noise", r.Noise)
	check("ratings.accuracy", r.Accuracy)
	return errs
}

// IncidentType enumerates the discrete negative events a host may flag.
type IncidentType string

const (
	IncPropertyDamage    IncidentType = "property_damage"
	IncNoiseComplaint    IncidentType = "noise_complaint"
	IncUnauthorizedGuest IncidentType = "unauthorized_guests"
	IncRulesViolation    IncidentType = "house_rules_violation"
	IncLateCheckout      IncidentType = "late_checkout"
	IncPaymentIssue      IncidentType = "payment_issue"
)

// IncidentCatalogEntry describes an incident type for the API and UI.
type IncidentCatalogEntry struct {
	Type        IncidentType `json:"type"`
	Label       string       `json:"label"`
	BasePenalty float64      `json:"base_penalty"`
	Description string       `json:"description"`
}

// IncidentCatalog is the published list of incident types and their base
// penalties in composite-score points, before severity and recency scaling.
var IncidentCatalog = []IncidentCatalogEntry{
	{IncPropertyDamage, "Property damage", 14.0, "Physical damage to the property or its contents."},
	{IncUnauthorizedGuest, "Unauthorized guests", 10.0, "More occupants than the booking allowed, or an unapproved party."},
	{IncNoiseComplaint, "Noise complaint", 9.0, "A complaint from neighbors, building management, or local authorities."},
	{IncRulesViolation, "House rules violation", 7.0, "Smoking, pets, or other explicitly prohibited conduct."},
	{IncPaymentIssue, "Payment issue", 6.0, "Chargeback, failed payment, or refusal to settle documented charges."},
	{IncLateCheckout, "Late checkout", 3.0, "Departure past the agreed time without arrangement."},
}

// BasePenalty returns the unscaled point penalty for an incident type.
func (t IncidentType) BasePenalty() float64 {
	for _, e := range IncidentCatalog {
		if e.Type == t {
			return e.BasePenalty
		}
	}
	return 0
}

// Label returns the human-readable name of an incident type.
func (t IncidentType) Label() string {
	for _, e := range IncidentCatalog {
		if e.Type == t {
			return e.Label
		}
	}
	return string(t)
}

// Valid reports whether the incident type is in the catalog.
func (t IncidentType) Valid() bool {
	for _, e := range IncidentCatalog {
		if e.Type == t {
			return true
		}
	}
	return false
}

// Severity scales an incident's penalty.
type Severity string

const (
	SevMinor    Severity = "minor"
	SevModerate Severity = "moderate"
	SevSevere   Severity = "severe"
)

// Multiplier returns the penalty scaling factor for a severity level.
func (s Severity) Multiplier() float64 {
	switch s {
	case SevMinor:
		return 0.5
	case SevModerate:
		return 1.0
	case SevSevere:
		return 1.8
	}
	return 1.0
}

// Valid reports whether the severity is a known level.
func (s Severity) Valid() bool {
	return s == SevMinor || s == SevModerate || s == SevSevere
}

// Incident is a discrete negative event attached to a review.
type Incident struct {
	Type     IncidentType `json:"type"`
	Severity Severity     `json:"severity"`
	Note     string       `json:"note,omitempty"`
}

// Guest is a person who stays at properties.
//
// Note that there is no score field. Per the spec, a score is always computed
// from reviews and never stored; persisting it would let the two drift apart.
type Guest struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	Phone     string    `json:"phone,omitempty"`
	City      string    `json:"city,omitempty"`
	Verified  bool      `json:"verified"`
	JoinedAt  time.Time `json:"joined_at"`
	AvatarSeed string   `json:"avatar_seed"`
}

// Validate checks the required identity fields on a guest.
func (g Guest) Validate() FieldErrors {
	errs := FieldErrors{}
	if strings.TrimSpace(g.Name) == "" {
		errs["name"] = "is required"
	}
	if len(g.Name) > 120 {
		errs["name"] = "must be 120 characters or fewer"
	}
	email := strings.TrimSpace(g.Email)
	if email == "" {
		errs["email"] = "is required"
	} else if !strings.Contains(email, "@") || strings.HasPrefix(email, "@") || strings.HasSuffix(email, "@") {
		errs["email"] = "must be a valid email address"
	}
	return errs
}

// Review is one host's structured assessment of one guest for one stay.
type Review struct {
	ID          string     `json:"id"`
	GuestID     string     `json:"guest_id"`
	HostID      string     `json:"host_id"`
	HostName    string     `json:"host_name"`
	PropertyID  string     `json:"property_id"`
	PropertyName string    `json:"property_name"`
	StayID      string     `json:"stay_id"`
	Ratings     Ratings    `json:"ratings"`
	Incidents   []Incident `json:"incidents"`
	Comment     string     `json:"comment"`
	CheckIn     time.Time  `json:"check_in"`
	CheckOut    time.Time  `json:"check_out"`
	SubmittedAt time.Time  `json:"submitted_at"`
}

// Validate enforces the review submission rules from the spec.
func (r Review) Validate() FieldErrors {
	errs := r.Ratings.Validate()
	if strings.TrimSpace(r.GuestID) == "" {
		errs["guest_id"] = "is required"
	}
	if strings.TrimSpace(r.HostID) == "" {
		errs["host_id"] = "is required"
	}
	if strings.TrimSpace(r.StayID) == "" {
		errs["stay_id"] = "is required"
	}
	if len(r.Comment) > 2000 {
		errs["comment"] = "must be 2000 characters or fewer"
	}
	for i, inc := range r.Incidents {
		if !inc.Type.Valid() {
			errs[fmt.Sprintf("incidents.%d.type", i)] = fmt.Sprintf("unknown incident type %q", inc.Type)
		}
		if !inc.Severity.Valid() {
			errs[fmt.Sprintf("incidents.%d.severity", i)] = fmt.Sprintf("must be minor, moderate, or severe, got %q", inc.Severity)
		}
	}
	if !r.CheckIn.IsZero() && !r.CheckOut.IsZero() && r.CheckOut.Before(r.CheckIn) {
		errs["check_out"] = "must not be before check_in"
	}
	return errs
}

// Nights returns the length of the stay in nights.
func (r Review) Nights() int {
	if r.CheckIn.IsZero() || r.CheckOut.IsZero() {
		return 0
	}
	d := r.CheckOut.Sub(r.CheckIn).Hours() / 24
	if d < 0 {
		return 0
	}
	return int(d + 0.5)
}

// SortReviewsByRecency orders reviews newest-submitted first, in place.
func SortReviewsByRecency(rs []Review) {
	sort.SliceStable(rs, func(i, j int) bool {
		return rs[i].SubmittedAt.After(rs[j].SubmittedAt)
	})
}

// FieldErrors maps a field path to a validation message.
type FieldErrors map[string]string

// Any reports whether any validation errors were recorded.
func (f FieldErrors) Any() bool { return len(f) > 0 }

// Error renders the field errors as a single deterministic string.
func (f FieldErrors) Error() string {
	if len(f) == 0 {
		return ""
	}
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+" "+f[k])
	}
	return strings.Join(parts, "; ")
}
