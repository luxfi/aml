// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

// An issuer's published signing keys: how they are read, how long they are held,
// and what counts as a key at all.
//
// The algorithms accepted and the key types decoded are stated together, in this
// one file, because they have to agree. An algorithm accepted with no key type
// that can verify it locks out the brand that rotates to it; a key type decoded
// with no accepted algorithm is dead weight that reads as support. IAM publishes
// RS256/RS512, ES256/ES384/ES512 and MLDSA65 (internal/oidc/jwks.go signingAlgs),
// and every one of those is verifiable here.

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/luxfi/crypto/pq/mldsa/mldsa65"
)

// jwksPath is where a Hanzo IAM issuer publishes its signing keys, relative to
// the issuer itself. The issuer is authoritative for its own keys, so this is the
// only place they are ever read from.
const jwksPath = "/v1/iam/.well-known/jwks"

// methods are the signing algorithms accepted.
//
// It is an allow-list of families whose verification key is a public key, which is
// what refuses HS256 — the algorithm-confusion attack that would have the verifier
// treat a published RSA modulus as a shared secret. An algorithm outside this list
// has no key in any JWKS and is refused before a signature is ever checked.
//
// The RSA-PSS and wider-hash rows are here so that a rotation to a stronger hash
// under the same key does not need a release; MLDSA65 is here because this stack is
// post-quantum-forward and IAM already signs with it where a Cert says so.
var methods = []string{
	"RS256", "RS384", "RS512",
	"PS256", "PS384", "PS512",
	"ES256", "ES384", "ES512",
	algMLDSA65,
}

// minModulus is the smallest RSA modulus whose signature counts as evidence. An
// issuer publishing a shorter key cannot be told apart from a JWKS that was
// tampered with in transit, and a signature under a forgeable key proves nothing
// about who is calling.
const minModulus = 2048

// Keyset is an issuer's public signing keys, addressed by JWK `kid`. A key is an
// RSA, EC or ML-DSA-65 public key — whichever the issuer published.
type Keyset map[string]crypto.PublicKey

// Keysets resolves an issuer to the keyset it signs with.
//
// It is a seam so that the validator holds no network: a deployment passes JWKS,
// a test passes the key it signed with, and both exercise the same validation.
// (Distinct from Handler.Keys, which is the tokenisation keyring — that holds
// secrets, this holds published verification keys.)
type Keysets func(issuer string) (Keyset, error)

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
		ttl:   ttl,
		stale: stale,
		retry: retry,
		sets:  map[string]*issuerKeys{},
		// The timeout is the ceiling on how long one issuer can hold an auth path:
		// a caller with no usable keys waits for the fetch in front of it rather
		// than being refused on a cold start, so that wait has to be short. An
		// issuer answers this in milliseconds in-cluster.
		client: &http.Client{Timeout: 5 * time.Second},
		now:    time.Now,
	}
	return c.get
}

// retry is how long a failed fetch is remembered. Inside it the failure is
// answered from memory instead of being attempted again.
//
// Without it an unreachable issuer costs a fetch per request — so a caller who can
// choose the issuer, by choosing the Host, can point every request at whichever
// brand is unreachable and make the auth path pay a network timeout each time. It
// is seconds rather than minutes because it also bounds how long a recovered
// issuer stays refused for a caller holding no stale keys.
const retry = 15 * time.Second

// issuerKeys is one issuer's state: the keys, when they were confirmed, and the
// last failure to confirm them.
type issuerKeys struct {
	// mu serialises this issuer's fetches, and only this issuer's. A burst against
	// a cold cache asks the issuer once rather than once per request, and — because
	// the map is not locked while a fetch is outstanding — an issuer that does not
	// answer holds up nothing but itself. Holding one process-wide lock across the
	// HTTP fetch is how one unreachable issuer came to stall every request in the
	// process, including those for issuers that were answering.
	mu        sync.Mutex
	keys      Keyset
	fetchedAt time.Time
	failedAt  time.Time
	err       error
}

type cache struct {
	// mu guards the map of issuers and is never held across a fetch.
	mu     sync.Mutex
	sets   map[string]*issuerKeys
	ttl    time.Duration
	stale  time.Duration
	retry  time.Duration
	client *http.Client
	// now is the clock, so the staleness bound is testable without waiting for it.
	now func() time.Time
}

// issuer returns the state for an issuer, creating it if this is the first time it
// has been asked for. This is the only critical section over the map.
func (c *cache) issuer(url string) *issuerKeys {
	c.mu.Lock()
	defer c.mu.Unlock()
	i, ok := c.sets[url]
	if !ok {
		i = &issuerKeys{}
		c.sets[url] = i
	}
	return i
}

