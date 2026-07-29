// Package rules holds the detection library.
//
// Every rule names the typology it detects and cites the published requirement it
// implements. The citation is not decoration: it is what lets a reviewer read the
// standard, read the expression, and decide whether the second implements the
// first. A rule that cannot name its standard is a rule nobody can defend, and the
// library test rejects one.
//
// Thresholds are values in the expression rather than constants in Go, so an
// institution can tighten one for its own risk appetite and the rule text shows
// what was in force. Where a regulation states a figure, the rule uses that figure
// and the citation says where it comes from.
package rules

import (
	"github.com/luxfi/aml/pkg/standard"
	"github.com/luxfi/aml/pkg/types"
)

// Typologies detected by this library.
const (
	Structuring   = "structuring"
	Currency      = "currency reporting"
	Transmittal   = "funds transmittal"
	Velocity      = "velocity"
	Layering      = "layering"
	Dormancy      = "dormancy"
	Concentration = "concentration"
	Behaviour     = "behavioural deviation"
	Sanctions     = "sanctions exposure"
	Geography     = "geographic risk"
	Exposure      = "political exposure"
	RoundAmount   = "round amounts"
)

// Library returns the detection rules for an organisation.
func Library(orgID string) []types.Rule {
	rules := []types.Rule{
		{
			ID:          "currency-day",
			Name:        "Currency transactions over the reporting threshold in one business day",
			Typology:    Currency,
			Description: "The customer's transactions total more than $10,000 across one business day in the institution's own time zone. The day is a calendar day, not a rolling twenty-four hours, because the regulation aggregates transactions by or on behalf of one person totalling more than the threshold during any one business day.",
			DSL:         `Day("user") > 10000.0`,
			Citations:   []standard.Citation{cfrCurrency, cfrAggregate},
			Severity:    types.SeverityHigh,
			Weight:      0.30,
			Action:      types.ActionReport,
			Priority:    1,
		},
		{
			ID:          "structuring-day",
			Name:        "Sub-threshold transactions aggregating over the reporting threshold in a day",
			Typology:    Structuring,
			Description: "Three or more transactions each below $10,000 that together reach it within a day. This is the detectable signature of causing the institution to fail to file a required report; a band check on one amount cannot see it, and neither can a threshold check on each amount alone.",
			DSL:         `Structured("user", "24h", 10000.0, 3)`,
			Citations:   []standard.Citation{cfrStructuring, usCode},
			Severity:    types.SeverityHigh,
			Weight:      0.35,
			Action:      types.ActionReview,
			Priority:    2,
		},
		{
			ID:          "structuring-week",
			Name:        "Sub-threshold transactions aggregating over a week",
			Typology:    Structuring,
			Description: "The same pattern spread over a week, which a daily window does not see. Five or more sub-threshold transactions reaching the threshold in aggregate.",
			DSL:         `Structured("user", "7d", 10000.0, 5)`,
			Citations:   []standard.Citation{cfrStructuring, usCode},
			Severity:    types.SeverityMedium,
			Weight:      0.25,
			Action:      types.ActionReview,
			Priority:    3,
		},
		{
			ID:          "structuring-accounts",
			Name:        "Sub-threshold transactions split across the customer's own accounts",
			Typology:    Structuring,
			Description: "Sub-threshold amounts reaching the threshold when the customer's accounts are counted together. Aggregation is by or on behalf of a person, so splitting across accounts the same person controls does not defeat it.",
			DSL:         `Distinct("user", "account", "24h") > 1 && Structured("user", "24h", 10000.0, 2)`,
			Citations:   []standard.Citation{cfrAggregate, cfrStructuring},
			Severity:    types.SeverityHigh,
			Weight:      0.30,
			Action:      types.ActionReview,
			Priority:    4,
		},
		{
			ID:          "structuring-near-threshold",
			Name:        "Amount just below the reporting threshold",
			Typology:    Structuring,
			Description: "A single amount in the tenth immediately below $10,000. Reported on its own, because proximity to a reporting threshold is an indicator whether or not the amounts also aggregate over it.",
			DSL:         `Near(10000.0, 0.1)`,
			Citations:   []standard.Citation{cfrStructuring},
			Severity:    types.SeverityLow,
			Weight:      0.10,
			Action:      types.ActionFlag,
			Priority:    5,
		},
		{
			ID:          "transmittal-recordkeeping",
			Name:        "Transmittal of funds at or over the recordkeeping threshold",
			Typology:    Transmittal,
			Description: "A transmittal of funds of $3,000 or more, at which point the originator and beneficiary information the regulation lists must be obtained, retained, and included in the order passed to the next institution.",
			DSL:         `USD() >= 3000.0`,
			Citations:   []standard.Citation{cfrTransmittalSend, cfrTransmittalKeep},
			Severity:    types.SeverityLow,
			Weight:      0.05,
			Action:      types.ActionReport,
			Priority:    6,
		},
		{
			ID:          "transmittal-payment-transparency",
			Name:        "Cross-border transfer over the payment transparency de minimis",
			Typology:    Transmittal,
			Description: "A cross-border transfer over USD/EUR 1,000, the highest de minimis the payment transparency standard permits. This is a different and lower figure than the US recordkeeping threshold, and the two are routinely conflated; both rules exist so that neither obligation is met only by accident.",
			DSL:         `USD() > 1000.0 && Entity.Jurisdiction != "" && Tier(Entity.Jurisdiction) != "domestic"`,
			Citations:   []standard.Citation{fatfPayment},
			Severity:    types.SeverityLow,
			Weight:      0.05,
			Action:      types.ActionReport,
			Priority:    7,
		},
		{
			ID:               "transmittal-crypto",
			Name:             "Transfer of crypto-assets, at any amount",
			Typology:         Transmittal,
			Description:      "A transfer of crypto-assets must be accompanied by originator and beneficiary information regardless of amount: the recast transfer regulation applies the same requirements whatever the size of the transfer, and the review clause asks whether a de minimis should be introduced, which confirms none exists. A crypto rule carrying a value floor does not implement this, so this rule carries none.",
			DSL:              `Tx.Counterparty != ""`,
			Citations:        []standard.Citation{euTransfer, fatfNewTech},
			Severity:         types.SeverityLow,
			Weight:           0.05,
			Action:           types.ActionReport,
			Enabled:          true,
			Priority:         8,
			AssetClassFilter: []string{"crypto"},
		},
		{
			ID:          "sanctions-customer",
			Name:        "Customer matches a sanctions list",
			Typology:    Sanctions,
			Description: "The customer's name matches a designated subject on a published list, after folding and transliteration, with any date-of-birth or nationality conflict taken into account and recorded.",
			DSL:         `Entity.Name != "" && Screened(Entity.Name, "sanctions")`,
			Citations:   []standard.Citation{cfrProgramme, fatfRBA},
			Severity:    types.SeverityCritical,
			Weight:      0.50,
			Action:      types.ActionBlock,
			Priority:    9,
		},
		{
			ID:          "sanctions-counterparty",
			Name:        "Counterparty matches a sanctions list",
			Typology:    Sanctions,
			Description: "The counterparty named on the transaction matches a designated subject.",
			DSL:         `Tx.Counterparty != "" && Screened(Tx.Counterparty, "sanctions")`,
			Citations:   []standard.Citation{cfrProgramme, fatfRBA},
			Severity:    types.SeverityCritical,
			Weight:      0.50,
			Action:      types.ActionBlock,
			Priority:    10,
		},
		{
			ID:          "exposure-political",
			Name:        "Politically exposed customer transacting at scale",
			Typology:    Exposure,
			Description: "A customer identified as politically exposed transacting over $10,000. The standard requires enhanced ongoing monitoring of such a relationship, in addition to senior management approval and establishing source of wealth and funds, which are decisions outside this engine.",
			DSL:         `Entity.Name != "" && Screened(Entity.Name, "pep") && USD() > 10000.0`,
			Citations:   []standard.Citation{fatfPEP, euPEP},
			Severity:    types.SeverityHigh,
			Weight:      0.25,
			Action:      types.ActionReview,
			Priority:    11,
		},
		{
			ID:          "geography-countermeasures",
			Name:        "Jurisdiction for which countermeasures are called for",
			Typology:    Geography,
			Description: "The customer's jurisdiction appears on the loaded listing of countries for which countermeasures are called for.",
			DSL:         `Tier(Entity.Jurisdiction) == "action"`,
			Citations:   []standard.Citation{fatfHighRisk},
			Severity:    types.SeverityCritical,
			Weight:      0.45,
			Action:      types.ActionBlock,
			Priority:    12,
		},
		{
			ID:          "geography-monitoring",
			Name:        "Jurisdiction under increased monitoring",
			Typology:    Geography,
			Description: "The customer's jurisdiction appears on the loaded listing of countries under increased monitoring, which calls for enhanced due diligence proportionate to the risk rather than refusal.",
			DSL:         `Tier(Entity.Jurisdiction) == "monitoring"`,
			Citations:   []standard.Citation{fatfHighRisk},
			Severity:    types.SeverityMedium,
			Weight:      0.20,
			Action:      types.ActionReview,
			Priority:    13,
		},
		{
			ID:          "layering-pass-through",
			Name:        "Funds passing straight through the account",
			Typology:    Layering,
			Description: "At least $10,000 arrived and left within a day, retaining no more than a twentieth. An account used as a conduit rather than to hold value is activity with no apparent business purpose.",
			DSL:         `InOut("user", "24h", 10000.0, 0.05)`,
			Citations:   []standard.Citation{cfrNoPurpose, fatfOngoing},
			Severity:    types.SeverityHigh,
			Weight:      0.30,
			Action:      types.ActionReview,
			Priority:    14,
		},
		{
			ID:          "velocity-count",
			Name:        "Transaction count far above an ordinary day",
			Typology:    Velocity,
			Description: "More than fifty transactions in a day. A count rather than a value, because splitting value into many small movements is precisely what defeats a value threshold.",
			DSL:         `Count("user", "24h") > 50`,
			Citations:   []standard.Citation{cfrNoPurpose, fatfOngoing},
			Severity:    types.SeverityMedium,
			Weight:      0.15,
			Action:      types.ActionReview,
			Priority:    15,
		},
		{
			ID:          "behaviour-deviation",
			Name:        "Transaction far outside the customer's established pattern",
			Typology:    Behaviour,
			Description: "The amount deviates from the customer's own ninety-day baseline by a robust score over 3.5, measured against the median and the median absolute deviation so that one large past transaction cannot inflate the baseline enough to hide the next one. This addresses the limb of the suspicion test covering a transaction not of the sort in which the particular customer would normally be expected to engage, and answers the industry recommendation that monitoring combine customer behaviour with transactions rather than rest on fixed rules alone.",
			DSL:         `Deviation("user", "90d", 10) > 3.5`,
			Citations:   []standard.Citation{cfrNoPurpose, fatfOngoing, euMonitoring, ukMonitoring, wolfsbergMonitoring},
			Severity:    types.SeverityMedium,
			Weight:      0.20,
			Action:      types.ActionReview,
			Priority:    16,
		},
		{
			ID:          "dormancy-reactivation",
			Name:        "Long-idle account reactivated with a large transaction",
			Typology:    Dormancy,
			Description: "An account idle more than 180 days transacting over $10,000. A dormant account that suddenly moves value is inconsistent with the pattern the institution knows.",
			DSL:         `Dormant("user", "730d") > 180.0 && USD() > 10000.0`,
			Citations:   []standard.Citation{cfrNoPurpose, fatfOngoing, euMonitoring, ukMonitoring},
			Severity:    types.SeverityMedium,
			Weight:      0.20,
			Action:      types.ActionReview,
			Priority:    17,
		},
		{
			ID:          "concentration-counterparties",
			Name:        "Value fanning out across many counterparties",
			Typology:    Concentration,
			Description: "More than twenty distinct counterparties in a day. Dispersal across many parties is how value is moved on once it has been placed.",
			DSL:         `Distinct("user", "counterparty", "24h") > 20`,
			Citations:   []standard.Citation{cfrNoPurpose, fatfOngoing},
			Severity:    types.SeverityMedium,
			Weight:      0.20,
			Action:      types.ActionReview,
			Priority:    18,
		},
		{
			ID:          "concentration-device",
			Name:        "Several customers transacting from one device",
			Typology:    Concentration,
			Description: "More than five distinct customers transacting from the same device in a week, which is how nominally unrelated accounts under one controller present. Guarded on the device being recorded, because a measure cannot group by an identifier the transaction did not carry, and the engine treats an absent subject as a failure rather than as an empty history.",
			DSL:         `Tx.DeviceFingerprint != "" && Distinct("device", "user", "7d") > 5`,
			Citations:   []standard.Citation{cfrNoPurpose, wolfsbergMonitoring},
			Severity:    types.SeverityHigh,
			Weight:      0.30,
			Action:      types.ActionReview,
			Priority:    19,
		},
		{
			ID:          "round-amounts",
			Name:        "A book of exactly round amounts",
			Typology:    RoundAmount,
			Description: "More than four fifths of at least ten transactions over a month are exact multiples of 1,000. Commercial amounts carry odd units; a book of round numbers indicates value moved for its own sake rather than to settle a trade.",
			DSL:         `Count("user", "30d") >= 10 && Round("user", "30d", 1000.0) > 0.8`,
			Citations:   []standard.Citation{cfrNoPurpose},
			Severity:    types.SeverityLow,
			Weight:      0.10,
			Action:      types.ActionFlag,
			Priority:    20,
		},
	}

	for i := range rules {
		rules[i].OrgID = orgID
		rules[i].Enabled = true
	}
	return rules
}

