package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/luxfi/aml/internal/instance"
	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/history"
	"github.com/luxfi/aml/pkg/receipt"
	"github.com/luxfi/aml/pkg/reference"
	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/velocity"
	"github.com/luxfi/aml/pkg/watch"
)

// A retry is not a second transaction.
//
// A client that never saw the first response resends. Every aggregate this
// engine exists to compute — a 24-hour count, a 30-day sum, a structuring
// spread — is a count of transactions, so a retry that is counted is an
// aggregate wrong by the number of times the network failed. The ledger has
// always been idempotent; these tests are about everything else, because a
// ledger holding one record while the aggregates hold three is worse than a
// double count everywhere: the numbers disagree with each other and nothing
// says which is right.
//
// They run on the shelves cmd/amld wires. That is the whole lesson of the
// record fingerprint: an identity property proven against a memory stand-in is
// a property of the stand-in.

// offers sends the same payload n times and returns every response.
func offers(t *testing.T, h *Handler, n int, body any) []string {
	t.Helper()
	out := make([]string, 0, n)
	for i := range n {
		rec := ingest(t, h, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("offer %d = %d: %s", i+1, rec.Code, rec.Body.String())
		}
		out = append(out, rec.Body.String())
	}
	return out
}

// window is this tenant's 24-hour aggregate for the account the payments name.
func window(t *testing.T, h *Handler, org, account string) velocity.Observation {
	t.Helper()
	for _, o := range h.Velocity.Observe(velocity.Key{OrgID: org, Kind: anomaly.AxisAccount, Value: account}) {
		if o.Window == "24h" {
			return o
		}
	}
	t.Fatal("no 24h window")
	return velocity.Observation{}
}

// events is how many history rows this tenant holds for the account.
func events(t *testing.T, h *Handler, org, account string) int {
	t.Helper()
	got, err := h.History.Window(context.Background(),
		history.Subject{OrgID: org, Kind: history.SubjectAccount, ID: account}, 24*time.Hour)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	return len(got)
}

// TestARetryIsCountedOnce: three offers of ONE payment, on every plane the
// ingest path writes to.
//
// The ledger was the only one that was idempotent. Velocity counted three,
// history held three rows, the alert store held three copies of one piece of
// evidence and the case plane minted three cases — so a supervisor reading the
// queue saw three investigations of one payment.
func TestARetryIsCountedOnce(t *testing.T) {
	h := monitored(t)

	got := offers(t, h, 3, payment("tx-1", 25_000))

	// Every offer of one transaction gets the same answer, byte for byte. A
	// client that retried cannot tell that it retried, which is what idempotent
	// means.
	for i, body := range got[1:] {
		if body != got[0] {
			t.Errorf("offer %d answered differently:\n first %s\n then  %s", i+2, got[0], body)
		}
	}

	if w := window(t, h, acme, "acct-1"); w.Count != 1 || w.Sum != 25_000 {
		t.Errorf("velocity 24h after three offers of one payment: count=%d sum=%v, want 1 and 25000", w.Count, w.Sum)
	}
	if n := events(t, h, acme, "acct-1"); n != 1 {
		t.Errorf("history rows after three offers of one payment: %d, want 1", n)
	}
	if n := len(h.Alerts.ByTx(acme, "tx-1")); n != 1 {
		t.Errorf("alerts on one transaction: %d, want 1", n)
	}
	if n := len(h.Cases.List(acme, "")); n != 1 {
		t.Errorf("cases from one transaction: %d, want 1", n)
	}
	if n := ledgerLen(t, h); n != 1 {
		t.Errorf("ledger records for one transaction: %d, want 1", n)
	}

	// And the monitoring plane saw one firing, not three.
	feed, err := h.Planes.Watch.Feed(context.Background(), acme, &watch.FeedIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Activations) != 1 {
		t.Errorf("activations from one transaction: %d, want 1", len(feed.Activations))
	}
}

