package retention

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/tests"

	_ "github.com/hanzoai/base/migrations"
)

// The suite runs once per shelf. An invariant that holds only in memory is not an
// invariant of this package: the durable shelf is what an instance serves from, and
// it is the one where a filter, an index or a forgotten column can quietly turn a
// held record into no result. So every test below and in ledger_test.go is asked of
// both, and each one starts from an empty ledger.
var run struct {
	name  string
	fresh func(t *testing.T) *Ledger
}

func TestMain(m *testing.M) {
	for _, each := range []struct {
		name  string
		fresh func(t *testing.T) *Ledger
	}{
		{name: "memory", fresh: func(*testing.T) *Ledger { return New() }},
		{name: "base", fresh: func(t *testing.T) *Ledger { return NewBase(app(t)) }},
	} {
		run = each
		if code := m.Run(); code != 0 {
			os.Exit(code)
		}
	}
	os.Exit(0)
}

// fresh is an empty ledger on the shelf this run is exercising. It names the shelf
// in the test log so that a failure says which one failed.
func fresh(t *testing.T) *Ledger {
	t.Helper()
	t.Logf("shelf: %s", run.name)
	return run.fresh(t)
}

// app is a Base app with the retention collections created.
func app(t *testing.T) core.App {
	t.Helper()
	built, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("test app: %v", err)
	}
	t.Cleanup(built.Cleanup)
	if err := Ensure(built); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return built
}

// poison puts an entry in the party index that names a record which is not there —
// the state a disposal run leaves behind if it destroys a record and not the index
// entries that found it. It reaches around the ledger on purpose: the ledger has no
// operation that produces this state, and the point is what happens when something
// else has.
func poison(t *testing.T, l *Ledger, party, id string) {
	t.Helper()
	switch s := l.shelf.(type) {
	case *memory:
		s.held.party[key(org, party)] = []string{id}
	case durable:
		entry, err := parties.New(s.app, org)
		if err != nil {
			t.Fatalf("party index: %v", err)
		}
		entry.Set(fieldParty, party)
		entry.Set(fieldRecord, id)
		if err := s.app.Save(entry); err != nil {
			t.Fatalf("party index: %v", err)
		}
	default:
		t.Fatalf("no way to corrupt a %T", s)
	}
}

// boot opens a Base app on a directory, creating the databases if they are not
// there. Two of them over one directory, in sequence, is a restart.
func boot(t *testing.T, dir string) *core.BaseApp {
	t.Helper()
	opened := core.NewBaseApp(core.BaseAppConfig{DataDir: dir, EncryptionEnv: "hz_test_env"})
	if err := opened.Bootstrap(); err != nil {
		t.Fatalf("bootstrap %s: %v", dir, err)
	}
	if err := opened.RunAllMigrations(); err != nil {
		t.Fatalf("migrate %s: %v", dir, err)
	}
	return opened
}

// TestRecordsSurviveARestart is the whole point of a durable shelf. The retention
// period is five years (AMLR Art. 77(3)); a ledger that starts empty every time the
// process starts has kept nothing, and the breach is of the record-keeping
// obligation itself rather than of anything a customer did.
func TestRecordsSurviveARestart(t *testing.T) {
	if run.name != "base" {
		t.Skip("a memory shelf cannot survive a restart, which is why it is not what an instance serves from")
	}

	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	first := boot(t, dir)
	if err := Ensure(first); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	before := NewBase(first)

	rel, err := open(before, org, "payments", []string{"name:ivan petrov"}, ago(9))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	tx, err := transact(before, org, rel, "tx-1", []string{"name:ivan petrov"}, ago(8), body("kept"))
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}
	if _, err := before.Close(org, rel, ago(4)); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := first.ResetBootstrapState(); err != nil {
		t.Fatalf("shutting down: %v", err)
	}

	second := boot(t, dir)
	t.Cleanup(func() { _ = second.ResetBootstrapState() })
	// Ensure runs on every start, and the second run must not disturb what the
	// first one created.
	if err := Ensure(second); err != nil {
		t.Fatalf("Ensure after restart: %v", err)
	}
	after := NewBase(second)

	held, err := after.Len()
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if held != 2 {
		t.Fatalf("ledger holds %d records after a restart, want 2", held)
	}

	kept, err := after.Get(PurposeInvestigation, org, tx)
	if err != nil {
		t.Fatalf("the transaction did not survive the restart: %v", err)
	}
	if string(kept.Body) != string(body("kept")) {
		t.Errorf("body = %q", kept.Body)
	}
	// The cascaded clock survived too, not just the record.
	if want := ago(4).AddDate(Period, 0, 0); !kept.Expiry().Equal(want) {
		t.Errorf("expiry = %s, want %s", kept.Expiry(), want)
	}

	// And the index the Art. 78 answer comes from.
	answer, err := after.Lookback(PurposeDisclosure, org, "name:ivan petrov", time.Now().UTC())
	if err != nil {
		t.Fatalf("Lookback: %v", err)
	}
	if !answer.Maintained || answer.Current {
		t.Errorf("answer after restart = %+v, want maintained and not current", answer)
	}
	if answer.Examined != 2 {
		t.Errorf("examined = %d, want 2", answer.Examined)
	}
}

