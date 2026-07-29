package retention

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

const org = "acme"

// base is fixed for the run so that ago(n) is one moment however often it is
// called, and an expiry computed in an assertion is the expiry the ledger stored.
var base = time.Now().UTC().Truncate(time.Second)

func ago(years int) time.Time { return base.AddDate(-years, 0, 0) }

func body(s string) []byte { return []byte(`{"tx":"` + s + `"}`) }

// The four shapes a caller retains, as helpers. The ledger has one write method
// that takes a whole record; these keep the tests reading like the instrument.

func open(l *Ledger, o, nature string, parties []string, at time.Time) (string, error) {
	return l.Retain(Record{
		Org: o, Class: ClassRelationship, Trigger: TriggerRelationshipEnd,
		Nature: nature, Parties: parties, Occurred: at, Body: body("rel"),
	})
}

func transact(l *Ledger, o, rel, ref string, parties []string, at time.Time, b []byte) (string, error) {
	trigger := TriggerOccasional
	if rel != "" {
		trigger = TriggerRelationshipEnd
	}
	return l.Retain(Record{
		Org: o, Class: ClassTransaction, Trigger: trigger, Relationship: rel,
		Ref: ref, Parties: parties, Occurred: at, Body: b,
	})
}

func refuse(l *Ledger, o, ref string, parties []string, at time.Time, reason string) (string, error) {
	return l.Retain(Record{
		Org: o, Class: ClassRefusal, Trigger: TriggerRefusal,
		Ref: ref, Parties: parties, Occurred: at, Reason: reason, Body: body("refused"),
	})
}

func assess(l *Ledger, o, rel, ref string, parties []string, a Assessment) (string, error) {
	trigger := TriggerOccasional
	if rel != "" {
		trigger = TriggerRelationshipEnd
	}
	return l.Retain(Record{
		Org: o, Class: ClassAssessment, Trigger: trigger, Relationship: rel,
		Ref: ref, Parties: parties, Occurred: a.At, Assessment: &a, Body: body("assessed"),
	})
}

