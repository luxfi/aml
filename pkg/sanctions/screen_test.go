package sanctions

import "testing"

func subject(name string, opts ...func(*Entry)) Entry {
	e := Entry{
		List:  OFAC,
		RefID: "1",
		Group: "1",
		Kind:  Individual,
		Names: []Name{{Full: name, Type: Primary, Strong: true}},
	}
	for _, o := range opts {
		o(&e)
	}
	return e
}

func TestExactNameMatches(t *testing.T) {
	list := []Entry{subject("Ivan Petrov")}
	m := Screen(Query{Name: "Ivan Petrov"}, list, Threshold)
	if len(m) != 1 || m[0].Score != 1 {
		t.Fatalf("exact name must score 1, got %+v", m)
	}
}

func TestReversedNameOrderMatches(t *testing.T) {
	list := []Entry{subject("Kim Jong Un")}
	if m := Screen(Query{Name: "Un Kim Jong"}, list, Threshold); len(m) != 1 {
		t.Fatal("a name in a different word order must still match")
	}
}

func TestDiacriticsFolded(t *testing.T) {
	// One accent in a long name is bridged by the similarity measure on its own.
	// Several accents in a short one are not: the accented letters count as
	// mismatches, the prefix bonus is lost when the first letter is one of them,
	// and the score drops below any usable threshold. Decomposition is what makes
	// these the same name, and these are ordinary surnames rather than contrived
	// strings.
	for _, c := range []struct{ listed, queried string }{
		{"José Ramírez", "Jose Ramirez"},
		{"Öztürk", "Ozturk"},
		{"Ñuñez", "Nunez"},
		{"Ibrahim Šehić", "Ibrahim Sehic"},
	} {
		list := []Entry{subject(c.listed)}
		if m := Screen(Query{Name: c.queried}, list, Threshold); len(m) != 1 {
			t.Errorf("%q must match the listed %q, scored %.3f",
				c.queried, c.listed, Similarity(c.queried, c.listed))
		}
	}
}

func TestCyrillicTransliterated(t *testing.T) {
	// The list carries the Latin transliteration; the customer record carries
	// Cyrillic. Both must fold to the same string.
	list := []Entry{subject("Aleksandr Zhukov")}
	if m := Screen(Query{Name: "Александр Жуков"}, list, Threshold); len(m) != 1 {
		t.Fatal("a Cyrillic name must match its own Latin transliteration")
	}
}

func TestCyrillicDistinctLettersStayDistinct(t *testing.T) {
	// ц and ч both became "c" under a single-rune map, and ж and з both became
	// "z". Distinct names must not collapse onto each other.
	if a, b := fold("цар"), fold("чар"); a == b {
		t.Fatalf("ц and ч must transliterate differently, both gave %q", a)
	}
	if a, b := fold("жар"), fold("зар"); a == b {
		t.Fatalf("ж and з must transliterate differently, both gave %q", a)
	}
}

func TestNonLatinScriptsAreNotConfused(t *testing.T) {
	// Byte-wise comparison of multi-byte runes can score unrelated non-Latin names
	// as identical because their encodings share leading bytes. Rune-wise
	// comparison cannot.
	pairs := [][2]string{
		{"محمد علي", "حسين كامل"},
		{"张伟", "李娜"},
		{"Γεώργιος", "Δημήτριος"},
	}
	for _, p := range pairs {
		if s := Similarity(p[0], p[1]); s >= Threshold {
			t.Errorf("unrelated names %q and %q scored %v, which would be reported as a match", p[0], p[1], s)
		}
	}
}

func TestSelfSimilarityIsOneForNonLatin(t *testing.T) {
	for _, n := range []string{"محمد علي", "张伟", "Γεώργιος", "Александр"} {
		if s := Similarity(n, n); s != 1 {
			t.Errorf("%q against itself scored %v, want 1", n, s)
		}
	}
}

