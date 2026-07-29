package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/history"
	"github.com/luxfi/aml/pkg/reference"
	"github.com/luxfi/aml/pkg/types"
)

var ref = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

// screenList answers from a fixed set of names.
type screenList struct {
	sanctioned []string
	pep        []string
	err        error
}

func (s screenList) Hit(_ context.Context, name, class string) (Hit, error) {
	if s.err != nil {
		return Hit{}, s.err
	}
	want := s.sanctioned
	if class == ClassPEP {
		want = s.pep
	}
	for _, n := range want {
		if strings.EqualFold(n, name) {
			return Hit{Matched: true, Score: 1, Name: n, List: class}, nil
		}
	}
	return Hit{}, nil
}

func providers(store history.Store) Providers {
	return Providers{
		History: store,
		Screen:  screenList{sanctioned: []string{"Ivan Petrov"}, pep: []string{"Jane Minister"}},
		Reference: reference.Jurisdictions{
			AsOf:       ref.AddDate(0, -1, 0),
			Action:     []string{"KP"},
			Monitoring: []string{"SY"},
		},
		Rate: reference.Rates{AsOf: ref, USDPer: map[string]float64{"EUR": 1.08, "KWD": 3.25}},
		Now:  func() time.Time { return ref },
		Zone: time.UTC,
	}
}

func rule(id, dsl string) types.Rule {
	return types.Rule{
		ID: id, Name: id, DSL: dsl, Enabled: true, Weight: 0.3,
		Severity: types.SeverityHigh, Action: types.ActionReview,
	}
}

func tx(usd float64) types.Transaction {
	return types.Transaction{
		ID: "tx-1", OrgID: "org", UserID: "u1", Notional: usd,
		Currency: "USD", Timestamp: ref,
	}
}

// --- admission ---------------------------------------------------------------

func TestRuleNamingAbsentEvidenceIsRejected(t *testing.T) {
	// The central fix. A deployment with no screening provider must refuse a rule
	// that screens, rather than installing it and answering "no match" for ever.
	e := New(Providers{History: history.NewMemory(nil), Rate: reference.Rates{}})
	err := e.SetRules([]types.Rule{rule("sanctions", `Screened(Entity.Name, "sanctions")`)})
	if err == nil {
		t.Fatal("a rule that screens must be rejected when no screening provider is configured")
	}
	if !errors.Is(err, ErrNoProvider) {
		t.Fatalf("want a missing-provider error, got %v", err)
	}
	if !strings.Contains(err.Error(), "Screened") {
		t.Fatalf("the error must name the term that cannot be answered, got %v", err)
	}
}

func TestRejectedRuleSetInstallsNothing(t *testing.T) {
	store := history.NewMemory(func() time.Time { return ref })
	e := New(providers(store))
	good := rule("ok", `Count("user", "24h") > 0`)
	bad := rule("bad", `Nonsense("user") > 0`)

	if err := e.SetRules([]types.Rule{good, bad}); err == nil {
		t.Fatal("a set containing an uninstallable rule must be rejected")
	}
	if n := len(e.Rules()); n != 0 {
		t.Fatalf("nothing must be installed from a rejected set, got %d rules", n)
	}
	// The good rule alone installs.
	if err := e.SetRules([]types.Rule{good}); err != nil {
		t.Fatalf("the valid rule alone must install: %v", err)
	}
	if n := len(e.Rules()); n != 1 {
		t.Fatalf("want 1 rule installed, got %d", n)
	}
}

func TestRuleMustYieldBoolean(t *testing.T) {
	e := New(providers(history.NewMemory(func() time.Time { return ref })))
	// A rule that returns a number is an authoring error: read as truthy it fires
	// on any activity at all.
	if err := e.SetRules([]types.Rule{rule("numeric", `Sum("user", "24h")`)}); err == nil {
		t.Fatal("a rule that does not yield a boolean must be rejected")
	}
	if err := e.SetRules([]types.Rule{rule("boolean", `Sum("user", "24h") > 100`)}); err != nil {
		t.Fatalf("the comparison form must be accepted: %v", err)
	}
}

