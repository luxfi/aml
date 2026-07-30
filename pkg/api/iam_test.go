// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/pkg/brand"
	"github.com/luxfi/aml/pkg/types"
)

// One RSA key per test binary: generating one is the slowest thing in this file
// and every test wants the same thing from it.
var key = sync.OnceValue(func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
})

// mint signs a token with kid "iam" and the given claims, defaulting the expiry to
// an hour out so a test only states the claim it is about.
func mint(t *testing.T, signer *rsa.PrivateKey, c jwt.MapClaims) string {
	t.Helper()
	if _, ok := c["exp"]; !ok {
		c["exp"] = time.Now().Add(time.Hour).Unix()
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	tok.Header["kid"] = "iam"
	raw, err := tok.SignedString(signer)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw
}

// published is the keyset every brand issuer publishes in these tests: ONE key,
// shared. That is what the in-cluster IAM does — it serves every brand's signing
// certificate from one JWKS — and it is the sharp version of the white-label
// claim: with the same key behind every issuer, the only thing that can refuse a
// Lux token on a Zoo host is the `iss` pin itself, not a signature that happens
// not to verify.
func published(issuer string) (Keyset, error) {
	return Keyset{"iam": &key().PublicKey}, nil
}

// request builds an event addressed to a specific Host, optionally bearing a token.
func request(method, target, host, token string) (*core.RequestEvent, *httptest.ResponseRecorder) {
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	e, rec := event(method, target, headers)
	e.Request.Host = host
	return e, rec
}

// bearing builds a plain request to a Host carrying a token, for the Identity
// under test on its own.
func bearing(host, token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/aml/cases", nil)
	r.Host = host
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

type config struct {
	Brand   string `json:"brand"`
	Display string `json:"display"`
	Issuer  string `json:"issuer"`
	Domain  string `json:"domain"`
}

// TestWhiteLabelByHost is the white-label claim itself: ONE handler, hit on four
// hosts, answers as four brands, and a token from one brand's issuer does not
// authenticate on another brand's host. Without the second half the first half is
// decoration — the surface would say "Zoo" while admitting Lux's tenants.
func TestWhiteLabelByHost(t *testing.T) {
	h := &Handler{Identity: IAMIdentity(published), Alerts: NewAlertStore()}

	for _, tc := range []config{
		{"lux", "Lux", "https://lux.id", "lux.network"},
		{"hanzo", "Hanzo", "https://hanzo.id", "hanzo.ai"},
		{"zoo", "Zoo", "https://zoolabs.id", "zoo.ngo"},
	} {
		for _, host := range []string{"api." + tc.Domain, tc.Domain} {
			e, rec := request(http.MethodGet, "/v1/aml/config", host, "")
			if err := h.brandConfig()(e); err != nil {
				t.Fatalf("%s: transport error: %v", host, err)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status %d, want 200", host, rec.Code)
			}
			var got config
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("%s: %v", host, err)
			}
			if got != tc {
				t.Errorf("%s: config = %+v, want %+v", host, got, tc)
			}
		}
	}

	// A console host under a brand's further domain is that brand, not the default.
	e, rec := request(http.MethodGet, "/v1/aml/config", "console.zoo.cloud", "")
	if err := h.brandConfig()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	var zoo config
	if err := json.Unmarshal(rec.Body.Bytes(), &zoo); err != nil {
		t.Fatal(err)
	}
	if zoo.Brand != "zoo" || zoo.Issuer != "https://zoolabs.id" {
		t.Errorf("console.zoo.cloud = %+v, want the zoo brand", zoo)
	}

	// An unrecognised Host falls back to the default brand rather than to no brand.
	e, rec = request(http.MethodGet, "/v1/aml/config", "aml.internal", "")
	if err := h.brandConfig()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	var fallback config
	if err := json.Unmarshal(rec.Body.Bytes(), &fallback); err != nil {
		t.Fatal(err)
	}
	if fallback.Brand != brand.Default || fallback.Issuer != brand.IssuerFor(brand.Default) {
		t.Errorf("unknown host = %+v, want the %s brand", fallback, brand.Default)
	}

	// A Lux-issued token: its own host authenticates it, every other brand's host
	// refuses it, and the signature is valid in both cases.
	lux := mint(t, key(), jwt.MapClaims{"iss": "https://lux.id", "owner": "lux-tenant", "sub": "u-1"})
	org, err := h.Identity(bearing("api.lux.network", lux))
	if err != nil {
		t.Fatalf("a Lux token on a Lux host was refused: %v", err)
	}
	if org != "lux-tenant" {
		t.Errorf("org = %q, want lux-tenant", org)
	}
	for _, host := range []string{"api.zoo.ngo", "console.zoo.cloud", "api.hanzo.ai", "aml.pars.network", "aml.internal"} {
		if org, err := h.Identity(bearing(host, lux)); err == nil {
			t.Errorf("a Lux token authenticated as %q on %s — brand tenancy is not enforced", org, host)
		}
	}

	// And the refusal reaches the route, not only the seam: the same token that
	// reads its own tenant's alerts on a Lux host gets 401 on a Zoo host.
	h.Alerts.Add("tx-1", []types.Alert{{ID: "lux-alert", OrgID: "lux-tenant"}})
	for _, tc := range []struct {
		host string
		code int
	}{{"api.lux.network", http.StatusOK}, {"api.zoo.ngo", http.StatusUnauthorized}} {
		e, rec := request(http.MethodGet, "/v1/aml/transactions/tx-1/alerts", tc.host, lux)
		e.Request.SetPathValue("id", "tx-1")
		if err := h.getAlerts()(e); err != nil {
			t.Fatalf("%s: transport error: %v", tc.host, err)
		}
		if rec.Code != tc.code {
			t.Errorf("%s: status %d, want %d", tc.host, rec.Code, tc.code)
		}
		if tc.code == http.StatusUnauthorized && contains(rec.Body.String(), "lux-alert") {
			t.Errorf("%s: a refused request received the tenant's data", tc.host)
		}
	}
}

// The tenant is the `owner` claim of a verified token — the same claim the gateway
// reads to set X-Org-Id, taken first-hand here instead of trusting the header.
func TestIAMIdentityYieldsTheOwnerAsTenant(t *testing.T) {
	id := IAMIdentity(published)
	tok := mint(t, key(), jwt.MapClaims{"iss": "https://hanzo.id", "owner": "acme-org", "sub": "u-1", "isAdmin": true})
	org, err := id(bearing("api.hanzo.ai", tok))
	if err != nil {
		t.Fatalf("refused a valid token: %v", err)
	}
	if org != "acme-org" {
		t.Fatalf("org = %q, want acme-org", org)
	}
}

// Every way a token can fail to establish a tenant, and each must refuse. These
// are one test because the property is one property: on doubt, no tenant.
func TestIAMIdentityRefusesEveryDoubt(t *testing.T) {
	stranger, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	id := IAMIdentity(published)
	hanzo := "https://hanzo.id"

	// A token signed with a key the issuer does not publish. Same claims, valid
	// shape, no provenance.
	forged := mint(t, stranger, jwt.MapClaims{"iss": hanzo, "owner": "acme-org"})

	// Symmetric signing over the public modulus: the algorithm-confusion attack,
	// which succeeds wherever the verifier takes the token's word for the family.
	confused := func() string {
		tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"iss": hanzo, "owner": "acme-org", "exp": time.Now().Add(time.Hour).Unix(),
		})
		tok.Header["kid"] = "iam"
		raw, err := tok.SignedString(key().PublicKey.N.Bytes())
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return raw
	}()

	// A token with no `alg` at all: "none" carries no signature to check.
	unsigned := func() string {
		tok := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
			"iss": hanzo, "owner": "acme-org", "exp": time.Now().Add(time.Hour).Unix(),
		})
		raw, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return raw
	}()

	for _, tc := range []struct {
		name  string
		host  string
		token string
	}{
		{"no token at all", "api.hanzo.ai", ""},
		{"another brand's issuer", "api.hanzo.ai", mint(t, key(), jwt.MapClaims{"iss": "https://lux.id", "owner": "acme-org"})},
		{"no issuer", "api.hanzo.ai", mint(t, key(), jwt.MapClaims{"owner": "acme-org"})},
		{"an issuer no brand claims", "api.hanzo.ai", mint(t, key(), jwt.MapClaims{"iss": "https://evil.example", "owner": "acme-org"})},
		{"the issuer as a prefix of ours", "api.hanzo.ai", mint(t, key(), jwt.MapClaims{"iss": "https://hanzo.id.evil.example", "owner": "acme-org"})},
		{"a signature from an unpublished key", "api.hanzo.ai", forged},
		{"a symmetric signature over the modulus", "api.hanzo.ai", confused},
		{"no signature", "api.hanzo.ai", unsigned},
		{"expired", "api.hanzo.ai", mint(t, key(), jwt.MapClaims{"iss": hanzo, "owner": "acme-org", "exp": time.Now().Add(-time.Hour).Unix()})},
		{"no expiry", "api.hanzo.ai", mint(t, key(), jwt.MapClaims{"iss": hanzo, "owner": "acme-org", "exp": nil})},
		{"not yet valid", "api.hanzo.ai", mint(t, key(), jwt.MapClaims{"iss": hanzo, "owner": "acme-org", "nbf": time.Now().Add(time.Hour).Unix()})},
		{"no owner", "api.hanzo.ai", mint(t, key(), jwt.MapClaims{"iss": hanzo, "sub": "u-1"})},
		{"an empty owner", "api.hanzo.ai", mint(t, key(), jwt.MapClaims{"iss": hanzo, "owner": ""})},
		{"a blank owner", "api.hanzo.ai", mint(t, key(), jwt.MapClaims{"iss": hanzo, "owner": "   "})},
		{"not a token", "api.hanzo.ai", "not.a.token"},
	} {
		if org, err := id(bearing(tc.host, tc.token)); err == nil {
			t.Errorf("%s authenticated as %q", tc.name, org)
		}
	}

	// A credential presented anywhere but the Authorization header is not a
	// credential here.
	valid := mint(t, key(), jwt.MapClaims{"iss": hanzo, "owner": "acme-org"})
	for _, r := range []*http.Request{
		func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/v1/aml/cases?access_token="+valid, nil)
			r.Host = "api.hanzo.ai"
			return r
		}(),
		func() *http.Request {
			r := bearing("api.hanzo.ai", "")
			r.Header.Set("Cookie", "token="+valid)
			return r
		}(),
		func() *http.Request {
			r := bearing("api.hanzo.ai", "")
			r.Header.Set("Authorization", "Basic "+valid)
			return r
		}(),
	} {
		if org, err := id(r); err == nil {
			t.Errorf("a token outside the bearer header authenticated as %q", org)
		}
	}
}

