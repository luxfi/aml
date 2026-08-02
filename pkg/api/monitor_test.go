package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/dictionary"
	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/lists"
	"github.com/luxfi/aml/pkg/reference"
	"github.com/luxfi/aml/pkg/suppress"
	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/watch"
)

// The ingest path meets the five planes here, and this is where that meeting is
// checked end to end: a rule fires, an activation is written, a declared
// suppression changes the answer the caller gets, and the field catalog sees the
// payload.
//
// The planes' own tests establish that each one is correct. This one establishes
// that ingest is WIRED to them — the failure it exists to catch is five correct
// planes that nothing calls.

// monitored is the deployment. It is an alias of plane, kept because the tests
// that use it are about ingest meeting the planes rather than about ingest
// alone — and there is one assembly, so there is nothing for it to add.
func monitored(t *testing.T, rules ...types.Rule) *Handler {
	t.Helper()
	return plane(t, rules...)
}

// payment is the wire shape the ingest route takes: the transaction at the top
// level, with the customer beside it.
func payment(id string, usd float64) map[string]any {
	return map[string]any{
		"id": id, "user_id": "u1", "account_id": "acct-1",
		"currency": "USD", "notional": usd, "timestamp": time.Now().UTC(),
		"entity": map[string]any{"id": "u1", "name": "Acme Trading"},
	}
}

// TestIngestRecordsWhatTheRulesDid.
func TestIngestRecordsWhatTheRulesDid(t *testing.T) {
	h := monitored(t)
	ctx := context.Background()

	rec := ingest(t, h, payment("tx-1", 25_000))
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest = %d: %s", rec.Code, rec.Body.String())
	}
	var out types.EvalResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Action != types.ActionReport {
		t.Fatalf("the rule asked for report, got %q", out.Action)
	}

	feed, err := h.Planes.Watch.Feed(ctx, acme, &watch.FeedIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Activations) != 1 {
		t.Fatalf("the rule fired and nothing recorded it: %+v", feed.Activations)
	}
	a := feed.Activations[0]
	if a.Rule != "ctr" || a.Tx != "tx-1" || a.Subject.Value != "acct-1" || a.Subject.Kind != "account" {
		t.Fatalf("the activation does not describe what happened: %+v", a)
	}
	if a.Action != types.ActionReport || a.Response != types.ActionReport {
		t.Fatalf("with nothing declared, the response is what the rule asked for: %+v", a)
	}

	// And the payload reached the catalog.
	cat, err := h.Planes.Dictionary.Catalog(ctx, acme, &dictionary.CatalogIn{})
	if err != nil {
		t.Fatal(err)
	}
	if cat.Payloads != 1 {
		t.Fatalf("the field catalog did not see the payload: %d", cat.Payloads)
	}
}

// TestADeclaredSuppressionChangesTheAnswer is the whole of what suppression does,
// exercised where a customer would feel it.
func TestADeclaredSuppressionChangesTheAnswer(t *testing.T) {
	h := monitored(t)
	ctx := context.Background()
	if _, err := h.Planes.Suppress.Suppress(ctx, acme, &suppress.SuppressIn{
		Rule: "ctr", Kind: "account", Value: "acct-1",
		Reason: "treasury sweep, agreed with the MLRO 2026-02", By: "a.mensah",
	}); err != nil {
		t.Fatal(err)
	}

	rec := ingest(t, h, payment("tx-2", 25_000))
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest = %d: %s", rec.Code, rec.Body.String())
	}
	var out types.EvalResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Action != types.ActionAllow {
		t.Fatalf("a covered detection must not reach the caller as a report: %q", out.Action)
	}
	// The alert itself is still evidence and is still there.
	if len(out.AlertIDs) != 1 {
		t.Fatalf("suppression must not destroy the alert: %+v", out.AlertIDs)
	}

	feed, err := h.Planes.Watch.Feed(ctx, acme, &watch.FeedIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Activations) != 1 {
		t.Fatalf("a suppressed detection is recorded, not dropped: %+v", feed.Activations)
	}
	a := feed.Activations[0]
	if !a.Suppressed || a.Cause != watch.CauseSuppressed || a.By == "" {
		t.Fatalf("the activation must name the suppression that covered it: %+v", a)
	}
	if a.Action != types.ActionReport {
		t.Fatalf("what the rule asked for is still on the row: %+v", a)
	}
}

// TestADeclaredRungRaisesTheAnswer.
func TestADeclaredRungRaisesTheAnswer(t *testing.T) {
	h := monitored(t)
	ctx := context.Background()
	if _, err := h.Planes.Watch.Declare(ctx, acme, &watch.DeclareIn{
		Rule: "ctr", Kind: "account", Count: 2, Within: watch.Span(time.Hour), To: types.ActionBlock,
		Reason: "two reportable transactions in an hour on one account", By: "a.mensah",
	}); err != nil {
		t.Fatal(err)
	}

	if rec := ingest(t, h, payment("tx-3", 25_000)); rec.Code != http.StatusOK {
		t.Fatalf("ingest = %d: %s", rec.Code, rec.Body.String())
	}
	rec := ingest(t, h, payment("tx-4", 25_000))
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest = %d: %s", rec.Code, rec.Body.String())
	}
	var out types.EvalResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Action != types.ActionBlock {
		t.Fatalf("the declared escalation did not reach the caller: %q", out.Action)
	}
}