// TestEveryFieldSurvivesTheRoundTrip: a column the writer forgets to set does not
// fail, it reads back empty — and an empty field in a retained record is a
// transaction that can no longer be reconstructed. So a record with every field
// populated is written, read back and compared whole.
func TestEveryFieldSurvivesTheRoundTrip(t *testing.T) {
	l := fresh(t)

	rel, err := open(l, org, "payments", []string{"name:ivan petrov"}, ago(9))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := l.Close(org, rel, ago(4)); err != nil {
		t.Fatalf("Close: %v", err)
	}

	decided := ago(3).Add(90 * time.Minute)
	full := Record{
		Org:          org,
		Class:        ClassTransaction,
		Trigger:      TriggerRelationshipEnd,
		Relationship: rel,
		Ref:          "tx-full",
		Nature:       "payments",
		Reason:       "kept for the round trip",
		Parties:      []string{"subject:p1", "name:ivan petrov"},
		Occurred:     ago(3),
		Body:         []byte{0x00, 0x01, 0xff, 0xfe, '{', '}'},
		Assessment: &Assessment{
			Alerts:     []string{"alert-1", "alert-2"},
			Case:       "case-1",
			Considered: []string{"the counterparty", "the pattern"},
			Result:     NotReported,
			Rationale:  "explained by the customer's business",
			By:         "analyst-1",
			At:         decided,
		},
	}
	id, err := l.Retain(full)
	if err != nil {
		t.Fatalf("Retain: %v", err)
	}
	if err := l.Extend(org, id, 30*24*time.Hour, "an authority has asked", "officer-1"); err != nil {
		t.Fatalf("Extend: %v", err)
	}

	got := mustGet(t, l, id)
	want := full
	want.ID = id
	want.Start = ago(4).UTC()
	want.Occurred = full.Occurred.UTC()
	want.Written = got.Written
	want.Extended = got.Extended
	want.identity = got.identity

	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip lost or changed a field:\n got %+v\nwant %+v", got, want)
	}
	if got.Extended == nil || got.Extended.Reason != "an authority has asked" || got.Extended.Who != "officer-1" {
		t.Errorf("extension = %+v", got.Extended)
	}
	if got.Written.IsZero() {
		t.Error("the write time was not kept")
	}
	if got.identity == "" {
		t.Error("the identity was not kept, so a retry would write a second record")
	}
}

// TestRetryDoesNotRetainTwice: a client that resends because it never saw the first
// response must not put a second record of one transaction in the ledger. The
// second call returns the id the first one wrote.
func TestRetryDoesNotRetainTwice(t *testing.T) {
	l := fresh(t)

	first, err := transact(l, org, "", "tx-1", []string{"subject:p1"}, ago(1), body("tx"))
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}
	again, err := transact(l, org, "", "tx-1", []string{"subject:p1"}, ago(1), body("tx"))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if again != first {
		t.Errorf("retry returned %s, want the record it already wrote %s", again, first)
	}

	held, err := l.Len()
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if held != 1 {
		t.Fatalf("ledger holds %d records after one transaction and a retry", held)
	}

	// The party index must not have gained a second entry either, or the same
	// relationship would be counted twice in an Art. 78 answer.
	answer, err := l.Lookback(PurposeDisclosure, org, "subject:p1", time.Now().UTC())
	if err != nil {
		t.Fatalf("Lookback: %v", err)
	}
	if answer.Examined != 1 {
		t.Errorf("party index holds %d entries for one record", answer.Examined)
	}

	// A refusal is recognised the same way: one refused transaction, one record.
	refused, err := refuse(l, org, "tx-2", []string{"subject:p2"}, ago(1), "identification impossible")
	if err != nil {
		t.Fatalf("refusal: %v", err)
	}
	repeated, err := refuse(l, org, "tx-2", []string{"subject:p2"}, ago(1), "identification impossible")
	if err != nil {
		t.Fatalf("refusal retry: %v", err)
	}
	if repeated != refused {
		t.Errorf("refusal retry returned %s, want %s", repeated, refused)
	}
}

