// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

// ML-DSA-65 (FIPS 204, NIST level 3) as a JWT verification method.
//
// It exists here because IAM already signs with it: a Cert whose CryptoAlgorithm
// is ML-DSA makes MLDSA65 the alg of every token that app issues, and the key is
// published in the JWKS as {"kty":"MLDSA","x":<packed public key>} (hanzoai/iam
// internal/oidc/mldsa.go and certkey.go). A verifier that knows only RSA drops that
// key on the floor, and then no token from that brand verifies at all — the
// post-quantum path would be a lockout rather than an upgrade.
//
// Only the verifying half is implemented. This is a resource server; it has no
// signing key and no reason to hold one, and a Sign that returns an error is a
// smaller surface than a Sign that works.

import (
	"errors"

	"github.com/golang-jwt/jwt/v5"
	"github.com/luxfi/crypto/pq/mldsa/mldsa65"
)

// algMLDSA65 is the JOSE `alg` value, and ktyMLDSA the JWK `kty`, that IAM
// publishes ML-DSA-65 keys and tokens under.
const (
	algMLDSA65 = "MLDSA65"
	ktyMLDSA   = "MLDSA"
)

// mldsa65Method verifies ML-DSA-65 signatures over the JWS signing input:
// pure ML-DSA-65, no context, which is what IAM's signer produces.
type mldsa65Method struct{}

func init() {
	jwt.RegisterSigningMethod(algMLDSA65, func() jwt.SigningMethod { return mldsa65Method{} })
}

func (mldsa65Method) Alg() string { return algMLDSA65 }

// Sign is not implemented. Nothing in a record plane mints an IAM token, so there
// is no key here to sign with and no caller that should be asking.
func (mldsa65Method) Sign(string, any) ([]byte, error) {
	return nil, errors.New("mldsa65: this verifies tokens, it does not issue them")
}

// Verify checks the signature under a published ML-DSA-65 key. A key of another
// type, or a signature of the wrong length, is a refusal and never a panic: both
// arrive from the network.
func (mldsa65Method) Verify(signing string, sig []byte, key any) error {
	pub, ok := key.(*mldsa65.PublicKey)
	if !ok {
		return jwt.ErrInvalidKeyType
	}
	if len(sig) != mldsa65.SignatureSize {
		return jwt.ErrSignatureInvalid
	}
	if !mldsa65.Verify(pub, []byte(signing), nil, sig) {
		return jwt.ErrSignatureInvalid
	}
	return nil
}
