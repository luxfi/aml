// Package standard models a citation to a published regulatory standard.
//
// Every rule in the detection library carries at least one citation. The
// citation is the reason the rule exists: it names the authority, the document,
// and the locator within that document that requires or describes the control.
// An examiner tests the institution against the standard, so a control that
// cannot name its standard cannot be defended.
package standard

import "fmt"

// Authority is a standard-setting or supervisory body.
type Authority string

const (
	// FATF is the Financial Action Task Force, which issues the 40
	// Recommendations, the assessment Methodology, and typology reports.
	FATF Authority = "FATF"
	// FinCEN is the US Financial Crimes Enforcement Network, which
	// administers the Bank Secrecy Act regulations at 31 CFR Chapter X
	// and issues advisories carrying red-flag indicators.
	FinCEN Authority = "FinCEN"
	// OFAC is the US Office of Foreign Assets Control, which publishes the
	// SDN and consolidated sanctions lists and their data specifications.
	OFAC Authority = "OFAC"
	// UN is the United Nations Security Council, whose committees maintain
	// the consolidated sanctions list.
	UN Authority = "UN"
	// EU is the European Union: the anti-money-laundering directives and
	// regulations, and the consolidated CFSP financial sanctions list.
	EU Authority = "EU"
	// UK covers UK statute and statutory instruments, and the HM Treasury
	// and OFSI financial sanctions regime.
	UK Authority = "UK"
	// Wolfsberg is the Wolfsberg Group, an association of international
	// banks publishing industry standards for financial crime compliance.
	Wolfsberg Authority = "Wolfsberg"
	// FFIEC is the US Federal Financial Institutions Examination Council,
	// whose BSA/AML examination manual states what examiners test.
	FFIEC Authority = "FFIEC"
	// BIS is the US Bureau of Industry and Security, which issues joint
	// export-control evasion alerts with FinCEN.
	BIS Authority = "BIS"
)

// Citation locates a requirement or indicator within a published document.
//
// Locator is the smallest addressable unit the document offers: a
// Recommendation number, an interpretive-note paragraph, a CFR section, an
// article, a regulation number, or a named indicator group. It is what a
// reviewer types into the document to land on the text.
type Citation struct {
	Authority Authority `json:"authority"`
	Document  string    `json:"document"`
	Locator   string    `json:"locator"`
	URL       string    `json:"url"`
}

// String renders the citation for a narrative or an audit export.
func (c Citation) String() string {
	return fmt.Sprintf("%s, %s, %s", c.Authority, c.Document, c.Locator)
}

// Valid reports whether the citation is complete enough to defend a control.
// A citation missing any field cannot be checked by a reviewer, so the
// library rejects it rather than shipping a claim nobody can verify.
func (c Citation) Valid() bool {
	return c.Authority != "" && c.Document != "" && c.Locator != "" && c.URL != ""
}