// With no way to reach an issuer's keys there is no way to check a signature, so
// there is no tenant. The same holds for an issuer that publishes an empty set.
func TestIAMIdentityRefusesWithoutKeys(t *testing.T) {
	tok := mint(t, key(), jwt.MapClaims{"iss": "https://hanzo.id", "owner": "acme-org"})
	for _, tc := range []struct {
		name string
		jwks Keysets
	}{
		{"no keyset source", nil},
		{"unreachable issuer", func(string) (Keyset, error) { return nil, fmt.Errorf("dial: refused") }},
		{"an empty keyset", func(string) (Keyset, error) { return Keyset{}, nil }},
	} {
		if org, err := IAMIdentity(tc.jwks)(bearing("api.hanzo.ai", tok)); err == nil {
			t.Errorf("%s authenticated as %q", tc.name, org)
		}
	}
}

// A token minted before a rotation still verifies when the issuer publishes both
// keys, and a `kid` naming a key the issuer never published does not on its own
// admit anything.
func TestRotationKeepsLiveTokensValid(t *testing.T) {
	next, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	both := func(string) (Keyset, error) {
		return Keyset{"iam": &key().PublicKey, "iam-next": &next.PublicKey}, nil
	}
	id := IAMIdentity(both)

	old := mint(t, key(), jwt.MapClaims{"iss": "https://hanzo.id", "owner": "acme-org"})
	if _, err := id(bearing("api.hanzo.ai", old)); err != nil {
		t.Errorf("a token from the previous key was refused during rotation: %v", err)
	}

	// A stale or absent label does not lock out a live token: with no matching kid
	// the signature is checked against every key the issuer publishes, and this one
	// holds under the new key.
	unlabelled := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://hanzo.id", "owner": "acme-org", "exp": time.Now().Add(time.Hour).Unix(),
	})
	unlabelled.Header["kid"] = "a-kid-nobody-published"
	raw, err := unlabelled.SignedString(next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := id(bearing("api.hanzo.ai", raw)); err != nil {
		t.Errorf("a token with an unknown kid but a published key's signature was refused: %v", err)
	}

	// Naming one published key while being signed by another is a key substitution.
	// The kid binds: that key must be the one that verifies it.
	substituted := mint(t, next, jwt.MapClaims{"iss": "https://hanzo.id", "owner": "acme-org"}) // kid "iam", signed by next
	if org, err := id(bearing("api.hanzo.ai", substituted)); err == nil {
		t.Errorf("a token labelled kid=iam but signed by another key authenticated as %q", org)
	}
}

