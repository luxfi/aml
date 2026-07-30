// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/base/tools/router"

	"github.com/luxfi/aml/pkg/cases"
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

// aud is the clientId of the AML application these tests are the resource server
// for — the value IAM stamps as `aud` on a token minted for it.
const aud = "aml-lux"

// mint signs a token with kid "iam" and the given claims. What a real IAM token
// always carries is defaulted — an hour's expiry, this application's audience and
// tokenType "access-token" — so a row states only the claim it is about. A claim
// set to nil is omitted, which is how a row says "without this claim".
func mint(t *testing.T, signer any, method jwt.SigningMethod, c jwt.MapClaims) string {
	t.Helper()
	for k, v := range map[string]any{
		"exp":       time.Now().Add(time.Hour).Unix(),
		"aud":       aud,
		"tokenType": accessToken,
	} {
		if _, ok := c[k]; !ok {
			c[k] = v
		}
	}
	for k, v := range c {
		if v == nil {
			delete(c, k)
		}
	}
	tok := jwt.NewWithClaims(method, c)
	tok.Header["kid"] = "iam"
	raw, err := tok.SignedString(signer)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return raw
}

// rs256 signs with the shared RSA key, which is what every brand's issuer publishes
// in these tests.
func rs256(t *testing.T, c jwt.MapClaims) string {
	t.Helper()
	return mint(t, key(), jwt.SigningMethodRS256, c)
}

// orgs is the membership set claim, in the shape IAM emits it (schema.OrgRef).
func orgs(names ...string) []any {
	out := make([]any, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]any{"org": n, "role": "member"})
	}
	return out
}

// published is the keyset every brand issuer publishes in these tests: ONE key,
// shared. That is what the live IAM does — every brand's issuer serves the same
// set, and zoolabs.id's /v1/iam/.well-known/jwks carries cert-lux, cert-hanzo,
// cert-pars and cert-zoo together — so a Lux token's signature really does verify
// against the Zoo issuer's keys.
//
// It is therefore the sharp version of the white-label claim: with the same key
// behind every issuer, the only thing that can refuse a Lux token on a Zoo host is
// the `iss` pin itself, and not a signature that happens not to verify.
func published(issuer string) (Keyset, error) {
	return Keyset{"iam": &key().PublicKey}, nil
}

// identity is the Identity under test: this application's audience over the shared
// keyset.
func identity() Identity { return IAMIdentity(published, aud) }

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
	h := &Handler{Identity: identity(), Alerts: NewAlertStore()}

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

	// A Host no brand claims gets no brand and no issuer to go and get a token from.
	// Naming one here would name an issuer that the auth path on this same host must
	// refuse — the route would be telling the caller to do something that cannot
	// work — and would publish one brand's identity on a surface that is not its.
	for _, host := range []string{"aml.internal", "10.42.0.7:8090", "localhost:8090", ""} {
		e, rec := request(http.MethodGet, "/v1/aml/config", host, "")
		if err := h.brandConfig()(e); err != nil {
			t.Fatalf("%s: transport error: %v", host, err)
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("config on %q: status %d, want 404; body=%s", host, rec.Code, rec.Body.String())
		}
		if contains(rec.Body.String(), "hanzo") {
			t.Errorf("config on %q named a brand: %s", host, rec.Body.String())
		}
	}

	// A Lux-issued token: its own host authenticates it, every other brand's host
	// refuses it, and the signature is valid in both cases.
	lux := rs256(t, jwt.MapClaims{"iss": "https://lux.id", "owner": "acme", "sub": "u-1"})
	tenant, err := h.Identity(bearing("api.lux.network", lux))
	if err != nil {
		t.Fatalf("a Lux token on a Lux host was refused: %v", err)
	}
	if tenant != "lux/acme" {
		t.Errorf("tenant = %q, want lux/acme", tenant)
	}
	for _, host := range []string{"api.zoo.ngo", "console.zoo.cloud", "api.hanzo.ai", "aml.pars.network", "aml.internal"} {
		if tenant, err := h.Identity(bearing(host, lux)); err == nil {
			t.Errorf("a Lux token authenticated as %q on %s — brand tenancy is not enforced", tenant, host)
		}
	}

	// And the refusal reaches the route, not only the seam: the same token that
	// reads its own tenant's alerts on a Lux host gets 401 on a Zoo host.
	h.Alerts.Add("tx-1", []types.Alert{{ID: "lux-alert", OrgID: "lux/acme"}})
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

