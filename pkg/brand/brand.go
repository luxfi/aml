// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// Package brand maps a brand id, and a request Host, to that brand's public
// identity: the OIDC issuer whose JWKS signs its tokens, and the domains it
// serves on.
//
// One amld serves every brand's host, so which brand a request belongs to is a
// property of the request rather than of the deployment, and this is the one
// place the engine answers it. Everything downstream — which issuer may
// authenticate a caller, which identity a white-label console renders — reads the
// answer from here instead of deciding it again.
//
// The registry is a deliberate copy of the fleet's canonical white-label registry
// (HIP-0111; hanzoai/cloud brand/brand.go): the same brands, the same issuers, the
// same Host resolution. A copy and not an import because this package must stay a
// leaf — strings and nothing else, exactly as cloud's own brand package is one —
// and importing it would pull the whole cloud module in for forty lines of public
// facts. The cost of copying is drift, so the values are the entire content of
// this file and both sides read as the same short table.
//
// Nothing here is a secret: an issuer host and a brand domain are published
// facts, so they live in code. Key material lives in KMS and never here.
package brand

import "strings"

// Info is a brand's public identity.
type Info struct {
	// ID is the canonical brand key.
	ID string
	// IAMIssuer is the OIDC issuer for this brand: the value a token's `iss` claim
	// must equal, and the host whose /v1/iam/.well-known/jwks signs it.
	IAMIssuer string
	// Domain is the brand's primary domain, used for scoping and base-URL
	// derivation.
	Domain string
	// AltDomains are further registrable domains belonging to the same brand, used
	// only to resolve a Host to a brand. A brand serves on more than its primary
	// domain — a console runs on <brand>.cloud — and a request arriving there must
	// resolve to its own brand rather than falling through to the default one.
	AltDomains []string
}

// brands is the registry. Per HIP-0111: hanzo→hanzo.id, lux→lux.id,
// zoo→zoolabs.id (zoo.id does not resolve; the live IAM stamps iss=zoolabs.id),
// pars→pars.id, bootnode→id.bootno.de.
//
// IAMIssuer must equal the `iss` that brand's IAM actually stamps and must host
// the signing JWKS. Pinning a routing alias (iam.hanzo.ai rather than hanzo.id)
// instead would fail the issuer check on every real token, and a compliance
// engine that authenticates nobody serves nobody.
var brands = map[string]Info{
	"hanzo":    {ID: "hanzo", IAMIssuer: "https://hanzo.id", Domain: "hanzo.ai", AltDomains: []string{"hanzo.cloud", "hanzo.app"}},
	"lux":      {ID: "lux", IAMIssuer: "https://lux.id", Domain: "lux.network", AltDomains: []string{"lux.cloud"}},
	"zoo":      {ID: "zoo", IAMIssuer: "https://zoolabs.id", Domain: "zoo.ngo", AltDomains: []string{"zoo.network", "zoo.cloud"}},
	"pars":     {ID: "pars", IAMIssuer: "https://pars.id", Domain: "pars.network", AltDomains: []string{"pars.ai"}},
	"bootnode": {ID: "bootnode", IAMIssuer: "https://id.bootno.de", Domain: "bootno.de"},
}

// Default is the brand for a Host that matches none.
const Default = "hanzo"

// For returns a brand id's Info, falling back to the default brand for an
// unknown id. Lookup is case-insensitive.
func For(id string) Info {
	if b, ok := brands[strings.ToLower(strings.TrimSpace(id))]; ok {
		return b
	}
	return brands[Default]
}

// IssuerFor returns the OIDC issuer for a brand id.
func IssuerFor(id string) string {
	return For(id).IAMIssuer
}

// ForHostOK resolves a request Host to a brand id: a Host at or under one of a
// brand's domains (lux.network, aml.lux.network) is that brand. The port is
// stripped, a trailing root dot is stripped, and the compare is case-insensitive.
// The longest matching domain wins, so a domain nested under another brand's is
// never shadowed by the shorter one — see TestNoDomainShadowsAnother, which holds
// the registry to the shape that makes this rule inert today.
//
// ok is false when nothing matches, so the caller decides its own fallback rather
// than being handed a brand the request never named.
func ForHostOK(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimSuffix(host, ".")
	best, bestLen := "", -1
	for id, b := range brands {
		for _, d := range append([]string{b.Domain}, b.AltDomains...) {
			d = strings.ToLower(d)
			if d == "" {
				continue
			}
			if (host == d || strings.HasSuffix(host, "."+d)) && len(d) > bestLen {
				best, bestLen = id, len(d)
			}
		}
	}
	return best, best != ""
}

// ForHost is ForHostOK with the default brand for an unmatched Host.
func ForHost(host string) string {
	if b, ok := ForHostOK(host); ok {
		return b
	}
	return Default
}

// Display is a brand id's human name: the id with an upper-cased first letter.
// Derived from the id so there is no second list to keep in step with the
// registry.
func Display(id string) string {
	if id == "" {
		id = Default
	}
	return strings.ToUpper(id[:1]) + id[1:]
}