func TestEmptyAndUnparseableRulesRejected(t *testing.T) {
	e := New(providers(history.NewMemory(nil)))
	for _, dsl := range []string{"", "   ", "Count(", "&&"} {
		if err := e.SetRules([]types.Rule{rule("r", dsl)}); err == nil {
			t.Errorf("rule %q must be rejected", dsl)
		}
	}
}

func TestNegativeWeightRejected(t *testing.T) {
	e := New(providers(history.NewMemory(nil)))
	r := rule("negative", `Count("user", "24h") > 0`)
	r.Weight = -1
	if err := e.SetRules([]types.Rule{r}); err == nil {
		t.Fatal("a negative weight would subtract from the risk score and must be rejected")
	}
}

func TestVocabularyReportsOnlyAnswerableTerms(t *testing.T) {
	full := New(providers(history.NewMemory(nil))).Evaluator().Vocabulary()
	if len(full) == 0 {
		t.Fatal("a fully configured deployment must report a vocabulary")
	}
	bare := New(Providers{History: history.NewMemory(nil)}).Evaluator().Vocabulary()
	for _, term := range bare {
		if term == "Screened" || term == "Tier" || term == "USD" {
			t.Fatalf("term %q must not be offered when its provider is absent", term)
		}
	}
	if len(bare) >= len(full) {
		t.Fatalf("a deployment missing providers must report fewer terms: bare %d, full %d", len(bare), len(full))
	}
}

// --- detection ---------------------------------------------------------------

func deposit(user string, hoursAgo int, usd float64, dir string) history.Event {
	return history.Event{
		ID: "e", At: ref.Add(-time.Duration(hoursAgo) * time.Hour),
		USD: usd, Currency: "USD", Direction: dir, User: user,
	}
}

func TestStructuringFiresOnSplitDeposits(t *testing.T) {
	store := history.NewMemory(func() time.Time { return ref })
	for i := 1; i <= 5; i++ {
		store.Append("org", deposit("u1", i, 2500, history.In))
	}
	e := New(providers(store))
	r := rule("structuring", `Structured("user", "24h", 10000.0, 3)`)
	if err := e.SetRules([]types.Rule{r}); err != nil {
		t.Fatal(err)
	}
	alerts, _, action := e.Evaluate(context.Background(), tx(2500), types.Entity{ID: "u1"})
	if len(alerts) != 1 {
		t.Fatalf("five sub-threshold deposits totalling 12,500 must raise an alert, got %d", len(alerts))
	}
	if action != types.ActionReview {
		t.Fatalf("action = %q, want review", action)
	}
}

func TestStructuringDoesNotFireOnOrdinaryActivity(t *testing.T) {
	store := history.NewMemory(func() time.Time { return ref })
	store.Append("org", deposit("u1", 2, 200, history.In))
	store.Append("org", deposit("u1", 4, 150, history.In))
	e := New(providers(store))
	if err := e.SetRules([]types.Rule{rule("structuring", `Structured("user", "24h", 10000.0, 3)`)}); err != nil {
		t.Fatal(err)
	}
	alerts, _, action := e.Evaluate(context.Background(), tx(200), types.Entity{ID: "u1"})
	if len(alerts) != 0 {
		t.Fatalf("two small deposits must not raise an alert, got %d", len(alerts))
	}
	if action != types.ActionAllow {
		t.Fatalf("action = %q, want allow", action)
	}
}

func TestHistoryIsScopedToTheOrganisation(t *testing.T) {
	// The same customer identifier in two tenants must not aggregate together.
	store := history.NewMemory(func() time.Time { return ref })
	for i := 1; i <= 5; i++ {
		store.Append("other-org", deposit("u1", i, 2500, history.In))
	}
	e := New(providers(store))
	if err := e.SetRules([]types.Rule{rule("structuring", `Structured("user", "24h", 10000.0, 3)`)}); err != nil {
		t.Fatal(err)
	}
	alerts, _, _ := e.Evaluate(context.Background(), tx(2500), types.Entity{ID: "u1"})
	if len(alerts) != 0 {
		t.Fatalf("another tenant's transactions must not count towards this one, got %d alerts", len(alerts))
	}
}

