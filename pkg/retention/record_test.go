package retention

import (
	"errors"
	"testing"
	"time"
)

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// TestExpiryIsFiveCalendarYears: the period is five years, not 1825 days. The
// dates here span two leap years, so a duration constant gets this wrong.
func TestExpiryIsFiveCalendarYears(t *testing.T) {
	for _, c := range []struct{ start, want time.Time }{
		{date(2020, time.January, 1), date(2025, time.January, 1)},
		{date(2020, time.February, 29), date(2025, time.March, 1)},
		{date(2021, time.June, 30), date(2026, time.June, 30)},
	} {
		r := Record{Class: ClassRefusal, Trigger: TriggerRefusal, Start: c.start}
		if got := r.Expiry(); !got.Equal(c.want) {
			t.Errorf("start %s: expiry = %s, want %s",
				c.start.Format(time.DateOnly), got.Format(time.DateOnly), c.want.Format(time.DateOnly))
		}
	}
}

// TestClockNotStartedNeverExpires: an open business relationship has no expiry,
// because the five years run from the end of it. A record with no clock must
// never be swept up by a disposal run.
func TestClockNotStartedNeverExpires(t *testing.T) {
	r := Record{Class: ClassRelationship, Trigger: TriggerRelationshipEnd, Occurred: date(1999, time.January, 1)}

	if got := r.Expiry(); !got.IsZero() {
		t.Fatalf("expiry = %s, want zero", got)
	}
	if r.Expired(time.Now().UTC().AddDate(500, 0, 0)) {
		t.Fatal("a relationship with no end date expired")
	}
}

// TestExpiredBoundary: the record is expired on the expiry date, not the day
// after. Deletion is due when the period has run out.
func TestExpiredBoundary(t *testing.T) {
	start := date(2020, time.January, 1)
	r := Record{Class: ClassRefusal, Trigger: TriggerRefusal, Start: start}
	expiry := date(2025, time.January, 1)

	if r.Expired(expiry.Add(-time.Nanosecond)) {
		t.Error("expired one instant early")
	}
	if !r.Expired(expiry) {
		t.Error("not expired on the expiry date")
	}
	if !r.Expired(expiry.Add(time.Hour)) {
		t.Error("not expired after the expiry date")
	}
}

// TestExtensionCappedAtFiveFurtherYears: whatever an extension claims, the
// expiry never passes ten years from the trigger.
func TestExtensionCappedAtFiveFurtherYears(t *testing.T) {
	start := date(2020, time.January, 1)
	r := Record{
		Class:    ClassRefusal,
		Trigger:  TriggerRefusal,
		Start:    start,
		Extended: &Extended{By: 40 * 365 * 24 * time.Hour, Reason: "forged", Who: "nobody"},
	}

	want := date(2030, time.January, 1)
	if got := r.Expiry(); !got.Equal(want) {
		t.Fatalf("expiry = %s, want %s (five years plus five further)",
			got.Format(time.DateOnly), want.Format(time.DateOnly))
	}
}

// TestExtensionMovesExpiry: an extension inside the cap does what it says.
func TestExtensionMovesExpiry(t *testing.T) {
	start := date(2020, time.January, 1)
	plain := Record{Class: ClassRefusal, Trigger: TriggerRefusal, Start: start}
	extended := plain
	extended.Extended = &Extended{By: 365 * 24 * time.Hour, Reason: "live investigation", Who: "mlro"}

	if !extended.Expiry().After(plain.Expiry()) {
		t.Fatalf("extended expiry %s is not after %s", extended.Expiry(), plain.Expiry())
	}
	if got, want := extended.Expiry(), plain.Expiry().Add(365*24*time.Hour); !got.Equal(want) {
		t.Fatalf("expiry = %s, want %s", got, want)
	}
}

