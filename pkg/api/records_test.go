package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/internal/instance"
	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/reference"
	"github.com/luxfi/aml/pkg/replay"
	"github.com/luxfi/aml/pkg/retention"
	"github.com/luxfi/aml/pkg/token"
	"github.com/luxfi/aml/pkg/topology"
	"github.com/luxfi/aml/pkg/types"
)

// The record plane joins three packages that do not know about each other. These
// tests are where that join is checked, and they are also the only place the
// engine's real evaluator is put behind the sandbox's interface — if the two ever
// stop fitting, this file stops compiling.

var root = bytes.Repeat([]byte{0x5a}, 32)

// send builds a request event carrying a JSON body, which the identity helper's
// event() deliberately does not.
func send(method, target string, body any) (*core.RequestEvent, *httptest.ResponseRecorder) {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e := &core.RequestEvent{}
	e.Request = req
	e.Response = rec
	return e, rec
}

// shelves opens an instance the deployment's way.
//
// There is one assembly (api.Wire) and these tests use it, deliberately. A green
// suite over an arrangement production does not have proves nothing about
// production: the record fingerprint was a struct field no column stored, so
// every retry of one transaction conflicted permanently on the durable shelf
// while every test passed on a hand-built handler over a memory one. See
// wire_test.go.
func shelves(t *testing.T) core.App {
	t.Helper()
	app := instance.New(t)
	t.Cleanup(app.Cleanup)
	return app
}

// planeOn is the deployment, over one app, with a rule set. It is separate from
// plane so a restart test can open a second instance over the first one's bytes.
func planeOn(t *testing.T, app core.App, rules ...types.Rule) *Handler {
	t.Helper()
	if len(rules) == 0 {
		rules = []types.Rule{{
			ID: "ctr", Name: "CTR Threshold", DSL: "Tx.Notional > 10000.0",
			Severity: types.SeverityHigh, Weight: 0.3, Action: types.ActionReport, Enabled: true,
		}}
	}
	d := deployment()
	d.Rules = rules
	h, err := Wire(app, d)
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	return h
}

// plane is the deployment over a fresh app.
func plane(t *testing.T, rules ...types.Rule) *Handler {
	t.Helper()
	return planeOn(t, shelves(t), rules...)
}

// keyless is a handler whose key material has not arrived.
func keyless(t *testing.T) *Handler {
	t.Helper()
	h := plane(t)
	h.Keys = token.NewKeyring(func(string) ([]byte, error) { return nil, token.ErrNoKey })
	return h
}

func ingest(t *testing.T, h *Handler, body any) *httptest.ResponseRecorder {
	t.Helper()
	e, rec := send(http.MethodPost, "/v1/aml/transactions", body)
	if err := h.ingestTransaction()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	return rec
}

// ledgerLen is how many records the handler's ledger holds.
func ledgerLen(t *testing.T, h *Handler) int {
	t.Helper()
	held, err := h.Records.Len()
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	return held
}