// The tenant is the brand of the Host qualifying the `owner` claim of a verified
// token — the same claim the gateway reads to set X-Org-Id, taken first-hand here
// instead of trusting the header, and qualified so that one org name under two
// brands is two tenants.
func TestIAMIdentityYieldsTheQualifiedTenant(t *testing.T) {
	id := identity()
	for _, tc := range []struct{ host, issuer, org, want string }{
		{"api.hanzo.ai", "https://hanzo.id", "acme", "hanzo/acme"},
		{"api.lux.network", "https://lux.id", "acme", "lux/acme"},
		{"api.zoo.ngo", "https://zoolabs.id", "acme", "zoo/acme"},
		{"console.zoo.cloud", "https://zoolabs.id", "acme", "zoo/acme"},
	} {
		tok := rs256(t, jwt.MapClaims{"iss": tc.issuer, "owner": tc.org, "sub": "u-1", "isAdmin": true})
		got, err := id(bearing(tc.host, tok))
		if err != nil {
			t.Fatalf("%s: refused a valid token: %v", tc.host, err)
		}
		if got != tc.want {
			t.Errorf("%s: tenant = %q, want %q", tc.host, got, tc.want)
		}
	}
}

// A Host no brand claims must refuse, whatever the token says.
//
// Every row here is a Host this process really is reached on: the pod IP that any
// in-cluster caller can dial, the Service DNS name, localhost through a
// port-forward, an absent Host, and lookalike domains under somebody else's
// registrable suffix. While the brand resolver defaulted, each of them was
// authenticated by the default brand's issuer — a hanzo.id token opened this
// service on all of them, and the white-label pin was inert everywhere the ingress
// had not put the brand's own name in the Host.
func TestIAMIdentityRefusesAnUnnamedHost(t *testing.T) {
	id := identity()
	// A token that is beyond reproach everywhere else: current, correctly audienced,
	// an access token, signed by the published key, from a first-party issuer.
	tok := rs256(t, jwt.MapClaims{"iss": "https://hanzo.id", "owner": "acme", "sub": "u-1"})

	for _, host := range []string{
		"10.42.0.7:8090",
		"aml.aml.svc.cluster.local",
		"aml.svc.cluster.local:8090",
		"localhost",
		"localhost:8090",
		"127.0.0.1:8090",
		"",
		"a.zoo.ngo.attacker.example",
		"hanzo.ai.attacker.example",
		"aml.internal",
	} {
		if tenant, err := id(bearing(host, tok)); err == nil {
			t.Errorf("a hanzo.id token authenticated as %q on %q", tenant, host)
		}
	}

	// The same token on the brand's own Host is accepted, so the rows above fail for
	// the Host and not because the token was bad.
	if _, err := id(bearing("api.hanzo.ai", tok)); err != nil {
		t.Fatalf("the same token on api.hanzo.ai was refused: %v", err)
	}
}