// TestTransactionCeilingBoundsExtensionsNotTheFloor is the reg. 40(4) ten-year
// ceiling, and the resolution of its conflict with the AMLR: it can shorten an
// extension, and it can never shorten the five years the AMLR requires from the
// end of the relationship.
func TestTransactionCeilingBoundsExtensionsNotTheFloor(t *testing.T) {
	// Transaction in 2019, relationship ended 2022. Floor 2027, extension to
	// 2032, ceiling 2029 — the ceiling bites.
	bites := Record{
		Class:    ClassTransaction,
		Trigger:  TriggerRelationshipEnd,
		Occurred: date(2019, time.January, 1),
		Start:    date(2022, time.January, 1),
		Extended: &Extended{By: 5 * 365 * 24 * time.Hour, Reason: "r", Who: "w"},
	}
	if got, want := bites.Expiry(), date(2029, time.January, 1); !got.Equal(want) {
		t.Errorf("ceiling did not bind the extension: expiry = %s, want %s",
			got.Format(time.DateOnly), want.Format(time.DateOnly))
	}

	// Transaction in 2010, relationship ended 2024. Ceiling 2020 is before the
	// mandatory floor 2029, so the floor wins and nothing is destroyed early.
	floorWins := Record{
		Class:    ClassTransaction,
		Trigger:  TriggerRelationshipEnd,
		Occurred: date(2010, time.January, 1),
		Start:    date(2024, time.January, 1),
	}
	if got, want := floorWins.Expiry(), date(2029, time.January, 1); !got.Equal(want) {
		t.Errorf("ceiling cut below the mandatory period: expiry = %s, want %s",
			got.Format(time.DateOnly), want.Format(time.DateOnly))
	}

	// And it does not touch other classes: a refusal is not an in-relationship
	// transaction record.
	refusal := Record{
		Class:    ClassRefusal,
		Trigger:  TriggerRefusal,
		Occurred: date(2010, time.January, 1),
		Start:    date(2024, time.January, 1),
	}
	if got, want := refusal.Expiry(), date(2029, time.January, 1); !got.Equal(want) {
		t.Errorf("refusal expiry = %s, want %s", got.Format(time.DateOnly), want.Format(time.DateOnly))
	}
}

// TestAssessmentRequiresItsParts is the record most systems miss: a dismissed
// alert is a retained decision with its rationale. An assessment missing any
// part of Art. 69(2) is refused, so it cannot be retained as an empty gesture.
func TestAssessmentRequiresItsParts(t *testing.T) {
	whole := Assessment{
		Considered: []string{"velocity over 30 days", "customer profile"},
		Result:     NotReported,
		Rationale:  "salary payments consistent with the profile on file",
		By:         "mlro",
		At:         time.Now().UTC(),
	}
	if err := whole.validate(); err != nil {
		t.Fatalf("complete assessment refused: %v", err)
	}

	for name, broken := range map[string]func(*Assessment){
		"no result":          func(a *Assessment) { a.Result = "" },
		"bad result":         func(a *Assessment) { a.Result = "maybe" },
		"nothing considered": func(a *Assessment) { a.Considered = nil },
		"no rationale":       func(a *Assessment) { a.Rationale = "" },
		"no decider":         func(a *Assessment) { a.By = "" },
		"no date":            func(a *Assessment) { a.At = time.Time{} },
	} {
		a := whole.clone()
		broken(&a)
		if err := a.validate(); !errors.Is(err, ErrAssessment) {
			t.Errorf("%s: err = %v, want ErrAssessment", name, err)
		}
	}

	// A report needs its reasons too: AMLR Art. 77(1)(b) retains the result
	// either way.
	reported := whole.clone()
	reported.Result = Reported
	reported.Rationale = ""
	if err := reported.validate(); !errors.Is(err, ErrAssessment) {
		t.Errorf("reported without rationale: err = %v, want ErrAssessment", err)
	}
}

// TestCheckNowRefusesTheFuture: destruction does not run on a date the clock has
// not reached, so a clock lie cannot bring it forward.
func TestCheckNowRefusesTheFuture(t *testing.T) {
	if err := checkNow(time.Now().UTC()); err != nil {
		t.Errorf("now refused: %v", err)
	}
	if err := checkNow(time.Now().UTC().Add(30 * time.Second)); err != nil {
		t.Errorf("now within clock skew refused: %v", err)
	}
	if err := checkNow(time.Now().UTC().Add(time.Hour)); !errors.Is(err, ErrFuture) {
		t.Errorf("an hour ahead: err = %v, want ErrFuture", err)
	}
	if err := checkNow(time.Now().UTC().AddDate(6, 0, 0)); !errors.Is(err, ErrFuture) {
		t.Errorf("six years ahead: err = %v, want ErrFuture", err)
	}
}

// TestPurposeIsClosed: only money laundering and terrorist financing prevention.
// Anything else has no representation the ledger accepts.
func TestPurposeIsClosed(t *testing.T) {
	for _, p := range []Purpose{PurposeMonitoring, PurposeInvestigation, PurposeDisclosure, PurposeRetention} {
		if !p.permitted() {
			t.Errorf("%q should be permitted", p)
		}
	}
	for _, p := range []Purpose{"", "marketing", "analytics", "credit scoring", "training", "MONITORING"} {
		if p.permitted() {
			t.Errorf("%q should not be permitted", p)
		}
	}
}
