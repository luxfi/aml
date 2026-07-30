// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

// The IAM Identity: the tenant of a request is the brand of the request's own
// Host, qualifying the org named by an access token that this AML application
// obtained from that brand's issuer.
//
// This is the second implementation of the Identity seam and the one a directly
// reachable deployment must use. TrustedProxyHeader is sound only where an
// authenticating proxy is the sole route in and strips the client's copy of the
// header; where that does not hold, the header is an unauthenticated assertion and
// any caller can name any tenant. Here the org arrives inside a signature.
//
// Four things have to hold before a token names a tenant, and they are four
// because each one alone is satisfied by a token that must not be served:
//
//   - The signature verifies under a key the brand of THIS Host publishes. Which
//     brand is decided by the Host, so a Lux-issued token presented on a Zoo host
//     is refused even though both issuers are first-party and, in-cluster, may
//     publish their signing certificates in one JWKS. A Host no brand claims names
//     no issuer, and refuses: this is the auth path, and it is the one caller that
//     must not be handed a default.
//   - The audience is THIS application. IAM stamps aud = the app's clientId
//     (internal/oidc/jwt.go, audienceFor), so with no pin any first-party token
//     from the same issuer authenticates here — one minted for a marketing site,
//     for another tenant's app, for anything at all on hanzo.id. RFC 9068 §4 makes
//     this the resource server's own check, and nobody else can make it.
//
//     What the aud pin does NOT establish is that the token came through this
//     app's own login gate. IAM's RFC 8693 token-exchange path
//     (internal/oidc/token_exchange.go) takes aud from the caller's requested
//     resource and can stamp a chosen owner, so a token bearing this app's aud
//     need not have transited login.go. That path is not attacker-reachable — it
//     is gated by IAM_KEY_MINT_ALLOWED_APPS, an exact-clientId allowlist that
//     fails closed when empty and additionally requires reserved-org ownership —
//     so the pin's guarantee rests on that env value being audited for the brand
//     serving AML. This is a deploy-time trust delegation to IAM, named here so it
//     is a stated dependency rather than an implied one.
//   - It is an access token. IAM stamps tokenType, so an id_token — issued to a
//     browser for the browser's own consumption, not a credential for an API, and
//     carrying the same iss and the same aud — is refusable, and is refused.
//   - The org it names is one the caller belongs to. IAM's `owner` claim is the
//     APP's org (jwt.go Sign: Owner: app.Organization), and its `orgs` claim is the
//     caller's own membership set. For the deployment shape this engine supports —
//     one IAM application per financial institution, neither shared nor org-choice
//     — IAM itself refuses a login from outside the app's org (internal/oidc/
//     login.go), so the two always agree. Where they do not agree the application
//     is shared, and every one of its users would otherwise be handed the app
//     owner's tenant.
//
// A record plane is stricter here than the fleet edge (hanzoai/cloud
// auth_identity.go), which trusts the union of brand issuers because one binary
// serves every brand's apps. This answers for one brand at a time, so it pins.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/luxfi/aml/pkg/brand"
)

// skew is the clock difference tolerated on exp and nbf. It matches the fleet
// edge validator, so a token that is live at the gateway is live here and a
// caller cannot be authenticated by one layer and refused by the other.
const skew = 2 * time.Minute

// accessToken is the tokenType IAM stamps on a token that authorises API calls.
// Its sibling is "id-token"; anything else is refused rather than assumed, because
// the claim exists precisely so that a bearer of one kind cannot be spent as
// another.
const accessToken = "access-token"

// claims is the part of an IAM access token this engine reads.
type claims struct {
	jwt.RegisteredClaims
	// Owner is the org the token acts for — IAM's tenant claim, which every layer
	// that scopes data reads (the gateway sets X-Org-Id from it, which is what
	// TrustedProxyHeader consumes second-hand).
	Owner string `json:"owner"`
	// TokenType distinguishes an access token from an id_token.
	TokenType string `json:"tokenType"`
	// Orgs is the caller's membership set, home org first — present on every token
	// minted for a user, absent on a machine token (IAM store.MemberOrgRefs always
	// emits the user's home org; client_credentials passes nil, so the claim is
	// omitted entirely). It is what makes `owner` checkable rather than trusted.
	Orgs []orgRef `json:"orgs"`
}

// orgRef is one membership in the shape IAM emits it (schema.OrgRef).
type orgRef struct {
	Org string `json:"org"`
}

// member reports whether the caller's membership set names org.
//
// A token with no membership set is a machine token: it authenticated as an
// application rather than as a person, so the org it acts for is the org of that
// application, and which application it is has already been pinned by the
// audience. There is nothing further to check and nothing to fall back to.
func (c *claims) member(org string) bool {
	if len(c.Orgs) == 0 {
		return true
	}
	for _, o := range c.Orgs {
		if strings.TrimSpace(o.Org) == org {
			return true
		}
	}
	return false
}

