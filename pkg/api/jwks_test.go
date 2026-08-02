// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/luxfi/crypto/pq/mldsa/mldsa65"
)

// The JWK documents below are the shapes IAM actually serves
// (internal/oidc/certkey.go certToJWK): RSA as (n, e), EC as (crv, x, y) with
// fixed-width coordinates, and ML-DSA-65 as kty "MLDSA" with the packed public key
// in x. A verifier tested against a shape nobody publishes proves nothing.

func doc(keys ...string) string { return `{"keys":[` + strings.Join(keys, ",") + `]}` }

func rsaJWK(kid string, k *rsa.PublicKey) string {
	return fmt.Sprintf(`{"kty":"RSA","use":"sig","alg":"RS256","kid":%q,"n":%q,"e":%q}`,
		kid,
		base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
		base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes()))
}

func ecJWK(kid string, k *ecdsa.PublicKey) string {
	var crv, alg string
	var size int
	switch k.Curve.Params().BitSize {
	case 256:
		crv, alg, size = "P-256", "ES256", 32
	case 384:
		crv, alg, size = "P-384", "ES384", 48
	default:
		crv, alg, size = "P-521", "ES512", 66
	}
	pad := func(b []byte) string {
		out := make([]byte, size)
		copy(out[size-len(b):], b)
		return base64.RawURLEncoding.EncodeToString(out)
	}
	return fmt.Sprintf(`{"kty":"EC","use":"sig","alg":%q,"kid":%q,"crv":%q,"x":%q,"y":%q}`,
		alg, kid, crv, pad(k.X.Bytes()), pad(k.Y.Bytes()))
}

func mldsaJWK(kid string, k *mldsa65.PublicKey) string {
	return fmt.Sprintf(`{"kty":"MLDSA","use":"sig","alg":"MLDSA65","kid":%q,"x":%q}`,
		kid, base64.RawURLEncoding.EncodeToString(k.Bytes()))
}

// mldsaSigner is the ISSUER's half of ML-DSA-65. It lives here because this engine
// verifies tokens and never mints them: the signing side belongs to whoever plays
// the issuer, which in a test is the test.
type mldsaSigner struct{ mldsa65Method }

func (mldsaSigner) Sign(signing string, key any) ([]byte, error) {
	sk, ok := key.(*mldsa65.PrivateKey)
	if !ok {
		return nil, jwt.ErrInvalidKeyType
	}
	return mldsa65.Sign(sk, []byte(signing), nil, false)
}

func ecKey(t *testing.T, curve elliptic.Curve) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
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
		fmt.Fprint(w, doc(rsaJWK("iam", &key().PublicKey)))
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
	got, ok := set["iam"].(*rsa.PublicKey)
	if !ok {
		t.Fatalf("decoded key is %T, want an RSA public key", set["iam"])
	}
	if got.N.Cmp(key().PublicKey.N) != 0 || got.E != key().PublicKey.E {
		t.Fatalf("decoded key does not match the published one: %+v", got)
	}

	// The decoded key is a verification key, not just a struct that parsed.
	tok := rs256(t, jwt.MapClaims{"iss": iam.URL, "owner": "acme"})
	parsed, err := jwt.ParseWithClaims(tok, &claims{}, set.verify, jwt.WithValidMethods(methods), jwt.WithIssuer(iam.URL))
	if err != nil || !parsed.Valid {
		t.Fatalf("a token signed by the published key did not verify: %v", err)
	}
}

