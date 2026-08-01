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