func mustGet(t *testing.T, l *Ledger, id string) Record {
	t.Helper()
	r, err := l.Get(PurposeInvestigation, org, id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	return r
}

// TestThreeTriggers is AMLR Art. 77(3) in full: five years from the end of the
// relationship, from the occasional transaction, or from the date of refusal.
// The third is the one implementations miss, so it is asserted like the others.
func TestThreeTriggers(t *testing.T) {
	l := New()

	// From the occasional transaction.
	occasional, err := transact(l, org, "", "tx-occasional", []string{"subject:p1"}, ago(4), body("occ"))
	if err != nil {
		t.Fatalf("occasional transaction: %v", err)
	}
	r := mustGet(t, l, occasional)
	if r.Trigger != TriggerOccasional {
		t.Errorf("occasional trigger = %q", r.Trigger)
	}
	if want := ago(4).AddDate(Period, 0, 0); !r.Expiry().Equal(want) {
		t.Errorf("occasional expiry = %s, want %s", r.Expiry(), want)
	}

	// From the date of refusal.
	refused, err := refuse(l, org, "tx-refused", []string{"subject:p2"}, ago(4), "identification impossible")
	if err != nil {
		t.Fatalf("refusal: %v", err)
	}
	r = mustGet(t, l, refused)
	if r.Trigger != TriggerRefusal || r.Class != ClassRefusal {
		t.Errorf("refusal trigger/class = %q/%q", r.Trigger, r.Class)
	}
	if r.Reason != "identification impossible" {
		t.Errorf("refusal reason = %q", r.Reason)
	}
	if want := ago(4).AddDate(Period, 0, 0); !r.Expiry().Equal(want) {
		t.Errorf("refusal expiry = %s, want %s", r.Expiry(), want)
	}

	// From the end of the business relationship.
	rel, err := open(l, org, "payments", []string{"subject:p3"}, ago(9))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := mustGet(t, l, rel).Expiry(); !got.IsZero() {
		t.Fatalf("open relationship expires at %s, want never", got)
	}
	if _, err := l.Close(org, rel, ago(4)); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r = mustGet(t, l, rel)
	if r.Trigger != TriggerRelationshipEnd {
		t.Errorf("relationship trigger = %q", r.Trigger)
	}
	if want := ago(4).AddDate(Period, 0, 0); !r.Expiry().Equal(want) {
		t.Errorf("relationship expiry = %s, want %s", r.Expiry(), want)
	}
}

// TestRefusalNeedsAReason: a refusal with no reason answers nothing about why
// the firm refrained, which is the point of retaining it.
func TestRefusalNeedsAReason(t *testing.T) {
	l := New()
	if _, err := refuse(l, org, "tx-1", []string{"subject:p1"}, ago(1), ""); !errors.Is(err, ErrReason) {
		t.Fatalf("err = %v, want ErrReason", err)
	}
}

// TestTheClockIsNotTheCallersToSet: a caller that can name its own expiry can
// name one that never arrives, so Retain refuses a record that brings a clock.
func TestTheClockIsNotTheCallersToSet(t *testing.T) {
	l := New()
	_, err := l.Retain(Record{
		Org: org, Class: ClassRefusal, Trigger: TriggerRefusal, Ref: "tx-1",
		Parties: []string{"subject:p1"}, Occurred: ago(6), Reason: "refused",
		Body: body("x"),
		// Six years ago, so the record would arrive already expired; a hundred
		// years hence and it would never expire. Neither is the caller's to say.
		Start: base.AddDate(100, 0, 0),
	})
	if !errors.Is(err, ErrClock) {
		t.Fatalf("err = %v, want ErrClock", err)
	}
}

// TestTriggerAndRelationshipMustAgree: a trigger is a statement about where the
// period runs from, so it cannot contradict where the record sits.
func TestTriggerAndRelationshipMustAgree(t *testing.T) {
	l := New()
	rel, err := open(l, org, "payments", []string{"subject:p1"}, ago(3))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// An occasional transaction is by definition outside a relationship.
	_, err = l.Retain(Record{
		Org: org, Class: ClassTransaction, Trigger: TriggerOccasional, Relationship: rel,
		Ref: "tx-1", Parties: []string{"subject:p1"}, Occurred: ago(1), Body: body("x"),
	})
	if !errors.Is(err, ErrTrigger) {
		t.Errorf("occasional inside a relationship: err = %v, want ErrTrigger", err)
	}

	// Only a relationship record may run from a relationship end it does not name.
	_, err = l.Retain(Record{
		Org: org, Class: ClassTransaction, Trigger: TriggerRelationshipEnd,
		Ref: "tx-2", Parties: []string{"subject:p1"}, Occurred: ago(1), Body: body("x"),
	})
	if !errors.Is(err, ErrRelationship) {
		t.Errorf("relationship-end with no relationship: err = %v, want ErrRelationship", err)
	}

	// And an unknown trigger is not defaulted into one that works.
	_, err = l.Retain(Record{
		Org: org, Class: ClassTransaction, Trigger: Trigger("whenever"),
		Ref: "tx-3", Parties: []string{"subject:p1"}, Occurred: ago(1), Body: body("x"),
	})
	if !errors.Is(err, ErrTrigger) {
		t.Errorf("unknown trigger: err = %v, want ErrTrigger", err)
	}
}

// TestClockCascadesOnClose: a record retained inside a relationship cannot know
// its expiry when it is written, because the period runs from the end of the
// relationship. Closing the relationship starts every one of those clocks.
func TestClockCascadesOnClose(t *testing.T) {
	l := New()

	rel, err := open(l, org, "brokerage", []string{"subject:p1"}, ago(9))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	var inside []string
	for i := range 3 {
		id, err := transact(l, org, rel, fmt.Sprint("tx-", i), []string{"subject:p1"}, ago(8-i), body(fmt.Sprint("tx", i)))
		if err != nil {
			t.Fatalf("transaction: %v", err)
		}
		inside = append(inside, id)
		if got := mustGet(t, l, id).Expiry(); !got.IsZero() {
			t.Fatalf("in-relationship record expires at %s while the relationship is open", got)
		}
	}

	started, err := l.Close(org, rel, ago(4))
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if want := len(inside) + 1; started != want {
		t.Errorf("Close started %d clocks, want %d", started, want)
	}

	end := ago(4)
	for _, id := range inside {
		r := mustGet(t, l, id)
		if !r.Start.Equal(end) {
			t.Errorf("%s clock started %s, want %s", id, r.Start, end)
		}
		if want := end.AddDate(Period, 0, 0); !r.Expiry().Equal(want) {
			t.Errorf("%s expiry = %s, want %s", id, r.Expiry(), want)
		}
	}

	// A record written into an already-closed relationship inherits its clock
	// rather than starting a new one.
	late, err := transact(l, org, rel, "tx-late", []string{"subject:p1"}, ago(5), body("late"))
	if err != nil {
		t.Fatalf("transaction after close: %v", err)
	}
	if got := mustGet(t, l, late).Start; !got.Equal(end) {
		t.Errorf("late record clock = %s, want %s", got, end)
	}
}

// TestCloseRefusals: a relationship closes once, in its own org, and not before
// it opened.
func TestCloseRefusals(t *testing.T) {
	l := New()
	rel, err := open(l, org, "payments", []string{"subject:p1"}, ago(3))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if _, err := l.Close("other", rel, ago(1)); !errors.Is(err, ErrRelationship) {
		t.Errorf("cross-org close: err = %v, want ErrRelationship", err)
	}
	if _, err := l.Close(org, "no-such-id", ago(1)); !errors.Is(err, ErrRelationship) {
		t.Errorf("unknown close: err = %v, want ErrRelationship", err)
	}
	if _, err := l.Close(org, rel, ago(5)); !errors.Is(err, ErrOccurred) {
		t.Errorf("close before open: err = %v, want ErrOccurred", err)
	}

	if _, err := l.Close(org, rel, ago(1)); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := l.Close(org, rel, ago(1)); !errors.Is(err, ErrClosed) {
		t.Errorf("second close: err = %v, want ErrClosed", err)
	}
}

// TestDisposeDestroysExpiredAndProvesIt: the whole record and every index entry
// that referenced it, and nothing that is still within its period.
func TestDisposeDestroysExpiredAndProvesIt(t *testing.T) {
	l := New()

	expired, err := refuse(l, org, "tx-old", []string{"subject:gone", "name:gone"}, ago(6), "refused")
	if err != nil {
		t.Fatalf("refusal: %v", err)
	}
	live, err := refuse(l, org, "tx-new", []string{"subject:kept"}, ago(1), "refused")
	if err != nil {
		t.Fatalf("refusal: %v", err)
	}
	relationship, err := open(l, org, "payments", []string{"subject:open"}, ago(20))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	d, err := l.Dispose(time.Now().UTC())
	if err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if len(d.Disposed) != 1 || d.Disposed[0] != expired {
		t.Fatalf("disposed = %v, want [%s]", d.Disposed, expired)
	}
	if d.Examined != 3 {
		t.Errorf("examined = %d, want 3", d.Examined)
	}

	if _, err := l.Get(PurposeInvestigation, org, expired); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired record still readable: err = %v", err)
	}
	if _, err := l.Get(PurposeInvestigation, org, live); err != nil {
		t.Errorf("record within its period was destroyed: %v", err)
	}
	if _, err := l.Get(PurposeInvestigation, org, relationship); err != nil {
		t.Errorf("open relationship was destroyed: %v", err)
	}

	// The index entries went with it, so nothing can find a destroyed record.
	for _, party := range []string{"subject:gone", "name:gone"} {
		a, err := l.Lookback(PurposeDisclosure, org, party, time.Now().UTC())
		if err != nil {
			t.Fatalf("Lookback: %v", err)
		}
		if a.Examined != 0 {
			t.Errorf("party %q still indexes %d records", party, a.Examined)
		}
	}

	// A second run has nothing left to do and still proves it.
	again, err := l.Dispose(time.Now().UTC())
	if err != nil {
		t.Fatalf("second Dispose: %v", err)
	}
	if len(again.Disposed) != 0 {
		t.Errorf("second run disposed %v", again.Disposed)
	}
}

