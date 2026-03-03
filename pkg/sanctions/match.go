// Package sanctions implements name-matching and list ingestion for
// OFAC SDN, UN, EU, and HMT sanctions lists.
package sanctions

import (
	"math"
	"strings"
	"unicode"
)

// JaroWinkler computes the Jaro-Winkler distance between two strings.
// Returns a value in [0, 1] where 1 is an exact match.
func JaroWinkler(s1, s2 string) float64 {
	s1 = normalize(s1)
	s2 = normalize(s2)

	if s1 == s2 {
		return 1.0
	}
	if len(s1) == 0 || len(s2) == 0 {
		return 0.0
	}

	jaro := jaroDistance(s1, s2)

	// Winkler prefix bonus: up to 4 common prefix chars.
	prefix := 0
	for i := 0; i < min(len(s1), len(s2)) && i < 4; i++ {
		if s1[i] != s2[i] {
			break
		}
		prefix++
	}

	return jaro + float64(prefix)*0.1*(1.0-jaro)
}

// jaroDistance computes the Jaro distance between two strings.
func jaroDistance(s1, s2 string) float64 {
	if s1 == s2 {
		return 1.0
	}

	l1, l2 := len(s1), len(s2)
	matchWindow := max(l1, l2)/2 - 1
	if matchWindow < 0 {
		matchWindow = 0
	}

	s1Matches := make([]bool, l1)
	s2Matches := make([]bool, l2)

	var matches float64
	var transpositions float64

	for i := 0; i < l1; i++ {
		lo := max(0, i-matchWindow)
		hi := min(l2-1, i+matchWindow)

		for j := lo; j <= hi; j++ {
			if s2Matches[j] || s1[i] != s2[j] {
				continue
			}
			s1Matches[i] = true
			s2Matches[j] = true
			matches++
			break
		}
	}

	if matches == 0 {
		return 0.0
	}

	k := 0
	for i := 0; i < l1; i++ {
		if !s1Matches[i] {
			continue
		}
		for !s2Matches[k] {
			k++
		}
		if s1[i] != s2[k] {
			transpositions++
		}
		k++
	}

	return (matches/float64(l1) + matches/float64(l2) + (matches-transpositions/2)/matches) / 3.0
}

// normalize lowercases and strips non-alphanumeric characters.
func normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// TokenMatch computes token-based matching: splits both strings into
// tokens and returns the average best Jaro-Winkler score per input token.
func TokenMatch(input, candidate string) float64 {
	inputTokens := tokenize(input)
	candidateTokens := tokenize(candidate)
	if len(inputTokens) == 0 || len(candidateTokens) == 0 {
		return 0.0
	}

	var totalBest float64
	for _, it := range inputTokens {
		best := 0.0
		for _, ct := range candidateTokens {
			score := JaroWinkler(it, ct)
			best = math.Max(best, score)
		}
		totalBest += best
	}

	return totalBest / float64(len(inputTokens))
}

// tokenize splits a string on whitespace and returns non-empty tokens.
func tokenize(s string) []string {
	parts := strings.Fields(strings.ToLower(s))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// MatchThreshold is the default Jaro-Winkler threshold for a sanctions match.
const MatchThreshold = 0.85

// Match checks if a name matches any entry in the list above the threshold.
// Returns matching entries with their scores.
type MatchResult struct {
	EntryID   string  `json:"entry_id"`
	ListID    string  `json:"list_id"`
	Name      string  `json:"name"`
	Score     float64 `json:"score"`
	MatchType string  `json:"match_type"` // exact, fuzzy, token
}
