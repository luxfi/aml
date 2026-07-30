// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package brand

import (
	"strings"
	"testing"
)

// A Host resolves to the brand that owns it, whether that is the brand's primary
// domain or one of the further domains it serves on. The alt-domain rows are the
// ones worth stating: a console on zoo.cloud is Zoo, and resolving it to the
// default brand would put a Hanzo identity on a Zoo surface.
func TestHostResolvesToItsBrand(t *testing.T) {
	for _, tc := range []struct {
		host, want string
	}{
		{"aml.lux.network", "lux"},
		{"lux.network", "lux"},
		{"console.lux.cloud", "lux"},
		{"api.hanzo.ai", "hanzo"},
		{"hanzo.app", "hanzo"},
		{"api.zoo.ngo", "zoo"},
		{"console.zoo.cloud", "zoo"},
		{"zoo.network", "zoo"},
		{"aml.pars.network", "pars"},
		{"pars.ai", "pars"},
		{"bootno.de", "bootnode"},

		// A Host arrives as the client wrote it: with a port, in any case, and
		// occasionally fully qualified with the root dot. All three name the same
		// brand, and a brand that depended on the spelling would be decided by the
		// caller's HTTP library.
		{"AML.Lux.Network:8090", "lux"},
		{"aml.lux.network.", "lux"},
		{"  api.zoo.ngo:443  ", "zoo"},
	} {
		if got, ok := ForHostOK(tc.host); !ok || got != tc.want {
			t.Errorf("ForHostOK(%q) = %q, %v; want %q, true", tc.host, got, ok, tc.want)
		}
	}
}

// A Host no brand claims resolves to nothing, so the caller chooses; ForHost is
// the caller that chooses the default. The failure this prevents is a surface on
// an unknown host quietly presenting some brand's issuer as its own.
func TestUnknownHostFallsBackToDefault(t *testing.T) {
	for _, host := range []string{"", "example.com", "aml.internal", "localhost", "notlux.network", "lux.network.evil.com"} {
		if got, ok := ForHostOK(host); ok {
			t.Errorf("ForHostOK(%q) = %q, true; want no match", host, got)
		}
		if got := ForHost(host); got != Default {
			t.Errorf("ForHost(%q) = %q, want %q", host, got, Default)
		}
	}
	if Default != "hanzo" {
		t.Errorf("Default = %q, want hanzo", Default)
	}
}

// The suffix match is on a domain label boundary. "notlux.network" ends in
// "lux.network" as a string and belongs to whoever registered it, so matching by
// raw suffix would hand an attacker's domain a first-party brand and its issuer.
func TestSuffixMatchIsOnALabelBoundary(t *testing.T) {
	for _, host := range []string{"notlux.network", "xhanzo.ai", "azoo.ngo"} {
		if got, ok := ForHostOK(host); ok {
			t.Errorf("ForHostOK(%q) = %q, true; a non-label suffix must not match", host, got)
		}
	}
}

// ForHostOK keeps the longest matching domain, which only decides anything if one
// registered domain is a suffix of another. None is today, so the rule is inert
// and the resolution is unambiguous whatever order the registry is walked in.
// This test is the guard on that precondition: adding a domain nested under
// another brand's makes the tie-break load-bearing, and the failure here is the
// notice to prove it rather than to assume it.
func TestNoDomainShadowsAnother(t *testing.T) {
	type owned struct{ id, domain string }
	var all []owned
	for id, b := range brands {
		for _, d := range append([]string{b.Domain}, b.AltDomains...) {
			all = append(all, owned{id, d})
		}
	}
	for _, a := range all {
		for _, b := range all {
			if a.id == b.id && a.domain == b.domain {
				continue
			}
			if a.domain == b.domain {
				t.Errorf("%s and %s both claim %s", a.id, b.id, a.domain)
			}
			if strings.HasSuffix(a.domain, "."+b.domain) {
				t.Errorf("%s (%s) is nested under %s (%s): the longest-match rule now decides a brand, so ForHostOK needs a test that proves which one wins",
					a.domain, a.id, b.domain, b.id)
			}
		}
	}
}

// Every brand states an issuer, and no two brands share one. A shared issuer
// would make the per-Host issuer pin meaningless — one brand's token would
// validate on the other's surface.
func TestEachBrandHasItsOwnIssuer(t *testing.T) {
	seen := map[string]string{}
	for id, b := range brands {
		if b.ID != id {
			t.Errorf("registry key %q holds ID %q", id, b.ID)
		}
		if !strings.HasPrefix(b.IAMIssuer, "https://") {
			t.Errorf("%s issuer %q is not https", id, b.IAMIssuer)
		}
		if b.Domain == "" {
			t.Errorf("%s has no domain", id)
		}
		if other, dup := seen[b.IAMIssuer]; dup {
			t.Errorf("%s and %s share issuer %s", id, other, b.IAMIssuer)
		}
		seen[b.IAMIssuer] = id
	}
}

// The issuers are the values the live IAM stamps as `iss`. They are asserted
// literally because a typo here does not fail loudly: it fails as "no caller can
// authenticate", and zoo is the row that catches a plausible guess — zoo.id does
// not resolve, and the live IAM stamps zoolabs.id.
func TestIssuersAreTheOnesIAMStamps(t *testing.T) {
	for id, want := range map[string]string{
		"hanzo":    "https://hanzo.id",
		"lux":      "https://lux.id",
		"zoo":      "https://zoolabs.id",
		"pars":     "https://pars.id",
		"bootnode": "https://id.bootno.de",
	} {
		if got := IssuerFor(id); got != want {
			t.Errorf("IssuerFor(%q) = %q, want %q", id, got, want)
		}
	}
}

// An unknown or oddly-spelled brand id resolves to the default rather than to a
// zero Info: an empty issuer would be an issuer no token can match, which reads
// as "every caller is refused" instead of "this id was wrong".
func TestUnknownIDFallsBackToDefault(t *testing.T) {
	for _, id := range []string{"", "nope", "  ", "LUX", " lux "} {
		got := For(id)
		if got.IAMIssuer == "" || got.Domain == "" {
			t.Errorf("For(%q) = %+v, want a populated Info", id, got)
		}
	}
	if For(" LUX ").ID != "lux" {
		t.Error("For is not case- and space-insensitive")
	}
	if For("nope").ID != Default {
		t.Errorf("For(unknown) = %q, want %q", For("nope").ID, Default)
	}
}

func TestDisplayIsDerivedFromTheID(t *testing.T) {
	for id, want := range map[string]string{
		"lux": "Lux", "hanzo": "Hanzo", "zoo": "Zoo", "bootnode": "Bootnode", "": "Hanzo",
	} {
		if got := Display(id); got != want {
			t.Errorf("Display(%q) = %q, want %q", id, got, want)
		}
	}
}