// Every algorithm IAM publishes signing keys for must authenticate a caller here,
// fetched over HTTP from a real key set and verified through the Identity.
//
// RED-9: the decoder read RSA alone, so an issuer that rotated a brand onto an EC
// or ML-DSA-65 cert published keys this engine dropped on the floor — and then no
// token from that brand verified at all. The post-quantum path would have been a
// lockout rather than an upgrade, and the only symptom would have been every caller
// of one brand refused at once.
func TestEveryAlgorithmIAMPublishesVerifies(t *testing.T) {
	p256, p384, p521 := ecKey(t, elliptic.P256()), ecKey(t, elliptic.P384()), ecKey(t, elliptic.P521())
	pqPub, pqKey, err := mldsa65.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	// One set carrying every shape, as an issuer mid-rotation serves it.
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, doc(
			rsaJWK("rsa", &key().PublicKey),
			ecJWK("p256", &p256.PublicKey),
			ecJWK("p384", &p384.PublicKey),
			ecJWK("p521", &p521.PublicKey),
			mldsaJWK("pq", pqPub),
		))
	}))
	defer iam.Close()

	set, err := JWKS(time.Minute, time.Hour)(iam.URL)
	if err != nil {
		t.Fatalf("JWKS: %v", err)
	}
	if len(set) != 5 {
		t.Fatalf("loaded %d keys, want 5: %v", len(set), set)
	}
	id := IAMIdentity(func(string) (Keyset, error) { return set, nil }, aud)

	for _, tc := range []struct {
		kid    string
		method jwt.SigningMethod
		signer any
	}{
		{"rsa", jwt.SigningMethodRS256, key()},
		{"p256", jwt.SigningMethodES256, p256},
		{"p384", jwt.SigningMethodES384, p384},
		{"p521", jwt.SigningMethodES512, p521},
		{"pq", mldsaSigner{}, pqKey},
	} {
		tok := jwt.NewWithClaims(tc.method, jwt.MapClaims{
			"iss": "https://hanzo.id", "owner": "acme", "aud": aud,
			"tokenType": accessToken, "exp": time.Now().Add(time.Hour).Unix(),
		})
		tok.Header["kid"] = tc.kid
		raw, err := tok.SignedString(tc.signer)
		if err != nil {
			t.Fatalf("%s: sign: %v", tc.kid, err)
		}
		who, err := id(bearing("api.hanzo.ai", raw))
		if err != nil {
			t.Errorf("%s (%s): a token signed by a published key was refused: %v", tc.kid, tc.method.Alg(), err)
			continue
		}
		if who.Tenant != "hanzo/acme" {
			t.Errorf("%s: tenant = %q, want hanzo/acme", tc.kid, who.Tenant)
		}

		// And the signature is what did it: the same claims under a different key of
		// the same algorithm must not verify against this kid.
		other := ecKey(t, elliptic.P256())
		if tc.method == jwt.SigningMethodES256 {
			tok.Header["kid"] = tc.kid
			forged, err := tok.SignedString(other)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := id(bearing("api.hanzo.ai", forged)); err == nil {
				t.Errorf("%s: a signature from an unpublished EC key authenticated", tc.kid)
			}
		}
	}
}

// The accepted algorithms and the decodable key types are one decision, so they
// have to agree. An algorithm accepted with no key type that can verify it locks
// out the brand that rotates to it; a key type decoded with no accepted algorithm
// reads as support and is not. This is the check that the two lists in jwks.go stay
// one list.
func TestAcceptedAlgorithmsHaveAKeyTypeThatVerifies(t *testing.T) {
	p256 := ecKey(t, elliptic.P256())
	p384 := ecKey(t, elliptic.P384())
	p521 := ecKey(t, elliptic.P521())
	pqPub, _, err := mldsa65.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	// The key each accepted algorithm verifies under, and the JWK that publishes it.
	published := map[string]string{
		"RS256":   rsaJWK("k", &key().PublicKey),
		"RS384":   rsaJWK("k", &key().PublicKey),
		"RS512":   rsaJWK("k", &key().PublicKey),
		"PS256":   rsaJWK("k", &key().PublicKey),
		"PS384":   rsaJWK("k", &key().PublicKey),
		"PS512":   rsaJWK("k", &key().PublicKey),
		"ES256":   ecJWK("k", &p256.PublicKey),
		"ES384":   ecJWK("k", &p384.PublicKey),
		"ES512":   ecJWK("k", &p521.PublicKey),
		"MLDSA65": mldsaJWK("k", pqPub),
	}
	for _, alg := range methods {
		jwk, ok := published[alg]
		if !ok {
			t.Errorf("%s is accepted but no key type here publishes a key for it", alg)
			continue
		}
		set, err := decodeKeys([]byte(doc(jwk)))
		if err != nil || set["k"] == nil {
			t.Errorf("%s: the key it verifies under does not decode: %v", alg, err)
			continue
		}
		if jwt.GetSigningMethod(alg) == nil {
			t.Errorf("%s is accepted but no signing method is registered for it", alg)
		}
	}
	// And nothing is accepted that a public key cannot verify — HS256 in particular,
	// which would have the verifier treat a published modulus as a shared secret.
	for _, alg := range methods {
		if strings.HasPrefix(alg, "HS") || alg == "none" {
			t.Errorf("%s is accepted, and it is verified with a secret rather than a published key", alg)
		}
	}
}

