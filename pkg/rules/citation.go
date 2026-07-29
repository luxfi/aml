package rules

import "github.com/luxfi/aml/pkg/standard"

// The citations below were read from the primary text at the URL each names.
// Where a locator was checked against the document and the document's own wording
// differs from the common description of it, the document wins and the difference
// is recorded in a comment — those are the places a coverage claim is most often
// wrong.

// FATF. The Recommendations are published at an evergreen path and updated in
// place, so the document is cited by its span and its stated update.
const fatfDoc = "FATF, International Standards on Combating Money Laundering and the Financing of Terrorism & Proliferation — The FATF Recommendations (2012-2026, updated June 2026)"
const fatfURL = "https://www.fatf-gafi.org/content/dam/fatf-gafi/recommendations/fatf-recommendations-2012.pdf"

func fatf(locator string) standard.Citation {
	return standard.Citation{Authority: standard.FATF, Document: fatfDoc, Locator: locator, URL: fatfURL}
}

var (
	// fatfRBA is the risk-based approach: mitigating measures proportionate to the
	// risks identified.
	fatfRBA = fatf("Recommendation 1")

	// fatfCDD is customer due diligence. Measure (d) is the one a monitoring
	// system implements, and it is worth quoting because it defines the test the
	// behavioural rules apply: transactions must be "consistent with the
	// institution's knowledge of the customer, their business and risk profile".
	fatfCDD      = fatf("Recommendation 10, measures (a)-(d)")
	fatfOngoing  = fatf("Recommendation 10, measure (d)")
	fatfRecords  = fatf("Recommendation 11")
	fatfPEP      = fatf("Recommendation 12, measures (a)-(d)")
	fatfCorresp  = fatf("Recommendation 13, measures (a)-(e)")
	fatfNewTech  = fatf("Recommendation 15; INR.15 paragraph 7(a)-(b)")
	fatfHighRisk = fatf("Recommendation 19; INR.19 paragraphs 1-2")

	// fatfReport is the reporting obligation. INR.20 paragraph 3 is the sentence
	// that removes any threshold: "All suspicious transactions, including attempted
	// transactions, should be reported regardless of the amount of the
	// transaction." A monitoring product that only evaluates settled transactions
	// above a floor does not meet it.
	fatfReport    = fatf("Recommendation 20; INR.20 paragraph 3")
	fatfTippedOff = fatf("Recommendation 21, (a)-(b)")

	// fatfPayment is Recommendation 16. Its title is "Payment transparency": FATF
	// revised and renamed it in June 2025, recorded in Annex II of the current
	// document. The pre-revised text is still hosted separately and must not be
	// cited as current. INR.16 paragraph 8 sets the cross-border de minimis at "no
	// higher than USD/EUR 1 000", which is a different figure from the US
	// recordkeeping threshold and is frequently conflated with it.
	fatfPayment = fatf("Recommendation 16 (Payment transparency); INR.16 paragraph 8")
)

// FinCEN and the US Code.
func cfr(section, locator string) standard.Citation {
	return standard.Citation{
		Authority: standard.FinCEN,
		Document:  "31 CFR " + section,
		Locator:   locator,
		URL:       "https://www.ecfr.gov/current/title-31/section-" + section,
	}
}

var (
	// cfrCurrency carries the threshold — "more than $10,000" — but states no
	// aggregation period. The period is in the aggregation section, and citing only
	// the first is how a point-in-time threshold check comes to be described as
	// implementing currency reporting.
	cfrCurrency = cfr("1010.311", "§ 1010.311")

	// cfrAggregate supplies the period: multiple currency transactions are one
	// transaction where the institution knows they are "by or on behalf of any
	// person" and total more than $10,000 "during any one business day". Night and
	// weekend deposits count to the next business day.
	cfrAggregate = cfr("1010.313", "§ 1010.313(b)")

	cfrStructuring = cfr("1010.314", "§ 1010.314(a)-(c)")

	// cfrTransmittalSend and cfrTransmittalKeep are the travel rule and its
	// recordkeeping companion, both at "$3,000 or more". The section carries a
	// published amendment delayed to 1 January 2028, so the figure is current but
	// dated.
	cfrTransmittalSend = cfr("1010.410", "§ 1010.410(f)(1)")
	cfrTransmittalKeep = cfr("1010.410", "§ 1010.410(e)(1)")

	// cfrSuspicious is the reporting obligation. There is one threshold in this
	// section — "involves or aggregates at least $5,000 in funds or other assets" —
	// and it applies whether or not a suspect is identified. The familiar
	// $5,000-with-suspect and $25,000-without split belongs to the federal banking
	// agencies' parallel rules in 12 CFR, not here; within this section the suspect
	// distinction affects only the deadline.
	cfrSuspicious = cfr("1020.320", "§ 1020.320(a)(2)")

	// cfrNoPurpose is the limb of the suspicion test the behavioural rules serve:
	// a transaction with "no business or apparent lawful purpose or is not the sort
	// in which the particular customer would normally be expected to engage".
	cfrNoPurpose = cfr("1020.320", "§ 1020.320(a)(2)(iii)")

	// cfrDeadline is the filing clock: 30 calendar days from initial detection, a
	// further 30 where no suspect has been identified, and never more than 60.
	cfrDeadline = cfr("1020.320", "§ 1020.320(b)(3)")

	// cfrRetain requires the report and its supporting documentation to be kept
	// five years from filing.
	cfrRetain = cfr("1020.320", "§ 1020.320(d)")

	cfrProgramme = cfr("1020.210", "§ 1020.210")

	usCode = standard.Citation{
		Authority: standard.FinCEN,
		Document:  "31 U.S.C. 5324 — Structuring transactions to evade reporting requirement prohibited",
		Locator:   "§ 5324(a)(1)-(3)",
		URL:       "https://uscode.house.gov/view.xhtml?req=granuleid:USC-prelim-title31-section5324&num=0&edition=prelim",
	}
)

