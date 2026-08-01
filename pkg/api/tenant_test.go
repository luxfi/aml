// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/pkg/cases"
	"github.com/luxfi/aml/pkg/reference"
	"github.com/luxfi/aml/pkg/retention"
	"github.com/luxfi/aml/pkg/token"
	"github.com/luxfi/aml/pkg/types"
)

// This file holds the cross-brand tenancy property, which is the sharpest one in
// the engine: two financial institutions that happen to have the same org name on
// two brands are two tenants, in the store index AND in the cryptographic vault.
//
// The reproduction it closes is one shared record plane. Every store here — the
// ledger, the case store, the alert store and the tokenisation keyring, whose root
// secret is ONE secret for the deployment — is shared between the two brands, which
// is the arrangement in which nothing but the key itself separates them. A
// deployment per brand does not have to rely on this; it is what makes the reliance
// unnecessary.

// shared is one record plane, reachable on every brand's Host, authenticating
// through real tokens.
func shared(t *testing.T) *Handler {
	t.Helper()
	return &Handler{
		Identity: identity(),
		Engine: testEngine([]types.Rule{{
			ID: "ctr", Name: "CTR Threshold", DSL: "Tx.Notional > 10000.0",
			Severity: types.SeverityHigh, Weight: 0.3, Action: types.ActionReport, Enabled: true,
		}}),
		Rate:    reference.Rates{},
		Cases:   cases.NewStore(),
		Alerts:  NewAlertStore(),
		Records: retention.New(),
		Keys:    token.NewKeyring(func(string) ([]byte, error) { return root, nil }),
	}
}

// call drives a handler as a caller on one brand's Host holding that brand's token.
func call(t *testing.T, handle func(*core.RequestEvent) error, method, path, host, tok string, body any, pathValues ...string) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Host = host
	req.Header.Set("Authorization", "Bearer "+tok)
	for i := 0; i+1 < len(pathValues); i += 2 {
		req.SetPathValue(pathValues[i], pathValues[i+1])
	}
	rec := httptest.NewRecorder()
	e := &core.RequestEvent{}
	e.Request, e.Response = req, rec
	if err := handle(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	return rec
}