func TestSanctionsRuleFires(t *testing.T) {
	// A literal listed name must produce an alert. In the previous engine this
	// rule could not fire under any circumstances.
	store := history.NewMemory(func() time.Time { return ref })
	e := New(providers(store))
	r := rule("sanctions", `Screened(Entity.Name, "sanctions")`)
	r.Action = types.ActionBlock
	if err := e.SetRules([]types.Rule{r}); err != nil {
		t.Fatal(err)
	}
	alerts, _, action := e.Evaluate(context.Background(), tx(100), types.Entity{ID: "u1", Name: "Ivan Petrov"})
	if len(alerts) != 1 {
		t.Fatalf("a listed name must raise an alert, got %d", len(alerts))
	}
	if action != types.ActionBlock {
		t.Fatalf("action = %q, want block", action)
	}
	// An unlisted name must not.
	alerts, _, action = e.Evaluate(context.Background(), tx(100), types.Entity{ID: "u1", Name: "Maria Gonzalez"})
	if len(alerts) != 0 || action != types.ActionAllow {
		t.Fatalf("an unlisted name must not raise an alert, got %d alerts and action %q", len(alerts), action)
	}
}

func TestJurisdictionTiersAreDistinguished(t *testing.T) {
	e := New(providers(history.NewMemory(nil)))
	r := rule("countermeasures", `Tier(Entity.Jurisdiction) == "action"`)
	r.Action = types.ActionBlock
	if err := e.SetRules([]types.Rule{r}); err != nil {
		t.Fatal(err)
	}
	for code, wantAlerts := range map[string]int{"KP": 1, "SY": 0, "FR": 0} {
		alerts, _, _ := e.Evaluate(context.Background(), tx(100), types.Entity{ID: "u1", Jurisdiction: code})
		if len(alerts) != wantAlerts {
			t.Errorf("jurisdiction %s: got %d alerts, want %d", code, len(alerts), wantAlerts)
		}
	}
}

func TestCurrencyConvertedBeforeComparingToThreshold(t *testing.T) {
	e := New(providers(history.NewMemory(nil)))
	if err := e.SetRules([]types.Rule{rule("report", `USD() > 10000.0`)}); err != nil {
		t.Fatal(err)
	}
	// 10,000 Kuwaiti dinar is about 32,500 dollars and is over the threshold.
	// Passing the amount through as though it were dollars would place it exactly
	// on the wrong side.
	kwd := tx(10000)
	kwd.Currency = "KWD"
	alerts, _, _ := e.Evaluate(context.Background(), kwd, types.Entity{ID: "u1"})
	if len(alerts) != 1 {
		t.Fatalf("10,000 KWD is over a 10,000 dollar threshold and must alert, got %d", len(alerts))
	}
	// 10,000 dollars exactly is not over it.
	alerts, _, _ = e.Evaluate(context.Background(), tx(10000), types.Entity{ID: "u1"})
	if len(alerts) != 0 {
		t.Fatalf("exactly 10,000 dollars is not over the threshold, got %d alerts", len(alerts))
	}
}

func TestUnknownCurrencyGoesToReviewNotThrough(t *testing.T) {
	e := New(providers(history.NewMemory(nil)))
	if err := e.SetRules([]types.Rule{rule("report", `USD() > 10000.0`)}); err != nil {
		t.Fatal(err)
	}
	odd := tx(10_000_000)
	odd.Currency = "ZWL"
	alerts, _, action := e.Evaluate(context.Background(), odd, types.Entity{ID: "u1"})
	if len(alerts) != 1 {
		t.Fatalf("a transaction the engine cannot value must not pass silently, got %d alerts", len(alerts))
	}
	if alerts[0].EvalErr == "" {
		t.Fatal("the alert must record why the rule reached no verdict")
	}
	if action != types.ActionReview {
		t.Fatalf("action = %q, want review — an unassessable transaction needs a person", action)
	}
}