// Typologies returns the distinct typologies the library covers, in the order they
// first appear.
func Typologies() []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range Library("") {
		if !seen[r.Typology] {
			seen[r.Typology] = true
			out = append(out, r.Typology)
		}
	}
	return out
}

// Obligations returns the citations the library claims coverage of, deduplicated
// by authority, document and locator.
//
// This is the coverage claim in the form a reviewer can check: every requirement
// the product says it addresses, with the locator to read. It is generated from
// the rules rather than maintained beside them, so it cannot drift from what is
// actually installed.
func Obligations() []standard.Citation {
	var out []standard.Citation
	seen := map[string]bool{}
	for _, r := range Library("") {
		for _, c := range r.Citations {
			key := string(c.Authority) + "|" + c.Document + "|" + c.Locator
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, c)
		}
	}
	return out
}

// Gap is a requirement that has been located in the primary text and is known not
// to be implemented by this engine.
//
// Publishing these is the point. A compliance product is asked what it covers, and
// the honest answer has two halves; a product that ships only the first half
// leaves the buyer to discover the second during an examination. Each entry names
// the requirement, why the engine does not meet it, and what would.
type Gap struct {
	Citation standard.Citation `json:"citation"`
	Why      string            `json:"why"`
	Needs    string            `json:"needs"`
}

