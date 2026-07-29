package sanctions

import (
	"sort"
	"strings"
)

// Threshold is the default name similarity at or above which a name is reported
// for review.
//
// It is deliberately low. A screening threshold trades false positives, which
// cost an analyst minutes, against false negatives, which are a sanctions breach.
// The published lists themselves contemplate the first: the freezing obligation
// applies to the designated party, and a hit on a namesake is expected to be
// resolved by checking the other identifiers rather than avoided by matching
// less.
const Threshold = 0.85

// weakPenalty is applied to a name its publisher marks low quality.
//
// A weak alias is a fragment or a guessed transliteration. It is still screened —
// discarding it would ignore evidence the publisher chose to publish — but it does
// not carry the same confidence as a designated primary name, and scoring it
// identically is what fills a review queue with unrelated people.
const weakPenalty = 0.12

// conflictPenalty is applied when the customer and the subject both state an
// identifier and the two disagree.
//
// A date of birth that cannot overlap is the strongest routinely available signal
// that two same-named people are different people. The penalty is large enough to
// drop an otherwise-perfect name match below the threshold, which is the entire
// value of collecting a date of birth. It is a penalty and not an exclusion: the
// match is still returned, carrying its conflict, so a decision to set it aside is
// recorded rather than silent.
const conflictPenalty = 0.30

// corroborationBonus is applied when an identifier agrees.
const corroborationBonus = 0.05

// Match reasons.
const (
	ByName     = "name"
	ByDocument = "document"
	ByChain    = "chain"
)

// Query is who to screen.
//
// Birth, Nationality and Kind are optional and are the difference between a
// screening system that can clear a namesake and one that cannot. Supplying none
// of them is supported, and produces a name-only match with no corroboration —
// which the result says plainly.
type Query struct {
	Name        string
	Birth       Birth
	Nationality string
	Kind        string
}

// Match is one screening result.
type Match struct {
	Entry Entry   `json:"entry"`
	Name  Name    `json:"name"`
	Score float64 `json:"score"`
	// Reason names what matched: the name, an identity document number, or a
	// digital currency address.
	Reason string `json:"reason"`
	// Agree and Conflict name the identifiers that corroborated or contradicted
	// the match. They are what an analyst reads first and what a regulator asks
	// about when a hit was cleared.
	Agree    []string `json:"agree,omitempty"`
	Conflict []string `json:"conflict,omitempty"`
}