// Two tenants of the same brand, each with a real token, must not see each other's
// alerts. The tenant is whatever the token says and nothing else says otherwise.
func TestTenantsDoNotBleedUnderIAMIdentity(t *testing.T) {
	alerts := NewAlertStore()
	alerts.Add("tx-1", []types.Alert{{ID: "first-alert", OrgID: "first"}})
	alerts.Add("tx-1", []types.Alert{{ID: "second-alert", OrgID: "second"}})
	h := &Handler{Alerts: alerts, Identity: IAMIdentity(published)}

	for _, tc := range []struct{ org, mine, theirs string }{
		{"first", "first-alert", "second-alert"},
		{"second", "second-alert", "first-alert"},
	} {
		tok := mint(t, key(), jwt.MapClaims{"iss": "https://hanzo.id", "owner": tc.org})
		e, rec := request(http.MethodGet, "/v1/aml/transactions/tx-1/alerts", "api.hanzo.ai", tok)
		e.Request.SetPathValue("id", "tx-1")
		if err := h.getAlerts()(e); err != nil {
			t.Fatalf("%s: transport error: %v", tc.org, err)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, want 200", tc.org, rec.Code)
		}
		body := rec.Body.String()
		if contains(body, tc.theirs) {
			t.Errorf("%s read another tenant's alert: %s", tc.org, body)
		}
		if !contains(body, tc.mine) {
			t.Errorf("%s did not read its own alert: %s", tc.org, body)
		}
	}
}