// TestDisposeCascadeLeavesNoDanglingIndex: closing a relationship expires it and
// everything inside it together, and the relationship index goes with them.
func TestDisposeCascadeLeavesNoDanglingIndex(t *testing.T) {
	l := New()
	rel, err := open(l, org, "payments", []string{"subject:p1"}, ago(12))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := transact(l, org, rel, "tx-1", []string{"subject:p1"}, ago(11), body("tx")); err != nil {
		t.Fatalf("transaction: %v", err)
	}
	if _, err := l.Close(org, rel, ago(6)); err != nil {
		t.Fatalf("Close: %v", err)
	}

	d, err := l.Dispose(time.Now().UTC())
	if err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if len(d.Disposed) != 2 {
		t.Fatalf("disposed %d records, want 2", len(d.Disposed))
	}
	if l.Len() != 0 {
		t.Errorf("ledger holds %d records, want 0", l.Len())
	}
	if len(l.parties) != 0 || len(l.inside) != 0 {
		t.Errorf("indexes left behind: %d party keys, %d relationships", len(l.parties), len(l.inside))
	}
}

// TestDisposeRefusesTheFuture: a disposal run cannot be talked into early
// destruction by a date the clock has not reached.
func TestDisposeRefusesTheFuture(t *testing.T) {
	l := New()
	id, err := refuse(l, org, "tx-1", []string{"subject:p1"}, ago(1), "refused")
	if err != nil {
		t.Fatalf("refusal: %v", err)
	}

	if _, err := l.Dispose(time.Now().UTC().AddDate(10, 0, 0)); !errors.Is(err, ErrFuture) {
		t.Fatalf("err = %v, want ErrFuture", err)
	}
	if _, err := l.Get(PurposeInvestigation, org, id); err != nil {
		t.Fatalf("record destroyed by a future date: %v", err)
	}
}