// The audience is this application, and the token is an access token.
//
// IAM stamps aud = the app's clientId, and every first-party token on an issuer is
// signed by the same keys and carries the same `iss`. The audience is the only
// thing that says which application a token was minted FOR, and RFC 9068 §4 makes
// checking it the resource server's own job — nobody else can. Without it a token
// issued to a marketing site, or to any other tenant's application on the same
// issuer, is a credential for a compliance record plane.
func TestAudienceIsThisApplication(t *testing.T) {
	id := identity()
	hanzo := "https://hanzo.id"
	base := func() jwt.MapClaims {
		return jwt.MapClaims{"iss": hanzo, "owner": "acme", "sub": "u-1"}
	}
	with := func(k string, v any) jwt.MapClaims {
		c := base()
		c[k] = v
		return c
	}

	for _, tc := range []struct{ name, token string }{
		{"a token for a marketing site", rs256(t, with("aud", "marketing-site"))},
		{"a token for another tenant's app on this issuer", rs256(t, with("aud", "acme-portal"))},
		{"a token with no audience at all", rs256(t, with("aud", nil))},
		{"an audience that merely contains ours", rs256(t, with("aud", aud+"-staging"))},
		{"an audience ours is a prefix of", rs256(t, with("aud", "x-"+aud))},
		{"an empty audience", rs256(t, with("aud", ""))},
		// IAM scopes a SHARED application's audience to the org (audienceFor:
		// clientId + "-org-" + org), so a shared application's token cannot satisfy a
		// pin on a dedicated application's clientId. Here the audience itself refuses
		// the deployment shape whose tenant claim would be wrong.
		{"a shared application's org-scoped audience", rs256(t, with("aud", aud+"-org-hanzo"))},

		// tokenType: an id_token is issued to a browser for the browser's own
		// consumption, carries the same iss and the same aud, and is not a credential
		// for an API. IAM says which kind it minted, so this is refusable and refused.
		{"an id_token", rs256(t, with("tokenType", "id-token"))},
		{"no tokenType at all", rs256(t, with("tokenType", nil))},
		{"an unrecognised tokenType", rs256(t, with("tokenType", "refresh-token"))},
		{"an empty tokenType", rs256(t, with("tokenType", ""))},
	} {
		if tenant, err := id(bearing("api.hanzo.ai", tc.token)); err == nil {
			t.Errorf("%s authenticated as %q", tc.name, tenant)
		}
	}

	// An audience list containing ours is accepted: `aud` is a set, and the question
	// RFC 9068 asks is whether this resource server is in it.
	if _, err := id(bearing("api.hanzo.ai", rs256(t, with("aud", []string{"other", aud})))); err != nil {
		t.Errorf("an audience list naming this application was refused: %v", err)
	}

	// With no audience configured nothing authenticates, including a token that is
	// otherwise perfect. An unpinned deployment would accept every token its issuer
	// ever minted, so it accepts none — and it says so without asking the issuer for
	// keys, because a deployment that cannot identify anybody must not spend a
	// network round trip per request finding that out. Refusing at the top is what
	// makes this independent of how the JWT library happens to treat an empty
	// expected audience.
	for _, unpinned := range []string{"", "   "} {
		var asked atomic.Int64
		counted := func(issuer string) (Keyset, error) {
			asked.Add(1)
			return published(issuer)
		}
		if tenant, err := IAMIdentity(counted, unpinned)(bearing("api.hanzo.ai", rs256(t, base()))); err == nil {
			t.Errorf("with audience %q configured, a token authenticated as %q", unpinned, tenant)
		}
		if n := asked.Load(); n != 0 {
			t.Errorf("with audience %q configured, a request cost %d issuer fetches, want 0", unpinned, n)
		}
	}
}

// The tenant is an org the caller actually belongs to.
//
// IAM's `owner` claim is the APP's org, not the user's (internal/oidc/jwt.go Sign:
// Owner: app.Organization). For the shape this engine supports — one dedicated
// application per financial institution — IAM refuses a login from outside the
// app's org (login.go: user.Owner != app.Organization && !app.IsShared &&
// app.OrgChoiceMode == "" → refused), so `owner` IS the caller's org and the two
// always agree.
//
// A SHARED or org-choice application breaks that: users of any org may authenticate
// to it, and every one of their tokens carries the APP owner's org as `owner`. Taken
// on faith that makes all of them one tenant, and one institution's analyst reads
// another's records. IAM also emits the caller's own membership set, so the claim is
// checkable rather than trusted: an owner the caller is not a member of refuses.
// It is the same predicate the fleet uses to authorize an org switch (X-Org-Id ∈
// orgs).
func TestTenantIsAnOrgTheCallerBelongsTo(t *testing.T) {
	id := identity()
	hanzo := "https://hanzo.id"

	for _, tc := range []struct {
		name  string
		token string
		want  string
	}{
		{
			"a member of the org the token acts for",
			rs256(t, jwt.MapClaims{"iss": hanzo, "owner": "acme", "orgs": orgs("acme")}),
			"hanzo/acme",
		},
		{
			// An analyst whose home org is not the institution but who is a member of
			// it. The home org is orgs[0] and is deliberately NOT what scopes the read:
			// the token says which institution it acts for, and the membership set says
			// whether it may.
			"a member whose home org is another",
			rs256(t, jwt.MapClaims{"iss": hanzo, "owner": "acme", "orgs": orgs("consultancy", "acme")}),
			"hanzo/acme",
		},
		{
			// client_credentials: no user, so no membership set — IAM passes nil orgs
			// and the claim is omitted. The org is the org of the application, and
			// which application it is has already been pinned by the audience.
			"a machine token with no membership set",
			rs256(t, jwt.MapClaims{"iss": hanzo, "owner": "acme", "sub": "acme/robot"}),
			"hanzo/acme",
		},
	} {
		got, err := id(bearing("api.hanzo.ai", tc.token))
		if err != nil {
			t.Errorf("%s was refused: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: tenant = %q, want %q", tc.name, got, tc.want)
		}
	}

	for _, tc := range []struct{ name, token string }{
		{
			// The shared-application case: the app is owned by `hanzo`, the caller is a
			// member of `zoo-tenant` alone, and IAM stamped the app's org as the owner.
			"a shared application's token, whose owner the caller is not in",
			rs256(t, jwt.MapClaims{"iss": hanzo, "owner": "hanzo", "orgs": orgs("zoo-tenant")}),
		},
		{
			"an owner that only looks like a member org",
			rs256(t, jwt.MapClaims{"iss": hanzo, "owner": "acme-holdings", "orgs": orgs("acme")}),
		},
		{
			"a membership set that names no org",
			rs256(t, jwt.MapClaims{"iss": hanzo, "owner": "acme", "orgs": []any{map[string]any{"role": "member"}}}),
		},
	} {
		if tenant, err := id(bearing("api.hanzo.ai", tc.token)); err == nil {
			t.Errorf("%s authenticated as %q", tc.name, tenant)
		}
	}
}