func (c *cache) get(issuer string) (Keyset, error) {
	i := c.issuer(issuer)

	i.mu.Lock()
	defer i.mu.Unlock()

	now := c.now()
	if i.keys != nil && now.Sub(i.fetchedAt) < c.ttl {
		return i.keys, nil
	}
	// A failure this recent is answered from memory. Stale keys are still preferred
	// to a refusal while they are inside the bound — the issuer being unreachable
	// is not evidence against keys it published.
	if i.err != nil && now.Sub(i.failedAt) < c.retry {
		if i.keys != nil && now.Sub(i.fetchedAt) < c.stale {
			return i.keys, nil
		}
		return nil, i.err
	}

	keys, err := c.fetch(issuer)
	now = c.now()
	if err != nil {
		i.failedAt, i.err = now, err
		if i.keys != nil && now.Sub(i.fetchedAt) < c.stale {
			return i.keys, nil
		}
		return nil, err
	}
	i.keys, i.fetchedAt = keys, now
	i.err, i.failedAt = nil, time.Time{}
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

// jwk is one published key, in the shapes IAM emits: RSA (n, e), EC (crv, x, y)
// and ML-DSA-65 (x), each with kty, kid, use and alg (internal/oidc/certkey.go).
type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	Crv string `json:"crv"`
	N   string `json:"n"`
	E   string `json:"e"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// decodeKeys reads the signing keys out of a JWKS document.
//
// A key that cannot verify anything is left out rather than loaded, so it cannot
// be the reason a forged token passes — but it is left out LOUDLY. A silent skip
// is how an EC or ML-DSA rotation locks out a whole brand with nothing in the log
// to say why: every token then names a kid the verifier does not have, the set
// looks merely empty, and the operator sees an issuer that publishes keys and a
// service that refuses everybody.
func decodeKeys(body []byte) (Keyset, error) {
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	out := Keyset{}
	for _, k := range doc.Keys {
		key, err := k.public()
		if err != nil {
			log.Printf("[aml] jwks: unsupported key kid=%q kty=%q alg=%q crv=%q: %v", k.Kid, k.Kty, k.Alg, k.Crv, err)
			continue
		}
		out[k.Kid] = key
	}
	if len(out) == 0 {
		return nil, errors.New("no usable signing key")
	}
	return out, nil
}

// public is the key a JWK stands for, or why it is not one.
func (k jwk) public() (crypto.PublicKey, error) {
	// `use` is optional, but a key published for encryption is published for
	// something other than proving who is calling.
	if k.Use != "sig" && k.Use != "" {
		return nil, fmt.Errorf("published for %q, not for signing", k.Use)
	}
	switch strings.ToUpper(k.Kty) {
	case "RSA":
		return k.rsa()
	case "EC":
		return k.ec()
	case ktyMLDSA:
		return k.mldsa()
	default:
		return nil, errors.New("no verifier for this key type")
	}
}

func (k jwk) rsa() (*rsa.PublicKey, error) {
	n, err := unpad(k.N)
	if err != nil {
		return nil, fmt.Errorf("modulus: %w", err)
	}
	e, err := unpad(k.E)
	if err != nil {
		return nil, fmt.Errorf("exponent: %w", err)
	}
	if len(e) == 0 || len(e) > 8 {
		return nil, fmt.Errorf("exponent is %d bytes", len(e))
	}
	modulus := new(big.Int).SetBytes(n)
	if modulus.BitLen() < minModulus {
		return nil, fmt.Errorf("modulus is %d bits, need %d", modulus.BitLen(), minModulus)
	}
	return &rsa.PublicKey{N: modulus, E: int(new(big.Int).SetBytes(e).Int64())}, nil
}

// ec decodes an EC signing key (RFC 7518 §6.2). The coordinates are checked to be
// a point on the named curve — a pair of integers that is not on the curve is not
// a public key, and feeding one to a verifier is undefined rather than false.
func (k jwk) ec() (*ecdsa.PublicKey, error) {
	var curve elliptic.Curve
	var size int
	switch k.Crv {
	case "P-256":
		curve, size = elliptic.P256(), 32
	case "P-384":
		curve, size = elliptic.P384(), 48
	case "P-521":
		curve, size = elliptic.P521(), 66
	default:
		return nil, fmt.Errorf("curve %q is not one this verifies", k.Crv)
	}
	x, err := coordinate(k.X, size)
	if err != nil {
		return nil, fmt.Errorf("x: %w", err)
	}
	y, err := coordinate(k.Y, size)
	if err != nil {
		return nil, fmt.Errorf("y: %w", err)
	}
	// SEC 1 uncompressed point, which is the form that gets validated on the way in.
	point := append([]byte{4}, append(x, y...)...)
	pub, err := ecdsa.ParseUncompressedPublicKey(curve, point)
	if err != nil {
		return nil, fmt.Errorf("not a point on %s: %w", k.Crv, err)
	}
	return pub, nil
}

// coordinate decodes one fixed-width EC coordinate. RFC 7518 requires the full
// width; a shorter value is left-padded because stripping leading zeros is a
// common encoder bug and the integer it names is the same one, while a longer
// value is not a coordinate on this curve at all.
func coordinate(s string, size int) ([]byte, error) {
	raw, err := unpad(s)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > size {
		return nil, fmt.Errorf("%d bytes, want at most %d", len(raw), size)
	}
	out := make([]byte, size)
	copy(out[size-len(raw):], raw)
	return out, nil
}

// unpad decodes base64url with or without padding: RFC 7517 omits it, and
// implementations that send it are common enough that refusing them would refuse
// a working issuer.
func unpad(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// mldsa decodes an ML-DSA-65 signing key: the packed public key in `x`, as IAM
// publishes it (internal/oidc/certkey.go certToJWK).
func (k jwk) mldsa() (*mldsa65.PublicKey, error) {
	raw, err := unpad(k.X)
	if err != nil {
		return nil, err
	}
	if len(raw) != mldsa65.PublicKeySize {
		return nil, fmt.Errorf("%d bytes, want %d", len(raw), mldsa65.PublicKeySize)
	}
	pub := new(mldsa65.PublicKey)
	if err := pub.UnmarshalBinary(raw); err != nil {
		return nil, err
	}
	return pub, nil
}
