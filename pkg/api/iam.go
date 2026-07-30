// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

// The IAM Identity: the tenant of a request is the org named by an access token
// that the brand of the request's own Host issued.
//
// This is the second implementation of the Identity seam and the one a directly
// reachable deployment must use. TrustedProxyHeader is sound only where an
// authenticating proxy is the sole route in and strips the client's copy of the
// header; where that does not hold, the header is an unauthenticated assertion and
// any caller can name any tenant. Here the org arrives inside a signature.
//
// Brand pinning is the white-label half. The Host decides which issuer may
// authenticate the caller, so a Lux-issued token presented on a Zoo host is
// refused even though both issuers are first-party and, in-cluster, may publish
// their signing certificates in one JWKS. Without that pin, tenancy across brands
// is decoration: one valid token from any brand would open every brand's surface.
// This is stricter than the fleet edge (hanzoai/cloud auth_identity.go), which
// trusts the union of brand issuers because one binary serves every brand's apps;
// an AML record plane answers for one brand at a time, so it pins.

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/luxfi/aml/pkg/brand"
)

// jwksPath is where a Hanzo IAM issuer publishes its signing keys, relative to
// the issuer itself. The issuer is authoritative for its own keys, so this is the
// only place they are ever read from.
const jwksPath = "/v1/iam/.well-known/jwks"

// skew is the clock difference tolerated on exp and nbf. It matches the fleet
// edge validator, so a token that is live at the gateway is live here and a
// caller cannot be authenticated by one layer and refused by the other.
const skew = 2 * time.Minute

// signing methods accepted. IAM signs RS256; the wider RSA families are listed
// because a rotation to a stronger hash must not need a release here. Anything
// else — HS256 in particular, the algorithm-confusion attack that would have the
// verifier treat a public modulus as a shared secret — has no key in a JWKS and
// is refused before a signature is ever checked.
var methods = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512"}

// minModulus is the smallest RSA modulus whose signature counts as evidence. An
// issuer publishing a shorter key cannot be told apart from a JWKS that was
// tampered with in transit, and a signature under a forgeable key proves nothing
// about who is calling.
const minModulus = 2048

// Keyset is an issuer's public signing keys, addressed by JWK `kid`.
type Keyset map[string]*rsa.PublicKey

// Keysets resolves an issuer to the keyset it signs with.
//
// It is a seam so that the validator holds no network: a deployment passes JWKS,
// a test passes the key it signed with, and both exercise the same validation.
// (Distinct from Handler.Keys, which is the tokenisation keyring — that holds
// secrets, this holds published verification keys.)
type Keysets func(issuer string) (Keyset, error)

// claims is the part of an IAM access token this engine reads.
//
// `owner` is the fleet's org claim — IAM states which org a token acts for, and
// every layer that scopes data reads that one claim (the gateway sets X-Org-Id
// from it, which is what TrustedProxyHeader was consuming second-hand). It is the
// tenant, and a token that names no owner is attributable to nobody.
type claims struct {
	jwt.RegisteredClaims
	Owner string `json:"owner"`
}

// IAMIdentity resolves the tenant from a bearer token issued by the brand that
// owns the request's Host.
//
// It refuses on every uncertainty: no token, a token from another brand's issuer,
// a signature no published key verifies, a missing or passed expiry, a keyset that
// cannot be reached, or a token naming no owner. A compliance control that
// authenticates when it is unsure is worse than one that will not serve, because
// it answers with another tenant's records and looks healthy doing it.
func IAMIdentity(jwks Keysets) Identity {
	return func(r *http.Request) (string, error) {
		if jwks == nil {
			return "", errors.New("no keyset source configured, so no token can be verified")
		}

		// The Host, not a forwarding header: an intermediary's claim about the
		// original host is a claim, and this decides which issuer is trusted.
		id := brand.ForHost(r.Host)
		issuer := brand.IssuerFor(id)

		raw, err := bearer(r)
		if err != nil {
			return "", err
		}
		set, err := jwks(issuer)
		if err != nil {
			return "", fmt.Errorf("keys for issuer %s: %w", issuer, err)
		}
		if len(set) == 0 {
			return "", fmt.Errorf("issuer %s published no signing key", issuer)
		}

		var c claims
		if _, err := jwt.ParseWithClaims(raw, &c, set.verify,
			jwt.WithValidMethods(methods),
			jwt.WithIssuer(issuer),
			jwt.WithExpirationRequired(),
			jwt.WithLeeway(skew),
		); err != nil {
			return "", fmt.Errorf("token on a %s host: %w", id, err)
		}

		owner := strings.TrimSpace(c.Owner)
		if owner == "" {
			return "", errors.New("token names no owner, so it acts for no tenant")
		}
		return owner, nil
	}
}

