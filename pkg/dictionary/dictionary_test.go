package dictionary

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/dbx"

	"github.com/luxfi/aml/internal/instance"
	"github.com/luxfi/aml/pkg/types"
)

const (
	acme  = "hanzo/acme"
	rival = "hanzo/rival"
	// other is the SAME org name under a different brand.
	other = "zoo/acme"
)

var noon = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func shelf(t *testing.T) *Shelf {
	t.Helper()
	app := instance.New(t)
	t.Cleanup(app.Cleanup)
	if err := Ensure(app); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	s := NewBase(app)
	s.Now = func() time.Time { return noon }
	return s
}

func payment(user string, usd float64, raw string) types.Transaction {
	tx := types.Transaction{
		ID: fmt.Sprintf("tx-%s-%v", user, usd), UserID: user, AccountID: "acct-" + user,
		Currency: "USD", Notional: usd, USD: usd, Timestamp: noon, Direction: "in",
	}
	if raw != "" {
		tx.Raw = json.RawMessage(raw)
	}
	return tx
}

func field(t *testing.T, cat *Catalog, name string) Field {
	t.Helper()
	for _, f := range cat.Fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("the catalog has no field %q", name)
	return Field{}
}

// TestDeclaredFieldsComeFromTheStruct. A hand-kept list is a second declaration
// of the payload, and the drift is invisible: the catalog simply never mentions
// the new field, which reads as a field nobody sends.
func TestDeclaredFieldsComeFromTheStruct(t *testing.T) {
	names := Fields()
	for _, want := range []string{"id", "user_id", "account_id", "notional", "currency", "timestamp", "usd", "ip_address"} {
		if !slices.Contains(names, want) {
			t.Errorf("the catalog does not know about %q, which the engine reads", want)
		}
	}
	if slices.Contains(names, "OrgID") {
		t.Error("the catalog is built from json tags, not Go field names")
	}
}

// TestBlindFieldIsStated. A coverage claim resting on a field nobody fills is the
// failure this catalog exists to surface, so the field appears with Blind set
// rather than being absent.
func TestBlindFieldIsStated(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if err := s.Observe(acme, payment("u1", float64(100*(i+1)), "")); err != nil {
			t.Fatal(err)
		}
	}
	cat, err := s.Catalog(ctx, acme, &CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	if cat.Payloads != 4 {
		t.Fatalf("payloads = %d, want 4", cat.Payloads)
	}

	ip := field(t, cat, "ip_address")
	if !ip.Blind || ip.Seen != 0 || ip.Fill != 0 {
		t.Fatalf("no payload carried an address, so the field is blind: %+v", ip)
	}
	user := field(t, cat, "user_id")
	if user.Blind || user.Seen != 4 || user.Fill != 1 {
		t.Fatalf("every payload carried a user: %+v", user)
	}
	if user.Distinct != 1 {
		t.Fatalf("one user in four payloads is one distinct value, got %d", user.Distinct)
	}
}

