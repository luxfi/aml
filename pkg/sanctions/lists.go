package sanctions

import "github.com/luxfi/aml/pkg/types"

// DefaultLists returns the default sanctions list configurations.
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
			URL:    "https://webgate.ec.europa.eu/fsd/fsf/public/files/xmlFullSanctionsList_1_1/content",
			Format: "xml",
			Active: true,
		},
		{
			Source: types.ListHMT,
			URL:    "https://ofsistorage.blob.core.windows.net/publishlive/ConList.xml",
			Format: "xml",
			Active: true,
		},
	}
}