// European Union.
var (
	// euMonitoring makes ongoing monitoring a due-diligence measure in the same
	// terms as the FATF standard.
	euMonitoring = standard.Citation{
		Authority: standard.EU,
		Document:  "Directive (EU) 2015/849 on the prevention of the use of the financial system for the purposes of money laundering or terrorist financing",
		Locator:   "Article 13(1)(d)",
		URL:       "https://eur-lex.europa.eu/legal-content/EN/TXT/HTML/?uri=CELEX:32015L0849",
	}

	// euPEP requires enhanced ongoing monitoring of politically exposed customers.
	euPEP = standard.Citation{
		Authority: standard.EU,
		Document:  "Directive (EU) 2015/849 on the prevention of the use of the financial system for the purposes of money laundering or terrorist financing",
		Locator:   "Article 20(b)(iii)",
		URL:       "https://eur-lex.europa.eu/legal-content/EN/TXT/HTML/?uri=CELEX:32015L0849",
	}

	// euTransfer governs information accompanying transfers of crypto-assets, and
	// it applies with no de minimis: recital 30 requires the same treatment
	// "regardless of their amount", and Article 37(d) tasks the Commission with
	// assessing whether to introduce a threshold — which confirms there is none.
	// A crypto rule carrying a dollar or euro floor does not implement this.
	euTransfer = standard.Citation{
		Authority: standard.EU,
		Document:  "Regulation (EU) 2023/1113 on information accompanying transfers of funds and certain crypto-assets (recast)",
		Locator:   "Article 14; recital 30",
		URL:       "https://eur-lex.europa.eu/legal-content/EN/TXT/HTML/?uri=CELEX:32023R1113",
	}
)

// United Kingdom.
var (
	ukMonitoring = standard.Citation{
		Authority: standard.UK,
		Document:  "The Money Laundering, Terrorist Financing and Transfer of Funds (Information on the Payer) Regulations 2017 (S.I. 2017/692)",
		Locator:   "regulation 28(11)",
		URL:       "https://www.legislation.gov.uk/uksi/2017/692/regulation/28",
	}

	// ukRecords sets five years, caps transaction records within a relationship at
	// ten, and requires deletion once the period expires. The deletion duty is the
	// one a retention design usually misses.
	ukRecords = standard.Citation{
		Authority: standard.UK,
		Document:  "The Money Laundering, Terrorist Financing and Transfer of Funds (Information on the Payer) Regulations 2017 (S.I. 2017/692)",
		Locator:   "regulation 40(3)-(5)",
		URL:       "https://www.legislation.gov.uk/uksi/2017/692/regulation/40",
	}
)

// Wolfsberg. The 2024 statement is the industry recommendation that monitoring
// move beyond rules alone, combining customer behaviour and attributes with
// transactions, and be measured on the usefulness of its outputs rather than on
// the number of reports filed. It is the standard a behavioural measure answers to.
var wolfsbergMonitoring = standard.Citation{
	Authority: standard.Wolfsberg,
	Document:  "The Wolfsberg Group Statement on Effective Monitoring for Suspicious Activity — Part I: Moving Beyond Automated Transaction Monitoring",
	Locator:   "Part I, whole statement",
	URL:       "https://db.wolfsberg-group.org/assets/e3d83d2f-fad9-46d2-b5a9-3cf4e932f53f/Wolfsberg%20Group%20Statement%20on%20Effective%20Monitoring%20for%20Suspicious%20Activity.pdf",
}