func TestWeakAliasScoresLower(t *testing.T) {
	strong := []Entry{subject("Mohammed", func(e *Entry) {
		e.Names = []Name{{Full: "Mohammed", Type: Also, Strong: true}}
	})}
	weak := []Entry{subject("Mohammed", func(e *Entry) {
		e.Names = []Name{{Full: "Mohammed", Type: Also, Strong: false}}
	})}

	s := Screen(Query{Name: "Mohammed"}, strong, 0.5)
	w := Screen(Query{Name: "Mohammed"}, weak, 0.5)
	if len(s) != 1 || len(w) != 1 {
		t.Fatalf("both must match at a low threshold, got %d and %d", len(s), len(w))
	}
	if !(w[0].Score < s[0].Score) {
		t.Fatalf("a weak alias must score below a strong one: weak %v, strong %v", w[0].Score, s[0].Score)
	}
}

func TestBirthConflictSuppressesNamesake(t *testing.T) {
	// The decisive case. Same name, different year of birth: a namesake, not the
	// designated subject.
	list := []Entry{subject("Ivan Petrov", func(e *Entry) {
		e.Births = []Birth{{From: "1955", To: "1955"}}
	})}
	q := Query{Name: "Ivan Petrov", Birth: Birth{From: "1988-03-04", To: "1988-03-04"}}
	if m := Screen(q, list, Threshold); len(m) != 0 {
		t.Fatalf("a perfect name match with a contradicting date of birth must fall below the threshold, got score %v", m[0].Score)
	}
}

func TestBirthConflictIsRecordedNotHidden(t *testing.T) {
	list := []Entry{subject("Ivan Petrov", func(e *Entry) {
		e.Births = []Birth{{From: "1955", To: "1955"}}
	})}
	q := Query{Name: "Ivan Petrov", Birth: Birth{From: "1988", To: "1988"}}
	// At a threshold low enough to keep it, the conflict must be stated so the
	// decision to set it aside is on the record.
	m := Screen(q, list, 0.5)
	if len(m) != 1 {
		t.Fatalf("want the match retained at a low threshold, got %d", len(m))
	}
	if len(m[0].Conflict) == 0 || m[0].Conflict[0] != "birth" {
		t.Fatalf("the conflicting identifier must be named, got %+v", m[0].Conflict)
	}
}

func TestBirthAgreementCorroborates(t *testing.T) {
	list := []Entry{subject("Ivan Petrov", func(e *Entry) {
		e.Births = []Birth{{From: "1955", To: "1955"}}
	})}
	q := Query{Name: "Ivan Petrov", Birth: Birth{From: "1955-06-02", To: "1955-06-02"}}
	m := Screen(q, list, Threshold)
	if len(m) != 1 {
		t.Fatal("a full date inside the listed year must match")
	}
	if len(m[0].Agree) == 0 {
		t.Fatal("agreement on the date of birth must be recorded")
	}
}

func TestBirthRangeMatches(t *testing.T) {
	list := []Entry{subject("Ivan Petrov", func(e *Entry) {
		e.Births = []Birth{{From: "1960", To: "1963"}}
	})}
	for _, y := range []string{"1960", "1961-07", "1963-12-31"} {
		if m := Screen(Query{Name: "Ivan Petrov", Birth: Birth{From: y, To: y}}, list, Threshold); len(m) != 1 {
			t.Errorf("%s falls inside the listed range 1960-1963 and must match", y)
		}
	}
	for _, y := range []string{"1959", "1964"} {
		if m := Screen(Query{Name: "Ivan Petrov", Birth: Birth{From: y, To: y}}, list, Threshold); len(m) != 0 {
			t.Errorf("%s falls outside the listed range 1960-1963 and must not match", y)
		}
	}
}