func only(t *testing.T, h *Handler, class retention.Class) retention.Record {
	t.Helper()
	var found []retention.Record
	if err := h.Records.Each(retention.PurposeRetention, acme, class, func(r retention.Record) error {
		found = append(found, r)
		return nil
	}); err != nil {
		t.Fatalf("Each: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("%s records = %d, want 1", class, len(found))
	}
	return found[0]
}

// TestIngestRefusesWithoutKeyMaterial is the sharpest fail-closed property in the
// plane: a transaction that cannot be recorded is not processed at all. The
// alternative is a clean receipt for a transaction nobody can produce a record
// of, which is the failure the whole record plane exists to prevent.
func TestIngestRefusesWithoutKeyMaterial(t *testing.T) {
	h := keyless(t)

	rec := ingest(t, h, map[string]any{"user_id": "ivan-petrov-42", "notional": 25000, "currency": "USD"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "AML_TOKEN_KEY") {
		t.Errorf("the refusal names the key source: %s", rec.Body.String())
	}

	// And nothing was stored on the way to refusing.
	if held, err := h.Records.Len(); err != nil || held != 0 {
		t.Errorf("ledger holds %d records (%v)", held, err)
	}
	if h.Alerts.Len() != 0 {
		t.Errorf("alert store holds %d transactions", h.Alerts.Len())
	}
	if h.Cases.Len(acme) != 0 {
		t.Errorf("case store holds %d cases", h.Cases.Len(acme))
	}
}

// TestHealthReportsUnfitWithoutKeyMaterial: an instance that cannot record must
// not be sent traffic, so it says so where a readiness probe can see it.
func TestHealthReportsUnfitWithoutKeyMaterial(t *testing.T) {
	fit := plane(t)
	e, rec := send(http.MethodGet, "/v1/aml/health", nil)
	if err := fit.health()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("healthy instance: status %d, want 200", rec.Code)
	}

	unfit := keyless(t)
	e, rec = send(http.MethodGet, "/v1/aml/health", nil)
	if err := unfit.health()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("instance with no key material: status %d, want 503", rec.Code)
	}

	unwired := plane(t)
	unwired.Records, unwired.Keys = nil, nil
	e, rec = send(http.MethodGet, "/v1/aml/health", nil)
	if err := unwired.health()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("instance with no record plane: status %d, want 503", rec.Code)
	}
}

