package sanctions

import "testing"

// matches reports whether a screening query would be reported against an entry.
func matches(query, entry string) bool { return Similarity(query, entry) >= Threshold }

// A customer key must not be reportable as a designated party.
//
// This is the defect these tests exist for, and it reached production behaviour: a
// transaction for customer "cust-1" was BLOCKED, naming a sanctions hit as the
// reason, because the key scored 0.902 against "BRANCH 1 OF THE SHIRAZ
// REVOLUTIONARY COUNCIL". The mean of the token partnerships carried it — the
// digit 1 matched a digit 1 exactly, while the only part that could identify
// anybody, "cust" against "council", scored 0.64.
func TestCustomerKeyIsNotADesignatedParty(t *testing.T) {
	for _, tc := range []struct{ key, entry string }{
		{"cust-1", "BRANCH 1 OF THE SHIRAZ REVOLUTIONARY COUNCIL"},
		{"cust-1", "CONSTELLO NO. 1 CORPORATION"},
		{"acct-1", "BRANCH 1 OF THE SHIRAZ REVOLUTIONARY COUNCIL"},
		{"user-42", "UNIT 42 OF THE ISLAMIC REVOLUTIONARY GUARD"},
		{"cust one", "BRANCH ONE OF THE SHIRAZ REVOLUTIONARY COUNCIL"},
		{"id-7", "GROUP 7 HOLDINGS COMPANY"},
	} {
		if matches(tc.key, tc.entry) {
			t.Errorf("customer key %q reported against %q at %.3f", tc.key, tc.entry, Similarity(tc.key, tc.entry))
		}
	}
}

// A query of nothing but initials and digits identifies nobody, however exactly
// its parts match.
//
// "a-1" scored a perfect 1.000 against "A ONE TRADING COMPANY 1" before this: each
// of its two trivial tokens found an exact partner, and the mean of two exact
// partnerships is exact. Every one-character token matches some one-character
// token somewhere in a list of 31,338 designations.
func TestATrivialQueryIdentifiesNobody(t *testing.T) {
	for _, tc := range []struct{ key, entry string }{
		{"a-1", "A ONE TRADING COMPANY 1"},
		{"1", "GROUP 1"},
		{"u1", "U ONE LIMITED"},
		{"7-9", "7 9 HOLDINGS"},
	} {
		s := Similarity(tc.key, tc.entry)
		if s >= Threshold {
			t.Errorf("%q reported against %q at %.3f with nothing identifying in it", tc.key, tc.entry, s)
		}
		// Exactly zero, not NaN: a NaN compares false against every threshold, so it
		// would read as no-match while being unorderable and refusing to marshal.
		if s != 0 {
			t.Errorf("%q vs %q scored %v, want exactly 0", tc.key, tc.entry, s)
		}
	}
}

// The weakest part decides, not the average. This is what separates a variant
// spelling of one person from a coincidence between two, and no threshold on the
// mean can do it: the false positives scored 0.86 and 0.88 while a genuine
// transliteration of Osama scores 0.87.
func TestEveryIdentifyingPartMustMatch(t *testing.T) {
	// Genuine variation perturbs every part a little and none of them much.
	for _, tc := range []struct{ query, entry string }{
		{"Osama bin Laden", "Usama bin Ladin"},
		{"Mohammed Ali", "Muhammad Ali"},
		{"Vladimir Putin", "Vladimir Vladimirovich PUTIN"},
		{"Un, Kim Jong", "Kim Jong Un"},
		{"Zhang Wei", "ZHANG WEI"},
		{"Ivan Petrov", "IVAN PETROV"},
	} {
		if !matches(tc.query, tc.entry) {
			t.Errorf("%q did not match %q — score %.3f", tc.query, tc.entry, Similarity(tc.query, tc.entry))
		}
	}

	// One part matching perfectly must not carry a part that does not.
	//
	// These are the cases the floor exists for: two identifying parts, so the
	// fragment rule does not apply, and a mean that clears the whole-name threshold
	// on the strength of an exact surname. Only the weakest part separates them.
	for _, tc := range []struct{ query, entry string }{
		{"Smith Petrov", "Smirnov Petrov"},
		{"Vladimir Kuznetsov", "Vladimir Vladimirovich Putin"},
		{"cust one", "BRANCH ONE OF THE SHIRAZ REVOLUTIONARY COUNCIL"},
	} {
		if matches(tc.query, tc.entry) {
			t.Errorf("%q reported against %q at %.3f on the strength of one exact part",
				tc.query, tc.entry, Similarity(tc.query, tc.entry))
		}
	}

	// And the floor must not be raised to the whole-name bar: a genuine
	// transliteration sits below it. Yusuf against Yousef is 0.840, so a floor at
	// Threshold would stop a real customer matching a real designation.
	if !matches("Yusuf Ibrahim", "Yousef Ibrahim") {
		t.Errorf("a genuine transliteration was refused: %.3f — the part floor is too strict",
			Similarity("Yusuf Ibrahim", "Yousef Ibrahim"))
	}
}