// A keyset is held past its refresh while the issuer is unreachable — a blip must
// not refuse every caller — but only up to the staleness bound, past which the
// keys are not something this instance can still confirm and it refuses instead.
//
// Both bounds are probed AT the boundary, because that is the only place `<` and
// `<=` differ and the difference is a policy: at exactly the bound the keys are no
// longer confirmable, so they are no longer served.
func TestJWKSHoldsStaleKeysOnlyWithinTheBound(t *testing.T) {
	var up atomic.Bool
	var hits atomic.Int64
	up.Store(true)
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !up.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, doc(rsaJWK("iam", &key().PublicKey)))
	}))
	defer iam.Close()

	now := time.Now()
	// retry is zero here so the memory of a failure does not stand in for the
	// staleness bound: this test is about how long confirmed keys are served, and
	// TestAFailedFetchIsRememberedBriefly is about how often a failure is retried.
	fresh := func() *cache {
		return &cache{
			ttl: time.Minute, stale: 10 * time.Minute, retry: 0,
			sets: map[string]*issuerKeys{}, client: iam.Client(),
			now: func() time.Time { return now },
		}
	}

	// The refresh bound: keys are served without asking again until they are ttl old,
	// and at exactly ttl they are refreshed.
	c := fresh()
	if _, err := c.get(iam.URL); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("first fetch asked the issuer %d times, want 1", n)
	}
	now = now.Add(time.Minute - time.Nanosecond) // one tick inside the refresh
	if _, err := c.get(iam.URL); err != nil {
		t.Fatalf("inside the refresh: %v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("keys inside the refresh were fetched again: %d asks, want 1", n)
	}
	now = now.Add(time.Nanosecond) // age == ttl exactly
	if _, err := c.get(iam.URL); err != nil {
		t.Fatalf("at the refresh bound: %v", err)
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("keys exactly ttl old were not refreshed: %d asks, want 2", n)
	}

	// The staleness bound, from a cache whose one fetch is at a known instant.
	now = time.Now()
	c = fresh()
	if _, err := c.get(iam.URL); err != nil {
		t.Fatalf("second cache, first fetch: %v", err)
	}
	up.Store(false)

	now = now.Add(10*time.Minute - time.Nanosecond) // one tick inside the bound
	if _, err := c.get(iam.URL); err != nil {
		t.Fatalf("a stale keyset one tick inside the bound was not served: %v", err)
	}
	now = now.Add(time.Nanosecond) // age == stale exactly
	if _, err := c.get(iam.URL); err == nil {
		t.Error("keys exactly as old as the staleness bound were still served; at the bound they are no longer confirmable")
	}
	now = now.Add(time.Nanosecond) // and past it
	if _, err := c.get(iam.URL); err == nil {
		t.Error("keys the issuer has not confirmed for longer than the bound were still served")
	}

	// Once the issuer answers again, so does the cache.
	up.Store(true)
	if _, err := c.get(iam.URL); err != nil {
		t.Fatalf("recovery: %v", err)
	}
}

// RED-5: one issuer that does not answer must not hold up any other.
//
// The reproduction is the auth path itself: the brand is chosen by the request's
// Host, so a caller picks which issuer this process goes and asks. While one
// process-wide mutex was held across the HTTP fetch, a single unreachable issuer
// serialised every request in the process behind it — including requests for the
// brand whose issuer was answering perfectly, and including the deployment's own.
// One unauthenticated request was enough to start it.
func TestOneUnreachableIssuerDoesNotStallAnother(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-release
	}))
	defer stalled.Close()
	defer close(release)

	answering := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, doc(rsaJWK("iam", &key().PublicKey)))
	}))
	defer answering.Close()

	c := &cache{
		ttl: time.Minute, stale: time.Hour, retry: retry,
		sets: map[string]*issuerKeys{}, client: &http.Client{Timeout: 30 * time.Second},
		now: time.Now,
	}

	outstanding := make(chan struct{})
	go func() {
		c.get(stalled.URL) //nolint:errcheck // the point is that it is still in flight
		close(outstanding)
	}()
	<-entered // the stalled fetch is now inside the client, holding whatever it holds

	resolved := make(chan error, 1)
	go func() {
		_, err := c.get(answering.URL)
		resolved <- err
	}()
	select {
	case err := <-resolved:
		if err != nil {
			t.Fatalf("the answering issuer failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a reachable issuer did not resolve while an unreachable one was outstanding: the auth path is serialised on one lock")
	}
}