// Screen matches a query against a set of entries.
//
// Results are sorted by score, highest first, and every result at or above the
// threshold is returned. Only one match is reported per subject: the strongest of
// its names, so a subject with forty aliases produces one line for an analyst
// rather than forty.
func Screen(q Query, entries []Entry, threshold float64) []Match {
	// An empty name is rejected here rather than left to score zero against every
	// name on every list. Correctness does not depend on this — an empty name is
	// not similar to anything — but a list set is tens of thousands of subjects
	// carrying a hundred thousand names between them, and an endpoint that walks
	// all of them for a blank query is a load amplifier reachable by anyone who can
	// call it.
	if strings.TrimSpace(q.Name) == "" {
		return nil
	}
	if threshold <= 0 {
		threshold = Threshold
	}

	var out []Match
	for _, e := range entries {
		// A person is not a ship. Screening a customer name against vessels and
		// aircraft produces hits that can never be the customer, and the kind is
		// stated by every list.
		if q.Kind != "" && e.Kind != q.Kind {
			continue
		}

		best, bestName, ok := bestName(q.Name, e)
		if !ok {
			continue
		}

		score := best
		if !bestName.Strong {
			score -= weakPenalty
		}
		agree, conflict, adjust := corroborate(q, e)
		score += adjust
		if score < threshold {
			continue
		}
		out = append(out, Match{
			Entry:    e,
			Name:     bestName,
			Score:    clamp(score),
			Reason:   ByName,
			Agree:    agree,
			Conflict: conflict,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// bestName returns the entry's name that best matches the query.
func bestName(name string, e Entry) (float64, Name, bool) {
	var best float64
	var which Name
	var found bool
	for _, n := range e.Names {
		if n.Full == "" {
			continue
		}
		s := Similarity(name, n.Full)
		// A name recorded in its original script is compared against the query as
		// well, so a customer whose record holds the Cyrillic or Arabic form matches
		// the list's own original-script spelling exactly rather than approximately
		// through two separate transliterations.
		if n.Script != "" {
			if t := Similarity(name, n.Script); t > s {
				s = t
			}
		}
		if s > best {
			best, which, found = s, n, true
		}
	}
	return best, which, found
}

// corroborate compares the identifiers the query and the entry both state.
//
// Only identifiers present on both sides are compared. An absent date of birth is
// not evidence of anything, and treating absence as disagreement would penalise
// the many list entries that carry no date at all.
func corroborate(q Query, e Entry) (agree, conflict []string, adjust float64) {
	if q.Birth.From != "" && len(e.Births) > 0 {
		overlap := false
		for _, b := range e.Births {
			if b.Overlaps(q.Birth) {
				overlap = true
				break
			}
		}
		if overlap {
			agree = append(agree, "birth")
			adjust += corroborationBonus
		} else {
			conflict = append(conflict, "birth")
			adjust -= conflictPenalty
		}
	}

	if q.Nationality != "" && (len(e.Nationalities) > 0 || len(e.Citizenships) > 0) {
		match := false
		for _, n := range append(append([]string{}, e.Nationalities...), e.Citizenships...) {
			if strings.EqualFold(strings.TrimSpace(n), strings.TrimSpace(q.Nationality)) {
				match = true
				break
			}
		}
		if match {
			agree = append(agree, "nationality")
			adjust += corroborationBonus
		} else {
			// Nationality is weaker evidence than a date of birth. People hold
			// several, lists record them inconsistently, and a mismatch is a hint
			// rather than a refutation — so it nudges the score instead of deciding
			// it.
			conflict = append(conflict, "nationality")
			adjust -= corroborationBonus
		}
	}
	return agree, conflict, adjust
}

// ScreenChain matches a blockchain address against the addresses the lists
// designate.
//
// The comparison is exact, never fuzzy. An address is a key, not a name: a
// one-character difference is a different address belonging to a different
// person, and a near match is not evidence of anything. This is the only path
// that catches a payment to a designated address, because the address carries the
// owner's name nowhere in the transaction.
func ScreenChain(address string, entries []Entry) []Match {
	want := strings.TrimSpace(address)
	if want == "" {
		return nil
	}
	var out []Match
	for _, e := range entries {
		for _, d := range e.Chains() {
			if !sameAddress(strings.TrimSpace(d.Number), want) {
				continue
			}
			out = append(out, Match{
				Entry:  e,
				Name:   Name{Full: e.PrimaryName(), Type: Primary, Strong: true},
				Score:  1,
				Reason: ByChain,
				Agree:  []string{"address " + d.Chain},
			})
		}
	}
	return out
}

// ScreenDocument matches an identity document number against the numbers the
// lists designate. Like an address, a document number is compared exactly: it
// identifies one person, and a near miss is a different document.
func ScreenDocument(number string, entries []Entry) []Match {
	want := foldDocument(number)
	if want == "" {
		return nil
	}
	var out []Match
	for _, e := range entries {
		for _, d := range e.Documents {
			if d.Kind == Address || foldDocument(d.Number) != want {
				continue
			}
			out = append(out, Match{
				Entry:  e,
				Name:   Name{Full: e.PrimaryName(), Type: Primary, Strong: true},
				Score:  1,
				Reason: ByDocument,
				Agree:  []string{d.Kind},
			})
		}
	}
	return out
}

// sameAddress reports whether two blockchain addresses denote the same account.
//
// Whether case matters depends on the encoding, so this cannot be settled by
// folding everything. Base58 — Bitcoin's legacy addresses, Tron, Solana, Monero,
// Litecoin, Dogecoin — uses upper and lower case as distinct symbols, and folding
// case there declares two different addresses equal. Hexadecimal addresses carry
// an optional checksum in their capitalisation but denote the same account in any
// case, and bech32 is defined to be single-case and equal either way. So the
// comparison is exact by default and case-insensitive only for the encodings where
// case genuinely carries no information.
func sameAddress(designated, query string) bool {
	if designated == query {
		return true
	}
	return caseless(designated) && strings.EqualFold(designated, query)
}

// caseless reports whether an address encoding ignores case.
func caseless(a string) bool {
	s := strings.ToLower(a)
	if rest, ok := strings.CutPrefix(s, "0x"); ok {
		return rest != "" && onlyIn(rest, "0123456789abcdef")
	}
	// A bech32 address is a human-readable prefix, the separator "1", then a data
	// part of at least six characters drawn from an alphabet that excludes 1, b, i
	// and o.
	if i := strings.LastIndex(s, "1"); i > 0 && len(s)-i-1 >= 6 {
		return onlyIn(s[i+1:], "qpzry9x8gf2tvdw0s3jn54khce6mua7l")
	}
	return false
}

func onlyIn(s, alphabet string) bool {
	for _, r := range s {
		if !strings.ContainsRune(alphabet, r) {
			return false
		}
	}
	return true
}

// foldDocument normalises a document number for comparison. Publishers and
// customer records differ on spacing, hyphens and case within the same number, so
// those are removed; the digits and letters are the number.
func foldDocument(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
