package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Identity is the bureau's answer to "is this the same person?" across borders.
//
// A credit file works because one consumer has one file no matter which lender
// pulls it. The same has to be true here or the deterrent evaporates: a guest
// who wrecks a room in Jaipur and checks into Lisbon next week must arrive with
// the same standing, not a clean slate. That requires two things this file
// provides — a stable bureau-issued identifier that never changes, and the
// ability to reach it from whatever document the guest happens to be carrying.

// GlobalID is the bureau's own permanent identifier for a person: "GS-" plus
// twelve base32 characters. It is what every score, stay, and inquiry hangs
// off. Documents come and go — passports expire, licences are reissued, people
// naturalise — so nothing durable may key off a document number.
type GlobalID string

// Country is an ISO 3166-1 alpha-2 code.
type Country string

// DocumentType enumerates the identity documents the bureau accepts. Which are
// valid depends on the country presenting them; see CountryRules.
type DocumentType string

const (
	DocPassport        DocumentType = "passport"
	DocDriversLicense  DocumentType = "drivers_license"
	DocNationalID      DocumentType = "national_id"
	DocAadhaar         DocumentType = "aadhaar"
	DocPAN             DocumentType = "pan"
	DocResidencePermit DocumentType = "residence_permit"
)

// Label renders a document type for display.
func (d DocumentType) Label() string {
	switch d {
	case DocPassport:
		return "Passport"
	case DocDriversLicense:
		return "Driver's licence"
	case DocNationalID:
		return "National ID"
	case DocAadhaar:
		return "Aadhaar"
	case DocPAN:
		return "PAN card"
	case DocResidencePermit:
		return "Residence permit"
	}
	return string(d)
}

// AcceptedDocument describes one document a country issues, and how to tell a
// real number from a typo before spending a call on the verifier.
type AcceptedDocument struct {
	Type DocumentType `json:"type"`
	// Pattern is the structural format. Passing it means the number is
	// well-formed, not that it exists.
	Pattern string `json:"pattern"`
	// Checksum names the integrity algorithm the number carries, if any.
	Checksum string `json:"checksum,omitempty"`
	// Authority is the body that can confirm the document is real.
	Authority string `json:"authority"`
	// Portable marks documents recognised outside the issuing country. Only a
	// portable document can open a file that another country's hotels will
	// resolve — a domestic licence cannot.
	Portable bool   `json:"portable"`
	Example  string `json:"example"`
	// Restricted marks documents whose numbers carry legal storage limits.
	// The bureau stores a keyed hash of every number, but these are the ones
	// where doing otherwise is not merely unwise but unlawful.
	Restricted bool   `json:"restricted,omitempty"`
	Note       string `json:"note,omitempty"`

	compiled *regexp.Regexp
}

// CountryRules is one country's accepted document set.
type CountryRules struct {
	Country   Country            `json:"country"`
	Name      string             `json:"name"`
	Documents []AcceptedDocument `json:"documents"`
}

// countryRules is the registry. It is deliberately small and explicit rather
// than a generated list of every jurisdiction: each entry encodes a real format
// and a real issuing authority, and an entry nobody has verified is worse than
// no entry, because it would let a malformed number through to a verifier that
// will reject it anyway.
var countryRules = map[Country]CountryRules{
	"IN": {Country: "IN", Name: "India", Documents: []AcceptedDocument{
		{
			Type: DocAadhaar, Pattern: `^[2-9][0-9]{11}$`, Checksum: "verhoeff",
			Authority: "UIDAI", Portable: false, Example: "2234 5678 9018",
			Restricted: true,
			Note: "Aadhaar numbers never begin 0 or 1 and carry a Verhoeff check digit. " +
				"The Aadhaar Act restricts storage of the number itself, so only a keyed hash is retained.",
		},
		{
			Type: DocPAN, Pattern: `^[A-Z]{5}[0-9]{4}[A-Z]$`, Authority: "Income Tax Department",
			Portable: false, Example: "ABCDE1234F",
		},
		{
			Type: DocPassport, Pattern: `^[A-PR-WY][0-9]{7}$`, Authority: "Ministry of External Affairs",
			Portable: true, Example: "M1234567",
		},
	}},
	"US": {Country: "US", Name: "United States", Documents: []AcceptedDocument{
		{
			Type: DocDriversLicense, Pattern: `^[A-Z0-9]{4,20}$`, Authority: "State DMV",
			Portable: false, Example: "D1234567",
			Note: "Format varies by state; the bureau checks length and character class only, then defers to the issuing DMV.",
		},
		{
			Type: DocPassport, Pattern: `^[0-9]{9}$|^[A-Z][0-9]{8}$`, Authority: "US Department of State",
			Portable: true, Example: "512345678",
		},
	}},
	"GB": {Country: "GB", Name: "United Kingdom", Documents: []AcceptedDocument{
		{
			Type: DocDriversLicense, Pattern: `^[A-Z9]{5}[0-9]{6}[A-Z9]{2}[A-Z0-9]{3}$`,
			Authority: "DVLA", Portable: false, Example: "SMITH751128SM9AB",
		},
		{
			Type: DocPassport, Pattern: `^[0-9]{9}$`, Authority: "HM Passport Office",
			Portable: true, Example: "123456789",
		},
	}},
	"AE": {Country: "AE", Name: "United Arab Emirates", Documents: []AcceptedDocument{
		{
			Type: DocNationalID, Pattern: `^784[0-9]{12}$`, Checksum: "luhn",
			Authority: "ICP", Portable: false, Example: "784197012345670",
			Note: "Emirates ID always begins 784 and ends in a Luhn check digit.",
		},
		{
			Type: DocPassport, Pattern: `^[A-Z0-9]{6,9}$`, Authority: "ICP",
			Portable: true, Example: "A1234567",
		},
	}},
	"SG": {Country: "SG", Name: "Singapore", Documents: []AcceptedDocument{
		{
			Type: DocNationalID, Pattern: `^[STFGM][0-9]{7}[A-Z]$`, Checksum: "nric",
			Authority: "ICA", Portable: false, Example: "S1234567D",
		},
		{
			Type: DocPassport, Pattern: `^[EK][0-9]{7}[A-Z]$`, Authority: "ICA",
			Portable: true, Example: "E1234567X",
		},
	}},
	"DE": {Country: "DE", Name: "Germany", Documents: []AcceptedDocument{
		{
			Type: DocNationalID, Pattern: `^[0-9A-Z]{9}$`, Authority: "Bundesdruckerei",
			Portable: false, Example: "L01X00T47",
		},
		{
			Type: DocPassport, Pattern: `^[CFGHJKLMNPRTVWXYZ0-9]{9}$`, Authority: "Bundesdruckerei",
			Portable: true, Example: "C01X00T47",
		},
	}},
}