// TestAssessmentRecursPerCase: an assessment is not unique per case. The same case
// is assessed again whenever new information arrives and each decision is retained
// (AMLR Art. 77(1)(b)), so two decisions are two records — while a retry of one
// decision is still one.
func TestAssessmentRecursPerCase(t *testing.T) {
	l := fresh(t)

	monday := Assessment{
		Considered: []string{"the first alert"},
		Result:     NotReported,
		Rationale:  "consistent with the stated business",
		By:         "analyst-1",
		At:         ago(2),
	}
	tuesday := monday
	tuesday.Considered = []string{"the first alert", "a second alert"}
	tuesday.Result = Reported
	tuesday.Rationale = "the pattern continued"
	tuesday.At = ago(2).Add(24 * time.Hour)

	one, err := assess(l, org, "", "case-1", []string{"subject:p1"}, monday)
	if err != nil {
		t.Fatalf("first assessment: %v", err)
	}
	two, err := assess(l, org, "", "case-1", []string{"subject:p1"}, tuesday)
	if err != nil {
		t.Fatalf("second assessment: %v", err)
	}
	if one == two {
		t.Fatal("the second decision on the case replaced the first")
	}

	repeated, err := assess(l, org, "", "case-1", []string{"subject:p1"}, tuesday)
	if err != nil {
		t.Fatalf("retry of the second assessment: %v", err)
	}
	if repeated != two {
		t.Errorf("retry wrote %s, want the decision already retained %s", repeated, two)
	}

	held, err := l.Len()
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if held != 2 {
		t.Fatalf("ledger holds %d assessments, want the two decisions", held)
	}
}

// TestTwoFactsCannotShareOneIdentity: a second record under the same reference that
// says something different is not a retry. The ledger is append-only, so it will
// neither rewrite what it holds nor quietly discard what the caller sent — it
// refuses and lets the caller find out.
func TestTwoFactsCannotShareOneIdentity(t *testing.T) {
	l := fresh(t)

	if _, err := transact(l, org, "", "tx-1", []string{"subject:p1"}, ago(1), body("as submitted")); err != nil {
		t.Fatalf("transaction: %v", err)
	}
	if _, err := transact(l, org, "", "tx-1", []string{"subject:p1"}, ago(1), body("something else")); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}

	held, err := l.Len()
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if held != 1 {
		t.Errorf("ledger holds %d records, want the first one only", held)
	}
	kept := mustGet(t, l, mustOnly(t, l))
	if string(kept.Body) != string(body("as submitted")) {
		t.Errorf("the retained record was rewritten: %q", kept.Body)
	}
}

// mustOnly is the id of the ledger's single record.
func mustOnly(t *testing.T, l *Ledger) string {
	t.Helper()
	var ids []string
	if err := l.Each(PurposeMonitoring, org, "", func(r Record) error {
		ids = append(ids, r.ID)
		return nil
	}); err != nil {
		t.Fatalf("Each: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("ledger holds %d records, want 1", len(ids))
	}
	return ids[0]
}

// TestOneIdentityOneRecord: whatever else happens — two retries in flight together,
// two processes writing to one ledger — a second record cannot take an identity that
// is already taken. The write is attempted directly on the shelf here, because the
// ledger's own read-then-write is what this is the backstop for.
func TestOneIdentityOneRecord(t *testing.T) {
	l := fresh(t)

	id, err := transact(l, org, "", "tx-1", []string{"subject:p1"}, ago(1), body("tx"))
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}

	twin := mustGet(t, l, id)
	twin.ID = uuid.NewString()
	if err := l.shelf.insert(twin); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second record took the identity %q: err = %v, want ErrConflict", twin.identity, err)
	}
	if held := mustLen(t, l); held != 1 {
		t.Errorf("ledger holds %d records under one identity", held)
	}
}

