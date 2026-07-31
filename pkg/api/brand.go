// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

import (
	"log"
	"net/http"

	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/pkg/brand"
)

// brandConfig reports the brand identity of the request's own Host: which brand
// this surface is, and which issuer a caller must get a token from to use it.
//
// It answers from the Host and not from configuration, which is what lets one
// amld serve every brand: a Lux console and a Zoo console read this same route on
// their own hosts and each renders its own identity. Deriving it from a startup
// flag instead would need one deployment per brand, and would silently brand the
// wrong one the first time a host moved.
//
// Alone among these routes it does not require a tenant. It cannot: the issuer it
// names is what a caller needs in order to obtain the token that would identify
// them, so requiring one would be a lock whose key is behind it. What it discloses
// is a brand's published identity — its issuer and domain are in DNS — so there is
// nothing here to withhold.
//
// A Host no brand claims gets no answer, through the same resolution the auth path
// uses (brand.ForHostOK). Answering with a default brand would make this route lie
// about the one thing it exists to state: it would send a caller to hanzo.id for a
// token that the auth path on that same host must then refuse, because there an
// unclaimed Host names no issuer to trust. One resolution, one answer, both paths.
func (h *Handler) brandConfig() func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		id, ok := brand.ForHostOK(e.Request.Host)
		if !ok {
			log.Printf("[aml] config asked on %q, which no brand claims", e.Request.Host)
			return fail(e, http.StatusNotFound, "no brand serves this host")
		}
		b, _ := brand.For(id)
		// The clientId is named here because this is the one place that already
		// answers "which identity does this surface have". A console that had to
		// be told its own clientId separately is a second place for the two to
		// disagree — and they disagree silently: a token minted for the wrong
		// audience is refused by the auth path with no hint as to why. It is not
		// a secret; IAM stamps it into every token as `aud`, and a public PKCE
		// client holds no credential at all.
		return e.JSON(http.StatusOK, map[string]string{
			"brand":     b.ID,
			"display":   brand.Display(b.ID),
			"issuer":    b.IAMIssuer,
			"domain":    b.Domain,
			"client_id": h.ClientID,
		})
	}
}