func TestStoreFailureGoesToReviewNotThrough(t *testing.T) {
	e := New(Providers{
		History: failing{},
		Rate:    reference.Rates{AsOf: ref},
		Now:     func() time.Time { return ref },
	})
	if err := e.SetRules([]types.Rule{rule("velocity", `Sum("user", "24h") > 1000.0`)}); err != nil {
		t.Fatal(err)
	}
	alerts, _, action := e.Evaluate(context.Background(), tx(500), types.Entity{ID: "u1"})
	if len(alerts) != 1 || action != types.ActionReview {
		t.Fatalf("a storage failure must route to review, got %d alerts and action %q", len(alerts), action)
	}
	if !strings.Contains(alerts[0].EvalErr, "unavailable") {
		t.Fatalf("the failure must be recorded, got %q", alerts[0].EvalErr)
	}
}

type failing struct{}

func (failing) Window(context.Context, history.Subject, time.Duration) ([]history.Event, error) {
	return nil, errors.New("store unavailable")
}

func TestAbsentSubjectIsAnErrorNotAnEmptyHistory(t *testing.T) {
	// A rule counting per device, on a transaction with no device fingerprint. An
	// empty window would report no velocity for exactly the transactions that
	// withheld the evidence.
	e := New(providers(history.NewMemory(func() time.Time { return ref })))
	if err := e.SetRules([]types.Rule{rule("device", `Count("device", "24h") > 5`)}); err != nil {
		t.Fatal(err)
	}
	alerts, _, action := e.Evaluate(context.Background(), tx(100), types.Entity{ID: "u1"})
	if len(alerts) != 1 || action != types.ActionReview {
		t.Fatalf("a rule needing an absent subject must route to review, got %d alerts action %q", len(alerts), action)
	}
}

func TestUnknownSubjectAndDimensionRejectedAtEvaluation(t *testing.T) {
	e := New(providers(history.NewMemory(func() time.Time { return ref })))
	// These compile — the argument is a string — so they are caught when evaluated,
	// and must produce a recorded failure rather than a zero count.
	for _, dsl := range []string{
		`Count("customer", "24h") > 0`,
		`Distinct("user", "postcode", "24h") > 0`,
		`Count("user", "7") > 0`,
		`Count("user", "24x") > 0`,
	} {
		e2 := New(providers(history.NewMemory(func() time.Time { return ref })))
		if err := e2.SetRules([]types.Rule{rule("r", dsl)}); err != nil {
			t.Fatalf("%s: %v", dsl, err)
		}
		alerts, _, action := e2.Evaluate(context.Background(), tx(100), types.Entity{ID: "u1"})
		if len(alerts) != 1 || alerts[0].EvalErr == "" || action != types.ActionReview {
			t.Errorf("%s: want a recorded failure routed to review, got %d alerts action %q", dsl, len(alerts), action)
		}
	}
	_ = e
}

func TestDayAggregatesTheBusinessDay(t *testing.T) {
	store := history.NewMemory(func() time.Time { return ref })
	// 6,000 earlier the same day, and 6,000 now: the day totals 12,000.
	store.Append("org", deposit("u1", 3, 6000, history.In))
	e := New(providers(store))
	if err := e.SetRules([]types.Rule{rule("report", `Day("user") > 10000.0`)}); err != nil {
		t.Fatal(err)
	}
	alerts, _, _ := e.Evaluate(context.Background(), tx(6000), types.Entity{ID: "u1"})
	if len(alerts) != 1 {
		t.Fatalf("two 6,000 transactions on one day exceed a 10,000 day threshold, got %d alerts", len(alerts))
	}

	// The same two amounts on different days do not.
	store2 := history.NewMemory(func() time.Time { return ref })
	store2.Append("org", deposit("u1", 14, 6000, history.In)) // previous calendar day
	e2 := New(providers(store2))
	if err := e2.SetRules([]types.Rule{rule("report", `Day("user") > 10000.0`)}); err != nil {
		t.Fatal(err)
	}
	alerts, _, _ = e2.Evaluate(context.Background(), tx(6000), types.Entity{ID: "u1"})
	if len(alerts) != 0 {
		t.Fatalf("amounts on separate calendar days must not aggregate, got %d alerts", len(alerts))
	}
}

