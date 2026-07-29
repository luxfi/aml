// Package sanctions parses the published sanctions lists and matches names and
// identifiers against them.
//
// The Entry model is the union of what the four list families actually publish,
// established by reading the files themselves rather than by describing them
// from memory. Fields exist here because a real list carries them and matching
// needs them: alias strength, because a sixth of OFAC's aliases are marked weak
// and matching them at full confidence is the largest single source of false
// positives; multiple dates of birth, because they are the tie-break that
// separates a real subject from a namesake; and digital currency addresses,
// because OFAC designates them and a payment to one is a sanctions breach that no
// amount of name matching will catch.
package sanctions

import "strings"

// List sources.
const (
	OFAC = "ofac"
	UN   = "un"
	EU   = "eu"
	OFSI = "ofsi"
)

// Subject kinds.
const (
	Individual = "individual"
	Entity     = "entity"
	Vessel     = "vessel"
	Aircraft   = "aircraft"
)

// Name types, as the lists themselves distinguish them. A formerly-known-as name
// is still screened — a renamed company does not stop being designated — but the
// type is recorded so an analyst reviewing a hit knows which name matched.
const (
	Primary  = "primary"
	Also     = "aka"
	Formerly = "fka"
	Now      = "nka"
)

// Document kinds. Address is a digital currency address, which is matched
// exactly rather than fuzzily: an address is either the designated one or it is
// somebody else's money.
const (
	Passport = "passport"
	National = "national"
	Address  = "address"
	Other    = "other"
)

// Name is one of a subject's names.
//
// Strong is false for the aliases their publisher marks as low quality. OFAC
// marks 4,383 of 24,546 aliases weak, the UN marks aliases Low, and OFSI marks
// them Low quality; all three mean the same thing — the name is a fragment, a
// transliteration guess, or a partial, and treating it as a full-confidence
// identifier produces hits on unrelated people.
type Name struct {
	Full   string `json:"full"`
	Type   string `json:"type"`
	Strong bool   `json:"strong"`
	// Script holds the name as the publisher records it in its original
	// non-Latin script, when it does. Matching an Arabic or Cyrillic name against
	// its own script is exact where matching it against a transliteration is a
	// guess.
	Script string `json:"script,omitempty"`
}

// Birth is a date of birth, which the lists publish as an exact date, a year
// alone, a range between two years, or an approximation.
//
// From and To are ISO 8601 prefixes: "1971", "1971-08", or "1971-08-14". They are
// equal for an exact date and differ for a range. Comparison is by prefix, so a
// subject known only by year still matches a customer whose full date falls in
// that year — and a customer born in a different year is excluded, which is the
// point.
type Birth struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Circa bool   `json:"circa,omitempty"`
}

// Document is an identifier a subject holds.
type Document struct {
	Kind    string `json:"kind"`
	Number  string `json:"number"`
	Country string `json:"country,omitempty"`
	// Chain names the network for a digital currency address, as the publisher
	// labels it: XBT, ETH, TRX, USDT and so on.
	Chain string `json:"chain,omitempty"`
}

// Entry is one designated subject.
type Entry struct {
	List  string `json:"list"`
	RefID string `json:"ref_id"`
	// Group ties together the rows of a list that describes one subject across
	// several records. OFSI publishes one row per name and joins them with a
	// group identifier; the other lists publish one record per subject, where the
	// group is the record.
	Group         string     `json:"group"`
	Kind          string     `json:"kind"`
	Names         []Name     `json:"names"`
	Births        []Birth    `json:"births,omitempty"`
	Places        []string   `json:"places,omitempty"`
	Nationalities []string   `json:"nationalities,omitempty"`
	Citizenships  []string   `json:"citizenships,omitempty"`
	Documents     []Document `json:"documents,omitempty"`
	Addresses     []string   `json:"addresses,omitempty"`
	Programs      []string   `json:"programs,omitempty"`
	Remarks       string     `json:"remarks,omitempty"`
	ListedOn      string     `json:"listed_on,omitempty"`
	UpdatedOn     string     `json:"updated_on,omitempty"`
}

// PrimaryName is the subject's main name, or the first name it has.
func (e Entry) PrimaryName() string {
	for _, n := range e.Names {
		if n.Type == Primary {
			return n.Full
		}
	}
	if len(e.Names) > 0 {
		return e.Names[0].Full
	}
	return ""
}

// Chains returns the digital currency addresses this entry designates.
func (e Entry) Chains() []Document {
	var out []Document
	for _, d := range e.Documents {
		if d.Kind == Address {
			out = append(out, d)
		}
	}
	return out
}

// join builds a whole name from the ordered parts a list splits it into,
// dropping the empty ones. Both OFAC and the UN carry an entity's whole name in
// the field nominally meant for a surname, so a naive first-plus-last
// concatenation produces a leading space for every entity on both lists.
func join(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " ")
}