// IAMIdentity resolves the tenant from an access token this application obtained
// from the issuer of the brand that owns the request's Host.
//
// audience is this AML application's IAM clientId — the value IAM stamps as `aud`.
// It is required, and an instance without it authenticates nobody: with no audience
// there is no pin, and every token that issuer ever minted for any application
// would be a credential here. One deployment serves one brand's institutions and
// therefore has one application, so this is one value.
//
// It refuses on every uncertainty: no token, a Host no brand claims, a token from
// another brand's issuer, a token for another application, an id_token, a signature
// no published key verifies, a missing or passed expiry, a keyset that cannot be
// reached, a token naming no owner, or an owner the caller is not a member of. A
// compliance control that authenticates when it is unsure is worse than one that
// will not serve, because it answers with another tenant's records and looks
// healthy doing it.
func IAMIdentity(jwks Keysets, audience string) Identity {
	return func(r *http.Request) (string, error) {
		if jwks == nil {
			return "", errors.New("no keyset source configured, so no token can be verified")
		}
		if strings.TrimSpace(audience) == "" {
			return "", errors.New("no audience configured, so any application's token would be a credential here")
		}

		// The Host, not a forwarding header: an intermediary's claim about the
		// original host is a claim, and this decides which issuer is trusted.
		//
		// A Host no brand claims refuses. Requests reach this process on a pod IP, an
		// in-cluster service name, localhost through a port-forward, and vhosts that
		// were misrouted; on each of those the caller named no brand, so no issuer's
		// signature means anything. Defaulting here is what made one brand's issuer
		// the authenticator of record for all of them.
		id, ok := brand.ForHostOK(r.Host)
		if !ok {
			return "", fmt.Errorf("host %q names no brand, so nothing is trusted to have signed this", r.Host)
		}
		b, _ := brand.For(id)
		// An empty expectation does not loosen the issuer check, it removes it:
		// golang-jwt validates `iss` only when one is stated (validator.go, `if
		// v.expectedIss != ""`). A brand row without an issuer would therefore admit
		// every brand's tokens on that host, so it admits none.
		if b.IAMIssuer == "" {
			return "", fmt.Errorf("brand %s states no issuer, so nothing is trusted to have signed this", id)
		}

		raw, err := bearer(r)
		if err != nil {
			return "", err
		}
		// Shape before keys. Resolving a keyset can cost a request to the issuer, and
		// an unauthenticated caller must not be able to spend one by putting a word in
		// the Authorization header.
		if err := compact(raw); err != nil {
			return "", err
		}

		set, err := jwks(b.IAMIssuer)
		if err != nil {
			return "", fmt.Errorf("keys for issuer %s: %w", b.IAMIssuer, err)
		}
		if len(set) == 0 {
			return "", fmt.Errorf("issuer %s published no signing key", b.IAMIssuer)
		}

		var c claims
		if _, err := jwt.ParseWithClaims(raw, &c, set.verify,
			jwt.WithValidMethods(methods),
			jwt.WithIssuer(b.IAMIssuer),
			jwt.WithAudience(audience),
			jwt.WithExpirationRequired(),
			jwt.WithLeeway(skew),
		); err != nil {
			return "", fmt.Errorf("token on a %s host: %w", id, err)
		}

		if c.TokenType != accessToken {
			return "", fmt.Errorf("token is a %q and not an %s", c.TokenType, accessToken)
		}
		owner := strings.TrimSpace(c.Owner)
		if owner == "" {
			return "", errors.New("token names no owner, so it acts for no tenant")
		}
		if !c.member(owner) {
			// The application minted a token naming an org its caller is not in, which
			// is what a shared or org-choice application does. Serving it would make
			// every one of that application's users one tenant: the app owner's.
			return "", fmt.Errorf("token acts for org %q, which the caller is not a member of", owner)
		}
		return qualify(id, owner)
	}
}

// bearer returns the token from the Authorization header. Bearer is the only
// place a credential is read from: a token accepted from a query string lands in
// access logs and browser history, and one accepted from a cookie is replayable
// cross-site.
func bearer(r *http.Request) (string, error) {
	const scheme = "bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(scheme) && strings.EqualFold(h[:len(scheme)], scheme) {
		if raw := strings.TrimSpace(h[len(scheme):]); raw != "" {
			return raw, nil
		}
	}
	return "", errors.New("no bearer token presented")
}

// maxToken bounds the credential this will look at at all. An IAM access token is
// a kilobyte or two, and an ML-DSA-65 signature alone is 3309 bytes, so this is
// generous for the post-quantum shape while refusing a megabyte of base64 before
// anything tries to decode it.
const maxToken = 16 << 10

// compact reports whether a bearer is a JWS at all: three non-empty base64url
// segments whose first decodes to a JSON header naming an algorithm.
//
// It is a filter, not a check. Which algorithms are acceptable, which key signs
// them and what the claims say are all decided once, by the parse. The only reason
// this exists is that reaching the parse can cost a request to the issuer, and an
// unauthenticated caller should not be able to spend one: "Bearer x" is refused
// here, for free.
func compact(raw string) error {
	if len(raw) > maxToken {
		return fmt.Errorf("bearer is %d bytes, which is not a token", len(raw))
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return errors.New("bearer is not a compact JWS")
	}
	for _, p := range parts {
		if p == "" {
			return errors.New("bearer has an empty segment")
		}
	}
	head, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return errors.New("bearer header is not base64url")
	}
	var h struct {
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(head, &h); err != nil {
		return errors.New("bearer header is not JSON")
	}
	if h.Alg == "" {
		return errors.New("bearer header names no algorithm")
	}
	return nil
}
