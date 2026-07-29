package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/luxfi/aml/pkg/retention"
)

// A customer identifier must never be screened as a name.
//
// The defect this guards was the most expensive kind: ingest defaulted the
// customer's name from its identifier, so "cust-1" was screened against the
// designation lists, scored 0.902 against "BRANCH 1 OF THE SHIRAZ REVOLUTIONARY
// COU" — above the reporting threshold — and an ordinary $100 payment came back
// blocked. An opaque handle cannot produce a true positive against a list of
// people's names, so every match it produces is false.
//
// With no name supplied, the customer-screening rule's own guard holds and the
// rule does not fire. That is the honest outcome: a customer whose name this
// service has never been given has not been screened.
func TestIdentifierIsNotScreenedAsAName(t *testing.T) {
	h := plane(t)

	rec := ingest(t, h, map[string]any{
		"id": "tx-id-only", "user_id": "cust-1", "account_id": "acct-1",
		"notional": 100, "currency": "USD",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// The retained record must not carry a party derived from the identifier as
	// though it were a name — indexing one invites the same mistake on the read
	// side, where the lookback would report a name that was never given.
	var out struct {
		Record string `json:"record"`
		Action string `json:"action"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if out.Action == "block" {
		t.Fatalf("an ordinary payment for a customer with no name on file was blocked")
	}
}

// When the caller does supply a name, it is used. Refusing to invent a name must
// not become a refusal to screen one that was given.
func TestSuppliedNameIsScreened(t *testing.T) {
	h := plane(t)

	rec := ingest(t, h, map[string]any{
		"id": "tx-named", "user_id": "cust-2", "account_id": "acct-2",
		"notional": 100, "currency": "USD",
		"entity": map[string]any{"id": "cust-2", "name": "Ordinary Customer"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var out struct {
		Record string `json:"record"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Record == "" {
		t.Fatal("ingest answered without naming the record it retained")
	}

	// The name reaches the record plane, which is what makes the customer findable
	// on a five-year lookback by name rather than only by key.
	record, err := h.Records.Get(retention.PurposeInvestigation, acme, out.Record)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(record.Parties) < 3 {
		t.Errorf("parties = %v; a supplied name should be indexed alongside the subject and account", record.Parties)
	}
}