func TestDisabledRuleDoesNotFire(t *testing.T) {
	store := history.NewMemory(func() time.Time { return ref })
	e := New(providers(store))
	r := rule("off", `USD() > 1.0`)
	r.Enabled = false
	if err := e.SetRules([]types.Rule{r}); err != nil {
		t.Fatal(err)
	}
	if alerts, _, _ := e.Evaluate(context.Background(), tx(100), types.Entity{ID: "u1"}); len(alerts) != 0 {
		t.Fatalf("a disabled rule must not fire, got %d alerts", len(alerts))
	}
}

func TestRuleScopeFilters(t *testing.T) {
	store := history.NewMemory(func() time.Time { return ref })
	e := New(providers(store))
	r := rule("crypto", `USD() > 1.0`)
	r.AssetClassFilter = []string{"crypto"}
	if err := e.SetRules([]types.Rule{r}); err != nil {
		t.Fatal(err)
	}
	equity := tx(100)
	equity.AssetClass = "equity"
	if alerts, _, _ := e.Evaluate(context.Background(), equity, types.Entity{ID: "u1"}); len(alerts) != 0 {
		t.Fatal("a rule scoped to crypto must not evaluate an equity transaction")
	}
	crypto := tx(100)
	crypto.AssetClass = "crypto"
	if alerts, _, _ := e.Evaluate(context.Background(), crypto, types.Entity{ID: "u1"}); len(alerts) != 1 {
		t.Fatal("a rule scoped to crypto must evaluate a crypto transaction")
	}
}

func TestAlertCarriesTypologyAndCitations(t *testing.T) {
	store := history.NewMemory(func() time.Time { return ref })
	e := New(providers(store))
	r := rule("cited", `USD() > 1.0`)
	r.Typology = "structuring"
	r.Citations = citationFixture()
	if err := e.SetRules([]types.Rule{r}); err != nil {
		t.Fatal(err)
	}
	alerts, _, _ := e.Evaluate(context.Background(), tx(100), types.Entity{ID: "u1"})
	if len(alerts) != 1 {
		t.Fatal("expected one alert")
	}
	if alerts[0].Typology != "structuring" {
		t.Errorf("alert must carry the typology, got %q", alerts[0].Typology)
	}
	if len(alerts[0].Citations) == 0 {
		t.Error("alert must carry the citations of the rule that raised it")
	}
}

func TestWindowIsQueriedOncePerSubjectAndLookback(t *testing.T) {
	// Several rules asking the same question of the same window must produce one
	// query, not one per rule.
	store := history.NewMemory(func() time.Time { return ref })
	store.Append("org", deposit("u1", 1, 100, history.In))
	counted := &counting{Store: store}

	e := New(Providers{
		History: counted,
		Rate:    reference.Rates{AsOf: ref},
		Now:     func() time.Time { return ref },
		Zone:    time.UTC,
	})
	if err := e.SetRules([]types.Rule{
		rule("a", `Count("user", "24h") > 0`),
		rule("b", `Sum("user", "24h") > 0.0`),
		rule("c", `Max("user", "24h") > 0.0`),
		rule("d", `Round("user", "24h", 100.0) > 0.5`),
	}); err != nil {
		t.Fatal(err)
	}
	e.Evaluate(context.Background(), tx(100), types.Entity{ID: "u1"})
	if counted.calls != 1 {
		t.Fatalf("four rules over one window must issue one query, got %d", counted.calls)
	}
}

type counting struct {
	history.Store
	calls int
}

func (c *counting) Window(ctx context.Context, s history.Subject, d time.Duration) ([]history.Event, error) {
	c.calls++
	return c.Store.Window(ctx, s, d)
}
