package sanctions

import (
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
// A name is a conjunction, not an average. Every identifying part of the shorter
// name must match the longer one, and the returned score is the mean of those
// partnerships — but the mean is reported, not tested. What decides a match is the
// weakest part, because that is what distinguishes a variant spelling of one
// person from a coincidence between two.
//
// Comparing in both orders is what makes a list's "Kim Jong Un" match a customer
// record's "Un, Kim Jong" without the caller having to know which convention
// either side used.
func Similarity(a, b string) float64 {
	fa, fb := fold(a), fold(b)
	if fa == fb && fa != "" {
		return 1
	}

	ta, tb := strings.Fields(fa), strings.Fields(fb)
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

	// Only identifying tokens decide the match. An initial, a particle and a bare
	// number match almost anything, so they can neither establish an identity nor
	// disqualify one — they are read past. "J Smith" is judged on Smith, "al-Assad"
	// on Assad, "Unit 42" on Unit.
	var mean float64
	weakest := 1.0
	identifying := 0
	for _, x := range ta {
		best := 0.0
		for _, y := range tb {
			if s := similar(x, y); s > best {
				best = s
			}
		}
		if !identifies(x) {
			continue
		}
		identifying++
		mean += best
		if best < weakest {
			weakest = best
		}
	}

	// Nothing identifying was offered, so nobody was identified. A query of
	// initials and digits — a customer key such as "a-1" — matched "A ONE TRADING
	// COMPANY 1" at 1.0 before this, because each of its trivial tokens found an
	// exact partner and the mean of two exact partnerships is exact.
	//
	// Returning zero rather than falling through matters: the division below would
	// be 0/0, and a NaN score compares false against every threshold, so it would
	// read as "no match" while being unorderable and unable to marshal.
	if identifying == 0 {
		return 0
	}
	mean /= float64(identifying)

	// Every identifying part must match. A mean lets one strong partnership carry a
	// weak one, which is how a customer key came to be reported as a designated
	// party: "cust-1" against "CONSTELLO NO. 1 CORPORATION" scored 0.862 on the
	// strength of 1 matching 1, while the only part that could identify anybody,
	// "cust" against "constello", scored 0.72.
	//
	// The same shape occurs between two real people. "Smith Petrov" against
	// "Smirnov Petrov" averages 0.89 on an exact surname while the given names
	// score 0.77 — one number no threshold on the mean can separate from a genuine
	// variant, and the weakest part can.
	if weakest < partFloor {
		return 0
	}

	// One identifying part against a name of several is a fragment: it named
	// somebody and left the rest of the person unaccounted for. A fragment
	// identifies only when it is near-exact, which is why Unit against
	// "UNIT 42 OF THE ISLAMIC REVOLUTIONARY GUARD" matches and cust does not.
	if identifying == 1 && len(tb) > 1 && mean < fragmentThreshold {
		return 0
	}
	return mean
}

// identifies reports whether a token can establish who somebody is.
//
// Initials, name particles and bare numbers cannot. They are short, they recur
// across unrelated names, and they match on the little they contain: every
// one-character token matches some one-character token exactly. Requiring at least
// three runes and at least one letter admits the shortest real name parts — bin,
// van, del, Wei, Ali — while excluding the tokens a database key decomposes into.
//
// A legal-form suffix cannot either. It names the form a company takes, not which
// company it is, and every company has one — so matching it is agreement about
// company-ness. "acme-ltd" was reported against an unrelated "…Co., Ltd" at 0.967
// on the strength of Ltd matching Ltd.
func identifies(token string) bool {
	if legalForm[token] {
		return false
	}
	letters := 0
	runes := 0
	for _, r := range token {
		runes++
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return runes >= 3 && letters > 0
}

// legalForm holds the corporate legal-form suffixes, folded. The set is closed on
// purpose: it is the forms a company registration takes, not a list of words that
// happen to be common, so it cannot grow into a general stopword list that starts
// discarding parts of real names.
var legalForm = map[string]bool{
	"ltd": true, "limited": true, "llc": true, "inc": true, "incorporated": true,
	"corp": true, "corporation": true, "plc": true, "gmbh": true, "mbh": true,
	"ag": true, "sa": true, "sas": true, "sarl": true, "srl": true, "spa": true,
	"bv": true, "nv": true, "oy": true, "ab": true, "as": true, "aps": true,
	"kk": true, "pte": true, "pty": true, "jsc": true, "ojsc": true, "zao": true,
	"ooo": true, "oao": true, "pao": true, "fze": true, "fzc": true, "fzco": true,
	"llp": true, "lp": true, "gie": true, "kft": true, "sro": true, "dooel": true,
}

// partFloor is how well the weakest identifying part of a name must match.
//
// It is measured, not chosen. Across the transliteration pairs the published lists
// actually contain, genuine variants of one name bottom out at 0.840 — Yusuf
// against Yousef, with Mohammed/Muhammad at 0.850, Osama/Usama and Ivan/Ivon at
// 0.867 — while unrelated parts top out at 0.773, Smith against Smirnov, with
// cust/constello at 0.725 and user/unit at 0.550. This sits between the two
// populations rather than inside either.
//
// It is deliberately below Threshold, the bar the whole name must clear. Requiring
// every part to individually meet the whole-name bar reads well and is wrong:
// Yusuf against Yousef is 0.840 and would be refused, so a real customer named
// Yousef would stop matching a designated Yusuf.
//
// Known limit: a name part of one or two characters carries too little to be
// judged this way — Lee against Li scores 0.650 — which is why parts that short
// are read past as incidental instead.
const partFloor = 0.80

// fragmentThreshold is how well a single identifying token must match a name of
// several parts before it counts as the same person.
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
