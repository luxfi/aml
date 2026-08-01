package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/internal/instance"
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

func monitored(t *testing.T) *Handler {
	t.Helper()
	app := instance.New(t)
	t.Cleanup(app.Cleanup)
	for _, ensure := range []func(core.App) error{lists.Ensure, suppress.Ensure, watch.Ensure, dictionary.Ensure} {
		if err := ensure(app); err != nil {
			t.Fatalf("ensure: %v", err)
		}
	}
	silence := suppress.NewBase(app)
	monitor := watch.NewBase(app)
	monitor.Cover = silence

	h := plane(t)
	h.Planes = Planes{
		Lists:      lists.NewBase(app),
		Suppress:   silence,
		Watch:      monitor,
		Dictionary: dictionary.NewBase(app),
	}
	return h
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
