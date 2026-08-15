package api

import (
	"net/http"
	"testing"
)

// TestResolve_OpensAFileFromACountrySpecificDocument: an Indian guest opens a
// file on an Aadhaar, a US guest on a driver's licence. Each country's own
// document works without the bureau needing a universal one.
func TestResolve_OpensAFileFromACountrySpecificDocument(t *testing.T) {
	cases := []struct {
		name    string
		country string
		docType string
		number  string
		guest   string
	}{
		{"India / Aadhaar", "IN", "aadhaar", "2345 6789 0124", "Ananya Iyer"},
		{"US / driver's licence", "US", "drivers_license", "D9988776", "Cole Whitaker"},
		{"Singapore / NRIC", "SG", "national_id", "S1234567D", "Wei Ling Tan"},
		{"UAE / Emirates ID", "AE", "national_id", "784197012345670", "Omar Al Mansoori"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _ := newTestServer(t)
			rec := do(t, h, "POST", "/api/identity/resolve", map[string]any{
				"country": c.country, "document_type": c.docType,
				"number": c.number, "name": c.guest, "member_id": "m_test",
			})
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
			}
			got := decode[resolveResponse](t, rec)

			if !got.Opened || got.Matched {
				t.Errorf("first contact should open a file, got opened=%v matched=%v", got.Opened, got.Matched)
			}
			if got.Guest == nil || got.Guest.Name != c.guest {
				t.Fatalf("file not opened for %s", c.guest)
			}
			if got.Guest.GlobalID == "" {
				t.Error("a file must be issued a global identifier")
			}
			// The opening score, not an unrated state.
			if got.Guest.Score.Tier != "New" {
				t.Errorf("a new file should open in tier New, got %q", got.Guest.Score.Tier)
			}
			// The raw number must not come back in the response.
			if body := rec.Body.String(); containsDigitsOf(body, c.number) {
				t.Error("the document number was echoed back in the response")
			}
		})
	}
}