// Every way a token can fail to establish a tenant, and each must refuse. These
// are one test because the property is one property: on doubt, no tenant.
func TestIAMIdentityRefusesEveryDoubt(t *testing.T) {
	stranger, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	id := identity()
	hanzo := "https://hanzo.id"

	// A token signed with a key the issuer does not publish. Same claims, valid
	// shape, no provenance.
	forged := mint(t, stranger, jwt.SigningMethodRS256, jwt.MapClaims{"iss": hanzo, "owner": "acme"})

	// Symmetric signing over the public modulus: the algorithm-confusion attack,
	// which succeeds wherever the verifier takes the token's word for the family.
	confused := mint(t, key().PublicKey.N.Bytes(), jwt.SigningMethodHS256, jwt.MapClaims{"iss": hanzo, "owner": "acme"})

	// A token with no `alg` at all: "none" carries no signature to check.
	unsigned := mint(t, jwt.UnsafeAllowNoneSignatureType, jwt.SigningMethodNone, jwt.MapClaims{"iss": hanzo, "owner": "acme"})

	for _, tc := range []struct {
		name  string
		host  string
		token string
	}{
		{"no token at all", "api.hanzo.ai", ""},
		{"another brand's issuer", "api.hanzo.ai", rs256(t, jwt.MapClaims{"iss": "https://lux.id", "owner": "acme"})},
		{"no issuer", "api.hanzo.ai", rs256(t, jwt.MapClaims{"owner": "acme"})},
		{"an issuer no brand claims", "api.hanzo.ai", rs256(t, jwt.MapClaims{"iss": "https://evil.example", "owner": "acme"})},
		{"the issuer as a prefix of ours", "api.hanzo.ai", rs256(t, jwt.MapClaims{"iss": "https://hanzo.id.evil.example", "owner": "acme"})},
		{"a signature from an unpublished key", "api.hanzo.ai", forged},
		{"a symmetric signature over the modulus", "api.hanzo.ai", confused},
		{"no signature", "api.hanzo.ai", unsigned},
		{"expired", "api.hanzo.ai", rs256(t, jwt.MapClaims{"iss": hanzo, "owner": "acme", "exp": time.Now().Add(-time.Hour).Unix()})},
		{"no expiry", "api.hanzo.ai", rs256(t, jwt.MapClaims{"iss": hanzo, "owner": "acme", "exp": nil})},
		{"not yet valid", "api.hanzo.ai", rs256(t, jwt.MapClaims{"iss": hanzo, "owner": "acme", "nbf": time.Now().Add(time.Hour).Unix()})},
		{"no owner", "api.hanzo.ai", rs256(t, jwt.MapClaims{"iss": hanzo, "sub": "u-1"})},
		{"an empty owner", "api.hanzo.ai", rs256(t, jwt.MapClaims{"iss": hanzo, "owner": ""})},
		{"a blank owner", "api.hanzo.ai", rs256(t, jwt.MapClaims{"iss": hanzo, "owner": "   "})},
		{"an owner carrying the tenant separator", "api.hanzo.ai", rs256(t, jwt.MapClaims{"iss": hanzo, "owner": "zoo/acme"})},
		{"not a token", "api.hanzo.ai", "not.a.token"},
	} {
		if tenant, err := id(bearing(tc.host, tc.token)); err == nil {
			t.Errorf("%s authenticated as %q", tc.name, tenant)
		}
	}

	// A credential presented anywhere but the Authorization header is not a
	// credential here.
	valid := rs256(t, jwt.MapClaims{"iss": hanzo, "owner": "acme"})
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
		// Digest is exactly as long as Bearer, so this row fails only if the scheme
		// is actually compared. With "Basic" alone a verifier that skipped the
		// compare would still refuse — it would read one character into the token
		// and reject a mangled one — and the test would pass for the wrong reason.
		func() *http.Request {
			r := bearing("api.hanzo.ai", "")
			r.Header.Set("Authorization", "Digest "+valid)
			return r
		}(),
	} {
		if tenant, err := id(r); err == nil {
			t.Errorf("a token outside the bearer header authenticated as %q", tenant)
		}
	}
}