func TestAbsentBirthIsNotDisagreement(t *testing.T) {
	// Most list entries carry no date. Absence must not penalise the match, or
	// screening would systematically miss the subjects the lists describe least.
	list := []Entry{subject("Ivan Petrov")}
	m := Screen(Query{Name: "Ivan Petrov", Birth: Birth{From: "1988", To: "1988"}}, list, Threshold)
	if len(m) != 1 {
		t.Fatal("an entry with no date of birth must still match on name alone")
	}
	if len(m[0].Conflict) != 0 {
		t.Fatalf("a missing date is not a conflict, got %+v", m[0].Conflict)
	}
}

func TestKindFilterExcludesVessels(t *testing.T) {
	list := []Entry{subject("Cape Ray", func(e *Entry) { e.Kind = Vessel })}
	if m := Screen(Query{Name: "Cape Ray", Kind: Individual}, list, Threshold); len(m) != 0 {
		t.Fatal("screening a person must not match a vessel")
	}
	if m := Screen(Query{Name: "Cape Ray", Kind: Vessel}, list, Threshold); len(m) != 1 {
		t.Fatal("screening a vessel must match a vessel")
	}
}

func TestOneMatchPerSubjectNotPerAlias(t *testing.T) {
	list := []Entry{subject("Ivan Petrov", func(e *Entry) {
		e.Names = append(e.Names,
			Name{Full: "Ivan Petrov", Type: Also, Strong: true},
			Name{Full: "Ivan Petrov", Type: Formerly, Strong: true},
			Name{Full: "I. Petrov", Type: Also, Strong: true},
		)
	})}
	m := Screen(Query{Name: "Ivan Petrov"}, list, Threshold)
	if len(m) != 1 {
		t.Fatalf("one subject must produce one match, got %d", len(m))
	}
}

func TestChainAddressMatchesExactly(t *testing.T) {
	list := []Entry{subject("Mixer Operator", func(e *Entry) {
		e.Documents = []Document{{Kind: Address, Chain: "XBT", Number: "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"}}
	})}
	if m := ScreenChain("1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", list); len(m) != 1 || m[0].Reason != ByChain {
		t.Fatal("a designated address must match exactly")
	}
	// One character different is a different address belonging to somebody else.
	if m := ScreenChain("1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNb", list); len(m) != 0 {
		t.Fatal("an address differing by one character must not match")
	}
	if m := ScreenChain("", list); len(m) != 0 {
		t.Fatal("an empty address must match nothing")
	}
}