// TestResolve_SameGuestAcrossBorders is the point of the whole design: a guest
// who opens a file at home and later presents a passport abroad must resolve to
// the same standing, not a clean slate.
func TestResolve_SameGuestAcrossBorders(t *testing.T) {
	h, _ := newTestServer(t)

	// Mumbai: opens a file on an Aadhaar, then adds a passport.
	rec := do(t, h, "POST", "/api/identity/resolve", map[string]any{
		"country": "IN", "document_type": "aadhaar", "number": "234567890124",
		"name": "Rohan Mehta", "member_id": "m_mumbai",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("opening the file failed: %s", rec.Body)
	}
	opened := decode[resolveResponse](t, rec)
	globalID := opened.Guest.GlobalID

	// A domestic-only file should be called out as unreachable from abroad.
	if opened.Notice == "" {
		t.Error("a file whose only document is domestic should warn that it is not portable")
	}

	// The desk attaches the guest's passport to the same file.
	rec = do(t, h, "POST", "/api/guests/"+opened.Guest.ID+"/documents", map[string]any{
		"country": "IN", "document_type": "passport", "number": "M1234567",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("attaching the passport failed: %s", rec.Body)
	}
	attached := decode[resolveResponse](t, rec)
	if attached.Notice != "" {
		t.Errorf("once a passport is attached the file is portable; unexpected notice: %q", attached.Notice)
	}

	// Lisbon: a member that has never seen this guest scans only the passport.
	rec = do(t, h, "POST", "/api/identity/resolve", map[string]any{
		"country": "IN", "document_type": "passport", "number": "M1234567",
		"member_id": "m_lisbon",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("cross-border resolve failed: %s", rec.Body)
	}
	abroad := decode[resolveResponse](t, rec)

	if !abroad.Matched {
		t.Fatal("a guest presenting an attached passport abroad must match their existing file")
	}
	if abroad.Opened {
		t.Error("resolving abroad must not open a second file — that is the clean slate the bureau exists to prevent")
	}
	if abroad.Guest.GlobalID != globalID {
		t.Errorf("cross-border lookup resolved to a different file: %s vs %s",
			abroad.Guest.GlobalID, globalID)
	}
	if abroad.Guest.Name != "Rohan Mehta" {
		t.Errorf("resolved to the wrong person: %q", abroad.Guest.Name)
	}
}

// TestAttachDocument_RefusesToStealAnotherProfilesDocument: one document
// belongs to one person. Allowing a second file to claim it would let someone
// launder a bad record onto a fresh identity.
func TestAttachDocument_RefusesToStealAnotherProfilesDocument(t *testing.T) {
	h, _ := newTestServer(t)

	a := decode[resolveResponse](t, do(t, h, "POST", "/api/identity/resolve", map[string]any{
		"country": "GB", "document_type": "passport", "number": "123456789",
		"name": "First Owner",
	}))
	b := decode[resolveResponse](t, do(t, h, "POST", "/api/identity/resolve", map[string]any{
		"country": "US", "document_type": "passport", "number": "512345678",
		"name": "Second Person",
	}))

	rec := do(t, h, "POST", "/api/guests/"+b.Guest.ID+"/documents", map[string]any{
		"country": "GB", "document_type": "passport", "number": "123456789",
	})
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 — a document already on another file must not be claimed", rec.Code)
	}

	// The original owner keeps it.
	still := decode[resolveResponse](t, do(t, h, "POST", "/api/identity/resolve", map[string]any{
		"country": "GB", "document_type": "passport", "number": "123456789",
		"member_id": "m_check",
	}))
	if still.Guest.GlobalID != a.Guest.GlobalID {
		t.Error("the document moved to a different file")
	}
}

// TestAttachDocument_IsIdempotent: re-presenting a document already on the file
// is normal at a front desk and must not error.
func TestAttachDocument_IsIdempotent(t *testing.T) {
	h, _ := newTestServer(t)
	g := decode[resolveResponse](t, do(t, h, "POST", "/api/identity/resolve", map[string]any{
		"country": "SG", "document_type": "passport", "number": "E1234567X",
		"name": "Repeat Presenter",
	}))
	body := map[string]any{"country": "SG", "document_type": "passport", "number": "E1234567X"}
	for i := 0; i < 3; i++ {
		if rec := do(t, h, "POST", "/api/guests/"+g.Guest.ID+"/documents", body); rec.Code != http.StatusCreated {
			t.Fatalf("attempt %d: status = %d, want 201: %s", i+1, rec.Code, rec.Body)
		}
	}
}

// TestResolve_ReturningGuestMatches: presenting the same document twice must
// find the existing file rather than opening a duplicate.
func TestResolve_ReturningGuestMatches(t *testing.T) {
	h, _ := newTestServer(t)
	req := map[string]any{
		"country": "GB", "document_type": "passport", "number": "123456789",
		"name": "Eleanor Vance", "member_id": "m_london",
	}

	first := decode[resolveResponse](t, do(t, h, "POST", "/api/identity/resolve", req))
	if !first.Opened {
		t.Fatal("first presentation should open a file")
	}

	// Same passport, different member, formatted differently.
	req["member_id"] = "m_edinburgh"
	req["number"] = " 123456789 "
	second := decode[resolveResponse](t, do(t, h, "POST", "/api/identity/resolve", req))

	if !second.Matched || second.Opened {
		t.Errorf("a returning guest must match, got matched=%v opened=%v", second.Matched, second.Opened)
	}
	if second.Guest.GlobalID != first.Guest.GlobalID {
		t.Errorf("the same document resolved to two different files: %s vs %s",
			first.Guest.GlobalID, second.Guest.GlobalID)
	}
}

// TestResolve_RejectsBadDocumentsBeforeCallingAnyAuthority
func TestResolve_RejectsBadDocumentsBeforeCallingAnyAuthority(t *testing.T) {
	cases := []struct{ name, country, docType, number string }{
		{"aadhaar with a bad check digit", "IN", "aadhaar", "234567890125"},
		{"aadhaar starting with 1", "IN", "aadhaar", "134567890123"},
		{"emirates id with a bad check digit", "AE", "national_id", "784197012345671"},
		{"nric with a wrong check letter", "SG", "national_id", "S1234567A"},
		{"a document the country does not issue", "US", "aadhaar", "234567890124"},
		{"an unsupported country", "ZZ", "passport", "123456789"},
		{"empty number", "US", "passport", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _ := newTestServer(t)
			rec := do(t, h, "POST", "/api/identity/resolve", map[string]any{
				"country": c.country, "document_type": c.docType, "number": c.number,
				"name": "Should Not Be Created",
			})
			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422: %s", rec.Code, rec.Body)
			}
			body := decode[errorBody](t, rec)
			if body.Error.Fields["number"] == "" {
				t.Error("the error should point at the number field")
			}
		})
	}
}