// TestConcurrentRetriesRetainOnce: retries in flight together must each come back
// with the one record. It cannot force the interleaving it is aiming at, so it is
// evidence about the ordinary case and not about the race; the guarantee under a
// race is the identity constraint, which TestOneIdentityOneRecord asks for directly.
func TestConcurrentRetriesRetainOnce(t *testing.T) {
	l := fresh(t)

	const tries = 4
	ids := make([]string, tries)
	errs := make([]error, tries)
	var wg sync.WaitGroup
	for i := range tries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids[i], errs[i] = transact(l, org, "", "tx-1", []string{"subject:p1"}, ago(1), body("tx"))
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
		if ids[i] != ids[0] {
			t.Errorf("retry %d retained %s, retry 0 retained %s", i, ids[i], ids[0])
		}
	}
	held, err := l.Len()
	if err != nil {
		t.Fatalf("Len: %v", err)
	}
	if held != 1 {
		t.Errorf("%d concurrent retries retained %d records", tries, held)
	}
}

// TestWalkCrossesPages: a walk reads a page at a time, and a ledger holds more than
// one page. A cursor that does not advance either repeats a page forever or, worse,
// stops early — and a walk that stops early is a file produced for an authority with
// records missing from it and nothing to say so.
func TestWalkCrossesPages(t *testing.T) {
	l := fresh(t)

	const held = batch + batch/2
	first := ago(2)
	for i := range held {
		ref := fmt.Sprintf("tx-%04d", i)
		if _, err := transact(l, org, "", ref, []string{"subject:p1"}, first.Add(time.Duration(i)*time.Minute), body(ref)); err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
	}

	seen := make(map[string]bool, held)
	previous := time.Time{}
	order := 0
	if err := l.Each(PurposeMonitoring, org, ClassTransaction, func(r Record) error {
		if seen[r.ID] {
			return fmt.Errorf("record %s visited twice", r.ID)
		}
		seen[r.ID] = true
		if r.Occurred.Before(previous) {
			return fmt.Errorf("record %d occurred %s, after the one before it at %s", order, r.Occurred, previous)
		}
		previous = r.Occurred
		order++
		return nil
	}); err != nil {
		t.Fatalf("Each: %v", err)
	}
	if len(seen) != held {
		t.Errorf("a walk of %d records visited %d", held, len(seen))
	}
}

// TestDisposalCrossesBatches: destruction is done in bounded pieces, and a ledger
// with more expired records than one piece must still end up with none.
func TestDisposalCrossesBatches(t *testing.T) {
	l := fresh(t)

	const doomed = batch + 5
	for i := range doomed {
		ref := fmt.Sprintf("tx-%04d", i)
		if _, err := refuse(l, org, ref, []string{"subject:" + ref}, ago(6), "refused"); err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
	}
	live, err := refuse(l, org, "tx-recent", []string{"subject:recent"}, ago(1), "refused")
	if err != nil {
		t.Fatalf("recent refusal: %v", err)
	}

	d, err := l.Dispose(time.Now().UTC())
	if err != nil {
		t.Fatalf("Dispose: %v", err)
	}
	if len(d.Disposed) != doomed {
		t.Errorf("disposed %d records, want %d", len(d.Disposed), doomed)
	}
	if _, err := l.Get(PurposeInvestigation, org, live); err != nil {
		t.Errorf("a record within its period was destroyed: %v", err)
	}
	if held, err := l.Len(); err != nil || held != 1 {
		t.Errorf("ledger holds %d records after disposal (%v), want the one within its period", held, err)
	}
}

// TestEnsureIsIdempotent: it runs on every start, so the second run must find
// everything already there and change nothing.
func TestEnsureIsIdempotent(t *testing.T) {
	if run.name != "base" {
		t.Skip("Ensure is the durable shelf's")
	}
	built := app(t)
	before, err := built.FindCollectionByNameOrId(records.Name)
	if err != nil {
		t.Fatalf("collection: %v", err)
	}
	indexes := len(before.Indexes)
	fields := len(before.Fields)

	if err := Ensure(built); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	after, err := built.FindCollectionByNameOrId(records.Name)
	if err != nil {
		t.Fatalf("collection: %v", err)
	}
	if len(after.Indexes) != indexes || len(after.Fields) != fields {
		t.Errorf("a second Ensure changed the collection: %d/%d fields, %d/%d indexes",
			len(after.Fields), fields, len(after.Indexes), indexes)
	}
}