// A bearer that is not a token must not cost a request to the issuer.
//
// Resolving a keyset can be a network round trip. An unauthenticated caller who can
// make the auth path take one per request — by choosing a Host and putting any word
// at all in the Authorization header — has a lever on this process that costs them
// nothing. So the shape is checked first, and only something that could be a signed
// token is worth asking an issuer about.
func TestGarbageBearerCostsNoIssuerFetch(t *testing.T) {
	var asked atomic.Int64
	counted := func(issuer string) (Keyset, error) {
		asked.Add(1)
		return published(issuer)
	}
	id := IAMIdentity(counted, aud)

	for _, garbage := range []string{
		"x",
		"not-a-token",
		"a.b",
		"a.b.c.d",
		"..",
		"a..c",
		"!!!.e30.sig",                            // a header that is not base64url
		"e30.e30.sig",                            // a header that is {} — no alg
		"eyJhIjoxfQ.e30.sig",                     // JSON without an alg
		"YQ.e30.sig",                             // a header that is not JSON
		strings.Repeat("A", maxToken+1) + ".b.c", // longer than any token
	} {
		if tenant, err := id(bearing("api.hanzo.ai", garbage)); err == nil {
			t.Errorf("%q authenticated as %q", garbage, tenant)
		}
	}
	if n := asked.Load(); n != 0 {
		t.Errorf("garbage bearers cost %d issuer fetches, want 0", n)
	}

	// A well-formed token does reach the keys, so the filter is a filter and not a
	// second refusal quietly standing in for the parse.
	if _, err := id(bearing("api.hanzo.ai", rs256(t, jwt.MapClaims{"iss": "https://hanzo.id", "owner": "acme"}))); err != nil {
		t.Fatalf("a real token was refused: %v", err)
	}
	if n := asked.Load(); n != 1 {
		t.Errorf("a real token cost %d issuer fetches, want 1", n)
	}
}

