// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

// The tenant of a request: which brand's issuer vouched for the caller, and which
// customer organisation — a financial institution — it is acting for. One value,
// minted in one place, because it is one key.

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/pkg/brand"
)

// Identity resolves the tenant a request is authenticated to act on, or returns
// an error if it is not authenticated.
//
// It is a seam rather than an implementation because this package must not be the
// second place that decides what a valid credential is. The deployment supplies
// one, and every route asks it the same question.
type Identity func(*http.Request) (tenant string, err error)

// ErrNoIdentity is returned when no Identity is configured.
var ErrNoIdentity = errors.New("no identity configured, so no request can be attributed to a tenant")

// sep divides a tenant key's two halves.
const sep = "/"

// qualify builds a tenant key from the brand whose issuer vouched for the caller
// and the org that caller acts for.
//
// It is one string because it is one key. The same value is the store index for
// alerts, cases and retained records, the org column on a history row, and the
// HKDF salt every tokenisation key is derived from (pkg/token New: salt =
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
// here, before any record exists, because doing it later re-keys every vault —
// pseudonyms and sealed bodies both.
func qualify(brandID, org string) (string, error) {
	b, ok := brand.For(brandID)
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
	if strings.Contains(org, sep) {
		return "", fmt.Errorf("org %q contains %q, so the tenant it names is not readable back", org, sep)
	}
	return b.ID + sep + org, nil
}

// qualified reports whether a key is one qualify would have produced. Derived from
// qualify rather than stated again, so there is one definition of the shape and a
// change to it cannot leave a validator behind.
func qualified(key string) bool {
	brandID, org, found := strings.Cut(key, sep)
	if !found {
		return false
	}
	again, err := qualify(brandID, org)
	return err == nil && again == key
}

// TrustedProxyHeader resolves the tenant's org from a header written by an
// authenticating proxy, and its brand from the request's own Host.
//
// This is only sound where the proxy authenticates the caller, sets this header
// from the verified token, strips any client-supplied copy of it, and is the sole
// route to this service. Where the service is directly reachable, the header is
// an unauthenticated assertion and anyone can name any tenant — so this
// constructor exists to make that assumption something a deployment states out
// loud rather than something a reader has to infer from a comment.
//
// The brand half is never taken from a header, even where the org is. A proxy that
// forwards a client's own X-Forwarded-Host would otherwise let the caller choose
// which brand's tenant space its org lands in, which is the same cross-brand
// collision qualify exists to prevent, arrived at from the other side.
func TrustedProxyHeader(name string) Identity {
	return func(r *http.Request) (string, error) {
		brandID, ok := brand.ForHostOK(r.Host)
		if !ok {
			return "", fmt.Errorf("host %q names no brand, so there is no tenant space to place an org in", r.Host)
		}
		org := r.Header.Get(name)
		if strings.TrimSpace(org) == "" {
			return "", fmt.Errorf("header %s is absent, so the caller named no tenant", name)
		}
		return qualify(brandID, org)
	}
}

// tenant returns the authenticated tenant for a request.
//
// With no Identity configured it refuses. A compliance product that falls back to
// trusting a client-supplied tenant header is worse than one that will not serve:
// the fallback answers every request with another tenant's records and looks
// healthy doing it.
//
// The key's shape is checked here and not only where it is minted, because the
// Identity is supplied by the deployment: this is the boundary the value crosses
// into the store index, the history column and the vault salt, and an unqualified
// org reaching any of those is the cross-brand collision. An Identity that returns
// a bare org authenticates nobody.
func (h *Handler) tenant(e *core.RequestEvent) (string, error) {
	if h.Identity == nil {
		return "", ErrNoIdentity
	}
	id, err := h.Identity(e.Request)
	if err != nil {
		return "", err
	}
	if !qualified(id) {
		return "", fmt.Errorf("identity resolved %q, which is not a <brand>%s<org> tenant", id, sep)
	}
	return id, nil
}