// verify supplies the keys a token's signature may be checked against: the one its
// `kid` names, and nothing else. A token that names a published key must be signed
// by that key — pointing at one key while being signed by another is a key
// substitution, not a rotation, and there is no reading of it that is legitimate.
//
// A token naming no `kid`, or naming one this issuer does not publish, is checked
// against every key the issuer does publish. That is what keeps a token minted
// either side of a rotation working: the label may be stale, but the signature is
// the evidence and it still has to hold under a key the issuer stands behind.
func (k Keyset) verify(t *jwt.Token) (any, error) {
	if kid, _ := t.Header["kid"].(string); kid != "" {
		if key, ok := k[kid]; ok {
			return jwt.VerificationKeySet{Keys: []jwt.VerificationKey{key}}, nil
		}
	}
	out := make([]jwt.VerificationKey, 0, len(k))
	for _, key := range k {
		out = append(out, key)
	}
	if len(out) == 0 {
		return nil, errors.New("no signing key to verify against")
	}
	return jwt.VerificationKeySet{Keys: out}, nil
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

// JWKS resolves each issuer's keyset from its own /v1/iam/.well-known/jwks,
// holding it for ttl.
//
// Keys are served past ttl while the issuer is unreachable, because a momentary
// blip must not refuse every caller at once. That grace is bounded by stale: past
// it the keys are no longer something this instance can confirm, and it refuses
// rather than authenticating against a set it can no longer see. The fleet edge
// holds a stale set indefinitely for availability; a record plane would rather
// stop answering than admit a caller on evidence it cannot check.
func JWKS(ttl, stale time.Duration) Keysets {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	if stale < ttl {
		stale = ttl
	}
	c := &cache{
		ttl:    ttl,
		stale:  stale,
		sets:   map[string]issuerKeys{},
		client: &http.Client{Timeout: 10 * time.Second},
		now:    time.Now,
	}
	return c.get
}

type issuerKeys struct {
	keys      Keyset
	fetchedAt time.Time
}

type cache struct {
	mu     sync.Mutex
	sets   map[string]issuerKeys
	ttl    time.Duration
	stale  time.Duration
	client *http.Client
	// now is the clock, so the staleness bound is testable without waiting for it.
	now func() time.Time
}

func (c *cache) get(issuer string) (Keyset, error) {
	// One issuer's fetch is held under the same lock as the map: a burst of
	// requests to a cold cache should ask the issuer once, not once each.
	c.mu.Lock()
	defer c.mu.Unlock()

	have, cached := c.sets[issuer]
	age := c.now().Sub(have.fetchedAt)
	if cached && age < c.ttl {
		return have.keys, nil
	}

	keys, err := c.fetch(issuer)
	if err != nil {
		if cached && age < c.stale {
			return have.keys, nil
		}
		return nil, err
	}
	c.sets[issuer] = issuerKeys{keys: keys, fetchedAt: c.now()}
	return keys, nil
}

func (c *cache) fetch(issuer string) (Keyset, error) {
	resp, err := c.client.Get(strings.TrimSuffix(issuer, "/") + jwksPath)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	// Bounded read: an issuer's key set is a few kilobytes, and an unbounded read
	// makes the size of the answer the caller's problem.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return decodeKeys(body)
}

// decodeKeys reads the RSA signing keys out of a JWKS document. A key of another
// type, or one published for encryption rather than signing, or one whose modulus
// is too short to be evidence, is left out — so it cannot verify anything — while
// the rest of the set still loads.
func decodeKeys(body []byte) (Keyset, error) {
	var doc struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	out := Keyset{}
	for _, k := range doc.Keys {
		if k.Kty != "RSA" || (k.Use != "sig" && k.Use != "") {
			continue
		}
		n, err := unpad(k.N)
		if err != nil {
			continue
		}
		e, err := unpad(k.E)
		if err != nil || len(e) == 0 || len(e) > 8 {
			continue
		}
		modulus := new(big.Int).SetBytes(n)
		if modulus.BitLen() < minModulus {
			continue
		}
		out[k.Kid] = &rsa.PublicKey{N: modulus, E: int(new(big.Int).SetBytes(e).Int64())}
	}
	if len(out) == 0 {
		return nil, errors.New("no usable signing key")
	}
	return out, nil
}

// unpad decodes base64url with or without padding: RFC 7517 omits it, and
// implementations that send it are common enough that refusing them would refuse
// a working issuer.
func unpad(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}
