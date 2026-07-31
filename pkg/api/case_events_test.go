// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/luxfi/aml/pkg/types"
)

// TestCaseTimelineIsReadableAndTenantScoped covers the read that was owed: the
// events a case accumulates must be readable by the tenant the case belongs to,
// and by nobody else.
//
// The second half is the same property the rest of this package holds to. Two
// brands, one org name, one shared store: a case id seen on one brand must not
// open its timeline on the other, and the refusal must be indistinguishable from
// a case that does not exist.
func TestCaseTimelineIsReadableAndTenantScoped(t *testing.T) {
	h := shared(t)

	luxTok := rs256(t, jwt.MapClaims{"iss": "https://lux.id", "owner": "acme", "orgs": orgs("acme")})
	zooTok := rs256(t, jwt.MapClaims{"iss": "https://zoolabs.id", "owner": "acme", "orgs": orgs("acme")})

	// A transaction over the reporting threshold opens a case.
	rec := call(t, h.ingestTransaction(), http.MethodPost, "/v1/aml/transactions", "api.lux.network", luxTok,
		map[string]any{
			"id": "tx-tl", "user_id": "ivan-petrov-42", "account_id": "acct-9",
			"notional": 25000, "currency": "USD",
			"entity": map[string]any{"name": "Ivan Petrov"},
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest: status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		types.EvalResult
		Record string `json:"record"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.CaseID == "" {
		t.Fatal("ingest opened no case, so there is no timeline to read")
	}

	// A note, written the way an analyst writes one.
	rec = call(t, h.addCaseEvent(), http.MethodPost, "/v1/aml/cases/"+out.CaseID+"/events", "api.lux.network", luxTok,
		map[string]any{"kind": types.EventNote, "body": "customer contacted, awaiting source of funds"},
		"id", out.CaseID)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add event: status %d: %s", rec.Code, rec.Body.String())
	}

	// The owner reads it back.
	rec = call(t, h.caseEvents(), http.MethodGet, "/v1/aml/cases/"+out.CaseID+"/events", "api.lux.network", luxTok, nil,
		"id", out.CaseID)
	if rec.Code != http.StatusOK {
		t.Fatalf("read timeline: status %d: %s", rec.Code, rec.Body.String())
	}
	var events []types.CaseEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range events {
		if e.Kind == types.EventNote && e.Body == "customer contacted, awaiting source of funds" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the note written to this case is not in its timeline: %+v", events)
	}

	// The other brand's `acme`, holding the case id, gets the same answer as for a
	// case that was never opened.
	rec = call(t, h.caseEvents(), http.MethodGet, "/v1/aml/cases/"+out.CaseID+"/events", "api.zoo.ngo", zooTok, nil,
		"id", out.CaseID)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("another tenant read the timeline: status %d: %s", rec.Code, rec.Body.String())
	}

	rec = call(t, h.caseEvents(), http.MethodGet, "/v1/aml/cases/no-such-case/events", "api.lux.network", luxTok, nil,
		"id", "no-such-case")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("an unknown case answered %d, which distinguishes it from a foreign one", rec.Code)
	}
}
