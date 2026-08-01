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

// Caller is who a request is authenticated as: the tenant it acts for, and the
// subject that authenticated.
//
// Two projections of ONE verified credential, because they are read for two
// different reasons and both are read from the same signature. Tenant indexes
// every store; Subject is what a governed record names as its decider, and it is
// the answer to "who turned this off". A design that took the second from the
// request body would be attributing the institution's decisions to whatever text
// a caller sent.
//
// Subject may be empty where the deployment's identity cannot name one. That is
// not an authentication failure — reads and ingest are the tenant's work, not a
// person's — but it refuses every operation that records a decision, because a
// decision naming nobody is the state this type exists to prevent.
type Caller struct {
	Tenant  string
	Subject string
}

// Identity resolves who a request is authenticated as, or returns an error if it
// is not authenticated.
//
// It is a seam rather than an implementation because this package must not be the
// second place that decides what a valid credential is. The deployment supplies
// one, and every route asks it the same question.
type Identity func(*http.Request) (Caller, error)

// ErrNoIdentity is returned when no Identity is configured.
var ErrNoIdentity = errors.New("no identity configured, so no request can be attributed to a tenant")

// sep divides a tenant key's two halves.
const sep = brand.Sep

// qualify and qualified are the tenant key, and they live in pkg/brand because
// the key is a brand fact: an org is only an identity once the brand whose issuer
// vouched for it is named. They are aliases rather than a second implementation
// so that the record planes below this package — lists, suppressions,
// activations, field statistics, model runs — hold the same shape at their own
// write boundary without any of them getting a private opinion about what a
// tenant is. See brand.Qualify for why the key is one string and what a bare org
// collides.
var (
	qualify   = brand.Qualify
	qualified = brand.Qualified
)

// TrustedProxyHeader resolves the tenant's org from a header written by an
// authenticating proxy, and its brand from the request's own Host. subject names
// the header carrying the authenticated user, which is what a governed record
// records as its decider; an empty name means the proxy states none, and every
// operation that records a decision then refuses.
//
// This is only sound where the proxy authenticates the caller, sets these headers
// from the verified token, strips any client-supplied copy of them, and is the
// sole route to this service. Where the service is directly reachable, a header is
// an unauthenticated assertion and anyone can name any tenant — so this
// constructor exists to make that assumption something a deployment states out
// loud rather than something a reader has to infer from a comment.
//
// The brand half is never taken from a header, even where the org is. A proxy that
// forwards a client's own X-Forwarded-Host would otherwise let the caller choose
// which brand's tenant space its org lands in, which is the same cross-brand
// collision qualify exists to prevent, arrived at from the other side.
func TrustedProxyHeader(name, subject string) Identity {
	return func(r *http.Request) (Caller, error) {
		brandID, ok := brand.ForHostOK(r.Host)
		if !ok {
			return Caller{}, fmt.Errorf("host %q names no brand, so there is no tenant space to place an org in", r.Host)
		}
		org := r.Header.Get(name)
		if strings.TrimSpace(org) == "" {
			return Caller{}, fmt.Errorf("header %s is absent, so the caller named no tenant", name)
		}
		tenant, err := qualify(brandID, org)
		if err != nil {
			return Caller{}, err
		}
		who := Caller{Tenant: tenant}
		if subject != "" {
			who.Subject = strings.TrimSpace(r.Header.Get(subject))
		}
		return who, nil
	}
}

// caller returns who a request is authenticated as.
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
func (h *Handler) caller(e *core.RequestEvent) (Caller, error) {
	if h.Identity == nil {
		return Caller{}, ErrNoIdentity
	}
	who, err := h.Identity(e.Request)
	if err != nil {
		return Caller{}, err
	}
	if !qualified(who.Tenant) {
		return Caller{}, fmt.Errorf("identity resolved %q, which is not a <brand>%s<org> tenant", who.Tenant, sep)
	}
	return who, nil
}

// tenant is the caller's tenant, for the routes that need nothing else. It is a
// projection of caller and not a second resolution of the credential.
func (h *Handler) tenant(e *core.RequestEvent) (string, error) {
	who, err := h.caller(e)
	return who.Tenant, err
}
