package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/udaykishore-resu/guest-score/backend/internal/domain"
	"github.com/udaykishore-resu/guest-score/backend/internal/scoring"
)

// The identity endpoints implement the front of the check-in flow: scan or key
// an ID, validate its format locally, verify it with the issuing authority,
// then resolve it to a bureau file — opening one if this is a first contact.

type resolveRequest struct {
	Country  domain.Country     `json:"country"`
	DocType  domain.DocumentType `json:"document_type"`
	Number   string             `json:"number"`
	MemberID string             `json:"member_id,omitempty"`
	Purpose  string             `json:"purpose,omitempty"`

	// Supplied only when opening a new file.
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

type resolveResponse struct {
	Matched bool                `json:"matched"`
	Opened  bool                `json:"opened"`
	Guest   *GuestSummary       `json:"guest,omitempty"`
	Document *documentView      `json:"document,omitempty"`

	// Verification records what the issuing authority said.
	Verification verificationView `json:"verification"`

	// Notice explains anything the desk agent should understand about this
	// resolution — a domestic-only document, an unverified authority, a first
	// contact.
	Notice string `json:"notice,omitempty"`
}

type documentView struct {
	Country   domain.Country      `json:"country"`
	Type      domain.DocumentType `json:"type"`
	Label     string              `json:"label"`
	Last4     string              `json:"last4"`
	Portable  bool                `json:"portable"`
	Authority string              `json:"authority"`
}

type verificationView struct {
	Status    string `json:"status"` // "verified" | "simulated" | "unavailable"
	Authority string `json:"authority"`
	Message   string `json:"message"`
}

// handleCountries publishes the accepted documents per country.
//
// The desk UI renders its form from this rather than hardcoding a list, so
// adding a country is a backend change only — and so the format hint shown to
// an agent can never drift from the pattern actually enforced.
func (s *Server) handleCountries(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"countries": domain.SupportedCountries(),
		"note": "A file can be opened with any accepted document. Only documents marked portable " +
			"let another country's members resolve the guest, so a domestic-only file should have a " +
			"passport attached before the guest travels.",
	})
}

// handleResolve is the scan-ID step of the check-in flow.
func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	var req resolveRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error(), nil)
		return
	}

	// Local format and checksum first. A typo caught here costs nothing; sent
	// upstream it costs a request and writes a near-miss identity number into
	// somebody's log.
	normalised, err := domain.ValidateDocumentNumber(req.Country, req.DocType, req.Number)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_document", err.Error(),
			domain.FieldErrors{"number": err.Error()})
		return
	}

	rules, _ := domain.RulesFor(req.Country)
	var accepted domain.AcceptedDocument
	for _, a := range rules.Documents {
		if a.Type == req.DocType {
			accepted = a
		}
	}

	// Verify with the issuing authority. In demo mode this is a fixture, and
	// the response says so rather than implying a real check happened.
	ver := s.verifier.Verify(r.Context(), req.Country, req.DocType, normalised)

	hash := domain.HashDocument(s.identityKey, req.Country, req.DocType, normalised)
	now := s.now()

	doc := domain.IdentityDocument{
		Country:   req.Country,
		Type:      req.DocType,
		Hash:      hash,
		Last4:     domain.Last4(normalised),
		Verified:  ver.OK,
		Authority: accepted.Authority,
		AddedAt:   now.Std(),
	}
	if ver.OK {
		t := now.Std()
		doc.VerifiedAt = &t
	}

	view := &documentView{
		Country: req.Country, Type: req.DocType, Label: req.DocType.Label(),
		Last4: doc.Last4, Portable: doc.Portable(), Authority: accepted.Authority,
	}
	vview := verificationView{Status: ver.Status, Authority: accepted.Authority, Message: ver.Message}

	// Already on file?
	if g, found := s.store.ResolveByDocument(hash); found {
		s.recordInquiry(g.ID, req)
		sum := s.summarise(r.Context(), g, now)
		writeJSON(w, http.StatusOK, resolveResponse{
			Matched: true, Guest: &sum, Document: view, Verification: vview,
			Notice: portabilityNotice(g, doc),
		})
		return
	}

	// First contact. Opening a file needs a name — the bureau will not create
	// an anonymous record that a score then attaches to.
	if strings.TrimSpace(req.Name) == "" {
		writeJSON(w, http.StatusOK, resolveResponse{
			Matched: false, Document: view, Verification: vview,
			Notice: "No file found for this document. Supply the guest's name to open one; " +
				"they will start at the standard opening score.",
		})
		return
	}

	g := domain.Guest{
		Name:        strings.TrimSpace(req.Name),
		Email:       strings.TrimSpace(req.Email),
		Nationality: req.Country,
		Verified:    ver.OK,
		JoinedAt:    now.Std(),
		Documents:   []domain.IdentityDocument{doc},
	}
	if g.Email == "" {
		// Email is the store's uniqueness key today; a synthesised, clearly
		// non-deliverable placeholder keeps that invariant without inventing a
		// plausible-looking address for a real person.
		g.Email = fmt.Sprintf("%s@no-email.guest-score.invalid", strings.ToLower(doc.Last4)+"-"+hash[:8])
	}
	g.GlobalID = domain.GlobalID("GS-" + strings.ToUpper(hash[:12]))

	created, err := s.store.CreateGuest(g)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.recordInquiry(created.ID, req)
	s.reindexGuest(r.Context(), created.ID)

	sum := s.summarise(r.Context(), created, now)
	writeJSON(w, http.StatusCreated, resolveResponse{
		Matched: false, Opened: true, Guest: &sum, Document: view, Verification: vview,
		Notice: portabilityNotice(created, doc),
	})
}

