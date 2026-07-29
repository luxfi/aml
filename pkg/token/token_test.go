package token

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// rootA and rootB are two distinct 32-byte roots. Fixed, so a failure is a
// failure of the construction and not of the day's randomness.
var (
	rootA = mustHex("6c75782d616d6c2d746f6b656e2d726f6f742d612d33326279746573212121")
	rootB = mustHex("6c75782d616d6c2d746f6b656e2d726f6f742d622d33326279746573212121")
)

func mustHex(s string) []byte {
	for len(s) < keyLen*2 {
		s += "0f"
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func vault(t *testing.T, org string, root []byte) *Vault {
	t.Helper()
	v, err := New(org, root)
	if err != nil {
		t.Fatalf("New(%q): %v", org, err)
	}
	return v
}

// TestPseudonymDeterministic is the property correlation rests on: the same
// value under the same org and domain is always the same token, so two records
// about one party can be found together.
func TestPseudonymDeterministic(t *testing.T) {
	v := vault(t, "acme", rootA)

	first, err := v.Pseudonym(DomainName, "Ivan Petrov")
	if err != nil {
		t.Fatalf("Pseudonym: %v", err)
	}
	second, err := v.Pseudonym(DomainName, "Ivan Petrov")
	if err != nil {
		t.Fatalf("Pseudonym: %v", err)
	}
	if first != second {
		t.Fatalf("not deterministic: %q != %q", first, second)
	}

	// A second vault over the same root and org must agree, or a restart loses
	// every correlation the index holds.
	if again, err := vault(t, "acme", rootA).Pseudonym(DomainName, "Ivan Petrov"); err != nil || again != first {
		t.Fatalf("not stable across vaults: %q vs %q (err %v)", again, first, err)
	}
}

// TestPseudonymNeverCorrelatesAcrossOrgs is the cross-tenant guarantee: the same
// person under two orgs is two unrelated tokens, so there is no join to make.
func TestPseudonymNeverCorrelatesAcrossOrgs(t *testing.T) {
	const name = "Ivan Petrov"

	// Same root, different org: the org is the salt, so the keys differ.
	a, err := vault(t, "acme", rootA).Pseudonym(DomainName, name)
	if err != nil {
		t.Fatalf("acme: %v", err)
	}
	b, err := vault(t, "beta", rootA).Pseudonym(DomainName, name)
	if err != nil {
		t.Fatalf("beta: %v", err)
	}
	if a == b {
		t.Fatalf("cross-org join is computable: %q == %q", a, b)
	}

	// Different root as well, for the per-org-material Source shape.
	c, err := vault(t, "beta", rootB).Pseudonym(DomainName, name)
	if err != nil {
		t.Fatalf("beta/rootB: %v", err)
	}
	if c == a || c == b {
		t.Fatalf("distinct material still correlates: %q %q %q", a, b, c)
	}
}

// TestPseudonymDomainSeparated stops an account token being compared against a
// name token: a match across domains would be a fabricated link.
func TestPseudonymDomainSeparated(t *testing.T) {
	v := vault(t, "acme", rootA)
	const value = "same-string"

	seen := make(map[string]Domain, len(domains))
	for _, d := range domains {
		p, err := v.Pseudonym(d, value)
		if err != nil {
			t.Fatalf("Pseudonym(%s): %v", d, err)
		}
		if !strings.HasPrefix(p, string(d)+":") {
			t.Errorf("%s token %q does not name its domain", d, p)
		}
		if prev, dup := seen[p]; dup {
			t.Errorf("domains %s and %s collide on %q", prev, d, p)
		}
		seen[p] = d
	}
}

// TestPseudonymUnknownDomainRefused: an invented domain has no key, and
// defaulting it would silently put two kinds of identifier in one namespace.
func TestPseudonymUnknownDomainRefused(t *testing.T) {
	v := vault(t, "acme", rootA)
	if _, err := v.Pseudonym(Domain("passport"), "X1234"); !errors.Is(err, ErrDomain) {
		t.Fatalf("unknown domain: err = %v, want ErrDomain", err)
	}
}

// TestPseudonymEmptyValueRefused: an empty identifier is not an identifier, and
// indexing one would make every party with a missing field the same party.
func TestPseudonymEmptyValueRefused(t *testing.T) {
	v := vault(t, "acme", rootA)
	for _, value := range []string{"", " ", "\t\n", " "} {
		if _, err := v.Pseudonym(DomainName, value); !errors.Is(err, ErrEmpty) {
			t.Errorf("Pseudonym(%q): err = %v, want ErrEmpty", value, err)
		}
	}
}

// TestCanonicalForm pins what a domain treats as the same value. Names fold;
// opaque handles do not, because case distinguishes real base58 addresses.
func TestCanonicalForm(t *testing.T) {
	v := vault(t, "acme", rootA)

	same := func(d Domain, a, b string) {
		t.Helper()
		pa, err := v.Pseudonym(d, a)
		if err != nil {
			t.Fatalf("%s %q: %v", d, a, err)
		}
		pb, err := v.Pseudonym(d, b)
		if err != nil {
			t.Fatalf("%s %q: %v", d, b, err)
		}
		if pa != pb {
			t.Errorf("%s: %q and %q should be one value", d, a, b)
		}
	}
	differ := func(d Domain, a, b string) {
		t.Helper()
		pa, _ := v.Pseudonym(d, a)
		pb, _ := v.Pseudonym(d, b)
		if pa == pb {
			t.Errorf("%s: %q and %q should be two values", d, a, b)
		}
	}

	same(DomainName, "Ivan Petrov", "  ivan   PETROV ")
	same(DomainName, "Ｉvan Petrov", "Ivan Petrov") // NFKC fullwidth I
	differ(DomainName, "Ivan Petrov", "Ivana Petrov")

	same(DomainWallet, " 1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa ", "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa")
	differ(DomainWallet, "1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa", "1a1zp1ep5qgefi2dmptftl5slmv7divfna")
	differ(DomainAccount, "GB33BUKB20201555555555", "gb33bukb20201555555555")
}

// TestNoKeyRefuses: a tokeniser without usable material must refuse. Every one
// of these inputs is a way a deployment ends up with no real key.
func TestNoKeyRefuses(t *testing.T) {
	if _, err := New("acme", nil); !errors.Is(err, ErrNoKey) {
		t.Errorf("nil root: err = %v, want ErrNoKey", err)
	}
	if _, err := New("acme", []byte{}); !errors.Is(err, ErrNoKey) {
		t.Errorf("empty root: err = %v, want ErrNoKey", err)
	}
	if _, err := New("acme", bytes.Repeat([]byte{7}, keyLen-1)); !errors.Is(err, ErrWeakKey) {
		t.Errorf("short root: err = %v, want ErrWeakKey", err)
	}
	if _, err := New("acme", make([]byte, keyLen)); !errors.Is(err, ErrWeakKey) {
		t.Errorf("all-zero root: err = %v, want ErrWeakKey", err)
	}
	if _, err := New("", rootA); !errors.Is(err, ErrOrg) {
		t.Errorf("empty org: err = %v, want ErrOrg", err)
	}
}

// TestEnvHasNoDefault: the key comes from KMS-mounted material, so an unset or
// placeholder variable is a refusal and never a fallback value.
func TestEnvHasNoDefault(t *testing.T) {
	const name = "AML_TOKEN_KEY_TEST"
	src := Env(name)

	t.Setenv(name, "")
	if _, err := src("acme"); !errors.Is(err, ErrNoKey) {
		t.Errorf("unset: err = %v, want ErrNoKey", err)
	}

	for _, bad := range []string{"not-hex-at-all", hex.EncodeToString(make([]byte, keyLen)), "0a0b0c"} {
		t.Setenv(name, bad)
		if _, err := src("acme"); !errors.Is(err, ErrWeakKey) {
			t.Errorf("Env(%q): err = %v, want ErrWeakKey", bad, err)
		}
	}

	t.Setenv(name, hex.EncodeToString(rootA))
	got, err := src("acme")
	if err != nil || !bytes.Equal(got, rootA) {
		t.Fatalf("Env: got %x err %v, want %x", got, err, rootA)
	}
}

// TestSealRoundTrip: the record comes back whole, which is what "not redacted"
// and "reconstruct the transaction" require of the storage layer.
func TestSealRoundTrip(t *testing.T) {
	v := vault(t, "acme", rootA)
	plain := []byte(`{"tx":{"id":"tx-1","notional":15000},"entity":{"name":"Ivan Petrov"}}`)

	sealed, err := v.Seal("record:r-1", plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains(sealed, []byte("Ivan")) {
		t.Fatal("sealed record carries the plaintext")
	}

	out, err := v.Open("record:r-1", sealed)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(out, plain) {
		t.Fatalf("Open = %q, want %q", out, plain)
	}
}

// TestSealIsFresh: two seals of one record differ, so ciphertext equality never
// discloses that two records hold the same body.
func TestSealIsFresh(t *testing.T) {
	v := vault(t, "acme", rootA)
	plain := []byte("same body")

	first, err := v.Seal("record:r-1", plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	second, err := v.Seal("record:r-1", plain)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("seal is deterministic: nonce is not fresh")
	}
}

// TestSealedRecordDoesNotOpenElsewhere: not under another org, not in another
// slot, not after a single flipped bit.
func TestSealedRecordDoesNotOpenElsewhere(t *testing.T) {
	acme := vault(t, "acme", rootA)
	beta := vault(t, "beta", rootA) // same root, other org

	sealed, err := acme.Seal("record:r-1", []byte("body"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if _, err := beta.Open("record:r-1", sealed); !errors.Is(err, ErrSealed) {
		t.Errorf("cross-org open: err = %v, want ErrSealed", err)
	}
	if _, err := acme.Open("record:r-2", sealed); !errors.Is(err, ErrSealed) {
		t.Errorf("cross-slot open: err = %v, want ErrSealed", err)
	}

	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 1
	if _, err := acme.Open("record:r-1", tampered); !errors.Is(err, ErrSealed) {
		t.Errorf("tampered open: err = %v, want ErrSealed", err)
	}
	if _, err := acme.Open("record:r-1", sealed[:acme.seal.NonceSize()]); !errors.Is(err, ErrSealed) {
		t.Errorf("truncated open: err = %v, want ErrSealed", err)
	}
}

// TestAADBindsOrgAndSlot pins both halves of the associated data. The org half
// is defence in depth — per-org key derivation already makes a cross-org open
// fail — so only this test can catch its removal.
func TestAADBindsOrgAndSlot(t *testing.T) {
	v := vault(t, "acme", rootA)
	aad := string(v.aad("record:r-1"))

	if !strings.Contains(aad, "acme") {
		t.Errorf("aad %q does not bind the org", aad)
	}
	if !strings.Contains(aad, "record:r-1") {
		t.Errorf("aad %q does not bind the slot", aad)
	}
}

// TestKeyringCachesVaultsNotFailures: a key a KMS sync has not delivered yet
// must be refused now and honoured when it lands, so a failed lookup cannot be
// remembered as "this org has no key".
func TestKeyringCachesVaultsNotFailures(t *testing.T) {
	var calls int
	var fail = true
	ring := NewKeyring(func(org string) ([]byte, error) {
		calls++
		if fail {
			return nil, ErrNoKey
		}
		return rootA, nil
	})

	if _, err := ring.Org("acme"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("first: err = %v, want ErrNoKey", err)
	}
	fail = false
	first, err := ring.Org("acme")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	second, err := ring.Org("acme")
	if err != nil {
		t.Fatalf("third: %v", err)
	}
	if first != second {
		t.Error("vault not cached: derived twice for one org")
	}
	if calls != 2 {
		t.Errorf("source called %d times, want 2 (one refusal, one success)", calls)
	}
}

// TestKeyringWithoutSourceRefuses: no seam wired is no key, not a pass-through.
func TestKeyringWithoutSourceRefuses(t *testing.T) {
	if _, err := NewKeyring(nil).Org("acme"); !errors.Is(err, ErrNoSource) {
		t.Fatalf("nil source: err = %v, want ErrNoSource", err)
	}
	if _, err := NewKeyring(Env("AML_TOKEN_KEY_ABSENT")).Org(""); !errors.Is(err, ErrOrg) {
		t.Fatalf("empty org: err = %v, want ErrOrg", err)
	}
}
