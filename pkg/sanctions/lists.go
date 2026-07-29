// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package sanctions

import "github.com/luxfi/aml/pkg/types"

// The EU Financial Sanctions File is served only with this token in the query
// string; without it the endpoint answers 403. The Commission publishes the value
// for anonymous access to the public list — it is not a credential.
const euPublicToken = "dG9rZW4tMjAxNw"

// DefaultLists returns the built-in sanctions list sources.
//
// Two of these endpoints move on the publisher's schedule and both have broken
// once already. Verify against the publisher before editing a URL here:
//
//   - The UK source is the FCDO UK Sanctions List, NOT the OFSI Consolidated
//     List. OFSI closed that list on 28 January 2026, and from that date the UK
//     Sanctions List is the only source gov.uk directs anyone to for UK
//     designations.
//
//     Take care here, because "closed" and "unreachable" are not the same and
//     the paths disagree. Measured 2026-07-29: publishlive/ConList.xml and
//     publishlive/ConList.csv both answer 404, while the legacy
//     publishlive/2022format/ConList.csv still answers 200 and still carried a
//     Last Updated of 03/06/2026. A live legacy blob the publisher no longer
//     documents is the worst of the options — it can stop without notice and
//     nothing would report that, which is the failure this whole file is about.
//     Use the documented source.
//   - The EU file needs the token above. Requested without it the endpoint
//     answers 403 rather than redirecting to an authenticated form.
func DefaultLists() []types.SanctionsList {
	return []types.SanctionsList{
		{
			Source: types.ListOFACSDN,
			URL:    "https://www.treasury.gov/ofac/downloads/sdn.xml",
			Format: "xml",
			Active: true,
		},
		{
			Source: types.ListUN,
			URL:    "https://scsanctions.un.org/resources/xml/en/consolidated.xml",
			Format: "xml",
			Active: true,
		},
		{
			Source: types.ListEU,
			URL:    "https://webgate.ec.europa.eu/fsd/fsf/public/files/xmlFullSanctionsList_1_1/content?token=" + euPublicToken,
			Format: "xml",
			Active: true,
		},
		{
			Source: types.ListHMT,
			URL:    "https://sanctionslist.fcdo.gov.uk/docs/UK-Sanctions-List.xml",
			Format: "xml",
			Active: true,
		},
	}
}
