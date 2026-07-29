package engine

import "github.com/luxfi/aml/pkg/standard"

// citationFixture is a syntactically complete citation for tests that only need
// to observe that citations travel with an alert.
func citationFixture() []standard.Citation {
	return []standard.Citation{{
		Authority: standard.FATF,
		Document:  "FATF Recommendations",
		Locator:   "R.20",
		URL:       "https://www.fatf-gafi.org/",
	}}
}