// TestARuleReadsTheTenantsOwnDenyList — the list plane reaching the rule
// vocabulary, through the engine's own evaluator.
func TestARuleReadsTheTenantsOwnDenyList(t *testing.T) {
	h := monitored(t)
	ctx := context.Background()
	if _, err := h.Planes.Lists.Declare(ctx, acme, &lists.DeclareIn{
		Name: "ip-deny", Kind: lists.Deny, Class: lists.IP, By: "a.mensah",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Planes.Lists.Add(ctx, acme, &lists.AddIn{
		Name: "ip-deny", By: "a.mensah",
		Values: []lists.Value{{Value: "198.51.100.7", Reason: "credential stuffing source"}},
	}); err != nil {
		t.Fatal(err)
	}

	// A rule over the list, admitted through the engine's own vocabulary check —
	// which is what refuses the rule outright if the provider is not wired.
	h.Engine = engine.New(engine.Providers{
		Rate:  reference.Rates{USDPer: map[string]float64{}},
		Lists: h.Planes.Lists,
		Zone:  time.UTC,
	})
	if err := h.Engine.SetRules([]types.Rule{{
		ID: "denied-address", Name: "Address on the deny list",
		DSL:      `Tx.IPAddress != "" && Listed("ip-deny", Tx.IPAddress)`,
		Severity: types.SeverityHigh, Weight: 0.4, Action: types.ActionBlock, Enabled: true,
	}}); err != nil {
		t.Fatalf("the deny-list rule must install where the list plane is wired: %v", err)
	}

	body := payment("tx-5", 100)
	body["ip_address"] = "198.51.100.7"
	rec := ingest(t, h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest = %d: %s", rec.Code, rec.Body.String())
	}
	var out types.EvalResult
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Action != types.ActionBlock {
		t.Fatalf("a payment from a listed address must be blocked, got %q", out.Action)
	}

	// And an address nobody listed passes.
	clean := payment("tx-6", 100)
	clean["ip_address"] = "203.0.113.9"
	rec = ingest(t, h, clean)
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Action != types.ActionAllow {
		t.Fatalf("an address nobody listed must pass, got %q", out.Action)
	}
}

// TestARetriedTransactionIsCountedOnce.
//
// Ingest records a transaction, then its alerts, then their activations. Anything
// after the first write that fails answers 503, and a client that retries a 503
// sends the whole transaction again — so every plane that ingest writes has to be
// able to see the same transaction twice and record it once. The activation plane
// is the one this track added, and a duplicated activation is worse than a
// duplicated row: it is counted in the streak, so a declared repetition policy
// fires on a repeat that never happened.
func TestARetriedTransactionIsCountedOnce(t *testing.T) {
	h := monitored(t)
	ctx := context.Background()

	body := payment("tx-1", 25_000)
	for range 3 {
		if rec := ingest(t, h, body); rec.Code != http.StatusOK {
			t.Fatalf("ingest = %d: %s", rec.Code, rec.Body.String())
		}
	}

	feed, err := h.Planes.Watch.Feed(ctx, acme, &watch.FeedIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Activations) != 1 {
		t.Fatalf("three offers of one transaction wrote %d activations: %+v", len(feed.Activations), feed.Activations)
	}
	rates, err := h.Planes.Watch.Rates(ctx, acme, &watch.RatesIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rates.Rules) != 1 || rates.Rules[0].Fired != 1 {
		t.Fatalf("the rates counted the retries: %+v", rates.Rules)
	}

	// And a different transaction is still a different activation, so the fix is
	// not "record nothing twice".
	if rec := ingest(t, h, payment("tx-2", 25_000)); rec.Code != http.StatusOK {
		t.Fatalf("ingest = %d: %s", rec.Code, rec.Body.String())
	}
	feed, err = h.Planes.Watch.Feed(ctx, acme, &watch.FeedIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Activations) != 2 {
		t.Fatalf("a second transaction did not produce a second activation: %+v", feed.Activations)
	}
}

// TestARetriedTransactionDoesNotEscalateItself.
//
// The sharpest form: a rung raises the response on the second firing, so a
// duplicated activation makes the RETRY of a single transaction escalate itself —
// a payment blocked because the client retried a 503.
func TestARetriedTransactionDoesNotEscalateItself(t *testing.T) {
	h := monitored(t)
	if _, err := h.Planes.Watch.Declare(context.Background(), acme, &watch.DeclareIn{
		Rule: "ctr", Kind: "account", Count: 2, Within: watch.Span(time.Hour), To: types.ActionBlock,
		Reason: "two reportable transactions in an hour on one account", By: "a.mensah",
	}); err != nil {
		t.Fatal(err)
	}

	body := payment("tx-1", 25_000)
	var out types.EvalResult
	for range 2 {
		rec := ingest(t, h, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("ingest = %d: %s", rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	if out.Action == types.ActionBlock {
		t.Fatal("retrying one transaction escalated it against itself")
	}

	// A genuinely second transaction still escalates, so the rung still works.
	rec := ingest(t, h, payment("tx-2", 25_000))
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Action != types.ActionBlock {
		t.Fatalf("the declared escalation stopped working: %q", out.Action)
	}
}

// TestOneTenantsSuppressionsAreNeverAnothersIngest.
//
// The isolation invariant, at the seam this track changed. A tenant that has
// crowded one rule with suppressions degrades ITS OWN cover check and nothing
// else: another institution's ingest is unaffected, its cover check is complete,
// and neither reads the other's rows.
func TestOneTenantsSuppressionsAreNeverAnothersIngest(t *testing.T) {
	h := monitored(t)
	ctx := context.Background()
	const rival = "hanzo/rival"

	for i := range 6 {
		if _, err := h.Planes.Suppress.Suppress(ctx, acme, &suppress.SuppressIn{
			Rule: "ctr", Kind: "account", Value: "acct-" + string(rune('a'+i)),
			Reason: "reviewed and agreed", By: "a.mensah",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// The other institution's own suppression still covers its own activation, and
	// this institution's six do not appear anywhere in its answer.
	if _, err := h.Planes.Suppress.Suppress(ctx, rival, &suppress.SuppressIn{
		Rule: "ctr", Kind: "account", Value: "acct-1", Reason: "their own decision", By: "b.tran",
	}); err != nil {
		t.Fatal(err)
	}
	cover, err := h.Planes.Suppress.Cover(ctx, rival, &suppress.CoverIn{Rule: "ctr", Kind: "account", Value: "acct-1"})
	if err != nil {
		t.Fatalf("the other tenant's cover check failed: %v", err)
	}
	if !cover.Covered || cover.Partial {
		t.Fatalf("the other tenant's answer was affected by this one's volume: %+v", cover)
	}
	ledger, err := h.Planes.Suppress.Ledger(ctx, rival, &suppress.LedgerIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Suppressions) != 1 {
		t.Fatalf("the other tenant reads %d suppressions, want only its own", len(ledger.Suppressions))
	}

	// And this institution's ingest still works, which is the self-DoS half.
	if rec := ingest(t, h, payment("tx-1", 25_000)); rec.Code != http.StatusOK {
		t.Fatalf("ingest = %d: %s", rec.Code, rec.Body.String())
	}
	feed, err := h.Planes.Watch.Feed(ctx, rival, &watch.FeedIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Activations) != 0 {
		t.Fatalf("one tenant's ingest wrote into another's activation plane: %+v", feed.Activations)
	}
}

// TestARealConflictIsAConflictAndNotAnOutage.
//
// The other half of making a retry work: an id reused for a DIFFERENT
// transaction is refused, and the refusal says which kind it is. 503 means "the
// engine could not, try again" and a client that retries a permanent refusal
// retries forever — which is the loop that turns one caller's mistake into a
// queue of transactions that never clears.
func TestARealConflictIsAConflictAndNotAnOutage(t *testing.T) {
	h := monitored(t)

	first := payment("tx-1", 25_000)
	if rec := ingest(t, h, first); rec.Code != http.StatusOK {
		t.Fatalf("ingest = %d: %s", rec.Code, rec.Body.String())
	}
	// Same id, a different amount. That is a different fact, and the ledger has
	// one under this name already.
	second := payment("tx-1", 90_000)
	second["timestamp"] = first["timestamp"]
	rec := ingest(t, h, second)
	if rec.Code != http.StatusConflict {
		t.Fatalf("a reused id with a different transaction = %d: %s, want 409", rec.Code, rec.Body.String())
	}

	// And the first record stands: a conflict refuses the second submission, it
	// does not overwrite what was retained.
	feed, err := h.Planes.Watch.Feed(context.Background(), acme, &watch.FeedIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Activations) != 1 {
		t.Fatalf("the conflicting submission left %d activations: %+v", len(feed.Activations), feed.Activations)
	}
}

// TestIngestStampsNoClockOfItsOwn reads the source. A reception clock written
// onto the transaction is what made the retry above impossible to recognise, and
// it is one line to reintroduce.
func TestIngestStampsNoClockOfItsOwn(t *testing.T) {
	raw, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"tx.CreatedAt =", "tx.UpdatedAt ="} {
		if strings.Contains(string(raw), banned) {
			t.Fatalf("%s: ingest stamps its own clock into the retained fact again, so no retry can be recognised", banned)
		}
	}
}
