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

// Label returns the human-readable name of a dimension, in hotel terms.
func (d Dimension) Label() string {
	switch d {
	case DimHouseRules:
		return "Hotel policy compliance"
	case DimPropertyCare:
		return "Room condition"
	case DimCommunication:
		return "Staff interaction"
	case DimNoise:
		return "Noise & other guests"
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
	// Named in the disclosure's incident categories alongside policy violations,
	// damage, and payment issues.
	IncMisconduct IncidentType = "general_misconduct"
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
// Penalties are expressed directly in bureau points on the 300–850 scale, so
// there is one unit throughout the engine and no hidden conversion factor.
// Base penalties are in points on the published 0–1000 scale, before severity
// and recency scaling. They are calibrated to the two worked examples in the
// invention disclosure:
//
//	"Minor policy violation: −50 points"  → policy violation 100 × minor 0.5  = 50
//	"Severe property damage: −100 points" → property damage  56 × severe 1.8 ≈ 100
var IncidentCatalog = []IncidentCatalogEntry{
	{IncRulesViolation, "Hotel policy violation", 100.0, "Smoking in a non-smoking room, pets, or other explicitly prohibited conduct."},
	{IncMisconduct, "General misconduct", 90.0, "Abusive or threatening behaviour toward staff or other guests."},
	{IncUnauthorizedGuest, "Unauthorized occupants", 70.0, "More occupants than the reservation allowed, or an unapproved gathering."},
	{IncPaymentIssue, "Payment issue", 65.0, "Bounced payment, chargeback, or an unpaid bill."},
	{IncPropertyDamage, "Property damage", 56.0, "Physical damage to the room or its contents."},
	{IncNoiseComplaint, "Noise complaint", 50.0, "A complaint from neighbouring rooms, security, or local authorities."},
	{IncLateCheckout, "Late checkout", 25.0, "Departure past the agreed time without arrangement."},
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

// Incident is a discrete negative event attached to a stay record.
type Incident struct {
	Type     IncidentType `json:"type"`
	Severity Severity     `json:"severity"`
	Note     string       `json:"note,omitempty"`
	// Evidence references supporting material (photo URLs, document IDs). The
	// disclosure requires incident reports to carry evidence; a score that can
	// deny someone a booking should not rest on an unsupported assertion.
	Evidence []string `json:"evidence,omitempty"`
}

// CommendationType enumerates the discrete positive events staff may record.
//
// The original app moved a running balance by explicit deltas in both
// directions — "-5 late checkout" but also "+10 room left in excellent
// condition". Penalties alone cannot express that, and without an upward
// channel a loyalty tier is unreachable for all but flawless guests, so
// commendations are a first-class counterpart to incidents.
type CommendationType string

const (
	ComExceptionalCare  CommendationType = "exceptional_room_care"
	ComStaffPraise      CommendationType = "staff_commendation"
	ComLoyalReturn      CommendationType = "repeat_stay"
	ComEarlyFlexibility CommendationType = "accommodating"
	ComReferral         CommendationType = "referral"
)

// CommendationCatalogEntry describes a commendation type for the API and UI.
type CommendationCatalogEntry struct {
	Type      CommendationType `json:"type"`
	Label     string           `json:"label"`
	BaseBonus float64          `json:"base_bonus"`
	Description string         `json:"description"`
}

// CommendationCatalog is the published list of positive events and the points
// they add, before recency scaling. Bonuses are deliberately smaller than the
// matching penalties: earning tier status should be slower than losing it.
// Bonuses are in points on the 0–1000 scale. Deliberately smaller per event
// than the matching penalties: standing should be slower to earn than to lose.
// The disclosure's "+100 for positive stays over a year" is handled separately
// as the tenure factor, not as a single commendation.
var CommendationCatalog = []CommendationCatalogEntry{
	{ComExceptionalCare, "Exceptional room care", 30.0, "Room left in notably better condition than expected."},
	{ComStaffPraise, "Staff commendation", 25.0, "Staff specifically noted this guest as a pleasure to host."},
	{ComEarlyFlexibility, "Accommodating", 20.0, "Accepted a room change, early checkout, or similar without friction."},
	{ComLoyalReturn, "Repeat stay", 15.0, "A returning guest with no issues on the stay."},
	{ComReferral, "Referral", 15.0, "Referred another guest who completed a stay."},
}

// BaseBonus returns the unscaled point bonus for a commendation type.
func (t CommendationType) BaseBonus() float64 {
	for _, e := range CommendationCatalog {
		if e.Type == t {
			return e.BaseBonus
		}
	}
	return 0
}

// Label returns the human-readable name of a commendation type.
func (t CommendationType) Label() string {
	for _, e := range CommendationCatalog {
		if e.Type == t {
			return e.Label
		}
	}
	return string(t)
}

// Valid reports whether the commendation type is in the catalog.
func (t CommendationType) Valid() bool {
	for _, e := range CommendationCatalog {
		if e.Type == t {
			return true
		}
	}
	return false
}

// Commendation is a discrete positive event attached to a stay record.
type Commendation struct {
	Type CommendationType `json:"type"`
	Note string           `json:"note,omitempty"`
}

// Member is a reporting institution — an independent hotel or a chain. The
// bureau model turns on this: members file stay records and any member can pull
// any guest's score, so the guest carries one standing across all of them
// rather than a private note at one chain.
type Member struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind"` // "chain" | "independent"
	City     string    `json:"city,omitempty"`
	JoinedAt time.Time `json:"joined_at"`
}

// InquiryPurpose records why a member pulled a score.
type InquiryPurpose string

const (
	InquiryCheckIn InquiryPurpose = "check_in"
	InquiryBooking InquiryPurpose = "booking"
	InquiryReview  InquiryPurpose = "manual_review"
)

// Inquiry is a record that a member looked up a guest.
//
// Bureaus log this for the same reason credit bureaus do: the guest is entitled
// to know who has been asking. Unlike a credit hard inquiry it does not affect
// the score — a hotel checking a guest says nothing about that guest.
type Inquiry struct {
	ID         string         `json:"id"`
	GuestID    string         `json:"guest_id"`
	MemberID   string         `json:"member_id"`
	MemberName string         `json:"member_name"`
	Purpose    InquiryPurpose `json:"purpose"`
	At         time.Time      `json:"at"`
}

// DisputeStatus tracks a guest's challenge to a stay record.
type DisputeStatus string

const (
	DisputeNone     DisputeStatus = ""
	DisputeOpen     DisputeStatus = "open"     // under review; excluded from scoring
	DisputeUpheld   DisputeStatus = "upheld"   // guest was right; permanently excluded
	DisputeRejected DisputeStatus = "rejected" // record stands; counts normally
)

// CountsTowardScore reports whether a record in this dispute state should be
// scored. An open or upheld dispute is excluded — scoring a record the guest is
// actively contesting, and which may be wrong, is the thing a dispute process
// exists to prevent.
func (d DisputeStatus) CountsTowardScore() bool {
	return d != DisputeOpen && d != DisputeUpheld
}

// Label renders the dispute state for the UI.
func (d DisputeStatus) Label() string {
	switch d {
	case DisputeOpen:
		return "Disputed — under review"
	case DisputeUpheld:
		return "Disputed — removed"
	case DisputeRejected:
		return "Disputed — record stands"
	}
	return ""
}

// Dispute is a guest's challenge to one stay record.
type Dispute struct {
	Status     DisputeStatus `json:"status,omitempty"`
	Reason     string        `json:"reason,omitempty"`
	RaisedAt   *time.Time    `json:"raised_at,omitempty"`
	ResolvedAt *time.Time    `json:"resolved_at,omitempty"`
	Resolution string        `json:"resolution,omitempty"`
}

// Guest is a person who stays at member properties.
//
// Note that there is no score field. Per the spec, a score is always computed
// from reviews and never stored; persisting it would let the two drift apart.
type Guest struct {
	ID string `json:"id"`

	// GlobalID is the bureau's permanent identifier — the thing that makes the
	// file portable. A guest who opens a file in Mumbai on an Aadhaar and later
	// presents a passport in Lisbon resolves to this same value, which is the
	// entire deterrent: standing follows the person across borders.
	GlobalID GlobalID `json:"global_id"`

	// Nationality is the country whose documents opened the file.
	Nationality Country `json:"nationality,omitempty"`

	// Documents are the identity documents on file. Numbers are never stored,
	// only keyed hashes and the last four characters (see identity.go).
	Documents []IdentityDocument `json:"documents"`

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
	MemberID      string         `json:"member_id"`
	MemberName    string         `json:"member_name"`
	StayID        string         `json:"stay_id"`
	RoomNumber    string         `json:"room_number,omitempty"`
	Ratings       Ratings        `json:"ratings"`
	Incidents     []Incident     `json:"incidents"`
	Commendations []Commendation `json:"commendations"`
	Comment       string         `json:"comment"`
	CheckIn       time.Time      `json:"check_in"`
	CheckOut      time.Time      `json:"check_out"`
	SubmittedAt   time.Time      `json:"submitted_at"`
	Dispute       Dispute        `json:"dispute"`
}

// Scoreable reports whether this record feeds the score.
func (r Review) Scoreable() bool { return r.Dispute.Status.CountsTowardScore() }

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
	for i, com := range r.Commendations {
		if !com.Type.Valid() {
			errs[fmt.Sprintf("commendations.%d.type", i)] = fmt.Sprintf("unknown commendation type %q", com.Type)
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