// jwksDoc is an issuer's published key set, as IAM serves it.
func jwksDoc(keys map[string]*rsa.PublicKey) string {
	var out []string
	for kid, k := range keys {
		out = append(out, fmt.Sprintf(`{"kty":"RSA","use":"sig","alg":"RS256","kid":%q,"n":%q,"e":%q}`,
			kid,
			base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
			base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes())))
	}
	return `{"keys":[` + strings.Join(out, ",") + `]}`
}

// JWKS reads a real key set off an issuer over HTTP, from the issuer's own path,
// and a token signed by the key it published verifies against it.
func TestJWKSReadsTheIssuersKeys(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path)
		mu.Unlock()
		fmt.Fprint(w, jwksDoc(map[string]*rsa.PublicKey{"iam": &key().PublicKey}))
	}))
	defer iam.Close()

	set, err := JWKS(time.Minute, time.Hour)(iam.URL)
	if err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 1 || asked[0] != jwksPath {
		t.Fatalf("asked %v, want one GET of %s", asked, jwksPath)
	}
	if got := set["iam"]; got == nil || got.N.Cmp(key().PublicKey.N) != 0 || got.E != key().PublicKey.E {
		t.Fatalf("decoded key does not match the published one: %+v", got)
	}

	// The decoded key is a verification key, not just a struct that parsed.
	tok := mint(t, key(), jwt.MapClaims{"iss": iam.URL, "owner": "acme-org"})
	parsed, err := jwt.ParseWithClaims(tok, &claims{}, set.verify, jwt.WithValidMethods(methods), jwt.WithIssuer(iam.URL))
	if err != nil || !parsed.Valid {
		t.Fatalf("a token signed by the published key did not verify: %v", err)
	}
}