// TestDisposeReportsNoSuccessWhenProofFails: the run must not report a count it
// cannot stand behind. The ledger is corrupted deliberately here — an index entry
// pointing at a record that is not there — because that is the shape of the bug a
// disposal job hides when it counts only its own delete statements.
func TestDisposeReportsNoSuccessWhenProofFails(t *testing.T) {
	l := New()
	if _, err := refuse(l, org, "tx-1", []string{"subject:p1"}, ago(1), "refused"); err != nil {
		t.Fatalf("refusal: %v", err)
	}
	l.parties[partyKey(org, "subject:ghost")] = []string{"a-record-that-is-not-there"}

	d, err := l.Dispose(time.Now().UTC())
	if !errors.Is(err, ErrDisposal) {
		t.Fatalf("err = %v, want ErrDisposal", err)
	}
	if d.Examined != 0 || len(d.Disposed) != 0 {
		t.Fatalf("failed run reported work: %+v", d)
	}
}

// TestLookbackAnswersArticle78: is or was a relationship maintained with this
// party in the prior five years, and what was its nature.
func TestLookbackAnswersArticle78(t *testing.T) {
	l := New()
	now := time.Now().UTC()

	// Ended inside the window.
	recent, err := open(l, org, "payments", []string{"name:ivan petrov"}, ago(9))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := l.Close(org, recent, ago(2)); err != nil {
		t.Fatalf("Close: %v", err)
	}

	a, err := l.Lookback(PurposeDisclosure, org, "name:ivan petrov", now)
	if err != nil {
		t.Fatalf("Lookback: %v", err)
	}
	if !a.Maintained {
		t.Error("relationship ended two years ago was not found")
	}
	if a.Current {
		t.Error("a closed relationship reported as current")
	}
	if len(a.Natures) != 1 || a.Natures[0] != "payments" {
		t.Errorf("natures = %v, want [payments]", a.Natures)
	}
	if len(a.Records) != 1 || a.Records[0] != recent {
		t.Errorf("records = %v, want [%s]", a.Records, recent)
	}
	if !a.From.Equal(now.AddDate(-Period, 0, 0)) || !a.To.Equal(now) {
		t.Errorf("window %s..%s is not the prior five years", a.From, a.To)
	}

	// Still open: the answer is "is", not "was".
	if _, err := open(l, org, "custody", []string{"name:open party"}, ago(8)); err != nil {
		t.Fatalf("open: %v", err)
	}
	a, err = l.Lookback(PurposeDisclosure, org, "name:open party", now)
	if err != nil {
		t.Fatalf("Lookback: %v", err)
	}
	if !a.Maintained || !a.Current {
		t.Errorf("open relationship: maintained=%v current=%v", a.Maintained, a.Current)
	}

	// Ended before the window: not within the prior five years.
	old, err := open(l, org, "payments", []string{"name:old party"}, ago(20))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := l.Close(org, old, ago(7)); err != nil {
		t.Fatalf("Close: %v", err)
	}
	a, err = l.Lookback(PurposeDisclosure, org, "name:old party", now)
	if err != nil {
		t.Fatalf("Lookback: %v", err)
	}
	if a.Maintained {
		t.Error("a relationship that ended seven years ago is inside the five-year window")
	}
	if a.Examined != 1 {
		t.Errorf("examined = %d, want 1 (the record exists, it is out of window)", a.Examined)
	}

	// A party with nothing on file.
	a, err = l.Lookback(PurposeDisclosure, org, "name:stranger", now)
	if err != nil {
		t.Fatalf("Lookback: %v", err)
	}
	if a.Maintained || a.Examined != 0 || len(a.Records) != 0 {
		t.Errorf("stranger: %+v", a)
	}
}