// Initials, particles and digits are read past rather than counted. They cannot
// establish an identity, and they must not prevent one either.
func TestIncidentalTokensAreReadPast(t *testing.T) {
	for _, tc := range []struct {
		query, entry string
		want         bool
		why          string
	}{
		{"al Assad", "Bashar al-Assad", true, "a two-letter particle must not be required to identify anybody"},
		{"J Smith", "John Smith", true, "an initial must not disqualify a surname that matches exactly"},
		{"Unit 42", "UNIT 42 OF THE ISLAMIC REVOLUTIONARY GUARD", true, "a digit must not disqualify a word that matches exactly"},
	} {
		if got := matches(tc.query, tc.entry); got != tc.want {
			t.Errorf("%q vs %q = %v (%.3f), want %v: %s",
				tc.query, tc.entry, got, Similarity(tc.query, tc.entry), tc.want, tc.why)
		}
	}
}

// identifies is the judgement about what can name somebody. The shortest real name
// parts have to survive it, or every Arabic, Dutch and Chinese name breaks.
func TestIdentifiesAdmitsTheShortestRealNameParts(t *testing.T) {
	for _, tok := range []string{"bin", "van", "del", "ibn", "wei", "ali", "kim", "putin", "assad"} {
		if !identifies(tok) {
			t.Errorf("%q is a real name part and was treated as incidental", tok)
		}
	}
	for _, tok := range []string{"a", "j", "1", "42", "al", "7", "", "007", "12345", "1000000"} {
		if identifies(tok) {
			t.Errorf("%q cannot identify anybody and was treated as identifying", tok)
		}
	}
}

// An exact whole-name match is a match whatever the name is composed of, so the
// composition rules cannot suppress the case they are least entitled to judge.
func TestAnExactNameAlwaysMatches(t *testing.T) {
	for _, n := range []string{"Vladimir Putin", "A-1", "42", "Wallet"} {
		if s := Similarity(n, n); s != 1 {
			t.Errorf("%q against itself scored %.3f, want 1", n, s)
		}
	}
}

// A legal-form suffix names the form a company takes, not which company it is.
// Every company has one, so matching it is agreement about company-ness: "acme-ltd"
// was reported against an unrelated "ACE (HK) Electronics Technology Co., Ltd" at
// 0.967 on the strength of Ltd matching Ltd.
func TestLegalFormDoesNotIdentifyACompany(t *testing.T) {
	for _, tok := range []string{"ltd", "limited", "inc", "llc", "gmbh", "plc", "ooo", "fze", "pte"} {
		if identifies(tok) {
			t.Errorf("%q is a legal form and was treated as identifying the company", tok)
		}
	}
	if matches("acme-ltd", "ACE (HK) Electronics Technology Co., Ltd") {
		t.Errorf("two unrelated companies matched on their legal form: %.3f",
			Similarity("acme-ltd", "ACE (HK) Electronics Technology Co., Ltd"))
	}
	// But a designated company must still match on its own name, suffix and all.
	if !matches("Smile Wallet Limited", "SMILE WALLET LIMITED") {
		t.Error("a designated company did not match its own name")
	}
	if !matches("Bank Melli Ltd", "BANK MELLI LIMITED") {
		t.Errorf("a company matched on its distinctive name was refused: %.3f",
			Similarity("Bank Melli Ltd", "BANK MELLI LIMITED"))
	}
}