func init() {
	// Compile once. A bad pattern here is a programming error, so panic at
	// startup rather than silently accepting every number at runtime.
	for c, rules := range countryRules {
		for i := range rules.Documents {
			rules.Documents[i].compiled = regexp.MustCompile(rules.Documents[i].Pattern)
		}
		countryRules[c] = rules
	}
}

// SupportedCountries lists every country the bureau can open a file for.
func SupportedCountries() []CountryRules {
	out := make([]CountryRules, 0, len(countryRules))
	for _, r := range countryRules {
		out = append(out, r)
	}
	// Deterministic order so the API response is stable.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Country < out[i].Country {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// RulesFor returns the accepted documents for a country.
func RulesFor(c Country) (CountryRules, bool) {
	r, ok := countryRules[Country(strings.ToUpper(string(c)))]
	return r, ok
}

// IdentityDocument is one document attached to a profile.
//
// The raw number is never a field. Only a keyed hash is stored, plus the last
// four characters for the desk agent to confirm they are looking at the right
// person. That is a hard requirement, not a precaution: a bureau that
// accumulates plaintext Aadhaar and passport numbers across dozens of
// countries is a single breach away from being the worst kind of incident, and
// in India storing the Aadhaar number itself is restricted by statute.
type IdentityDocument struct {
	Country Country      `json:"country"`
	Type    DocumentType `json:"type"`

	// Hash identifies the document without revealing it. Two presentations of
	// the same number produce the same hash, which is what lets any member
	// resolve the profile; the hash cannot be reversed to the number.
	Hash string `json:"hash"`
	// Last4 is for human confirmation at the desk, nothing more.
	Last4 string `json:"last4"`

	Verified   bool       `json:"verified"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	// Authority records who confirmed it, so a verification can be audited.
	Authority string     `json:"authority,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	AddedAt   time.Time  `json:"added_at"`
}

// Portable reports whether this document can be presented outside its issuing
// country. Passports can; domestic licences and national IDs cannot.
func (d IdentityDocument) Portable() bool {
	rules, ok := RulesFor(d.Country)
	if !ok {
		return false
	}
	for _, a := range rules.Documents {
		if a.Type == d.Type {
			return a.Portable
		}
	}
	return false
}

// Expired reports whether the document is past its expiry as of now.
func (d IdentityDocument) Expired(now time.Time) bool {
	return d.ExpiresAt != nil && now.After(*d.ExpiresAt)
}

// ValidateDocumentNumber checks a raw number against its country's format and
// checksum. It returns the normalised number, ready for hashing.
//
// This runs before any call to an issuing authority. A typo caught locally
// costs nothing; a typo sent to UIDAI costs a request, a log entry containing
// someone's near-miss Aadhaar number, and a confusing error at the desk.
func ValidateDocumentNumber(c Country, t DocumentType, raw string) (string, error) {
	rules, ok := RulesFor(c)
	if !ok {
		return "", fmt.Errorf("country %q is not supported by the bureau", c)
	}

	// People write identity numbers with spaces and dashes; the document does
	// not contain them.
	n := strings.ToUpper(strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '/' {
			return -1
		}
		return r
	}, strings.TrimSpace(raw)))

	for _, a := range rules.Documents {
		if a.Type != t {
			continue
		}
		if !a.compiled.MatchString(n) {
			return "", fmt.Errorf("%s numbers issued by %s do not look like %q (expected format e.g. %s)",
				t.Label(), rules.Name, raw, a.Example)
		}
		switch a.Checksum {
		case "verhoeff":
			if !verhoeffValid(n) {
				return "", fmt.Errorf("%s check digit does not match — the number was likely mistyped", t.Label())
			}
		case "luhn":
			if !luhnValid(n) {
				return "", fmt.Errorf("%s check digit does not match — the number was likely mistyped", t.Label())
			}
		case "nric":
			if !nricValid(n) {
				return "", fmt.Errorf("%s check letter does not match — the number was likely mistyped", t.Label())
			}
		}
		return n, nil
	}

	accepted := make([]string, 0, len(rules.Documents))
	for _, a := range rules.Documents {
		accepted = append(accepted, a.Type.Label())
	}
	return "", fmt.Errorf("%s does not issue a %s; it accepts: %s",
		rules.Name, t.Label(), strings.Join(accepted, ", "))
}