// RED-5: a failed fetch is remembered, so an unreachable issuer costs one attempt
// per window and not one per request.
//
// Without the memory, every request to a brand whose issuer is down pays a fresh
// network timeout — which is the same lever from the other side: the caller chooses
// the issuer by choosing the Host, and each attempt is a connection this process
// holds open on their behalf.
func TestAFailedFetchIsRememberedBriefly(t *testing.T) {
	var hits atomic.Int64
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer iam.Close()

	now := time.Now()
	c := &cache{
		ttl: time.Minute, stale: time.Hour, retry: 30 * time.Second,
		sets: map[string]*issuerKeys{}, client: iam.Client(),
		now: func() time.Time { return now },
	}

	for i := range 5 {
		if _, err := c.get(iam.URL); err == nil {
			t.Fatalf("attempt %d: an issuer serving 500 produced a keyset", i)
		}
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("five requests against a failing issuer cost %d fetches, want 1", n)
	}

	// The memory is short: past the retry window the issuer is asked again, so a
	// recovery is honoured without waiting for the process to restart.
	now = now.Add(30 * time.Second)
	if _, err := c.get(iam.URL); err == nil {
		t.Fatal("an issuer serving 500 produced a keyset")
	}
	if n := hits.Load(); n != 2 {
		t.Errorf("past the retry window the issuer was asked %d times, want 2", n)
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
	p256 := ecKey(t, elliptic.P256())
	good := base64.RawURLEncoding.EncodeToString(key().PublicKey.N.Bytes())
	coord := base64.RawURLEncoding.EncodeToString(make([]byte, 32))

	for _, tc := range []struct{ name, doc string }{
		{"a modulus too short to be evidence", doc(rsaJWK("weak", &weak.PublicKey))},
		{"an empty set", `{"keys":[]}`},
		{"not a key set", `{"keys":"nope"}`},
		{"an encryption key", `{"keys":[{"kty":"RSA","use":"enc","kid":"e","n":"` + good + `","e":"AQAB"}]}`},
		{"another key type", `{"keys":[{"kty":"oct","use":"sig","kid":"h","k":"c2VjcmV0"}]}`},
		{"a modulus that is not base64url", `{"keys":[{"kty":"RSA","use":"sig","kid":"x","n":"!!!","e":"AQAB"}]}`},
		{"an exponent too large to be one", `{"keys":[{"kty":"RSA","use":"sig","kid":"x","n":"` + good + `","e":"` + strings.Repeat("AQAB", 4) + `"}]}`},

		// EC rows: a curve nobody signs with, and coordinates that are not a point on
		// the curve they claim. A pair of integers off the curve is not a public key,
		// and handing one to a verifier is undefined rather than false.
		{"a curve this does not verify", `{"keys":[{"kty":"EC","use":"sig","kid":"x","crv":"P-224","x":"` + coord + `","y":"` + coord + `"}]}`},
		{"no curve at all", `{"keys":[{"kty":"EC","use":"sig","kid":"x","x":"` + coord + `","y":"` + coord + `"}]}`},
		{"coordinates that are not on the curve", `{"keys":[{"kty":"EC","use":"sig","kid":"x","crv":"P-256","x":"` + coord + `","y":"` + coord + `"}]}`},
		{"a coordinate wider than the curve", `{"keys":[{"kty":"EC","use":"sig","kid":"x","crv":"P-256","x":"` + good + `","y":"` + good + `"}]}`},
		{"an EC key with no y", `{"keys":[{"kty":"EC","use":"sig","kid":"x","crv":"P-256","x":"` + coord + `"}]}`},

		// ML-DSA rows: material of the wrong length is not a key of this scheme.
		{"an ML-DSA key of the wrong length", `{"keys":[{"kty":"MLDSA","use":"sig","kid":"x","alg":"MLDSA65","x":"` + coord + `"}]}`},
		{"an ML-DSA key that is not base64url", `{"keys":[{"kty":"MLDSA","use":"sig","kid":"x","alg":"MLDSA65","x":"!!!"}]}`},
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

	// A key left out is left out LOUDLY, and the line names the key type and
	// algorithm. Silence here is the failure mode that matters: an EC or ML-DSA
	// rotation would lock out a brand while the JWKS looked healthy and the log said
	// nothing, so the operator would see an issuer publishing keys and a service
	// refusing everybody, with nothing to connect them.
	var logged strings.Builder
	flags := log.Flags()
	log.SetOutput(&logged)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(flags)
	}()
	set, err := decodeKeys([]byte(doc(
		ecJWK("live", &p256.PublicKey),
		`{"kty":"OKP","use":"sig","kid":"dropped","alg":"EdDSA","crv":"Ed25519","x":"`+coord+`"}`,
	)))
	if err != nil || set["live"] == nil {
		t.Fatalf("the usable key in a mixed set was not loaded: %v", err)
	}
	if set["dropped"] != nil {
		t.Error("a key type with no verifier was loaded anyway")
	}
	for _, want := range []string{"dropped", "OKP", "EdDSA"} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("the log line for an unsupported key does not name %q: %q", want, logged.String())
		}
	}
}