// TestLookbackIgnoresRecordsThatAreNotRelationships: Art. 78 asks about business
// relationships, and a transaction with a party is not one.
func TestLookbackIgnoresRecordsThatAreNotRelationships(t *testing.T) {
	l := New()
	if _, err := transact(l, org, "", "tx-1", []string{"name:one off"}, ago(1), body("x")); err != nil {
		t.Fatalf("transaction: %v", err)
	}

	a, err := l.Lookback(PurposeDisclosure, org, "name:one off", time.Now().UTC())
	if err != nil {
		t.Fatalf("Lookback: %v", err)
	}
	if a.Maintained {
		t.Error("an occasional transaction was reported as a business relationship")
	}
	if a.Examined != 1 {
		t.Errorf("examined = %d, want 1", a.Examined)
	}
}

// TestLookbackIsIndexedNotScanned: the answer must be fully and speedily
// available, so the work a lookback does is proportional to one party's records
// and not to the ledger. Examined is that proof and it stays flat.
func TestLookbackIsIndexedNotScanned(t *testing.T) {
	l := New()
	now := time.Now().UTC()

	if _, err := open(l, org, "payments", []string{"name:target"}, ago(3)); err != nil {
		t.Fatalf("open: %v", err)
	}

	before, err := l.Lookback(PurposeDisclosure, org, "name:target", now)
	if err != nil {
		t.Fatalf("Lookback: %v", err)
	}

	for i := range 5000 {
		ref := fmt.Sprintf("tx-noise-%d", i)
		if _, err := refuse(l, org, ref, []string{"subject:noise-" + ref}, ago(1), "refused"); err != nil {
			t.Fatalf("refusal: %v", err)
		}
	}

	after, err := l.Lookback(PurposeDisclosure, org, "name:target", now)
	if err != nil {
		t.Fatalf("Lookback: %v", err)
	}
	if after.Examined != before.Examined || after.Examined != 1 {
		t.Fatalf("examined went from %d to %d as the ledger grew to %d records",
			before.Examined, after.Examined, l.Len())
	}
	if !after.Maintained {
		t.Error("answer changed as unrelated records were added")
	}
}

// TestOrgBoundary: no read crosses it, in either direction, on any surface.
func TestOrgBoundary(t *testing.T) {
	l := New()
	const other = "beta"

	mine, err := open(l, org, "payments", []string{"name:shared party"}, ago(2))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if _, err := l.Get(PurposeInvestigation, other, mine); !errors.Is(err, ErrNotFound) {
		t.Errorf("cross-org Get: err = %v, want ErrNotFound", err)
	}
	a, err := l.Lookback(PurposeDisclosure, other, "name:shared party", time.Now().UTC())
	if err != nil {
		t.Fatalf("Lookback: %v", err)
	}
	if a.Maintained || a.Examined != 0 {
		t.Errorf("cross-org lookback saw %d records: %+v", a.Examined, a)
	}

	seen := 0
	if err := l.Each(PurposeMonitoring, other, "", func(Record) error { seen++; return nil }); err != nil {
		t.Fatalf("Each: %v", err)
	}
	if seen != 0 {
		t.Errorf("cross-org Each saw %d records", seen)
	}
	if err := l.Extend(other, mine, time.Hour, "reason", "who"); err == nil {
		t.Error("cross-org Extend succeeded")
	}
	// A transaction cannot be filed into another org's relationship.
	if _, err := transact(l, other, mine, "tx-1", []string{"subject:p"}, ago(1), body("x")); !errors.Is(err, ErrRelationship) {
		t.Errorf("cross-org transaction: err = %v, want ErrRelationship", err)
	}
}

