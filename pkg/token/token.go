// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// Package token protects direct identifiers at rest while keeping the engine
// able to correlate.
//
// Two operations over one key schedule:
//
//   - [Vault.Pseudonym] is a deterministic, org-scoped, domain-separated token.
//     It is what the engine indexes and correlates on, so a retained record can
//     be found by party without the party's name being held in the clear.
//   - [Vault.Seal] is authenticated encryption of a whole retained record. It is
//     reversible under the org's key, which is what keeps a tokenised record
//     able to reconstruct the transaction (MLR 2017 reg. 40(2)(b)) and able to
//     be re-screened when a new designation enters into force
//     (EBA/GL/2024/15 §4.1.4 para 16(a)).
//
// The requirement served is purpose limitation — retained personal data may be
// processed only to prevent money laundering and terrorist financing, and
// processing for commercial purposes is prohibited (Directive (EU) 2015/849
// Art. 41(2)) — together with data minimisation. It is not obfuscation for its
// own sake, and it is deliberately not one-way: a hash-only design can neither
// reconstruct a transaction nor re-screen a customer base against a new
// designation, so it would fail the obligations it is supposed to be protecting.
//
// Determinism is per org and never across orgs. The org is the HKDF salt, so
// one value under two orgs yields two unrelated pseudonyms: a cross-tenant join
// is not merely disallowed, it is not computable (HIP-0302).
//
// Key material comes from KMS. [Source] is the seam; there is no literal key,
// no default and no plaintext path. A vault without key material refuses.
package token

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"golang.org/x/text/unicode/norm"
)

// Errors. Every one of them is a refusal: nothing here degrades to plaintext.
var (
	ErrNoKey    = errors.New("token: no key material")
	ErrWeakKey  = errors.New("token: key material is unusable")
	ErrOrg      = errors.New("token: empty org")
	ErrDomain   = errors.New("token: unknown domain")
	ErrEmpty    = errors.New("token: empty value")
	ErrSealed   = errors.New("token: sealed record does not open")
	ErrNoSource = errors.New("token: no key source")
)

// keyLen is the length of every derived key, and the minimum length of the root
// material they are derived from.
const keyLen = 32

// pseudonymLen is how much of the MAC a pseudonym carries. 16 bytes is a
// 128-bit token: the birthday bound is 2^64 values per org and domain, which no
// customer base approaches, and a shorter token is easier to hold in a case file.
const pseudonymLen = 16

// Domain names the kind of identifier a token stands for. Each domain has its
// own key, so a token minted in one domain cannot be joined against another.
type Domain string

const (
	// DomainSubject is the party identifier the firm itself holds — a user id,
	// an external id, a counterparty reference.
	DomainSubject Domain = "subject"
	// DomainName is a natural or legal person's name.
	DomainName Domain = "name"
	// DomainAccount is an account identifier.
	DomainAccount Domain = "account"
	// DomainWallet is a distributed-ledger address. EBA/GL/2024/15 §4.1.4
	// para 17(c) makes these screening fields where the lists carry them.
	DomainWallet Domain = "wallet"
	// DomainDevice is a device fingerprint.
	DomainDevice Domain = "device"
	// DomainNetwork is a network address.
	DomainNetwork Domain = "network"
)

// domains is every domain a vault derives a key for. A domain absent here has
// no key and is refused rather than defaulted.
var domains = []Domain{
	DomainSubject, DomainName, DomainAccount,
	DomainWallet, DomainDevice, DomainNetwork,
}

// canonical is the form a domain's values are tokenised in, and reports whether
// the domain is known. Determinism is only meaningful modulo a canonical form,
// so the form belongs to the domain rather than to each caller.
func canonical(d Domain, v string) (string, bool) {
	v = norm.NFKC.String(strings.TrimSpace(v))
	switch d {
	case DomainName:
		// A name is not an identifier: case and internal spacing carry nothing.
		// Homoglyphs and transliterations survive this and are the fuzzy
		// matcher's problem, not the tokeniser's.
		return strings.Join(strings.Fields(strings.ToLower(v)), " "), true
	case DomainSubject, DomainAccount, DomainWallet, DomainDevice, DomainNetwork:
		// Opaque handles. Case can distinguish two real identifiers — base58
		// ledger addresses are the clear case — so it is preserved.
		return v, true
	}
	return "", false
}

// Vault holds one org's derived keys.
type Vault struct {
	org  string
	seal cipher.AEAD
	keys map[Domain][]byte
	// mark keys the fingerprint of a sealed body. See Fingerprint.
	mark []byte
}

