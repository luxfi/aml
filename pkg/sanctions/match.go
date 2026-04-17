// Package sanctions implements name-matching and list ingestion for
// OFAC SDN, UN, EU, and HMT sanctions lists.
package sanctions

import (
	"math"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
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

// cyrillicToLatin maps Cyrillic homoglyphs to their Latin equivalents.
// RED-10: Prevents homoglyph evasion (e.g., Cyrillic 'а' vs Latin 'a').
var cyrillicToLatin = map[rune]rune{
	'а': 'a', 'б': 'b', 'в': 'v', 'г': 'g', 'д': 'd', 'е': 'e',
	'ё': 'e', 'ж': 'z', 'з': 'z', 'и': 'i', 'й': 'y', 'к': 'k',
	'л': 'l', 'м': 'm', 'н': 'n', 'о': 'o', 'п': 'p', 'р': 'r',
	'с': 's', 'т': 't', 'у': 'u', 'ф': 'f', 'х': 'h', 'ц': 'c',
	'ч': 'c', 'ш': 's', 'щ': 's', 'ъ': ' ', 'ы': 'y', 'ь': ' ',
	'э': 'e', 'ю': 'y', 'я': 'y',
}

// normalize performs NFKD Unicode normalization, Cyrillic→Latin transliteration,
// lowercasing, and non-alphanumeric stripping.
// RED-10: NFKD normalization decomposes compatibility characters, and the
// transliteration map catches Cyrillic homoglyphs used to evade sanctions.
func normalize(s string) string {
	// NFKD decomposition — normalizes ligatures, fullwidth chars, etc.
	s = norm.NFKD.String(s)
	s = strings.ToLower(s)

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		// Cyrillic transliteration.
		if lat, ok := cyrillicToLatin[r]; ok {
			if lat != ' ' {
				b.WriteRune(lat)
			}
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// tokenMatchScore computes the average best Jaro-Winkler per input token.
func tokenMatchScore(inputTokens, candidateTokens []string) float64 {
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

// reverseTokens returns the tokens in reverse order.
func reverseTokens(tokens []string) []string {
	n := len(tokens)
	rev := make([]string, n)
	for i, t := range tokens {
		rev[n-1-i] = t
	}
	return rev
}

// soundex computes a simplified Soundex code for a string.
func soundex(s string) string {
	s = strings.ToUpper(normalize(s))
	if len(s) == 0 {
		return ""
	}
	codes := map[byte]byte{
		'B': '1', 'F': '1', 'P': '1', 'V': '1',
		'C': '2', 'G': '2', 'J': '2', 'K': '2', 'Q': '2', 'S': '2', 'X': '2', 'Z': '2',
		'D': '3', 'T': '3',
		'L': '4',
		'M': '5', 'N': '5',
		'R': '6',
	}

	result := []byte{s[0]}
	lastCode := codes[s[0]]
	for i := 1; i < len(s) && len(result) < 4; i++ {
		code, ok := codes[s[i]]
		if ok && code != lastCode {
			result = append(result, code)
			lastCode = code
		} else if !ok {
			lastCode = 0
		}
	}
	for len(result) < 4 {
		result = append(result, '0')
	}
	return string(result)
}

// TokenMatch computes token-based matching with name reversal and phonetic fallback.
// RED-10: Runs both forward and reversed token orderings to catch "Jong Un Kim"
// matching "Kim Jong Un". Adds Soundex as a phonetic tie-breaker.
func TokenMatch(input, candidate string) float64 {
	inputTokens := tokenize(input)
	candidateTokens := tokenize(candidate)
	if len(inputTokens) == 0 || len(candidateTokens) == 0 {
		return 0.0
	}

	// Forward match.
	forward := tokenMatchScore(inputTokens, candidateTokens)

	// Reversed name-order match.
	reversed := tokenMatchScore(reverseTokens(inputTokens), candidateTokens)

	best := math.Max(forward, reversed)

	// Phonetic fallback: if token match is marginal, check Soundex similarity.
	if best < MatchThreshold && best > 0.5 {
		phonForward := tokenMatchScore(soundexTokens(inputTokens), soundexTokens(candidateTokens))
		phonReversed := tokenMatchScore(soundexTokens(reverseTokens(inputTokens)), soundexTokens(candidateTokens))
		phonBest := math.Max(phonForward, phonReversed)
		// Blend: 70% string match + 30% phonetic.
		blended := best*0.7 + phonBest*0.3
		best = math.Max(best, blended)
	}

	return best
}

// soundexTokens converts string tokens to their Soundex codes.
func soundexTokens(tokens []string) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = soundex(t)
	}
	return out
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