// TestIngestRetainsATransactionThatCanBeReconstructed is MLR reg. 40(2)(b): the
// retained record must be enough to reconstruct the transaction. Sealed is not
// redacted — it opens, whole.
func TestIngestRetainsATransactionThatCanBeReconstructed(t *testing.T) {
	h := plane(t)

	// The customer's name is supplied by the caller, not derived from the
	// identifier: a customer id is an opaque handle, and screening one as a name
	// produces false positives with no possible true positive.
	rec := ingest(t, h, map[string]any{
		"id": "tx-1", "user_id": "ivan-petrov-42", "account_id": "GB33BUKB20201555555555",
		"counterparty": "acme-ltd", "notional": 25000, "currency": "USD", "symbol": "BTC",
		"entity": map[string]any{"id": "ivan-petrov-42", "name": "Ivan Petrov"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Action string `json:"action"`
		Record string `json:"record"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Record == "" {
		t.Fatal("ingest answered without naming the record it retained")
	}

	record, err := h.Records.Get(retention.PurposeInvestigation, acme, out.Record)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if record.Class != retention.ClassTransaction || record.Trigger != retention.TriggerOccasional {
		t.Errorf("class/trigger = %q/%q", record.Class, record.Trigger)
	}
	if record.Ref != "tx-1" {
		t.Errorf("ref = %q, want tx-1", record.Ref)
	}
	// Four parties: the subject, its name, the account, and the counterparty.
	if len(record.Parties) != 4 {
		t.Errorf("parties = %v, want four", record.Parties)
	}

	vault, err := h.vault(acme)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	plain, err := vault.Open(slot(record.Class, record.Ref), record.Body)
	if err != nil {
		t.Fatalf("the retained record does not open: %v", err)
	}
	var ctx types.EvalContext
	if err := json.Unmarshal(plain, &ctx); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if ctx.Tx.ID != "tx-1" || ctx.Tx.Notional != 25000 || ctx.Tx.Currency != "USD" ||
		ctx.Tx.UserID != "ivan-petrov-42" || ctx.Tx.AccountID != "GB33BUKB20201555555555" ||
		ctx.Tx.Counterparty != "acme-ltd" || ctx.Tx.Symbol != "BTC" {
		t.Errorf("the transaction did not reconstruct: %+v", ctx.Tx)
	}
}

// TestRefusedTransactionIsRetainedAsARefusal is the AMLR Art. 77(3) trigger that
// implementations miss: a transaction the firm refused to carry out starts its own
// five-year period, and the record says why the firm refrained.
func TestRefusedTransactionIsRetainedAsARefusal(t *testing.T) {
	h := plane(t, types.Rule{
		ID: "mixer", Name: "Crypto Mixer", DSL: "Tx.Notional > 0.0",
		Severity: types.SeverityCritical, Weight: 1, Action: types.ActionBlock, Enabled: true,
	})

	rec := ingest(t, h, map[string]any{"id": "tx-1", "user_id": "ivan-petrov-42", "notional": 500})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}

	record := only(t, h, retention.ClassRefusal)
	if record.Trigger != retention.TriggerRefusal {
		t.Errorf("trigger = %q, want %q", record.Trigger, retention.TriggerRefusal)
	}
	if !strings.Contains(record.Reason, "Crypto Mixer") {
		t.Errorf("reason = %q, want the rule that refused it", record.Reason)
	}
	if want := record.Occurred.AddDate(retention.Period, 0, 0); !record.Expiry().Equal(want) {
		t.Errorf("expiry = %s, want five years from the refusal (%s)", record.Expiry(), want)
	}
	// And it is not also filed as an ordinary transaction record.
	count := 0
	if err := h.Records.Each(retention.PurposeRetention, acme, retention.ClassTransaction, func(retention.Record) error {
		count++
		return nil
	}); err != nil {
		t.Fatalf("Each: %v", err)
	}
	if count != 0 {
		t.Errorf("a refused transaction was also retained as %d transaction records", count)
	}
}

// TestRetainedRecordCarriesNoIdentifierInTheClear: the ledger holds pseudonyms and
// ciphertext. A record that named the customer in the clear would defeat the
// purpose limitation the tokenisation is there to serve.
func TestRetainedRecordCarriesNoIdentifierInTheClear(t *testing.T) {
	h := plane(t)
	const (
		user    = "ivan-petrov-42"
		account = "GB33BUKB20201555555555"
		other   = "acme-ltd"
	)

	if rec := ingest(t, h, map[string]any{
		"id": "tx-1", "user_id": user, "account_id": account, "counterparty": other,
		"notional": 25000, "currency": "USD",
	}); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	record := only(t, h, retention.ClassTransaction)
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, identifier := range []string{user, account, other} {
		if bytes.Contains(encoded, []byte(identifier)) {
			t.Errorf("%q appears in the retained record: %s", identifier, encoded)
		}
		if bytes.Contains(record.Body, []byte(identifier)) {
			t.Errorf("%q appears in the sealed body in the clear", identifier)
		}
	}
	// The reference is in the clear on purpose, and it is not an identifier.
	if !bytes.Contains(encoded, []byte("tx-1")) {
		t.Error("the record does not carry its own reference")
	}
}

// TestPseudonymsDoNotCrossTheOrgBoundary: the same customer under two orgs is two
// unrelated index keys, so one org's ledger cannot be joined to another's.
func TestPseudonymsDoNotCrossTheOrgBoundary(t *testing.T) {
	h := plane(t)
	mine, err := h.vault(acme)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	theirs, err := h.vault("beta")
	if err != nil {
		t.Fatalf("vault: %v", err)
	}

	a, err := mine.Pseudonym(token.DomainSubject, "ivan-petrov-42")
	if err != nil {
		t.Fatalf("Pseudonym: %v", err)
	}
	b, err := theirs.Pseudonym(token.DomainSubject, "ivan-petrov-42")
	if err != nil {
		t.Fatalf("Pseudonym: %v", err)
	}
	if a == b {
		t.Fatalf("one customer, two orgs, one key: %q", a)
	}
}

// TestLookbackAnswersByName is AMLR Art. 78 through the surface: the caller asks
// about a name, the name is tokenised, and the answer comes from the party index.
func TestLookbackAnswersByName(t *testing.T) {
	h := plane(t)

	e, rec := send(http.MethodPost, "/v1/aml/relationships", map[string]any{
		"ref": "rel-1", "nature": "payments", "name": "Ivan Petrov", "user_id": "ivan-petrov-42",
	})
	if err := h.openRelationship()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("open: status %d, want 201: %s", rec.Code, rec.Body.String())
	}

	ask := func(party string) retention.Answer {
		t.Helper()
		e, rec := send(http.MethodPost, "/v1/aml/relationships/search", map[string]any{"party": party})
		if err := h.searchRelationships()(e); err != nil {
			t.Fatalf("transport error: %v", err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("search: status %d: %s", rec.Code, rec.Body.String())
		}
		var answer retention.Answer
		if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return answer
	}

	// The name as asked, and the same name spelled differently: the canonical form
	// is the tokeniser's, so both find it.
	for _, party := range []string{"Ivan Petrov", "  ivan   PETROV "} {
		answer := ask(party)
		if !answer.Maintained || !answer.Current {
			t.Errorf("%q: maintained=%v current=%v", party, answer.Maintained, answer.Current)
		}
		if len(answer.Natures) != 1 || answer.Natures[0] != "payments" {
			t.Errorf("%q: natures = %v", party, answer.Natures)
		}
		if answer.Examined != 1 {
			t.Errorf("%q: examined = %d, want 1", party, answer.Examined)
		}
	}
	if answer := ask("Someone Else"); answer.Maintained || answer.Examined != 0 {
		t.Errorf("a party with nothing on file: %+v", answer)
	}

	// The answer does not leak the internal key back to the caller.
	if strings.Contains(rec.Body.String(), "name:") {
		t.Errorf("the answer carries the pseudonym: %s", rec.Body.String())
	}
}

// TestClosingARelationshipStartsTheClockOnWhatIsInsideIt: the surface has to be
// able to end a relationship, or the five-year period never starts and nothing is
// ever disposed of.
func TestClosingARelationshipStartsTheClockOnWhatIsInsideIt(t *testing.T) {
	h := plane(t)

	e, rec := send(http.MethodPost, "/v1/aml/relationships", map[string]any{
		"ref": "rel-1", "nature": "payments", "user_id": "ivan-petrov-42",
		"opened": time.Now().UTC().AddDate(-9, 0, 0),
	})
	if err := h.openRelationship()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	var opened struct{ Relationship string }
	if err := json.Unmarshal(rec.Body.Bytes(), &opened); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got := ingest(t, h, map[string]any{
		"id": "tx-1", "user_id": "ivan-petrov-42", "notional": 500,
		"relationship": opened.Relationship,
	}); got.Code != http.StatusOK {
		t.Fatalf("ingest: status %d: %s", got.Code, got.Body.String())
	}
	inside := only(t, h, retention.ClassTransaction)
	if !inside.Expiry().IsZero() {
		t.Errorf("in-relationship record expires at %s while the relationship is open", inside.Expiry())
	}

	ended := time.Now().UTC().AddDate(-1, 0, 0)
	e, rec = send(http.MethodPost, "/v1/aml/relationships/"+opened.Relationship+"/close",
		map[string]any{"ended": ended})
	e.Request.SetPathValue("id", opened.Relationship)
	if err := h.closeRelationship()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("close: status %d: %s", rec.Code, rec.Body.String())
	}
	var closed struct {
		Started int `json:"clocks_started"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &closed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if closed.Started != 2 {
		t.Errorf("clocks started = %d, want 2 (the relationship and the transaction)", closed.Started)
	}

	inside = only(t, h, retention.ClassTransaction)
	// To the millisecond the shelf keeps dates at. The ledger's own digest is
	// computed in UnixMilli for the same reason: a clock read back is the clock
	// that was stored, and comparing it against a nanosecond the caller happened
	// to hold tests the test's precision rather than the record's.
	if want := ended.AddDate(retention.Period, 0, 0).Truncate(time.Millisecond); !inside.Expiry().Equal(want) {
		t.Errorf("expiry = %s, want five years from the end of the relationship (%s)", inside.Expiry(), want)
	}

	// An unknown relationship on an ingest is the caller's mistake, not a refusal
	// of the record plane.
	if got := ingest(t, h, map[string]any{
		"user_id": "ivan-petrov-42", "notional": 500, "relationship": "no-such-relationship",
	}); got.Code != http.StatusBadRequest {
		t.Errorf("unknown relationship: status %d, want 400", got.Code)
	}
}

// TestResolveRetainsTheDecision: a case closes against a retained Art. 69(2)
// assessment, and a dismissal is retained with its rationale exactly like a
// report. Without the decision there is no closure.
func TestResolveRetainsTheDecision(t *testing.T) {
	h := plane(t)
	c := h.Cases.Create(acme, types.SeverityHigh, []string{"alert-1"}, []string{"ivan-petrov-42"})

	resolve := func(in map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		e, rec := send(http.MethodPost, "/v1/aml/cases/"+c.ID+"/resolve", in)
		e.Request.SetPathValue("id", c.ID)
		if err := h.resolveCase()(e); err != nil {
			t.Fatalf("transport error: %v", err)
		}
		return rec
	}

	// No rationale: refused, and the case stays open. This is the requirement most
	// systems miss — a dismissed alert is a retained decision, not a status change.
	if rec := resolve(map[string]any{
		"resolution": types.ResolutionFalsePositive,
		"considered": []string{"velocity over 30 days"},
		"by":         "mlro",
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("no rationale: status %d, want 400", rec.Code)
	}
	if got := h.Cases.Get(c.ID); got.Status == types.CaseClosed {
		t.Fatal("a case closed without a retained decision")
	}
	if held, err := h.Records.Len(); err != nil || held != 0 {
		t.Errorf("an incomplete assessment was retained: %d records (%v)", held, err)
	}

	rec := resolve(map[string]any{
		"resolution": types.ResolutionFalsePositive,
		"considered": []string{"velocity over 30 days", "customer profile"},
		"rationale":  "salary payments consistent with the profile on file",
		"by":         "mlro",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}

	closed := h.Cases.Get(c.ID)
	if closed.Status != types.CaseClosed {
		t.Errorf("status = %q, want closed", closed.Status)
	}
	if closed.Assessment == "" {
		t.Error("the closed case does not name the decision that closed it")
	}

	record := only(t, h, retention.ClassAssessment)
	if record.ID != closed.Assessment {
		t.Errorf("case names assessment %q, ledger holds %q", closed.Assessment, record.ID)
	}
	if record.Assessment.Result != retention.NotReported {
		t.Errorf("result = %q, want not_reported", record.Assessment.Result)
	}
	if !strings.Contains(record.Assessment.Rationale, "consistent with the profile") {
		t.Errorf("rationale = %q", record.Assessment.Rationale)
	}
	if len(record.Assessment.Considered) != 2 {
		t.Errorf("considered = %v, want two items", record.Assessment.Considered)
	}
	if len(record.Assessment.Alerts) != 1 || record.Assessment.Alerts[0] != "alert-1" {
		t.Errorf("alerts = %v, want [alert-1]", record.Assessment.Alerts)
	}
}

// TestFilingASARIsRetainedAsReported: the same record, the other result.
func TestFilingASARIsRetainedAsReported(t *testing.T) {
	h := plane(t)
	c := h.Cases.Create(acme, types.SeverityCritical, []string{"alert-1"}, []string{"ivan-petrov-42"})

	e, rec := send(http.MethodPost, "/v1/aml/cases/"+c.ID+"/resolve", map[string]any{
		"resolution": types.ResolutionSARFiled,
		"considered": []string{"structuring pattern over seven days"},
		"rationale":  "successive deposits below the reporting threshold, no economic rationale",
		"by":         "mlro",
	})
	e.Request.SetPathValue("id", c.ID)
	if err := h.resolveCase()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := only(t, h, retention.ClassAssessment).Assessment.Result; got != retention.Reported {
		t.Errorf("result = %q, want reported", got)
	}
}

// TestSandboxRefusesAnEmptyHistory: a replay over nothing looks exactly like a
// quiet rule. JMLSG 5.7.18 asks for a test before activation, and a test with no
// history is not one.
func TestSandboxRefusesAnEmptyHistory(t *testing.T) {
	h := plane(t)

	e, rec := send(http.MethodPost, "/v1/aml/rules/test", map[string]any{"dsl": "Tx.Notional > 10000.0"})
	if err := h.testRule()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "\"alerts\"") {
		t.Errorf("a refused replay reported alerts: %s", rec.Body.String())
	}
}

// TestSandboxReplaysTheEngineOverRetainedHistory is the whole sandbox: the real
// evaluator, the real retained transactions, and a count of what a candidate
// would have raised. It is also where *engine.Evaluator is checked against
// replay.Evaluator — if those two stop fitting, this file stops compiling.
func TestSandboxReplaysTheEngineOverRetainedHistory(t *testing.T) {
	h := plane(t)
	var _ replay.Evaluator = Compiled{E: h.Engine.Evaluator()}

	for _, notional := range []float64{500, 15000, 9500} {
		if rec := ingest(t, h, map[string]any{
			"user_id": "ivan-petrov-42", "notional": notional, "currency": "USD",
			"timestamp": time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC),
		}); rec.Code != http.StatusOK {
			t.Fatalf("ingest %v: status %d: %s", notional, rec.Code, rec.Body.String())
		}
	}
	records, opened := ledgerLen(t, h), h.Cases.Len(acme)

	e, rec := send(http.MethodPost, "/v1/aml/rules/test", map[string]any{
		"dsl":       "Tx.Notional >= 9000.0 && Tx.Notional < 10000.0",
		"incumbent": "ctr",
	})
	if err := h.testRule()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var report replay.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if report.Events != 3 {
		t.Errorf("events = %d, want 3", report.Events)
	}
	if report.Candidate.Alerts != 1 {
		t.Errorf("candidate alerts = %d, want 1 (the 9500)", report.Candidate.Alerts)
	}
	if report.Incumbent == nil || report.Incumbent.Alerts != 1 {
		t.Errorf("incumbent = %+v, want one alert (the 15000)", report.Incumbent)
	}
	if report.Delta == nil || report.Delta.Sizes.Added != 1 || report.Delta.Sizes.Dropped != 1 || report.Delta.Sizes.Kept != 0 {
		t.Errorf("delta = %+v, want one added and one dropped", report.Delta)
	}
	// Nothing was judged, so the two proportions the FCA asks for are absent
	// rather than zero — a false-positive proportion of 0.0 reads as a perfect rule.
	if report.Candidate.FalsePositive != nil || report.Candidate.Intelligence != nil {
		t.Errorf("unmeasured proportions reported: %v %v", report.Candidate.FalsePositive, report.Candidate.Intelligence)
	}

	// A sandbox that can mutate live state is not a sandbox. The ingested 15000
	// opened a case on the way in; the replay must not open another, nor retain
	// anything, nor alert.
	if held := ledgerLen(t, h); held != records {
		t.Errorf("the replay changed the ledger: %d records, was %d", held, records)
	}
	if h.Cases.Len(acme) != opened {
		t.Errorf("the replay opened %d cases", h.Cases.Len(acme)-opened)
	}
}

// TestSandboxRefusesARuleThatDoesNotEvaluate: a count taken from a rule that did
// not run is not a count.
func TestSandboxRefusesARuleThatDoesNotEvaluate(t *testing.T) {
	h := plane(t)
	if rec := ingest(t, h, map[string]any{"user_id": "ivan-petrov-42", "notional": 500}); rec.Code != http.StatusOK {
		t.Fatalf("ingest: status %d", rec.Code)
	}

	e, rec := send(http.MethodPost, "/v1/aml/rules/test", map[string]any{"dsl": "this is not an expression"})
	if err := h.testRule()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// TestSandboxOverASampleDoesNotTouchTheRecordPlane: an author trying an expression
// out replays a sample, and the sample replaces history rather than joining it.
func TestSandboxOverASampleDoesNotTouchTheRecordPlane(t *testing.T) {
	h := plane(t)

	e, rec := send(http.MethodPost, "/v1/aml/rules/test", map[string]any{
		"dsl": "Tx.Notional > 10000.0",
		"sample": []replay.Event{
			{Tx: types.Transaction{ID: "sample-1", Notional: 15000}},
			{Tx: types.Transaction{ID: "sample-2", Notional: 500}},
		},
	})
	if err := h.testRule()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var report replay.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if report.Events != 2 || report.Candidate.Alerts != 1 {
		t.Errorf("events=%d alerts=%d, want 2 and 1", report.Events, report.Candidate.Alerts)
	}
	// A synthetic sample has no window, which is how it is told apart from real
	// history in the report.
	if !report.From.IsZero() || !report.To.IsZero() {
		t.Errorf("a sample claims the window %s..%s", report.From, report.To)
	}
	if held := ledgerLen(t, h); held != 0 {
		t.Errorf("a sample replay wrote %d records", held)
	}
}

// TestSandboxCountsTheDispositionsThatWereRecorded: the false-positive proportion
// and the intelligence value the FCA asks for come from retained assessments,
// which is the only place a decision about an alert is written down.
func TestSandboxCountsTheDispositionsThatWereRecorded(t *testing.T) {
	h := plane(t)

	rec := ingest(t, h, map[string]any{"id": "tx-1", "user_id": "ivan-petrov-42", "notional": 15000, "currency": "USD"})
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest: status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		AlertIDs []string `json:"alert_ids"`
		CaseID   string   `json:"case_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.CaseID == "" || len(out.AlertIDs) != 1 {
		t.Fatalf("ingest did not open a case with one alert: %+v", out)
	}

	e, rec := send(http.MethodPost, "/v1/aml/cases/"+out.CaseID+"/resolve", map[string]any{
		"resolution": types.ResolutionFalsePositive,
		"considered": []string{"customer profile"},
		"rationale":  "expected for this customer",
		"by":         "mlro",
	})
	e.Request.SetPathValue("id", out.CaseID)
	if err := h.resolveCase()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("resolve: status %d: %s", rec.Code, rec.Body.String())
	}

	e, rec = send(http.MethodPost, "/v1/aml/rules/test", map[string]any{"dsl": "Tx.Notional > 10000.0"})
	if err := h.testRule()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("replay: status %d: %s", rec.Code, rec.Body.String())
	}

	var report replay.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if report.Candidate.Judged != 1 || report.Candidate.Unproductive != 1 {
		t.Fatalf("judged=%d unproductive=%d, want 1 and 1", report.Candidate.Judged, report.Candidate.Unproductive)
	}
	if report.Candidate.FalsePositive == nil || *report.Candidate.FalsePositive != 1 {
		t.Errorf("false positive proportion = %v, want 1", report.Candidate.FalsePositive)
	}
	if report.Candidate.Intelligence == nil || *report.Candidate.Intelligence != 0 {
		t.Errorf("intelligence value = %v, want 0", report.Candidate.Intelligence)
	}
}

// TestHistoryDoesNotSilentlySkipARecordItCannotOpen: a replay over a quietly
// shortened history is the same lie as a replay over an empty one.
func TestHistoryDoesNotSilentlySkipARecordItCannotOpen(t *testing.T) {
	h := plane(t)
	vault, err := h.vault(acme)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}

	// A record whose body was sealed for a different slot: the shape of a body
	// moved between records, and of one sealed under a rotated key.
	body, err := vault.Seal("transaction:somewhere-else", []byte(`{"tx":{"id":"tx-1"}}`))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	party, err := vault.Pseudonym(token.DomainSubject, "ivan-petrov-42")
	if err != nil {
		t.Fatalf("Pseudonym: %v", err)
	}
	if _, err := h.Records.Retain(retention.Record{
		Org: acme, Class: retention.ClassTransaction, Trigger: retention.TriggerOccasional,
		Ref: "tx-1", Parties: []string{party}, Occurred: time.Now().UTC(), Body: body,
	}); err != nil {
		t.Fatalf("Retain: %v", err)
	}

	if _, err := h.history(paid(), acme, vault); !errors.Is(err, token.ErrSealed) {
		t.Fatalf("err = %v, want ErrSealed", err)
	}

	e, rec := send(http.MethodPost, "/v1/aml/rules/test", map[string]any{"dsl": "Tx.Notional > 10000.0"})
	if err := h.testRule()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503 — an unreadable history is not an empty one", rec.Code)
	}
}

// TestHistoryStaysInsideTheOrg: a replay reads the caller's records and nobody
// else's, and another org's vault cannot open them either.
func TestHistoryStaysInsideTheOrg(t *testing.T) {
	h := plane(t)
	if rec := ingest(t, h, map[string]any{"user_id": "ivan-petrov-42", "notional": 15000}); rec.Code != http.StatusOK {
		t.Fatalf("ingest: status %d", rec.Code)
	}

	mine, err := h.vault(acme)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	theirs, err := h.vault("beta")
	if err != nil {
		t.Fatalf("vault: %v", err)
	}

	got, err := h.history(paid(), acme, mine)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("own history = %d events, want 1", len(got))
	}

	other, err := h.history(paid(), "beta", theirs)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("another org's history = %d events, want 0", len(other))
	}

	// And the record does not open under the other org's key even if it is reached.
	record := only(t, h, retention.ClassTransaction)
	if _, err := theirs.Open(slot(record.Class, record.Ref), record.Body); !errors.Is(err, token.ErrSealed) {
		t.Errorf("another org opened the record: err = %v", err)
	}
}

// TestKeyMaterialIsNotALiteral: the root comes from a source, and a source with
// nothing in it is a refusal. There is no default key anywhere in the plane.
func TestKeyMaterialIsNotALiteral(t *testing.T) {
	t.Setenv("AML_TOKEN_KEY", "")
	h := plane(t)
	h.Keys = token.NewKeyring(token.Env("AML_TOKEN_KEY"))

	if _, err := h.vault(acme); !errors.Is(err, token.ErrNoKey) {
		t.Fatalf("unset key: err = %v, want ErrNoKey", err)
	}
	if rec := ingest(t, h, map[string]any{"user_id": "ivan-petrov-42", "notional": 500}); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", rec.Code)
	}

	t.Setenv("AML_TOKEN_KEY", hex.EncodeToString(root))
	if _, err := h.vault(acme); err != nil {
		t.Fatalf("key present: %v", err)
	}
}

// testEngine builds an engine over providers a test can satisfy, and installs the
// rules through SetRules so the test exercises the same admission the daemon does.
// acme is the tenant every request in these tests is authenticated as: the org
// `acme` on the Hanzo brand, in the qualified form every store key is in.
const acme = "hanzo/acme"

func testEngine(rules []types.Rule) *engine.Engine {
	eng := engine.New(engine.Providers{Rate: reference.Rates{USDPer: map[string]float64{}}, Zone: time.UTC})
	if err := eng.SetRules(rules); err != nil {
		panic(err)
	}
	return eng
}

// paid is a hold on a budget wide enough for a test's read. The whole-history
// read demands one, because it is the one read that is worth paying for — see
// Handler.history and topology.Budget.
func paid() *topology.Grant {
	held, err := topology.NewBudget(1).Admit(context.Background(), 1)
	if err != nil {
		panic(err)
	}
	return held
}