// New derives an org's vault from root key material. It refuses an empty org,
// and refuses material shorter than 32 bytes or entirely zero: a tokeniser
// without a key refuses, it does not pass plaintext through.
func New(org string, root []byte) (*Vault, error) {
	if org == "" {
		return nil, ErrOrg
	}
	if len(root) == 0 {
		return nil, ErrNoKey
	}
	if len(root) < keyLen {
		return nil, fmt.Errorf("%w: %d bytes, need %d", ErrWeakKey, len(root), keyLen)
	}
	if allZero(root) {
		return nil, fmt.Errorf("%w: all-zero material is a placeholder, not a key", ErrWeakKey)
	}

	salt := []byte("aml/token/org/" + org)
	keys := make(map[Domain][]byte, len(domains))
	for _, d := range domains {
		k, err := hkdf.Key(sha256.New, root, salt, "pseudonym/"+string(d), keyLen)
		if err != nil {
			return nil, fmt.Errorf("token: derive %s: %w", d, err)
		}
		keys[d] = k
	}

	sk, err := hkdf.Key(sha256.New, root, salt, "seal", keyLen)
	if err != nil {
		return nil, fmt.Errorf("token: derive seal: %w", err)
	}
	block, err := aes.NewCipher(sk)
	if err != nil {
		return nil, fmt.Errorf("token: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("token: gcm: %w", err)
	}

	mark, err := hkdf.Key(sha256.New, root, salt, "fingerprint", keyLen)
	if err != nil {
		return nil, fmt.Errorf("token: derive fingerprint: %w", err)
	}

	return &Vault{org: org, seal: aead, keys: keys, mark: mark}, nil
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// Pseudonym returns the token for a value: deterministic within this org and
// domain, unrelated to the same value under any other org or domain.
func (v *Vault) Pseudonym(d Domain, value string) (string, error) {
	c, ok := canonical(d, value)
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrDomain, d)
	}
	if c == "" {
		return "", ErrEmpty
	}
	mac := hmac.New(sha256.New, v.keys[d])
	mac.Write([]byte(c))
	return string(d) + ":" + hex.EncodeToString(mac.Sum(nil)[:pseudonymLen]), nil
}

// Seal encrypts a retained record. slot binds the ciphertext to where it is
// stored and the org is bound with it, so a sealed record neither opens under
// another org nor moves to another slot.
func (v *Vault) Seal(slot string, plain []byte) ([]byte, error) {
	nonce := make([]byte, v.seal.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("token: nonce: %w", err)
	}
	return v.seal.Seal(nonce, nonce, plain, v.aad(slot)), nil
}

// Fingerprint is the stable name of what a Seal holds.
//
// A seal draws a fresh nonce every time, which is the right thing for
// confidentiality and the wrong thing for identity: the same record sealed twice
// is two different byte strings, so anything downstream that asks "is this the
// same record I already have" by comparing sealed bytes answers no, always. That
// is how a retried transaction became a conflict rather than a repeat.
//
// So the vault also names the plaintext, deterministically: an HMAC under a key
// derived for nothing else, bound to the org and to the slot the record occupies.
// Two seals of the same record in the same slot share a fingerprint; two
// different records never do; and the value discloses nothing without the key, so
// it can sit beside the sealed body in a row without becoming a way to confirm a
// guess at what the body holds.
func (v *Vault) Fingerprint(slot string, plain []byte) string {
	mac := hmac.New(sha256.New, v.mark)
	mac.Write(v.aad(slot))
	mac.Write([]byte{0})
	mac.Write(plain)
	return hex.EncodeToString(mac.Sum(nil))
}

// Open decrypts a record sealed for this org and slot.
func (v *Vault) Open(slot string, sealed []byte) ([]byte, error) {
	n := v.seal.NonceSize()
	if len(sealed) < n+v.seal.Overhead() {
		return nil, fmt.Errorf("%w: %d bytes", ErrSealed, len(sealed))
	}
	out, err := v.seal.Open(nil, sealed[:n], sealed[n:], v.aad(slot))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSealed, slot)
	}
	return out, nil
}

func (v *Vault) aad(slot string) []byte {
	return []byte("aml/token/v1\x00" + v.org + "\x00" + slot)
}

// Source supplies root key material for an org.
//
// The engine does not talk to KMS itself: the platform (credz, or a KMSSecret
// synced into the pod) puts the material where a Source reads it. What the
// engine needs is one secret of at least 32 random bytes per deployment,
// rotated by the platform. The org parameter is here so that per-org material —
// where one org's keys must be independently revocable — is a different Source
// and not a different design.
type Source func(org string) ([]byte, error)

// Env reads root key material from a process environment variable, which is
// where KMS-mounted material lands. Hex encoded, at least 32 bytes. There is no
// default and no literal: unset, malformed, short or all-zero is a refusal.
func Env(name string) Source {
	return func(string) ([]byte, error) {
		raw := strings.TrimSpace(os.Getenv(name))
		if raw == "" {
			return nil, fmt.Errorf("%w: %s is not set", ErrNoKey, name)
		}
		b, err := hex.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: %s is not hex", ErrWeakKey, name)
		}
		if len(b) < keyLen {
			return nil, fmt.Errorf("%w: %s is %d bytes, need %d", ErrWeakKey, name, len(b), keyLen)
		}
		if allZero(b) {
			return nil, fmt.Errorf("%w: %s is all zero", ErrWeakKey, name)
		}
		return b, nil
	}
}

// Keyring holds one vault per org, derived on demand from a Source. Failures
// are not cached: material that a KMS sync has not delivered yet must be
// refused now and honoured later.
type Keyring struct {
	src    Source
	mu     sync.Mutex
	vaults map[string]*Vault
}

// NewKeyring returns a keyring over a Source.
func NewKeyring(src Source) *Keyring {
	return &Keyring{src: src, vaults: make(map[string]*Vault)}
}

// Org returns the org's vault.
func (k *Keyring) Org(org string) (*Vault, error) {
	if org == "" {
		return nil, ErrOrg
	}
	k.mu.Lock()
	defer k.mu.Unlock()

	if v, ok := k.vaults[org]; ok {
		return v, nil
	}
	if k.src == nil {
		return nil, ErrNoSource
	}
	root, err := k.src(org)
	if err != nil {
		return nil, err
	}
	v, err := New(org, root)
	if err != nil {
		return nil, err
	}
	k.vaults[org] = v
	return v, nil
}