// TestResolve_WillNotOpenAnAnonymousFile: a score that can deny someone a room
// must not attach to a record with no name on it.
func TestResolve_WillNotOpenAnAnonymousFile(t *testing.T) {
	h, _ := newTestServer(t)
	rec := do(t, h, "POST", "/api/identity/resolve", map[string]any{
		"country": "US", "document_type": "passport", "number": "512345678",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decode[resolveResponse](t, rec)
	if got.Opened || got.Guest != nil {
		t.Error("a file must not be opened without a name")
	}
	if got.Notice == "" {
		t.Error("the response should say what is needed to open a file")
	}
}

// TestResolve_VerificationIsLabelledSimulated: a fixture verification must
// never read as a real check against an issuing authority.
func TestResolve_VerificationIsLabelledSimulated(t *testing.T) {
	h, _ := newTestServer(t)
	got := decode[resolveResponse](t, do(t, h, "POST", "/api/identity/resolve", map[string]any{
		"country": "IN", "document_type": "aadhaar", "number": "234567890124",
		"name": "Test Guest",
	}))
	if got.Verification.Status != "simulated" {
		t.Errorf("verification status = %q, want simulated", got.Verification.Status)
	}
	if got.Verification.Authority != "UIDAI" {
		t.Errorf("verification should name the authority, got %q", got.Verification.Authority)
	}
	if got.Verification.Message == "" {
		t.Error("a simulated verification must explain that no request was made")
	}
}

// TestResolve_RecordsAnInquiry: the guest is entitled to know who looked.
func TestResolve_RecordsAnInquiry(t *testing.T) {
	h, _ := newTestServer(t)
	opened := decode[resolveResponse](t, do(t, h, "POST", "/api/identity/resolve", map[string]any{
		"country": "DE", "document_type": "passport", "number": "C01X00T47",
		"name": "Lena Brandt", "member_id": "m_berlin", "purpose": "check_in",
	}))

	rec := do(t, h, "GET", "/api/guests/"+opened.Guest.ID+"/inquiries", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decode[map[string]any](t, rec)
	list, _ := got["inquiries"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected exactly 1 inquiry, got %d", len(list))
	}
	entry := list[0].(map[string]any)
	if entry["member_id"] != "m_berlin" {
		t.Errorf("inquiry member = %v, want m_berlin", entry["member_id"])
	}
	if entry["purpose"] != "check_in" {
		t.Errorf("inquiry purpose = %v, want check_in", entry["purpose"])
	}
}

// TestCountries_PublishesRulesForTheDeskForm
func TestCountries_PublishesRulesForTheDeskForm(t *testing.T) {
	h, _ := newTestServer(t)
	rec := do(t, h, "GET", "/api/identity/countries", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got := decode[map[string]any](t, rec)
	countries, _ := got["countries"].([]any)
	if len(countries) < 5 {
		t.Fatalf("only %d countries published", len(countries))
	}
	for _, c := range countries {
		m := c.(map[string]any)
		docs, _ := m["documents"].([]any)
		if len(docs) == 0 {
			t.Errorf("%v has no documents", m["country"])
		}
		for _, d := range docs {
			dm := d.(map[string]any)
			if dm["example"] == "" || dm["authority"] == "" {
				t.Errorf("%v/%v is missing an example or authority", m["country"], dm["type"])
			}
		}
	}
}

// containsDigitsOf reports whether the response leaked the raw number.
func containsDigitsOf(body, number string) bool {
	digits := ""
	for _, r := range number {
		if r >= '0' && r <= '9' {
			digits += string(r)
		}
	}
	if len(digits) < 8 {
		return false
	}
	return contains(body, digits)
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}
