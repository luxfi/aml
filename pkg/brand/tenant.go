// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package brand

// The tenant key lives here because it is a brand fact: a customer organisation
// is only identified once the brand whose issuer vouched for it is named. Every
// plane that indexes, salts or scopes on the tenant reads the shape from this one
// definition, so a new record plane cannot arrive with a second opinion about
// what a tenant is.

import (
	"errors"
	"fmt"
	"strings"
)

// Sep divides a tenant key's two halves.
const Sep = "/"

// Qualify builds a tenant key from the brand whose issuer vouched for the caller
// and the org that caller acts for.
//
// It is one string because it is one key. The same value is the store index for
// alerts, cases, retained records, lists, suppressions, activations, field stats
// and model runs; it is the org column on a history row; and it is the HKDF salt
// every tokenisation key is derived from (pkg/token New: salt =
// "aml/token/org/" + org). An org name is unique within an issuer and not across
// issuers, so the bare org is not an identity: `acme` on lux.id and `acme` on
// zoolabs.id are two unrelated financial institutions. Keyed on the bare org they
// are one — one set of rows, and worse, one vault: the same salt derives the same
// keys, so one brand's customer names tokenise to the other's pseudonyms and its
// sealed records open under the other's tenant. That is a cross-brand plaintext
// disclosure of retained personal data, against the purpose limitation retention
// is held to (Directive (EU) 2015/849 Art. 41(2)) and against HIP-0302, which is
// the requirement that the determinism stops at the tenant boundary.
//
// Qualifying at the key, rather than filtering on the brand afterwards, is what
// makes that leak uncomputable instead of merely disallowed: there is no query
// that returns the other brand's rows and no key that opens its vault. It is done
// before any record exists, because doing it later re-keys every vault —
// pseudonyms and sealed bodies both.
func Qualify(brandID, org string) (string, error) {
	b, ok := For(brandID)
	if !ok {
		return "", fmt.Errorf("no brand is registered as %q, so nothing vouches for this tenant", brandID)
	}
	org = strings.TrimSpace(org)
	if org == "" {
		return "", errors.New("no org, so the request acts for no tenant")
	}
	// Brand ids are a closed set and none contains the separator, so the first one
	// always ends the brand and the key is unambiguous either way. The org half is
	// held to it so the key stays readable back to the org it names: an operator
	// reading a retention row or a vault refusal has to be able to say which
	// institution it belongs to, and `zoo/lux/acme` does not say.
	if strings.Contains(org, Sep) {
		return "", fmt.Errorf("org %q contains %q, so the tenant it names is not readable back", org, Sep)
	}
	return b.ID + Sep + org, nil
}

// Qualified reports whether a key is one Qualify would have produced. Derived
// from Qualify rather than stated again, so there is one definition of the shape
// and a change to it cannot leave a validator behind.
func Qualified(key string) bool {
	brandID, org, found := strings.Cut(key, Sep)
	if !found {
		return false
	}
	again, err := Qualify(brandID, org)
	return err == nil && again == key
}

// ErrTenant is what a record plane returns for a key that is not brand-qualified.
//
// It is an error and never a filter, because the two failures are opposite in
// consequence: a refused write loses nothing, while a write admitted under a bare
// org lands in a tenant space shared with every other brand's institution of the
// same name — and the row is then indistinguishable from a legitimate one.
var ErrTenant = errors.New("tenant key is not <brand>/<org>")

// Tenant checks a key at the boundary of a record plane and names the failure.
//
// Every new plane calls this before it writes. The check exists in the planes and
// not only where the key is minted because the key arrives from a seam the
// deployment supplies: this is where the value crosses into a store index, and an
// unqualified org reaching one is the cross-brand collision.
func Tenant(key string) error {
	if !Qualified(key) {
		return fmt.Errorf("%w: %q", ErrTenant, key)
	}
	return nil
}