// TestOneOrgNameUnderTwoBrandsIsTwoTenants is RED-1's reproduction as an acceptance
// test: `acme` on Lux and `acme` on Zoo, one record plane, one root secret.
//
// The two halves are the two halves of the fix. The vault half is the one that
// cannot be filtered afterwards: the org was the HKDF salt, so with the bare org
// both brands derived the SAME keys — the same customer name tokenised to the same
// pseudonym, and one brand's sealed record opened under the other's tenant. That is
// a plaintext cross-brand disclosure of retained personal data, not a naming
// collision. The index half is what keeps every route's scope honest once the keys
// differ.
func TestOneOrgNameUnderTwoBrandsIsTwoTenants(t *testing.T) {
	h := shared(t)

	// Two real tokens, each from its own brand's issuer, naming the SAME org.
	luxTok := rs256(t, jwt.MapClaims{"iss": "https://lux.id", "owner": "acme", "orgs": orgs("acme")})
	zooTok := rs256(t, jwt.MapClaims{"iss": "https://zoolabs.id", "owner": "acme", "orgs": orgs("acme")})

	luxWho, err := h.Identity(bearing("api.lux.network", luxTok))
	lux := luxWho.Tenant
	if err != nil {
		t.Fatalf("lux: %v", err)
	}
	zooWho, err := h.Identity(bearing("api.zoo.ngo", zooTok))
	zoo := zooWho.Tenant
	if err != nil {
		t.Fatalf("zoo: %v", err)
	}
	if lux == zoo {
		t.Fatalf("both brands' `acme` resolved to one tenant %q: every store below shares a key space, and so does the vault", lux)
	}

	// (a) The vault. Same deployment secret, same org name, same value — and the
	// pseudonym must not be the same, or a join across the two brands is computable
	// from data either one can see.
	luxVault, err := h.vault(lux)
	if err != nil {
		t.Fatal(err)
	}
	zooVault, err := h.vault(zoo)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []token.Domain{token.DomainName, token.DomainSubject, token.DomainAccount, token.DomainWallet} {
		a, err := luxVault.Pseudonym(d, "Ivan Petrov")
		if err != nil {
			t.Fatal(err)
		}
		b, err := zooVault.Pseudonym(d, "Ivan Petrov")
		if err != nil {
			t.Fatal(err)
		}
		if a == b {
			t.Errorf("%s: one name under two brands tokenised to the same pseudonym %q — the vaults share a key", d, a)
		}
	}

	// And the seal: a record sealed for one brand's tenant must not open under the
	// other's. This is the read that was in the clear.
	sealed, err := luxVault.Seal("transaction:tx-1", []byte(`{"party":"Ivan Petrov","usd":25000}`))
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := zooVault.Open("transaction:tx-1", sealed); err == nil {
		t.Errorf("one brand's sealed record opened under another brand's tenant: %s", plain)
	}

	// (b) The stores. Lux ingests a transaction that alerts, which writes an alert,
	// a case and a retained record; Zoo then asks for every one of them by its exact
	// id, which is what a caller who has seen an id can do.
	body := map[string]any{
		"id": "tx-1", "user_id": "ivan-petrov-42", "account_id": "acct-9",
		"notional": 25000, "currency": "USD",
		"entity": map[string]any{"name": "Ivan Petrov"},
	}
	rec := call(t, h.ingestTransaction(), http.MethodPost, "/v1/aml/transactions", "api.lux.network", luxTok, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("lux ingest: status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		types.EvalResult
		Record string `json:"record"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Record == "" || out.CaseID == "" || len(out.AlertIDs) == 0 {
		t.Fatalf("lux ingest produced no record, case or alert: %+v", out)
	}

	// A relationship, so the Art. 78 lookback has something to find.
	rec = call(t, h.openRelationship(), http.MethodPost, "/v1/aml/relationships", "api.lux.network", luxTok,
		map[string]any{"ref": "rel-1", "nature": "correspondent banking", "name": "Ivan Petrov", "user_id": "ivan-petrov-42"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("lux relationship: status %d: %s", rec.Code, rec.Body.String())
	}

	// Alerts by transaction id.
	rec = call(t, h.getAlerts(), http.MethodGet, "/v1/aml/transactions/tx-1/alerts", "api.zoo.ngo", zooTok, nil, "id", "tx-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("zoo alerts: status %d", rec.Code)
	}
	if body := rec.Body.String(); body != "[]\n" && contains(body, out.AlertIDs[0]) {
		t.Errorf("zoo read lux's alerts: %s", body)
	}

	// Cases, both by listing and by the case's exact id.
	rec = call(t, h.listCases(), http.MethodGet, "/v1/aml/cases", "api.zoo.ngo", zooTok, nil)
	if contains(rec.Body.String(), out.CaseID) {
		t.Errorf("zoo listed lux's case: %s", rec.Body.String())
	}
	rec = call(t, h.resolveCase(), http.MethodPost, "/v1/aml/cases/"+out.CaseID+"/resolve", "api.zoo.ngo", zooTok,
		map[string]any{"resolution": "dismissed", "by": "zoo-analyst"}, "id", out.CaseID)
	if rec.Code != http.StatusNotFound {
		t.Errorf("zoo resolving lux's case: status %d, want 404: %s", rec.Code, rec.Body.String())
	}
	rec = call(t, h.addCaseEvent(), http.MethodPost, "/v1/aml/cases/"+out.CaseID+"/events", "api.zoo.ngo", zooTok,
		map[string]any{"note": "seen"}, "id", out.CaseID)
	if rec.Code != http.StatusNotFound {
		t.Errorf("zoo writing to lux's case: status %d, want 404: %s", rec.Code, rec.Body.String())
	}

	// Retained records, by the record id the ingest returned.
	if _, err := h.Records.Get(retention.PurposeInvestigation, zoo, out.Record); err == nil {
		t.Error("zoo read lux's retained record by id")
	}
	held := 0
	if err := h.Records.Each(retention.PurposeRetention, zoo, "", func(retention.Record) error {
		held++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if held != 0 {
		t.Errorf("zoo holds %d of lux's records", held)
	}

	// The Art. 78 lookback, on the same person's name. This is the query that would
	// have crossed brands through the vault rather than through the index: with one
	// salt, Zoo tokenising "Ivan Petrov" produced the pseudonym Lux's records are
	// indexed under.
	rec = call(t, h.searchRelationships(), http.MethodPost, "/v1/aml/relationships/search", "api.zoo.ngo", zooTok,
		map[string]any{"party": "Ivan Petrov"})
	if rec.Code != http.StatusOK {
		t.Fatalf("zoo lookback: status %d: %s", rec.Code, rec.Body.String())
	}
	var answer retention.Answer
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Maintained || answer.Examined != 0 || len(answer.Records) > 0 {
		t.Errorf("zoo's lookback found lux's relationship: %+v", answer)
	}

	// The positive control. Every read above must succeed for the tenant that owns
	// the data, or the test would pass on a plane where nothing works at all.
	rec = call(t, h.getAlerts(), http.MethodGet, "/v1/aml/transactions/tx-1/alerts", "api.lux.network", luxTok, nil, "id", "tx-1")
	if !contains(rec.Body.String(), out.AlertIDs[0]) {
		t.Errorf("lux cannot read its own alert: %s", rec.Body.String())
	}
	rec = call(t, h.listCases(), http.MethodGet, "/v1/aml/cases", "api.lux.network", luxTok, nil)
	if !contains(rec.Body.String(), out.CaseID) {
		t.Errorf("lux cannot list its own case: %s", rec.Body.String())
	}
	if _, err := h.Records.Get(retention.PurposeInvestigation, lux, out.Record); err != nil {
		t.Errorf("lux cannot read its own record: %v", err)
	}
	rec = call(t, h.searchRelationships(), http.MethodPost, "/v1/aml/relationships/search", "api.lux.network", luxTok,
		map[string]any{"party": "Ivan Petrov"})
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if !answer.Maintained || len(answer.Natures) == 0 {
		t.Errorf("lux's own lookback found nothing: %+v", answer)
	}
}

// The two Identity implementations must produce the same tenant key for the same
// caller, or the tenancy depends on which one a deployment wired: a plane that ran
// behind a gateway and then moved to verifying tokens itself would look for its
// records under a different key, and would find none. A record plane that cannot
// find yesterday's records is a retention failure (AMLR Art. 77), not an
// inconvenience.
func TestBothIdentitiesAgreeOnTheTenantKey(t *testing.T) {
	iam := identity()
	proxy := TrustedProxyHeader("X-Org-Id", "X-User-Id")

	for _, tc := range []struct{ host, issuer, org string }{
		{"api.hanzo.ai", "https://hanzo.id", "acme"},
		{"api.lux.network", "https://lux.id", "acme"},
		{"api.zoo.ngo", "https://zoolabs.id", "sanctions-desk"},
		{"aml.pars.network", "https://pars.id", "acme"},
	} {
		tok := rs256(t, jwt.MapClaims{"iss": tc.issuer, "owner": tc.org})
		fromToken, err := iam(bearing(tc.host, tok))
		if err != nil {
			t.Fatalf("%s: %v", tc.host, err)
		}

		r := httptest.NewRequest(http.MethodGet, "/v1/aml/cases", nil)
		r.Host = tc.host
		r.Header.Set("X-Org-Id", tc.org)
		fromHeader, err := proxy(r)
		if err != nil {
			t.Fatalf("%s: %v", tc.host, err)
		}

		if fromToken != fromHeader {
			t.Errorf("%s: the token yields tenant %q and the gateway header yields %q", tc.host, fromToken, fromHeader)
		}
	}
}

// A tenant key is a value with one spelling. Two spellings of one tenant are two
// vaults and two sets of rows, so the canonical form is the whole point.
func TestQualifyIsCanonical(t *testing.T) {
	for _, tc := range []struct{ brand, org, want string }{
		{"lux", "acme", "lux/acme"},
		{"LUX", "acme", "lux/acme"},   // the brand id is canonicalised
		{"lux", " acme ", "lux/acme"}, // the org is trimmed
		{"zoo", "Acme", "zoo/Acme"},   // but not case-folded: two orgs may differ by case
	} {
		got, err := qualify(tc.brand, tc.org)
		if err != nil {
			t.Errorf("qualify(%q, %q): %v", tc.brand, tc.org, err)
			continue
		}
		if got != tc.want {
			t.Errorf("qualify(%q, %q) = %q, want %q", tc.brand, tc.org, got, tc.want)
		}
		if !qualified(got) {
			t.Errorf("qualify produced %q, which qualified() rejects", got)
		}
	}

	for _, tc := range []struct{ name, brand, org string }{
		{"no brand", "", "acme"},
		{"a brand no registry row claims", "nobody", "acme"},
		{"a brand that is a domain", "hanzo.ai", "acme"},
		{"no org", "lux", ""},
		{"a blank org", "lux", "   "},
		{"an org carrying the separator", "lux", "zoo/acme"},
		{"an org that is only a separator", "lux", "/"},
	} {
		if got, err := qualify(tc.brand, tc.org); err == nil {
			t.Errorf("%s produced tenant %q", tc.name, got)
		}
	}

	// Distinctness is the property, stated over the whole registry: no two (brand,
	// org) pairs share a key. It holds because the brand ids contain no separator and
	// the org half may not either — which is what makes the first separator the
	// boundary, and the mapping injective.
	seen := map[string]string{}
	for _, b := range []string{"hanzo", "lux", "zoo", "pars", "bootnode"} {
		for _, org := range []string{"acme", "acme-ltd", "a", "sanctions-desk"} {
			k, err := qualify(b, org)
			if err != nil {
				t.Fatalf("qualify(%q, %q): %v", b, org, err)
			}
			if other, dup := seen[k]; dup {
				t.Errorf("%s/%s and %s share the tenant key %q", b, org, other, k)
			}
			seen[k] = b + "+" + org
		}
	}
	if len(seen) != 20 {
		t.Errorf("20 distinct tenants produced %d keys", len(seen))
	}
}

// The vault is derived from the whole tenant key, so the brand is inside the HKDF
// salt and not merely alongside it in an index. This is the unit-level statement of
// the same property: a keyring asked for two tenants that differ ONLY by brand must
// hand back vaults with nothing in common.
func TestTheVaultIsKeyedByTheWholeTenant(t *testing.T) {
	keys := token.NewKeyring(func(string) ([]byte, error) { return root, nil })

	got := map[string]string{}
	for _, tenant := range []string{"lux/acme", "zoo/acme", "hanzo/acme", "pars/acme"} {
		v, err := keys.Org(tenant)
		if err != nil {
			t.Fatalf("%s: %v", tenant, err)
		}
		p, err := v.Pseudonym(token.DomainName, "Ivan Petrov")
		if err != nil {
			t.Fatal(err)
		}
		if other, dup := got[p]; dup {
			t.Errorf("%s and %s derive the same pseudonym: the salt does not carry the brand", tenant, other)
		}
		got[p] = tenant

		// A vault for the same tenant is the same vault: determinism within a tenant is
		// what makes a retained record findable by party at all (AMLR Art. 78).
		again, err := keys.Org(tenant)
		if err != nil {
			t.Fatal(err)
		}
		same, err := again.Pseudonym(token.DomainName, "Ivan Petrov")
		if err != nil {
			t.Fatal(err)
		}
		if same != p {
			t.Errorf("%s: the same tenant and name produced two pseudonyms", tenant)
		}
	}
}

// A sealed record carries its tenant in its authenticated data, so moving one
// between tenants fails to open rather than opening someone else's record. Sealing
// under a bare org would have made this a no-op across brands.
func TestASealedRecordDoesNotTravelBetweenTenants(t *testing.T) {
	keys := token.NewKeyring(func(string) ([]byte, error) { return root, nil })
	slot := "relationship:rel-1"
	plain := []byte(`{"ref":"rel-1","name":"Ivan Petrov"}`)

	mine, err := keys.Org("lux/acme")
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := mine.Seal(slot, plain)
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := mine.Open(slot, sealed); err != nil || !bytes.Equal(opened, plain) {
		t.Fatalf("a record did not open under its own tenant: %v", err)
	}
	for _, other := range []string{"zoo/acme", "hanzo/acme", "lux/acme-ltd"} {
		v, err := keys.Org(other)
		if err != nil {
			t.Fatal(err)
		}
		if opened, err := v.Open(slot, sealed); err == nil {
			t.Errorf("%s opened lux/acme's record: %s", other, opened)
		}
	}
	// And not into another slot within the same tenant either.
	if _, err := mine.Open("transaction:rel-1", sealed); err == nil {
		t.Error("a sealed record opened in another slot")
	}
}