func TestChainAddressCaseRulesFollowTheEncoding(t *testing.T) {
	// Hexadecimal: capitalisation carries an optional checksum, not identity, so
	// the same account in any case is the same account.
	hex := []Entry{subject("Operator", func(e *Entry) {
		e.Documents = []Document{{Kind: Address, Chain: "ETH", Number: "0xAbC1234567890def"}}
	})}
	if m := ScreenChain("0xabc1234567890def", hex); len(m) != 1 {
		t.Error("a hexadecimal address must match irrespective of case")
	}

	// bech32 is defined to be single-case, so either case denotes the same address.
	b32 := []Entry{subject("Operator", func(e *Entry) {
		e.Documents = []Document{{Kind: Address, Chain: "XBT", Number: "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq"}}
	})}
	if m := ScreenChain("BC1QAR0SRRR7XFKVY5L643LYDNW9RE59GTZZWF5MDQ", b32); len(m) != 1 {
		t.Error("a bech32 address must match irrespective of case")
	}

	// Base58 uses upper and lower case as distinct symbols. Folding case here
	// would declare two different addresses — belonging to two different people —
	// to be the same, and report a match against an address nobody designated.
	b58 := []Entry{subject("Operator", func(e *Entry) {
		e.Documents = []Document{{Kind: Address, Chain: "XBT", Number: "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"}}
	})}
	if m := ScreenChain("1A1zP1eP5QGefi2DMPTfTL5SLmv7Divfna", b58); len(m) != 0 {
		t.Error("a base58 address differing only in case is a different address and must not match")
	}
	if m := ScreenChain("1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", b58); len(m) != 1 {
		t.Error("the designated base58 address must match itself")
	}

	// Tron addresses are base58 and must not fold either.
	trx := []Entry{subject("Operator", func(e *Entry) {
		e.Documents = []Document{{Kind: Address, Chain: "TRX", Number: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"}}
	})}
	if m := ScreenChain("tr7nhqjekqxgtci8q8zy4pl8otszgjlj6t", trx); len(m) != 0 {
		t.Error("a base58 Tron address must not match case-folded")
	}
}

func TestChainScreeningIgnoresNonAddressDocuments(t *testing.T) {
	list := []Entry{subject("Person", func(e *Entry) {
		e.Documents = []Document{{Kind: Passport, Number: "X123456"}}
	})}
	if m := ScreenChain("X123456", list); len(m) != 0 {
		t.Fatal("a passport number must not be matched as a blockchain address")
	}
}

func TestDocumentNumberMatchesAcrossFormatting(t *testing.T) {
	list := []Entry{subject("Person", func(e *Entry) {
		e.Documents = []Document{{Kind: Passport, Number: "X-123 456"}}
	})}
	if m := ScreenDocument("x123456", list); len(m) != 1 || m[0].Reason != ByDocument {
		t.Fatal("a document number must match across spacing, hyphens and case")
	}
	if m := ScreenDocument("x123457", list); len(m) != 0 {
		t.Fatal("a different document number must not match")
	}
}

func TestEmptyQueryMatchesNothing(t *testing.T) {
	list := []Entry{subject("Ivan Petrov")}
	if m := Screen(Query{Name: ""}, list, Threshold); len(m) != 0 {
		t.Fatal("an empty name must match nothing, not everything")
	}
	if m := Screen(Query{Name: "   "}, list, Threshold); len(m) != 0 {
		t.Fatal("a blank name must match nothing")
	}
}

func TestUnrelatedNameDoesNotMatch(t *testing.T) {
	list := []Entry{subject("Ivan Petrov")}
	for _, n := range []string{"Maria Gonzalez", "Acme Trading Limited", "Wei Zhang"} {
		if m := Screen(Query{Name: n}, list, Threshold); len(m) != 0 {
			t.Errorf("%q must not match Ivan Petrov, scored %v", n, m[0].Score)
		}
	}
}

func TestResultsSortedByScore(t *testing.T) {
	list := []Entry{
		subject("Ivan Petrovv", func(e *Entry) { e.RefID = "b"; e.Group = "b" }),
		subject("Ivan Petrov", func(e *Entry) { e.RefID = "a"; e.Group = "a" }),
	}
	m := Screen(Query{Name: "Ivan Petrov"}, list, Threshold)
	if len(m) != 2 {
		t.Fatalf("both must match, got %d", len(m))
	}
	if m[0].Score < m[1].Score {
		t.Fatal("results must be ordered strongest first")
	}
}

func TestBirthOverlapPrefixSemantics(t *testing.T) {
	cases := []struct {
		a, b Birth
		want bool
	}{
		{Birth{From: "1971", To: "1971"}, Birth{From: "1971-08-14", To: "1971-08-14"}, true},
		{Birth{From: "1971", To: "1971"}, Birth{From: "1972-08-14", To: "1972-08-14"}, false},
		{Birth{From: "1971-08", To: "1971-08"}, Birth{From: "1971-08-14", To: "1971-08-14"}, true},
		{Birth{From: "1971-08", To: "1971-08"}, Birth{From: "1971-09-14", To: "1971-09-14"}, false},
		{Birth{From: "1960", To: "1963"}, Birth{From: "1962", To: "1962"}, true},
		{Birth{From: "", To: ""}, Birth{From: "1971", To: "1971"}, false},
	}
	for _, c := range cases {
		if got := c.a.Overlaps(c.b); got != c.want {
			t.Errorf("%v overlaps %v = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestDocumentScreeningIgnoresChainAddresses(t *testing.T) {
	// The two exact-match paths must stay separate. A blockchain address is not an
	// identity document, and reporting it as one attributes a payment to a
	// passport nobody holds.
	list := []Entry{subject("Operator", func(e *Entry) {
		e.Documents = []Document{{Kind: Address, Chain: "ETH", Number: "0xdeadbeef"}}
	})}
	if m := ScreenDocument("0xdeadbeef", list); len(m) != 0 {
		t.Fatal("a blockchain address must not be matched as an identity document")
	}
}

func TestNamePartsOmittedByTheCustomerStillMatch(t *testing.T) {
	// The list carries a patronymic the customer record does not. Every token the
	// customer gave is accounted for, so this is the designated subject; scoring
	// the list's extra name against the customer's shorter one instead would drag
	// the result under the threshold and miss him.
	// Names carrying several given names are the ordinary case on these lists, not
	// the exception, and the customer record routinely holds two of the four.
	for _, c := range []struct{ listed, queried string }{
		{"Ivan Sergeyevich Petrov", "Ivan Petrov"},
		{"Abdullah Muhammad Yusuf Ibrahim", "Abdullah Ibrahim"},
		{"Maria Del Carmen Fernandez Gonzalez", "Maria Gonzalez"},
	} {
		list := []Entry{subject(c.listed)}
		if m := Screen(Query{Name: c.queried}, list, Threshold); len(m) != 1 {
			t.Errorf("%q must match the listed %q, scored %.4f",
				c.queried, c.listed, Similarity(c.queried, c.listed))
		}
		// The relation is symmetric: which side carries the extra names must not
		// change the answer.
		list = []Entry{subject(c.queried)}
		if m := Screen(Query{Name: c.listed}, list, Threshold); len(m) != 1 {
			t.Errorf("symmetry: %q must match the listed %q", c.listed, c.queried)
		}
	}
}

func TestByteIndexingWouldConfuseNonLatinNames(t *testing.T) {
	// Guards the rune-wise comparison with no escape hatch.
	//
	// Two distinct Han names sharing a first character have no second rune in
	// common. Compared one byte at a time they share four of six bytes and an
	// identical four-byte prefix, because every rune in the block encodes to three
	// bytes with a common lead — so the Winkler prefix bonus pushes them over a
	// screening threshold. Compared rune-wise they share one of two runes and fall
	// well below it.
	a, b := "张伟", "张俊"

	byteScore := byteSimilar(a, b)
	runeScore := Similarity(a, b)

	if byteScore < Threshold {
		t.Fatalf("premise broken: bytewise comparison scored %v, below the %v threshold, so it would not have produced the false match this test guards against",
			byteScore, Threshold)
	}
	if runeScore >= Threshold {
		t.Fatalf("rune-wise comparison scored %v, at or above the %v threshold — two different people would be reported as one",
			runeScore, Threshold)
	}
	t.Logf("bytewise %.3f (would match), rune-wise %.3f (correctly does not)", byteScore, runeScore)
}

// byteSimilar reproduces the discarded bytewise comparison, prefix bonus included,
// so the test above can show what it did.
func byteSimilar(a, b string) float64 {
	ra, rb := byteRunes(fold(a)), byteRunes(fold(b))
	j := jaro(ra, rb)
	prefix := 0
	for prefix < min(len(ra), len(rb)) && prefix < 4 && ra[prefix] == rb[prefix] {
		prefix++
	}
	return j + float64(prefix)*0.1*(1-j)
}

func TestPartialDateKeepsTheYearItKnows(t *testing.T) {
	// The UK list writes an unknown day and month as zero. The year is the only
	// part that was ever known and it is the part a tie-break needs; reading the
	// zeroes as a real day and month produces a date that matches nothing.
	b, ok := parseSlashBirth("00/00/1994")
	if !ok {
		t.Fatal("a year-only date must parse")
	}
	if b.From != "1994" || b.To != "1994" {
		t.Fatalf("got %+v, want the bare year 1994", b)
	}
	if !b.Overlaps(Birth{From: "1994-06-02", To: "1994-06-02"}) {
		t.Fatal("the parsed year must still overlap a full date inside it")
	}

	// An unknown day with a known month keeps the month.
	b, _ = parseSlashBirth("00/08/1994")
	if b.From != "1994-08" {
		t.Fatalf("got %q, want 1994-08", b.From)
	}
	// A full date keeps everything.
	b, _ = parseSlashBirth("22/08/1990")
	if b.From != "1990-08-22" {
		t.Fatalf("got %q, want 1990-08-22", b.From)
	}
	if _, ok := parseSlashBirth("22/08"); ok {
		t.Fatal("a date without three parts must not parse")
	}
}

func TestOFACBirthShapes(t *testing.T) {
	// Every shape OFAC actually publishes, taken from the file.
	cases := []struct {
		in, from, to string
		circa        bool
	}{
		{"10 Dec 1948", "1948-12-10", "1948-12-10", false},
		{"1938", "1938", "1938", false},
		{"circa 1951", "1951", "1951", true},
		{"1938 to 1940", "1938", "1940", false},
		{"01 Jan 1960 to 31 Dec 1960", "1960-01-01", "1960-12-31", false},
	}
	for _, c := range cases {
		b, ok := parseOFACBirth(c.in)
		if !ok {
			t.Errorf("%q did not parse", c.in)
			continue
		}
		if b.From != c.from || b.To != c.to || b.Circa != c.circa {
			t.Errorf("%q gave %+v, want from=%s to=%s circa=%v", c.in, b, c.from, c.to, c.circa)
		}
	}
	if _, ok := parseOFACBirth(""); ok {
		t.Error("an empty date must not parse")
	}
	if _, ok := parseOFACBirth("unknown"); ok {
		t.Error("a date with no year must not parse")
	}
}

func TestSingleNameFragmentDoesNotIdentifyAWholePerson(t *testing.T) {
	// Both cases are real: screening 31,338 published designations produced exactly
	// these two false positives, each a one-word list entry scoring above the
	// threshold against an unrelated three-part name because it shared a prefix with
	// the middle name.
	for _, c := range []struct{ listed, queried string }{
		{"Micha", "Jonathan Micklethwaite Harbourne"},
		{"PERIA", "Priya Venkataraman Rao"},
	} {
		list := []Entry{subject(c.listed)}
		if m := Screen(Query{Name: c.queried}, list, Threshold); len(m) != 0 {
			t.Errorf("the fragment %q must not identify %q, scored %.4f",
				c.listed, c.queried, m[0].Score)
		}
	}
}

func TestSingleNameStillMatchesSingleName(t *testing.T) {
	// A mononym against a mononym is all the evidence there is, and a genuine
	// transliteration of one scores below both of the false positives above — so the
	// fragment rule must not reach this case.
	for _, c := range []struct{ listed, queried string }{
		{"Usama", "Osama"},
		{"Mohammed", "Mohammad"},
		{"Ivan", "Ivan"},
	} {
		list := []Entry{subject(c.listed)}
		if m := Screen(Query{Name: c.queried}, list, Threshold); len(m) != 1 {
			t.Errorf("%q must match the listed mononym %q, scored %.4f",
				c.queried, c.listed, Similarity(c.queried, c.listed))
		}
	}
}

func TestSingleNameMatchesAFullNameOnlyWhenExact(t *testing.T) {
	// An exact fragment is still evidence: a designated mononym that appears
	// verbatim as one of a customer's names must be reported.
	list := []Entry{subject("Ibrahim")}
	if m := Screen(Query{Name: "Abdullah Ibrahim"}, list, Threshold); len(m) != 1 {
		t.Fatalf("an exact single name must match a full name containing it, scored %.4f",
			Similarity("Abdullah Ibrahim", "Ibrahim"))
	}
}