// TestReadsRefuseOtherPurposes: retained personal data is for preventing money
// laundering and terrorist financing, so a commercial purpose gets no data.
func TestReadsRefuseOtherPurposes(t *testing.T) {
	l := New()
	id, err := refuse(l, org, "tx-1", []string{"subject:p1"}, ago(1), "refused")
	if err != nil {
		t.Fatalf("refusal: %v", err)
	}

	for _, p := range []Purpose{"marketing", "credit scoring", "model training", ""} {
		if _, err := l.Get(p, org, id); !errors.Is(err, ErrPurpose) {
			t.Errorf("Get(%q): err = %v, want ErrPurpose", p, err)
		}
		if err := l.Each(p, org, "", func(Record) error { return nil }); !errors.Is(err, ErrPurpose) {
			t.Errorf("Each(%q): err = %v, want ErrPurpose", p, err)
		}
		if _, err := l.Lookback(p, org, "subject:p1", time.Now().UTC()); !errors.Is(err, ErrPurpose) {
			t.Errorf("Lookback(%q): err = %v, want ErrPurpose", p, err)
		}
	}
}

// TestRecordsAreNotRedactableThroughAReader: the ledger hands out copies, so a
// reader that alters what it was given has not altered the retained record.
// Redaction is prohibited, and here it is also not reachable.
func TestRecordsAreNotRedactableThroughAReader(t *testing.T) {
	l := New()
	id, err := assess(l, org, "", "case-1", []string{"subject:p1"}, Assessment{
		Considered: []string{"velocity", "profile"},
		Result:     NotReported,
		Rationale:  "consistent with the profile on file",
		By:         "mlro",
		At:         ago(1),
	})
	if err != nil {
		t.Fatalf("assessment: %v", err)
	}

	got := mustGet(t, l, id)
	got.Body[0] = 'X'
	got.Parties[0] = "subject:someone-else"
	got.Assessment.Rationale = ""
	got.Assessment.Considered = nil
	got.Nature = "rewritten"

	after := mustGet(t, l, id)
	if after.Body[0] == 'X' {
		t.Error("a reader rewrote the retained body")
	}
	if after.Parties[0] != "subject:p1" {
		t.Errorf("a reader rewrote the party index key: %q", after.Parties[0])
	}
	if after.Assessment.Rationale == "" || len(after.Assessment.Considered) != 2 {
		t.Error("a reader redacted the retained assessment")
	}

	// The same through a walk, which is the other way out of the ledger.
	if err := l.Each(PurposeInvestigation, org, "", func(r Record) error {
		r.Body[0] = 'Y'
		r.Assessment.Rationale = "rewritten"
		return nil
	}); err != nil {
		t.Fatalf("Each: %v", err)
	}
	walked := mustGet(t, l, id)
	if walked.Body[0] == 'Y' || walked.Assessment.Rationale == "rewritten" {
		t.Error("a walk rewrote the retained record")
	}
}

