package sanctions

import (
	"math"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// fold reduces a name to the form two spellings of it have in common:
// diacritics removed, Cyrillic transliterated, lowercased, and everything that is
// not a letter or a digit turned into a separator.
//
// Decomposing to NFD separates a letter from its accent: "é" becomes a plain "e"
// followed by a combining acute. The combining mark must then be dropped
// outright, not merely excluded from the letters kept — a mark that falls through
// to the separator case splits the name in two, so "Öztürk" would tokenise as
// "o zt u rk" and stop resembling "Ozturk" more than it started. Dropping the
// marks makes the two spellings one string, which matters because the customer
// record and the list rarely agree about accents.
func fold(s string) string {
	s = strings.ToLower(norm.NFD.String(s))

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if lat, ok := cyrillic[r]; ok {
			b.WriteString(lat)
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		// Everything else — punctuation, apostrophes, hyphens, spaces — becomes a
		// separator, so "al-Assad" and "al Assad" tokenise the same way.
		b.WriteByte(' ')
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// cyrillic transliterates Cyrillic to Latin.
//
// The mapping follows the BGN/PCGN romanisation used by the publishers themselves
// when they render a Cyrillic name in Latin script, so a customer name in
// Cyrillic folds onto the same string as the list's own transliteration. Multi-
// letter results are required and are why the values are strings: zh, kh, ts, ch,
// sh and shch have no single-letter Latin equivalent, and collapsing them to one
// letter merges names that are distinct — the previous single-rune map sent both
// ц and ч to c, and both ж and з to z.
var cyrillic = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e",
	'ж': "zh", 'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m",
	'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u",
	'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch",
	'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya",
	// Ukrainian and Belarusian letters that appear in designated names.
	'і': "i", 'ї': "yi", 'є': "ye", 'ґ': "g", 'ў': "w",
}

// jaro is the Jaro similarity of two rune slices.
//
// It works on runes, not bytes. Indexing a folded name by byte breaks every name
// that survives folding as non-ASCII — Greek, Arabic, Han, Thai — because a
// multi-byte rune is compared one byte at a time against unrelated bytes. The
// result is not merely imprecise: two different Arabic names can score 1.0
// because their byte sequences share a leading continuation byte.
func jaro(a, b []rune) float64 {
	la, lb := len(a), len(b)
	if la == 0 || lb == 0 {
		return 0
	}
	if la == 1 && lb == 1 {
		if a[0] == b[0] {
			return 1
		}
		return 0
	}

	window := max(la, lb)/2 - 1
	if window < 0 {
		window = 0
	}
	ma := make([]bool, la)
	mb := make([]bool, lb)

	var matches int
	for i := 0; i < la; i++ {
		lo := max(0, i-window)
		hi := min(lb-1, i+window)
		for j := lo; j <= hi; j++ {
			if mb[j] || a[i] != b[j] {
				continue
			}
			ma[i], mb[j] = true, true
			matches++
			break
		}
	}
	if matches == 0 {
		return 0
	}

	var half int
	k := 0
	for i := 0; i < la; i++ {
		if !ma[i] {
			continue
		}
		for !mb[k] {
			k++
		}
		if a[i] != b[k] {
			half++
		}
		k++
	}
	transpositions := float64(half) / 2

	m := float64(matches)
	return (m/float64(la) + m/float64(lb) + (m-transpositions)/m) / 3
}

// similar is the Jaro-Winkler similarity of two folded tokens.
func similar(a, b string) float64 {
	if a == b {
		return 1
	}
	ra, rb := []rune(a), []rune(b)
	j := jaro(ra, rb)
	// The Winkler prefix bonus rewards a shared opening, which is where names
	// agree when a transliteration differs only in its tail.
	prefix := 0
	for prefix < min(len(ra), len(rb)) && prefix < 4 && ra[prefix] == rb[prefix] {
		prefix++
	}
	return j + float64(prefix)*0.1*(1-j)
}

// Similarity compares two whole names, in either word order.
//
// Every token of the shorter name must find a partner in the longer one, and the
// score is the mean of those best partnerships. Comparing in both orders is what
// makes a list's "Kim Jong Un" match a customer record's "Un, Kim Jong" without
// the caller having to know which convention either side used.
func Similarity(a, b string) float64 {
	ta, tb := strings.Fields(fold(a)), strings.Fields(fold(b))
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	// Compare in the direction that asks every token of the shorter name to be
	// accounted for. Scoring the longer name's tokens against the shorter one
	// punishes a list entry that carries extra given names the customer omitted,
	// which is the common case rather than the suspicious one.
	if len(ta) > len(tb) {
		ta, tb = tb, ta
	}
	var total float64
	for _, x := range ta {
		best := 0.0
		for _, y := range tb {
			best = math.Max(best, similar(x, y))
		}
		total += best
	}
	score := total / float64(len(ta))

	// A single name compared against a name of several parts must match almost
	// exactly.
	//
	// Comparing the shorter name against the longer is what lets a customer who
	// omitted a patronymic still match, but read in the other direction it lets one
	// short fragment stand in for a whole person: screening 31,338 real designations
	// produced exactly two false positives, both of this shape — a one-word entry
	// scoring 0.86 and 0.88 against an unrelated three-part name because it happened
	// to share a prefix with the middle name. The fragment matched a name and left
	// the rest of the person unaccounted for.
	//
	// The threshold cannot be raised generally to exclude them: a genuine
	// transliteration of a single name, Osama against Usama, scores 0.87, below both
	// false positives. What separates the cases is not the score but the asymmetry.
	// One name against one name is all the evidence there is and is judged on the
	// ordinary threshold; one name against three is a fragment, and a fragment
	// identifies somebody only when it is exact.
	if len(ta) == 1 && len(tb) > 1 && score < fragmentThreshold {
		return 0
	}
	return score
}

// fragmentThreshold is how well a single-token name must match a name of several
// parts before it counts as the same person.
const fragmentThreshold = 0.95

// byteRunes exists only so the mutation harness can substitute bytewise indexing
// for rune indexing and demonstrate that the suite detects it.
func byteRunes(s string) []rune {
	out := make([]rune, len(s))
	for i := 0; i < len(s); i++ {
		out[i] = rune(s[i])
	}
	return out
}
