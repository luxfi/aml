// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

import (
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
func (h *Handler) brandConfig() func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		b := brand.For(brand.ForHost(e.Request.Host))
		return e.JSON(http.StatusOK, map[string]string{
			"brand":   b.ID,
			"display": brand.Display(b.ID),
			"issuer":  b.IAMIssuer,
			"domain":  b.Domain,
		})
	}
}