// With no way to reach an issuer's keys there is no way to check a signature, so
// there is no tenant. The same holds for an issuer that publishes an empty set.
func TestIAMIdentityRefusesWithoutKeys(t *testing.T) {
	tok := rs256(t, jwt.MapClaims{"iss": "https://hanzo.id", "owner": "acme"})
	for _, tc := range []struct {
		name string
		jwks Keysets
	}{
		{"no keyset source", nil},
		{"unreachable issuer", func(string) (Keyset, error) { return nil, fmt.Errorf("dial: refused") }},
		{"an empty keyset", func(string) (Keyset, error) { return Keyset{}, nil }},
	} {
		if tenant, err := IAMIdentity(tc.jwks, aud)(bearing("api.hanzo.ai", tok)); err == nil {
			t.Errorf("%s authenticated as %q", tc.name, tenant)
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
	id := IAMIdentity(both, aud)

	old := rs256(t, jwt.MapClaims{"iss": "https://hanzo.id", "owner": "acme"})
	if _, err := id(bearing("api.hanzo.ai", old)); err != nil {
		t.Errorf("a token from the previous key was refused during rotation: %v", err)
	}

	// A stale or absent label does not lock out a live token: with no matching kid
	// the signature is checked against every key the issuer publishes, and this one
	// holds under the new key.
	unlabelled := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://hanzo.id", "owner": "acme", "aud": aud, "tokenType": accessToken,
		"exp": time.Now().Add(time.Hour).Unix(),
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
	substituted := mint(t, next, jwt.SigningMethodRS256, jwt.MapClaims{"iss": "https://hanzo.id", "owner": "acme"}) // kid "iam", signed by next
	if tenant, err := id(bearing("api.hanzo.ai", substituted)); err == nil {
		t.Errorf("a token labelled kid=iam but signed by another key authenticated as %q", tenant)
	}
}

// Two tenants of the same brand, each with a real token, must not see each other's
// alerts. The tenant is whatever the token says and nothing else says otherwise.
func TestTenantsDoNotBleedUnderIAMIdentity(t *testing.T) {
	alerts := NewAlertStore()
	alerts.Add("tx-1", []types.Alert{{ID: "first-alert", OrgID: "hanzo/first"}})
	alerts.Add("tx-1", []types.Alert{{ID: "second-alert", OrgID: "hanzo/second"}})
	h := &Handler{Alerts: alerts, Identity: identity()}

	for _, tc := range []struct{ org, mine, theirs string }{
		{"first", "first-alert", "second-alert"},
		{"second", "second-alert", "first-alert"},
	} {
		tok := rs256(t, jwt.MapClaims{"iss": "https://hanzo.id", "owner": tc.org})
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

// A route nobody registered protects nothing, however well it is written: the
// documentation said GET /v1/aml/config was live while Register did not mention it,
// so the only thing serving it was a test calling the handler directly.
//
// This drives the real router — Register, BuildMux, an HTTP request — so a route is
// only accounted for here if the daemon would actually serve it.
func TestEveryRouteIsServed(t *testing.T) {
	h := &Handler{
		Identity: identity(),
		Alerts:   NewAlertStore(),
		Cases:    cases.NewStore(),
		Engine:   testEngine(nil),
	}
	se := &core.ServeEvent{Router: router.NewRouter(func(w http.ResponseWriter, r *http.Request) (*core.RequestEvent, router.EventCleanupFunc) {
		e := new(core.RequestEvent)
		e.Request, e.Response = r, w
		return e, nil
	})}
	h.Register(se)
	mux, err := se.Router.BuildMux()
	if err != nil {
		t.Fatalf("BuildMux: %v", err)
	}

	// Unauthenticated, so a tenant-scoped route answers 401 — which is proof it is
	// served, since a route nobody registered answers 404.
	for _, tc := range []struct {
		method, path string
		code         int
	}{
		{http.MethodPost, "/v1/aml/transactions", http.StatusUnauthorized},
		{http.MethodGet, "/v1/aml/transactions/tx-1/alerts", http.StatusUnauthorized},
		{http.MethodGet, "/v1/aml/cases", http.StatusUnauthorized},
		{http.MethodPost, "/v1/aml/cases/c-1/events", http.StatusUnauthorized},
		{http.MethodPost, "/v1/aml/cases/c-1/resolve", http.StatusUnauthorized},
		{http.MethodPost, "/v1/aml/rules/test", http.StatusUnauthorized},
		{http.MethodPost, "/v1/aml/relationships", http.StatusUnauthorized},
		{http.MethodPost, "/v1/aml/relationships/r-1/close", http.StatusUnauthorized},
		{http.MethodPost, "/v1/aml/relationships/search", http.StatusUnauthorized},
		{http.MethodGet, "/v1/aml/anomaly", http.StatusUnauthorized},
		{http.MethodGet, "/v1/aml/sanctions/sources", http.StatusUnauthorized},
		{http.MethodGet, "/v1/aml/catalog", http.StatusUnauthorized},
		{http.MethodPost, "/v1/aml/anomaly/test", http.StatusUnauthorized},
		{http.MethodPost, "/v1/aml/sanctions/search", http.StatusUnauthorized},
		{http.MethodGet, "/v1/aml/rules", http.StatusUnauthorized},
		// Two routes answer an unauthenticated caller, and only these two. The brand
		// config is what a caller reads BEFORE it has a token — the issuer it names is
		// where the token comes from — and health is what a probe reads on a Host that
		// names no brand at all. They are also what proves a 401 above is the tenancy
		// check and not a route that answers 401 whatever happens.
		{http.MethodGet, "/v1/aml/config", http.StatusOK},
		{http.MethodGet, "/v1/aml/health", http.StatusServiceUnavailable},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
		req.Host = "api.hanzo.ai"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Errorf("%s %s is not served", tc.method, tc.path)
			continue
		}
		if rec.Code != tc.code {
			t.Errorf("%s %s: status %d, want %d; body=%s", tc.method, tc.path, rec.Code, tc.code, rec.Body.String())
		}
	}

	// The config route serves the brand of the Host it was asked on, through the
	// router rather than by calling the handler.
	req := httptest.NewRequest(http.MethodGet, "/v1/aml/config", nil)
	req.Host = "api.lux.network"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var got config
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("config body %q: %v", rec.Body.String(), err)
	}
	if got.Brand != "lux" || got.Issuer != "https://lux.id" {
		t.Errorf("config on api.lux.network = %+v, want the lux brand", got)
	}
}