// TestARetryCannotFlipTheVerdict is the harm the double count did to a customer.
//
// With a rule that fires on the second transaction in a window, the second OFFER
// of one transaction saw a window it had put itself in twice, the verdict flipped
// from allow to block, and the ledger retained a REFUSAL beside the transaction
// record — a regulatory record under AMLR Art. 77(3) of a refusal the institution
// never made, filed because a client retried.
func TestARetryCannotFlipTheVerdict(t *testing.T) {
	h := monitored(t)
	// A rule over the tenant's own history, admitted through the engine's own
	// vocabulary check — which is what makes the aggregate this test is about the
	// real one rather than a stand-in.
	h.Engine = engine.New(engine.Providers{
		Rate: reference.Rates{USDPer: map[string]float64{}}, History: h.History, Zone: time.UTC,
	})
	if err := h.Engine.SetRules([]types.Rule{{
		ID: "struct", Name: "Two in a day", DSL: `Count("account", "24h") >= 2`,
		Severity: types.SeverityCritical, Weight: 0.9, Action: types.ActionBlock, Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}

	actions := make([]string, 0, 3)
	for _, body := range offers(t, h, 3, payment("tx-1", 25_000)) {
		var out types.EvalResult
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatal(err)
		}
		actions = append(actions, out.Action)
	}
	for i, a := range actions {
		if a != actions[0] {
			t.Fatalf("three offers of ONE transaction answered %v: offer %d changed the customer's outcome", actions, i+1)
		}
	}
	if actions[0] == types.ActionBlock {
		t.Fatalf("one transaction alone met a two-in-a-day rule: %v", actions)
	}
	if n := ledgerLen(t, h); n != 1 {
		t.Errorf("ledger records for one transaction: %d, want 1", n)
	}
}

// TestADifferentTransactionUnderOneIdIsRefused: the refusal the idempotency must
// not weaken. Two different facts under one reference is the caller's to resolve
// and it will never clear on its own, so it is 409 and never 503 — and it is
// refused BEFORE anything is written, not after the aggregates have taken it.
func TestADifferentTransactionUnderOneIdIsRefused(t *testing.T) {
	h := monitored(t)

	if rec := ingest(t, h, payment("tx-1", 25_000)); rec.Code != http.StatusOK {
		t.Fatalf("first = %d: %s", rec.Code, rec.Body.String())
	}
	rec := ingest(t, h, payment("tx-1", 40_000))
	if rec.Code != http.StatusConflict {
		t.Fatalf("a different transaction under one id = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if n := ledgerLen(t, h); n != 1 {
		t.Errorf("ledger records: %d, want 1 — the refused offer was retained", n)
	}
	if w := window(t, h, acme, "acct-1"); w.Count != 1 {
		t.Errorf("velocity counted the refused offer: count=%d, want 1", w.Count)
	}
	if n := events(t, h, acme, "acct-1"); n != 1 {
		t.Errorf("history took a row for the refused offer: %d, want 1", n)
	}
}

// TestOneTenantsRetryIsNotAnothersTransaction: reception is per tenant, like
// every other identity in this engine. Two institutions using one transaction id
// are two transactions.
func TestOneTenantsRetryIsNotAnothersTransaction(t *testing.T) {
	h := monitored(t)

	tenant := acme
	h.Identity = func(*http.Request) (Caller, error) { return Caller{Tenant: tenant, Subject: "u-analyst"}, nil }

	if rec := ingest(t, h, payment("tx-1", 25_000)); rec.Code != http.StatusOK {
		t.Fatalf("acme = %d: %s", rec.Code, rec.Body.String())
	}
	tenant = "zoo/acme"
	rec := ingest(t, h, payment("tx-1", 40_000))
	if rec.Code != http.StatusOK {
		t.Fatalf("another tenant's transaction under the same id = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if n := ledgerLen(t, h); n != 2 {
		t.Errorf("ledger records across two tenants: %d, want 2", n)
	}
}

// TestTheReceiptSurvivesARestart: a rollout is Recreate at one replica, so the
// process that answered the first offer is gone by the time the client retries.
// A reception kept in memory would be no reception at all.
func TestTheReceiptSurvivesARestart(t *testing.T) {
	// ONE body, offered twice. A retry resends what it sent; a body carrying a
	// fresh clock each time would be a different transaction, and this engine is
	// right to say so — see TestADifferentTransactionUnderOneIdIsRefused.
	offer := payment("tx-1", 25_000)

	first := instance.New(t)
	before := planeOn(t, first)
	one := ingest(t, before, offer)
	if one.Code != http.StatusOK {
		t.Fatalf("first offer = %d: %s", one.Code, one.Body.String())
	}

	second := instance.Restart(t, first)
	t.Cleanup(second.Cleanup)
	after := planeOn(t, second)
	again := ingest(t, after, offer)
	if again.Code != http.StatusOK {
		t.Fatalf("retry after a restart = %d: %s", again.Code, again.Body.String())
	}
	if again.Body.String() != one.Body.String() {
		t.Errorf("the retry got a different answer across the restart:\n first %s\n then  %s", one.Body.String(), again.Body.String())
	}
	if n := ledgerLen(t, after); n != 1 {
		t.Errorf("ledger records after a restart and a retry: %d, want 1", n)
	}
}

// TestAnOfferWithNoReceiptPlaneIsRefused: the plane is not optional. An engine
// that cannot recognise a retry counts one payment as many, and the aggregates
// are the whole product.
func TestAnOfferWithNoReceiptPlaneIsRefused(t *testing.T) {
	h := plane(t)
	h.Receipts = nil
	if rec := ingest(t, h, payment("tx-1", 25_000)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ingest with no receipt plane = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

// TestAnOversizedIdentifierIsRefusedAtTheDoor: every count-shaped bound in this
// engine is a bound in bytes only because the identifiers are bounded here. One
// number, checked once — see types.MaxIdent.
func TestAnOversizedIdentifierIsRefusedAtTheDoor(t *testing.T) {
	h := monitored(t)
	long := make([]byte, types.MaxIdent+1)
	for i := range long {
		long[i] = 'a'
	}

	body := payment("tx-1", 100)
	body["device_fingerprint"] = string(long)
	if rec := ingest(t, h, body); rec.Code != http.StatusBadRequest {
		t.Errorf("an identifier of %d bytes = %d, want 400", len(long), rec.Code)
	}
	if h.Velocity.Keys() != 0 {
		t.Errorf("the refused transaction still took an aggregate key")
	}
}

// TestAnInterruptedOfferIsStillCountedOnce is the half of idempotence a receipt
// cannot give on its own.
//
// The receipt is written after the work and before the response, which is what
// makes it true that a caller holding an answer has one on record. It also means
// a process that dies between the writes and the answer leaves the work done and
// no receipt — and the client, never having been answered, retries. Every plane
// the ingest path writes to therefore has to recognise the transaction by
// itself, not merely be sequenced behind something that does.
//
// The ledger already did, and the activation plane already did. This is the rest
// of them. Velocity is not in the list because it is in memory: the crash that
// loses the receipt loses the aggregate too, and it is rebuilt from the durable
// events these assertions are about.
func TestAnInterruptedOfferIsStillCountedOnce(t *testing.T) {
	h := monitored(t)
	offer := payment("tx-1", 25_000)

	if rec := ingest(t, h, offer); rec.Code != http.StatusOK {
		t.Fatalf("first offer = %d: %s", rec.Code, rec.Body.String())
	}
	// The crash: the work landed, the answer never reached the caller, and the
	// receipt that would have recognised the retry was never written.
	forget(t, h, acme, "tx-1")

	if rec := ingest(t, h, offer); rec.Code != http.StatusOK {
		t.Fatalf("retry after an interrupted offer = %d: %s", rec.Code, rec.Body.String())
	}

	if n := events(t, h, acme, "acct-1"); n != 1 {
		t.Errorf("history rows after an interrupted offer and a retry: %d, want 1", n)
	}
	if n := len(h.Alerts.ByTx(acme, "tx-1")); n != 1 {
		t.Errorf("alerts on one transaction after an interrupted offer: %d, want 1", n)
	}
	if n := ledgerLen(t, h); n != 1 {
		t.Errorf("ledger records after an interrupted offer: %d, want 1", n)
	}
	if n := len(h.Cases.List(acme, "")); n != 1 {
		t.Errorf("cases from one transaction after an interrupted offer: %d, want 1", n)
	}
	feed, err := h.Planes.Watch.Feed(context.Background(), acme, &watch.FeedIn{})
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Activations) != 1 {
		t.Errorf("activations after an interrupted offer: %d, want 1", len(feed.Activations))
	}
}

// forget drops this tenant's receipt for a transaction: the state a process
// that died between the last write and the response leaves behind.
func forget(t *testing.T, h *Handler, org, tx string) {
	t.Helper()
	if err := h.Receipts.Forget(context.Background(), org, tx); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if _, held, err := h.Receipts.Prior(context.Background(), org, receipt.Offer{Ref: tx, Mark: "any"}); err != nil || held {
		t.Fatalf("the receipt is still there: held=%v err=%v", held, err)
	}
}