// A keyset is held past its refresh while the issuer is unreachable — a blip must
// not refuse every caller — but only up to the staleness bound, past which the
// keys are not something this instance can still confirm and it refuses instead.
func TestJWKSHoldsStaleKeysOnlyWithinTheBound(t *testing.T) {
	var up atomic.Bool
	up.Store(true)
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, jwksDoc(map[string]*rsa.PublicKey{"iam": &key().PublicKey}))
	}))
	defer iam.Close()

	now := time.Now()
	c := &cache{
		ttl: time.Minute, stale: 10 * time.Minute,
		sets: map[string]issuerKeys{}, client: iam.Client(),
		now: func() time.Time { return now },
	}
	if _, err := c.get(iam.URL); err != nil {
		t.Fatalf("first fetch: %v", err)
	}

	up.Store(false)
	now = now.Add(5 * time.Minute) // past the refresh, inside the bound
	if _, err := c.get(iam.URL); err != nil {
		t.Fatalf("a stale keyset inside the bound was not served: %v", err)
	}
	now = now.Add(10 * time.Minute) // past the bound
	if _, err := c.get(iam.URL); err == nil {
		t.Fatal("keys the issuer has not confirmed for longer than the bound were still served")
	}

	// Once the issuer answers again, so does the cache.
	up.Store(true)
	if _, err := c.get(iam.URL); err != nil {
		t.Fatalf("recovery: %v", err)
	}
}

// What a published key must be to count as one. Each row is a key that cannot
// establish who is calling, so it must not be loaded at all: loading it would
// leave a verifier that accepts a signature it should not.
func TestKeysThatAreNotEvidenceAreNotLoaded(t *testing.T) {
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	good := base64.RawURLEncoding.EncodeToString(key().PublicKey.N.Bytes())

	for _, tc := range []struct{ name, doc string }{
		{"a modulus too short to be evidence", jwksDoc(map[string]*rsa.PublicKey{"weak": &weak.PublicKey})},
		{"an empty set", `{"keys":[]}`},
		{"not a key set", `{"keys":"nope"}`},
		{"an encryption key", `{"keys":[{"kty":"RSA","use":"enc","kid":"e","n":"` + good + `","e":"AQAB"}]}`},
		{"another key type", `{"keys":[{"kty":"oct","use":"sig","kid":"h","k":"c2VjcmV0"}]}`},
		{"a modulus that is not base64url", `{"keys":[{"kty":"RSA","use":"sig","kid":"x","n":"!!!","e":"AQAB"}]}`},
		{"an exponent too large to be one", `{"keys":[{"kty":"RSA","use":"sig","kid":"x","n":"` + good + `","e":"` + strings.Repeat("AQAB", 4) + `"}]}`},
	} {
		if set, err := decodeKeys([]byte(tc.doc)); err == nil {
			t.Errorf("%s was loaded as a signing key: %+v", tc.name, set)
		}
	}

	// Padded base64url is accepted: RFC 7517 omits the padding but implementations
	// send it, and refusing them would refuse a working issuer.
	padded := fmt.Sprintf(`{"keys":[{"kty":"RSA","use":"sig","kid":"iam","n":%q,"e":%q}]}`,
		base64.StdEncoding.EncodeToString(key().PublicKey.N.Bytes()),
		"AQAB=")
	if set, err := decodeKeys([]byte(strings.ReplaceAll(strings.ReplaceAll(padded, "+", "-"), "/", "_"))); err != nil || set["iam"] == nil {
		t.Errorf("a padded key set was refused: %v", err)
	}
}