// portabilityNotice warns when a file cannot be reached from abroad. A guest
// whose only document is a domestic licence has a file that stops at the
// border, which defeats the point.
func portabilityNotice(g domain.Guest, presented domain.IdentityDocument) string {
	for _, d := range g.Documents {
		if d.Portable() {
			return ""
		}
	}
	if !presented.Portable() {
		return "This file has no portable document. Add the guest's passport so members in other " +
			"countries can resolve them; a " + presented.Type.Label() +
			" is only recognised within " + string(presented.Country) + "."
	}
	return ""
}

// recordInquiry logs that a member looked this guest up. The guest is entitled
// to know who has been asking; unlike a credit hard inquiry it does not affect
// the score, because a hotel checking a guest says nothing about that guest.
func (s *Server) recordInquiry(guestID string, req resolveRequest) {
	purpose := domain.InquiryCheckIn
	switch req.Purpose {
	case string(domain.InquiryBooking):
		purpose = domain.InquiryBooking
	case string(domain.InquiryReview):
		purpose = domain.InquiryReview
	}
	member := req.MemberID
	if member == "" {
		member = "unknown_member"
	}
	s.store.RecordInquiry(domain.Inquiry{
		GuestID: guestID, MemberID: member, MemberName: member,
		Purpose: purpose, At: time.Now().UTC(),
	})
}

// summarise builds the directory-shaped view of a guest with their score.
func (s *Server) summarise(ctx context.Context, g domain.Guest, now scoring.Time) GuestSummary {
	reviews, _ := s.store.ReviewsForGuest(g.ID)
	return GuestSummary{Guest: g, Score: s.scorer.Score(ctx, g.ID, reviews, now)}
}

// handleInquiries returns who has pulled this guest's file.
func (s *Server) handleInquiries(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetGuest(id); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"inquiries": s.store.InquiriesFor(id),
		"note":      "Inquiries are recorded for transparency and do not affect the score.",
	})
}

type attachRequest struct {
	Country domain.Country      `json:"country"`
	DocType domain.DocumentType `json:"document_type"`
	Number  string              `json:"number"`
}

// handleAttachDocument adds a document to an existing file.
//
// This is how a domestic-only file becomes portable, and it is the step that
// makes the bureau global in practice rather than in principle: a guest who
// opened on an Aadhaar attaches their passport once, and from then on a member
// in any country resolves them to the same standing.
func (s *Server) handleAttachDocument(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	g, err := s.store.GetGuest(id)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	var req attachRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error(), nil)
		return
	}

	normalised, err := domain.ValidateDocumentNumber(req.Country, req.DocType, req.Number)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_document", err.Error(),
			domain.FieldErrors{"number": err.Error()})
		return
	}

	rules, _ := domain.RulesFor(req.Country)
	authority := ""
	for _, a := range rules.Documents {
		if a.Type == req.DocType {
			authority = a.Authority
		}
	}

	ver := s.verifier.Verify(r.Context(), req.Country, req.DocType, normalised)
	now := s.now()
	doc := domain.IdentityDocument{
		Country:   req.Country,
		Type:      req.DocType,
		Hash:      domain.HashDocument(s.identityKey, req.Country, req.DocType, normalised),
		Last4:     domain.Last4(normalised),
		Verified:  ver.OK,
		Authority: authority,
		AddedAt:   now.Std(),
	}
	if ver.OK {
		t := now.Std()
		doc.VerifiedAt = &t
	}

	if err := s.store.AttachDocument(g.ID, doc); err != nil {
		s.fail(w, r, err)
		return
	}

	updated, _ := s.store.GetGuest(g.ID)
	sum := s.summarise(r.Context(), updated, now)
	writeJSON(w, http.StatusCreated, resolveResponse{
		Matched: true, Guest: &sum,
		Document: &documentView{
			Country: req.Country, Type: req.DocType, Label: req.DocType.Label(),
			Last4: doc.Last4, Portable: doc.Portable(), Authority: authority,
		},
		Verification: verificationView{Status: ver.Status, Authority: authority, Message: ver.Message},
		Notice:       portabilityNotice(updated, doc),
	})
}