// Gaps returns the located requirements this engine does not implement.
func Gaps() []Gap {
	return []Gap{
		{
			Citation: fatfReport,
			Why:      "The engine raises alerts and opens cases, but there is no report workflow: no drafting, approval, filing, filing clock, or record that a report was made. It also evaluates transactions presented to it, whereas the standard requires attempted transactions to be reported regardless of amount, and a declined or abandoned attempt never reaches the engine unless the caller sends it.",
			Needs:    "A report lifecycle with a second-person approval step, and an ingestion contract that carries attempted and declined transactions as well as settled ones.",
		},
		{
			Citation: cfrSuspicious,
			Why:      "The reporting threshold and the four bases of suspicion are not modelled. Alerts carry a rule and a score, not a determination that one of the statutory bases is met, which is the finding a report has to assert.",
			Needs:    "A determination recorded against the named basis, and aggregation of a case's transactions against the threshold.",
		},
		{
			Citation: cfrDeadline,
			Why:      "Nothing measures the filing clock. There is no record of the date of initial detection, so the thirty-day deadline, the extension where no suspect is identified, and the sixty-day limit cannot be computed or enforced.",
			Needs:    "A detection timestamp on the case, and an escalation that measures against it rather than against case age.",
		},
		{
			Citation: cfrRetain,
			Why:      "Retention is not implemented and the current behaviour is contrary to it. Alerts are held in memory and evicted, and closed cases are discarded after ninety days, against a five-year obligation running from the date of filing.",
			Needs:    "Durable, append-only storage for alerts, cases and their timelines, with retention measured from filing and deletion only once the period expires.",
		},
		{
			Citation: ukRecords,
			Why:      "The same retention gap, plus a duty this one adds: personal data must be deleted once the period expires, and there is no deletion path at all.",
			Needs:    "A retention schedule that both preserves for the period and deletes at the end of it.",
		},
		{
			Citation: fatfRecords,
			Why:      "Five-year retention of transaction records, customer files and the results of analysis is not implemented for the analysis results, which are the alerts and case timelines this engine produces.",
			Needs:    "The same durable storage; the transaction records themselves are persisted.",
		},
		{
			Citation: fatfTippedOff,
			Why:      "Tipping-off is an access-control and disclosure question, and the engine applies no confidentiality classification to a case or its timeline. Nothing prevents a case note from being surfaced to a customer-facing surface.",
			Needs:    "A confidentiality marking on reports and case events, and an authorisation model that separates investigators from customer-facing roles.",
		},
		{
			Citation: fatfCorresp,
			Why:      "Correspondent banking is not modelled. The engine sees a transaction and one customer, so it cannot distinguish a respondent institution's underlying customer from the respondent itself, and none of the required measures are expressible.",
			Needs:    "A relationship model distinguishing respondent institutions from direct customers, and nested-relationship data on the transaction.",
		},
		{
			Citation: fatfCDD,
			Why:      "Due diligence itself is out of scope: the engine consumes a customer record with a name, jurisdiction and risk score, and does not collect or verify identity, establish beneficial ownership, or record the purpose of the relationship.",
			Needs:    "This belongs in an onboarding system. The engine's dependency is that the customer record it is given is populated, which today the ingestion path does not guarantee.",
		},
	}
}