// TestExtendRefusals: an extension is a decision somebody made about one case,
// within five further years, once.
func TestExtendRefusals(t *testing.T) {
	l := New()
	id, err := refuse(l, org, "tx-1", []string{"subject:p1"}, ago(1), "refused")
	if err != nil {
		t.Fatalf("refusal: %v", err)
	}
	relationship, err := open(l, org, "payments", []string{"subject:p2"}, ago(1))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	for name, call := range map[string]error{
		"no reason":       l.Extend(org, id, time.Hour, "", "mlro"),
		"no decider":      l.Extend(org, id, time.Hour, "reason", ""),
		"zero period":     l.Extend(org, id, 0, "reason", "mlro"),
		"negative":        l.Extend(org, id, -time.Hour, "reason", "mlro"),
		"over the cap":    l.Extend(org, id, 6*365*24*time.Hour, "reason", "mlro"),
		"clock unstarted": l.Extend(org, relationship, time.Hour, "reason", "mlro"),
	} {
		if call == nil {
			t.Errorf("%s: extension accepted", name)
		}
	}
	if err := l.Extend(org, "no-such-record", time.Hour, "reason", "mlro"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown record: err = %v, want ErrNotFound", err)
	}

	if err := l.Extend(org, id, 365*24*time.Hour, "live investigation", "mlro"); err != nil {
		t.Fatalf("Extend: %v", err)
	}
	if err := l.Extend(org, id, time.Hour, "again", "mlro"); !errors.Is(err, ErrExtension) {
		t.Errorf("second extension: err = %v, want ErrExtension", err)
	}

	r := mustGet(t, l, id)
	if r.Extended == nil || r.Extended.Who != "mlro" || r.Extended.Reason != "live investigation" {
		t.Fatalf("extension not recorded: %+v", r.Extended)
	}
	if want := r.Start.AddDate(Period, 0, 0).Add(365 * 24 * time.Hour); !r.Expiry().Equal(want) {
		t.Errorf("expiry = %s, want %s", r.Expiry(), want)
	}
}

// TestExtensionSurvivesDisposal: an extended record is not swept up on the date
// it would otherwise have expired.
func TestExtensionSurvivesDisposal(t *testing.T) {
	l := New()
	id, err := refuse(l, org, "tx-1", []string{"subject:p1"}, ago(6), "refused")
	if err != nil {
		t.Fatalf("refusal: %v", err)
	}
	if err := l.Extend(org, id, 2*365*24*time.Hour, "live investigation", "mlro"); err != nil {
		t.Fatalf("Extend: %v", err)
	}

	d, err := l.Dispose(time.Now().UTC())
	if err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if len(d.Disposed) != 0 {
		t.Fatalf("disposed an extended record: %v", d.Disposed)
	}
	if _, err := l.Get(PurposeInvestigation, org, id); err != nil {
		t.Fatalf("extended record destroyed: %v", err)
	}
}

// TestWriteRefusals: what the ledger will not accept, because a record that
// cannot answer for itself is not worth retaining.
func TestWriteRefusals(t *testing.T) {
	l := New()
	rel, err := open(l, org, "payments", []string{"subject:p1"}, ago(1))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if _, err := transact(l, "", "", "tx-1", []string{"subject:p1"}, ago(1), body("x")); !errors.Is(err, ErrOrg) {
		t.Errorf("no org: err = %v, want ErrOrg", err)
	}
	if _, err := transact(l, org, "", "tx-1", nil, ago(1), body("x")); !errors.Is(err, ErrParties) {
		t.Errorf("no parties: err = %v, want ErrParties", err)
	}
	if _, err := transact(l, org, "", "tx-1", []string{""}, ago(1), body("x")); !errors.Is(err, ErrParties) {
		t.Errorf("empty party: err = %v, want ErrParties", err)
	}
	// MLR reg. 40(2)(b): the record must be enough to reconstruct the transaction.
	if _, err := transact(l, org, "", "tx-1", []string{"subject:p1"}, ago(1), nil); !errors.Is(err, ErrBody) {
		t.Errorf("no body: err = %v, want ErrBody", err)
	}
	if _, err := transact(l, org, "", "", []string{"subject:p1"}, ago(1), body("x")); !errors.Is(err, ErrRef) {
		t.Errorf("no reference: err = %v, want ErrRef", err)
	}
	if _, err := transact(l, org, "", "tx-1", []string{"subject:p1"}, time.Time{}, body("x")); !errors.Is(err, ErrOccurred) {
		t.Errorf("no event date: err = %v, want ErrOccurred", err)
	}
	if _, err := open(l, org, "", []string{"subject:p1"}, ago(1)); !errors.Is(err, ErrNature) {
		t.Errorf("no nature: err = %v, want ErrNature", err)
	}
	if _, err := transact(l, org, "not-a-relationship", "tx-1", []string{"subject:p1"}, ago(1), body("x")); !errors.Is(err, ErrRelationship) {
		t.Errorf("unknown relationship: err = %v, want ErrRelationship", err)
	}
	// A transaction record is not a container for other records.
	tx, err := transact(l, org, rel, "tx-2", []string{"subject:p1"}, ago(1), body("x"))
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}
	if _, err := transact(l, org, tx, "tx-3", []string{"subject:p1"}, ago(1), body("x")); !errors.Is(err, ErrRelationship) {
		t.Errorf("transaction as relationship: err = %v, want ErrRelationship", err)
	}
	if _, err := assess(l, org, "", "case-1", []string{"subject:p1"}, Assessment{
		Result: NotReported, At: ago(1), // no rationale, nothing considered, nobody decided
	}); !errors.Is(err, ErrAssessment) {
		t.Errorf("incomplete assessment: err = %v, want ErrAssessment", err)
	}
	if _, err := l.Retain(Record{
		Org: org, Class: Class("guesswork"), Trigger: TriggerRefusal, Ref: "x",
		Parties: []string{"subject:p1"}, Occurred: ago(1), Body: body("x"),
	}); !errors.Is(err, ErrClass) {
		t.Errorf("unknown class: err = %v, want ErrClass", err)
	}
}