// HashDocument produces the stored identifier for a document number.
//
// Keyed with HMAC rather than a bare SHA-256: identity number spaces are small
// and highly structured — an Aadhaar is one of 10^11, a UK passport one of
// 10^9 — so an unkeyed digest is brute-forceable in minutes on a laptop. With a
// secret key, a stolen database of hashes is inert without the key.
//
// The country and type are mixed in so the same digit string issued by two
// different authorities cannot collide into one person.
func HashDocument(key []byte, c Country, t DocumentType, normalisedNumber string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(strings.ToUpper(string(c))))
	m.Write([]byte{0})
	m.Write([]byte(t))
	m.Write([]byte{0})
	m.Write([]byte(normalisedNumber))
	return hex.EncodeToString(m.Sum(nil))
}

// Last4 returns the trailing characters shown at the desk for confirmation.
func Last4(normalisedNumber string) string {
	if len(normalisedNumber) <= 4 {
		return normalisedNumber
	}
	return normalisedNumber[len(normalisedNumber)-4:]
}

// --- Checksums ---------------------------------------------------------------
// These are the published algorithms, not approximations. They are what make
// local validation meaningful: a random twelve-digit string passes the Aadhaar
// regex but fails Verhoeff roughly nine times in ten.

var verhoeffD = [10][10]int{
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
	{1, 2, 3, 4, 0, 6, 7, 8, 9, 5},
	{2, 3, 4, 0, 1, 7, 8, 9, 5, 6},
	{3, 4, 0, 1, 2, 8, 9, 5, 6, 7},
	{4, 0, 1, 2, 3, 9, 5, 6, 7, 8},
	{5, 9, 8, 7, 6, 0, 4, 3, 2, 1},
	{6, 5, 9, 8, 7, 1, 0, 4, 3, 2},
	{7, 6, 5, 9, 8, 2, 1, 0, 4, 3},
	{8, 7, 6, 5, 9, 3, 2, 1, 0, 4},
	{9, 8, 7, 6, 5, 4, 3, 2, 1, 0},
}

var verhoeffP = [8][10]int{
	{0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
	{1, 5, 7, 6, 2, 8, 3, 0, 9, 4},
	{5, 8, 0, 3, 7, 9, 6, 1, 4, 2},
	{8, 9, 1, 6, 0, 4, 3, 5, 2, 7},
	{9, 4, 5, 3, 1, 2, 6, 8, 7, 0},
	{4, 2, 8, 6, 5, 7, 3, 9, 0, 1},
	{2, 7, 9, 3, 8, 0, 6, 4, 1, 5},
	{7, 0, 4, 6, 9, 1, 3, 2, 5, 8},
}

// verhoeffValid implements the Verhoeff check used by Aadhaar.
func verhoeffValid(s string) bool {
	c := 0
	for i, j := len(s)-1, 0; i >= 0; i, j = i-1, j+1 {
		d := int(s[i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		c = verhoeffD[c][verhoeffP[j%8][d]]
	}
	return c == 0
}

// luhnValid implements the Luhn check used by the Emirates ID.
func luhnValid(s string) bool {
	sum, alt := 0, false
	for i := len(s) - 1; i >= 0; i-- {
		d := int(s[i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	return sum%10 == 0
}

// nricValid implements Singapore's NRIC/FIN check letter.
func nricValid(s string) bool {
	if len(s) != 9 {
		return false
	}
	weights := []int{2, 7, 6, 5, 4, 3, 2}
	sum := 0
	for i := 0; i < 7; i++ {
		d := int(s[1+i] - '0')
		if d < 0 || d > 9 {
			return false
		}
		sum += d * weights[i]
	}
	switch s[0] {
	case 'T', 'G':
		sum += 4
	case 'M':
		sum += 3
	}
	var table string
	switch s[0] {
	case 'S', 'T':
		table = "JZIHGFEDCBA"
	case 'F', 'G':
		table = "XWUTRQPNMLK"
	case 'M':
		table = "XWUTRQPNJLK"
	default:
		return false
	}
	return s[8] == table[sum%11]
}