// TestNumericStatistics are present for numbers and absent for everything else. A
// mean of 0.0 over an identifier reads as a fact.
func TestNumericStatistics(t *testing.T) {
	s := shelf(t)
	for _, v := range []float64{100, 200, 300} {
		if err := s.Observe(acme, payment("u1", v, "")); err != nil {
			t.Fatal(err)
		}
	}
	cat, err := s.Catalog(context.Background(), acme, &CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	usd := field(t, cat, "usd")
	if usd.Mean == nil || usd.Min == nil || usd.Max == nil || usd.Deviation == nil {
		t.Fatalf("a number carries its statistics: %+v", usd)
	}
	if *usd.Mean != 200 || *usd.Min != 100 || *usd.Max != 300 {
		t.Fatalf("mean=%v min=%v max=%v, want 200/100/300", *usd.Mean, *usd.Min, *usd.Max)
	}
	if user := field(t, cat, "user_id"); user.Mean != nil {
		t.Fatalf("text has no mean: %+v", user)
	}
}

// TestCustomFieldsAreTheTenantsOwnVocabulary.
func TestCustomFieldsAreTheTenantsOwnVocabulary(t *testing.T) {
	s := shelf(t)
	if err := s.Observe(acme, payment("u1", 100, `{"channel":"pos","terminal":42,"reversed":false}`)); err != nil {
		t.Fatal(err)
	}
	cat, err := s.Catalog(context.Background(), acme, &CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	channel := field(t, cat, Prefix+"channel")
	if channel.Origin != Custom || channel.Shape != Text || channel.Seen != 1 {
		t.Fatalf("a payload key is catalogued as the tenant's own: %+v", channel)
	}
	// A numeric payload key is catalogued as a number, is counted, and is COUNTED
	// DISTINCT — and carries no moment. See TestACustomNumberIsNeverStored.
	terminal := field(t, cat, Prefix+"terminal")
	if terminal.Shape != Number || terminal.Seen != 1 || terminal.Distinct != 1 {
		t.Fatalf("a numeric payload key is catalogued and counted: %+v", terminal)
	}
	if terminal.Min != nil || terminal.Max != nil || terminal.Mean != nil || terminal.Deviation != nil {
		t.Fatalf("a custom number's moments are the value itself: %+v", terminal)
	}
	reversed := field(t, cat, Prefix+"reversed")
	if reversed.Shape != Bool || reversed.Seen != 1 {
		t.Fatalf("false is a value and not an absence: %+v", reversed)
	}
	// The declared `raw` field is an object and is not confused with its keys.
	if raw := field(t, cat, "raw"); raw.Origin != Declared {
		t.Fatalf("the raw payload itself stays a declared field: %+v", raw)
	}
}

// TestFeaturesTravelWholeAndUnmeasured. Their statistics are the model's to
// report; inventing them here would be a second account of the same thing.
func TestFeaturesTravelWholeAndUnmeasured(t *testing.T) {
	s := shelf(t)
	cat, err := s.Catalog(context.Background(), acme, &CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Features) != 9 {
		t.Fatalf("the model's inventory is nine dimensions, got %d", len(cat.Features))
	}
	for _, f := range cat.Features {
		if f.Typology == "" || f.Citation == "" || f.Unit == "" {
			t.Fatalf("a feature travels with what makes it checkable: %+v", f)
		}
	}
}

// TestNoValueIsEverStored. The distinct count is a bitmap, and a bitmap is not a
// pseudonym: a statistics table does not get a second copy of an institution's
// identifiers under a weaker regime than the retained record plane's.
func TestNoValueIsEverStored(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	secret := "customer-of-interest@example.com"
	tx := payment("u1", 100, `{"contact":"`+secret+`"}`)
	if err := s.Observe(acme, tx); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	cat, err := s.Catalog(ctx, acme, &CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := json.Marshal(cat)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rendered), secret) {
		t.Fatal("the catalog handed back a payload value")
	}
	if strings.Contains(string(rendered), "u1") || strings.Contains(string(rendered), "acct-u1") {
		t.Fatal("the catalog handed back an identifier")
	}
	// And the row itself holds no value either.
	if strings.Contains(dump(t, s, acme), secret) {
		t.Fatal("a payload value reached the store")
	}
}

// dump renders every stored field row, which is what the no-value invariant is
// checked against.
func dump(t *testing.T, s *Shelf, org string) string {
	t.Helper()
	rows, err := fieldKind.Find(s.app, org, "", "", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, row := range rows {
		raw, err := json.Marshal(row.PublicExport())
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
	}
	return b.String()
}

// TestDistinctSaturatesRatherThanDrifting. A cardinality that silently stops
// rising reads as a field that stopped varying.
func TestDistinctSaturatesRatherThanDrifting(t *testing.T) {
	var s sketch
	for i := 0; i < 500; i++ {
		s.add(acme, "user_id", fmt.Sprintf("u%d", i))
	}
	n, saturated := s.estimate()
	if saturated {
		t.Fatalf("500 values must not saturate a %d-bit bitmap", buckets)
	}
	if n < 450 || n > 550 {
		t.Fatalf("estimate %d is not within 10%% of 500", n)
	}

	var full sketch
	for i := 0; i < 100_000; i++ {
		full.add(acme, "user_id", fmt.Sprintf("u%d", i))
	}
	if _, saturated := full.estimate(); !saturated {
		t.Fatal("a bitmap this full is no longer counting, and must say so")
	}
}

// TestSketchIsMergeableAndPerTenant.
//
// Mergeable is what makes a write-behind census correct across a restart rather
// than approximately correct. Per tenant is what stops two tenants' bitmaps being
// compared to learn whether they share a customer — an inference computed from a
// diagnostics table.
func TestSketchIsMergeableAndPerTenant(t *testing.T) {
	var a, b, both sketch
	for i := 0; i < 100; i++ {
		v := fmt.Sprintf("u%d", i)
		a.add(acme, "user_id", v)
		both.add(acme, "user_id", v)
	}
	for i := 50; i < 150; i++ {
		v := fmt.Sprintf("u%d", i)
		b.add(acme, "user_id", v)
		both.add(acme, "user_id", v)
	}
	a.merge(b)
	if a != both {
		t.Fatal("merging two halves must equal observing both, or a restart double-counts the overlap")
	}

	var mine, theirs sketch
	for i := 0; i < 200; i++ {
		v := fmt.Sprintf("u%d", i)
		mine.add(acme, "user_id", v)
		theirs.add(other, "user_id", v)
	}
	if mine == theirs {
		t.Fatal("two tenants observing the same values produced the same bitmap: they are comparable")
	}

	// And the encoding round-trips, which is what the durable row depends on.
	if decode(mine.encode()) != mine {
		t.Fatal("a bitmap must survive the row it is written to")
	}
	if decode("not base64") != (sketch{}) {
		t.Fatal("an unreadable bitmap restarts the count rather than corrupting it")
	}
}

// TestPendingIsPublished. A restart loses exactly the un-flushed observations, and
// the answer says how many that is rather than reading as a quieter institution.
func TestPendingIsPublished(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := s.Observe(acme, payment("u1", float64(i+1), "")); err != nil {
			t.Fatal(err)
		}
	}
	cat, err := s.Catalog(ctx, acme, &CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	if cat.Pending != 3 || cat.Payloads != 3 {
		t.Fatalf("un-flushed observations are counted and published: pending=%d payloads=%d", cat.Pending, cat.Payloads)
	}
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	cat, err = s.Catalog(ctx, acme, &CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	if cat.Pending != 0 || cat.Payloads != 3 {
		t.Fatalf("after a flush nothing is pending and nothing is lost: pending=%d payloads=%d", cat.Pending, cat.Payloads)
	}
}

// TestTenantIsolation, including the same org name under a second brand.
func TestTenantIsolation(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	if err := s.Observe(acme, payment("u1", 100, `{"channel":"pos"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Observe(other, payment("z9", 900, `{"lane":"web"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	mine, err := s.Catalog(ctx, acme, &CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	if mine.Payloads != 1 {
		t.Fatalf("payloads = %d, want 1 — another tenant's payloads were counted", mine.Payloads)
	}
	for _, f := range mine.Fields {
		if f.Name == Prefix+"lane" {
			t.Fatal("another tenant's payload key is in this catalog")
		}
	}

	theirs, err := s.Catalog(ctx, other, &CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	if theirs.Payloads != 1 {
		t.Fatalf("the second brand's catalog counted %d payloads, want 1", theirs.Payloads)
	}

	empty, err := s.Catalog(ctx, rival, &CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Payloads != 0 {
		t.Fatalf("a tenant that sent nothing has a catalog of nothing, got %d payloads", empty.Payloads)
	}
	// Its declared fields are all blind, which is the honest reading.
	for _, f := range empty.Fields {
		if f.Origin == Declared && !f.Blind {
			t.Fatalf("a tenant that sent nothing has no filled field: %+v", f)
		}
	}
}

// TestBareOrgIsRefused.
func TestBareOrgIsRefused(t *testing.T) {
	s := shelf(t)
	for _, bare := range []string{"acme", "", "unknown/acme"} {
		if err := s.Observe(bare, payment("u1", 100, "")); err == nil {
			t.Errorf("Observe accepted %q as a tenant", bare)
		}
		if _, err := s.Catalog(context.Background(), bare, &CatalogIn{}); err == nil {
			t.Errorf("Catalog accepted %q as a tenant", bare)
		}
	}
}

// TestNothingDeletes.
func TestNothingDeletes(t *testing.T) {
	for _, name := range []string{"dictionary.go", "shelf.go", "sketch.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), ".Delete(") {
			t.Errorf("%s calls Delete: disposal is pkg/retention's decision and nobody else's", name)
		}
	}
}

// TestRestart: what a restarted pod reads, and that the counts continue rather
// than starting again.
func TestRestart(t *testing.T) {
	first := instance.New(t)
	if err := Ensure(first); err != nil {
		t.Fatal(err)
	}
	before := NewBase(first)
	before.Now = func() time.Time { return noon }
	for i := 0; i < 10; i++ {
		if err := before.Observe(acme, payment(fmt.Sprintf("u%d", i), float64(100*(i+1)), `{"channel":"pos"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := before.Observe(other, payment("z9", 900, "")); err != nil {
		t.Fatal(err)
	}
	if err := before.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	second := instance.Restart(t, first)
	if err := Ensure(second); err != nil {
		t.Fatal(err)
	}
	after := NewBase(second)
	after.Now = func() time.Time { return noon }

	cat, err := after.Catalog(context.Background(), acme, &CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	if cat.Payloads != 10 {
		t.Fatalf("the census did not survive the restart: %d payloads, want 10", cat.Payloads)
	}
	user := field(t, cat, "user_id")
	if user.Seen != 10 || user.Distinct < 9 || user.Distinct > 11 {
		t.Fatalf("the distinct count did not survive: %+v", user)
	}
	if field(t, cat, Prefix+"channel").Seen != 10 {
		t.Fatal("the tenant's own payload key did not survive the restart")
	}

	// The counts CONTINUE. A census that restarted at zero would read as an
	// institution that just started sending.
	for i := 10; i < 15; i++ {
		if err := after.Observe(acme, payment(fmt.Sprintf("u%d", i), 100, `{"channel":"pos"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := after.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	cat, err = after.Catalog(context.Background(), acme, &CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	if cat.Payloads != 15 {
		t.Fatalf("the census restarted instead of continuing: %d payloads, want 15", cat.Payloads)
	}
	user = field(t, cat, "user_id")
	if user.Distinct < 13 || user.Distinct > 17 {
		t.Fatalf("the distinct count restarted instead of merging: %+v", user)
	}

	// Tenancy still holds.
	theirs, err := after.Catalog(context.Background(), other, &CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	if theirs.Payloads != 1 {
		t.Fatalf("the tenant boundary did not survive the restart: %d", theirs.Payloads)
	}
}

// TestACustomNumberIsNeverStored.
//
// "No payload value is ever stored" is the catalog's invariant, and the numeric
// moments are where it is easiest to lose: a minimum IS a value, exactly, at any
// volume, and at a count of one so are the sum and the mean derived from it. A
// custom key holds whatever the institution put under its own name — a national
// identifier, a date of birth written as a number, a card range — and nothing
// here knows which.
//
// So this reads the DURABLE ROW rather than the answer: the value must not be in
// any column of it, under any encoding a number takes.
func TestACustomNumberIsNeverStored(t *testing.T) {
	s := shelf(t)
	ctx := context.Background()
	// A number that is unmistakably an identifier rather than an amount.
	if err := s.Observe(acme, payment("u1", 100, `{"born":19750321}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	rows, err := fieldKind.Find(s.app, acme, fieldName+" = {:name}", "", 1,
		dbx.Params{"name": Prefix + "born"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("the field was not catalogued at all: %d rows", len(rows))
	}
	row := rows[0]
	for _, column := range []string{fieldMin, fieldMax, fieldSum, fieldSquare, fieldCount} {
		if v := row.GetFloat(column); v != 0 {
			t.Errorf("%s = %v: a custom payload value is in the catalog", column, v)
		}
	}
	serialised := dump(t, s, acme)
	for _, form := range []string{"19750321", "1.9750321e+07"} {
		if strings.Contains(serialised, form) {
			t.Fatalf("the value %s survives in the stored rows: %s", form, serialised)
		}
	}

	// What the catalog DOES keep is the shape, the fill and the variation — the
	// whole of what it is for — and the answer says the moments are absent rather
	// than zero.
	cat, err := s.Catalog(ctx, acme, &CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	born := field(t, cat, Prefix+"born")
	if born.Shape != Number || born.Seen != 1 || born.Distinct != 1 || born.Fill != 1 {
		t.Fatalf("a custom number is still measured: %+v", born)
	}
	if born.Min != nil || born.Max != nil || born.Mean != nil || born.Deviation != nil {
		t.Fatalf("a custom number carries a moment: %+v", born)
	}

	// The positive control: a DECLARED number is the transaction model's own, its
	// range is the statistic a reviewer asks for, and it keeps its moments. Without
	// this the test above would pass on a catalog that had stopped measuring.
	usd := field(t, cat, "usd")
	if usd.Min == nil || usd.Max == nil || usd.Mean == nil || *usd.Mean != 100 {
		t.Fatalf("a declared number lost its statistics: %+v", usd)
	}
}

// TestACustomNumberStillCountsDistinct. The sketch is what a custom number gets,
// and it has to keep working: a bitmap over hashed values, per tenant, which is
// the whole reason the moments can go.
func TestACustomNumberStillCountsDistinct(t *testing.T) {
	s := shelf(t)
	for _, v := range []string{`{"born":1}`, `{"born":2}`, `{"born":3}`, `{"born":3}`} {
		if err := s.Observe(acme, payment("u1", 100, v)); err != nil {
			t.Fatal(err)
		}
	}
	cat, err := s.Catalog(context.Background(), acme, &CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	born := field(t, cat, Prefix+"born")
	if born.Seen != 4 || born.Distinct != 3 {
		t.Fatalf("seen=%d distinct=%d, want 4 and 3", born.Seen, born.Distinct)
	}
}

// TestAVocabularyIsBoundedPerTenant.
//
// The accumulator is in memory, in the one process every institution's ingest
// runs in, and a tenant chooses its own payload keys. A per-payload bound
// (MaxKeys) does not bound a tenant that sends a DIFFERENT key on every
// transaction: that tenant's vocabulary grows for as long as it keeps sending,
// and what it exhausts is shared.
//
// So the vocabulary is bounded per tenant, and reaching it may only degrade the
// tenant that reached it: no error, no refused payload, the fields it already has
// keep measuring, and the readings it turned away are published.
func TestAVocabularyIsBoundedPerTenant(t *testing.T) {
	s := shelf(t)
	s.vocab = 4
	ctx := context.Background()

	// Twenty distinct keys, one per payload, against a vocabulary of four.
	for i := range 20 {
		if err := s.Observe(acme, payment("u1", 100, fmt.Sprintf(`{"k%d":"v"}`, i))); err != nil {
			t.Fatalf("a payload past the bound was refused: %v", err)
		}
	}

	// BEFORE the flush, because the accumulator is the half that matters: it is
	// in memory, in the process every institution's ingest runs in, and a bound
	// applied only when the rows are written would let it grow between flushes.
	held, _ := s.pending.Get(acme)
	if held == nil || held.names != 4 {
		t.Fatalf("the accumulator holds %v custom names against a bound of 4", held)
	}
	if kept(t, s, ctx, acme) != 4 {
		t.Fatal("the un-flushed answer is over more names than the bound")
	}

	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	cat, err := s.Catalog(ctx, acme, &CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	if n := kept(t, s, ctx, acme); n != 4 {
		t.Fatalf("the catalog holds %d custom names against a bound of 4", n)
	}
	if cat.Crowded != 16 {
		t.Fatalf("crowded = %d, want the 16 readings there was no room for", cat.Crowded)
	}
	// Every payload was still counted, and the declared fields still measure.
	if cat.Payloads != 20 {
		t.Fatalf("payloads = %d: the bound refused a payload", cat.Payloads)
	}
	if usd := field(t, cat, "usd"); usd.Seen != 20 {
		t.Fatalf("a declared field stopped measuring at the bound: %+v", usd)
	}

	// The bound holds ACROSS flushes, which is the half a bound on the
	// accumulator alone would miss: the rows are what grow without one.
	for i := 20; i < 40; i++ {
		if err := s.Observe(acme, payment("u1", 100, fmt.Sprintf(`{"k%d":"v"}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := fieldKind.Find(s.app, acme, fieldOrigin+" = {:o}", "", 0, dbx.Params{"o": Custom})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("%d custom rows are stored after a second flush, want 4", len(rows))
	}
}

// TestOneTenantsVocabularyIsNeverAnothersBound. The isolation half: the bound is
// this institution's own row count against its own bound, so a tenant that has
// filled its vocabulary takes no room from anybody else's.
func TestOneTenantsVocabularyIsNeverAnothersBound(t *testing.T) {
	s := shelf(t)
	s.vocab = 2
	ctx := context.Background()

	for i := range 10 {
		if err := s.Observe(acme, payment("u1", 100, fmt.Sprintf(`{"k%d":"v"}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	// A second institution, and the SAME org name under a second brand.
	for _, stranger := range []string{rival, other} {
		if err := s.Observe(stranger, payment("u1", 100, `{"channel":"atm"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	for _, stranger := range []string{rival, other} {
		cat, err := s.Catalog(ctx, stranger, &CatalogIn{})
		if err != nil {
			t.Fatal(err)
		}
		if cat.Crowded != 0 {
			t.Fatalf("%s was crowded out by another tenant's vocabulary: %d", stranger, cat.Crowded)
		}
		if f := field(t, cat, Prefix+"channel"); f.Seen != 1 {
			t.Fatalf("%s lost its own field to another tenant's volume: %+v", stranger, f)
		}
	}
	// And the tenant that filled its own vocabulary is the one that is degraded.
	mine, err := s.Catalog(ctx, acme, &CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	if mine.Crowded == 0 {
		t.Fatal("the tenant that reached its bound reports nothing about it")
	}
}

// kept is how many custom names a tenant's catalog answer carries.
func kept(t *testing.T, s *Shelf, ctx context.Context, org string) int {
	t.Helper()
	cat, err := s.Catalog(ctx, org, &CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range cat.Fields {
		if f.Origin == Custom {
			n++
		}
	}
	return n
}