// TestEachIsOrderedOldestFirst: a file produced from the ledger is reproducible.
func TestEachIsOrderedOldestFirst(t *testing.T) {
	l := New()
	for i, years := range []int{1, 4, 2, 3} {
		if _, err := refuse(l, org, fmt.Sprint("tx-", i), []string{"subject:p1"}, ago(years), "refused"); err != nil {
			t.Fatalf("refusal: %v", err)
		}
	}

	var seen []time.Time
	if err := l.Each(PurposeRetention, org, ClassRefusal, func(r Record) error {
		seen = append(seen, r.Occurred)
		return nil
	}); err != nil {
		t.Fatalf("Each: %v", err)
	}
	if len(seen) != 4 {
		t.Fatalf("saw %d records, want 4", len(seen))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i].Before(seen[i-1]) {
			t.Fatalf("out of order at %d: %s before %s", i, seen[i], seen[i-1])
		}
	}
}

// TestEachFiltersByClass: the class filter is a filter, and an empty one means
// every class.
func TestEachFiltersByClass(t *testing.T) {
	l := New()
	if _, err := refuse(l, org, "tx-1", []string{"subject:p1"}, ago(1), "refused"); err != nil {
		t.Fatalf("refusal: %v", err)
	}
	if _, err := transact(l, org, "", "tx-2", []string{"subject:p1"}, ago(1), body("x")); err != nil {
		t.Fatalf("transaction: %v", err)
	}

	count := func(c Class) int {
		n := 0
		if err := l.Each(PurposeMonitoring, org, c, func(Record) error { n++; return nil }); err != nil {
			t.Fatalf("Each(%q): %v", c, err)
		}
		return n
	}
	if got := count(ClassRefusal); got != 1 {
		t.Errorf("refusals = %d, want 1", got)
	}
	if got := count(ClassTransaction); got != 1 {
		t.Errorf("transactions = %d, want 1", got)
	}
	if got := count(""); got != 2 {
		t.Errorf("all classes = %d, want 2", got)
	}
	if got := count(ClassAssessment); got != 0 {
		t.Errorf("assessments = %d, want 0", got)
	}
}

// TestEachStopsOnError: a caller can abandon the walk, and the error is its own.
func TestEachStopsOnError(t *testing.T) {
	l := New()
	for i := range 3 {
		if _, err := refuse(l, org, fmt.Sprint("tx-", i), []string{"subject:p1"}, ago(1), "refused"); err != nil {
			t.Fatalf("refusal: %v", err)
		}
	}

	stop := errors.New("enough")
	seen := 0
	err := l.Each(PurposeMonitoring, org, "", func(Record) error {
		seen++
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatalf("err = %v, want the caller's error", err)
	}
	if seen != 1 {
		t.Errorf("visited %d records after the error, want 1", seen)
	}
}
