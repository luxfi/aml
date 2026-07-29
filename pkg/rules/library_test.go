package rules

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/history"
	"github.com/luxfi/aml/pkg/reference"
	"github.com/luxfi/aml/pkg/types"
)

var at = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

// screen answers from a fixed set so the library can be installed and evaluated.
type screen struct{}

func (screen) Hit(_ context.Context, name, class string) (engine.Hit, error) {
	return engine.Hit{}, nil
}

func full() engine.Providers {
	return engine.Providers{
		History: history.NewMemory(func() time.Time { return at }),
		Screen:  screen{},
		Reference: reference.Jurisdictions{
			AsOf: at.AddDate(0, -1, 0), Action: []string{"KP"}, Monitoring: []string{"SY"},
		},
		Rate: reference.Rates{AsOf: at, USDPer: map[string]float64{"EUR": 1.08}},
		Now:  func() time.Time { return at },
		Zone: time.UTC,
	}
}

func TestEveryLibraryRuleIsAdmissible(t *testing.T) {
	// The library must install as a whole against a fully configured deployment.
	// If it does not, a rule references evidence the product does not have — which
	// is the defect this design exists to make impossible.
	e := engine.New(full())
	if err := e.SetRules(Library("org")); err != nil {
		t.Fatalf("the library must install in full:\n%v", err)
	}
	if n := len(e.Rules()); n != len(Library("org")) {
		t.Fatalf("installed %d of %d rules", n, len(Library("org")))
	}
}

func TestEveryRuleCitesAVerifiableRequirement(t *testing.T) {
	for _, r := range Library("org") {
		if len(r.Citations) == 0 {
			t.Errorf("rule %q cites nothing, so its coverage claim cannot be checked", r.ID)
			continue
		}
		for i, c := range r.Citations {
			if !c.Valid() {
				t.Errorf("rule %q citation %d is incomplete: %+v", r.ID, i, c)
			}
			if u, err := url.Parse(c.URL); err != nil || !strings.HasPrefix(u.Scheme, "http") || u.Host == "" {
				t.Errorf("rule %q citation %d has an unusable URL %q", r.ID, i, c.URL)
			}
			// A locator has to point somewhere inside the document. A citation to a
			// whole document is not something a reviewer can check.
			if len(strings.TrimSpace(c.Locator)) < 3 {
				t.Errorf("rule %q citation %d has no usable locator: %q", r.ID, i, c.Locator)
			}
		}
	}
}

func TestEveryRuleIsCompleteAndActionable(t *testing.T) {
	seen := map[string]bool{}
	priorities := map[int]string{}
	for _, r := range Library("org") {
		if seen[r.ID] {
			t.Errorf("duplicate rule identifier %q", r.ID)
		}
		seen[r.ID] = true

		if r.Typology == "" {
			t.Errorf("rule %q names no typology", r.ID)
		}
		if strings.TrimSpace(r.Description) == "" {
			t.Errorf("rule %q has no description, so an analyst reading the alert learns nothing", r.ID)
		}
		if r.Name == "" {
			t.Errorf("rule %q has no name", r.ID)
		}
		if r.Weight <= 0 || r.Weight > 1 {
			t.Errorf("rule %q has weight %v, want a value in (0,1]", r.ID, r.Weight)
		}
		switch r.Severity {
		case types.SeverityLow, types.SeverityMedium, types.SeverityHigh, types.SeverityCritical:
		default:
			t.Errorf("rule %q has severity %q", r.ID, r.Severity)
		}
		switch r.Action {
		case types.ActionFlag, types.ActionReview, types.ActionReport, types.ActionBlock:
		default:
			t.Errorf("rule %q has action %q; a library rule that allows is not a control", r.ID, r.Action)
		}
		if !r.Enabled {
			t.Errorf("rule %q ships disabled, which is a control that is not in force", r.ID)
		}
		if prior, clash := priorities[r.Priority]; clash {
			t.Errorf("rules %q and %q share priority %d", prior, r.ID, r.Priority)
		}
		priorities[r.Priority] = r.ID
	}
}

func TestBlockingRulesAreCriticalOnly(t *testing.T) {
	// Blocking a payment is the most severe action available and stops a customer's
	// money. Only a critical finding justifies it without a person having looked.
	for _, r := range Library("org") {
		if r.Action == types.ActionBlock && r.Severity != types.SeverityCritical {
			t.Errorf("rule %q blocks on severity %q; blocking must require a critical finding", r.ID, r.Severity)
		}
	}
}

func TestStructuringThresholdsMatchTheCitedRegulation(t *testing.T) {
	// The figures in the expressions are the figures in the cited text. A rule
	// citing the currency reporting rule while testing a different number is the
	// most direct way for a coverage claim to be false.
	want := map[string]string{
		"currency-day":                     "10000",
		"structuring-day":                  "10000",
		"structuring-week":                 "10000",
		"structuring-accounts":             "10000",
		"structuring-near-threshold":       "10000",
		"transmittal-recordkeeping":        "3000",
		"transmittal-payment-transparency": "1000",
	}
	for _, r := range Library("org") {
		if figure, ok := want[r.ID]; ok && !strings.Contains(r.DSL, figure) {
			t.Errorf("rule %q must test the cited figure %s, expression is %q", r.ID, figure, r.DSL)
		}
	}
}

func TestCryptoTransferRuleCarriesNoValueFloor(t *testing.T) {
	// The recast transfer regulation applies regardless of amount. A floor here
	// would be a rule that cites the regulation and does not implement it.
	for _, r := range Library("org") {
		if r.ID != "transmittal-crypto" {
			continue
		}
		for _, forbidden := range []string{"USD()", "Near(", "> 1000", ">= 3000"} {
			if strings.Contains(r.DSL, forbidden) {
				t.Errorf("the crypto transfer rule must not carry a value test, found %q in %q", forbidden, r.DSL)
			}
		}
		return
	}
	t.Fatal("the crypto transfer rule is missing")
}

func TestTypologiesAreCovered(t *testing.T) {
	got := map[string]bool{}
	for _, tp := range Typologies() {
		got[tp] = true
	}
	for _, want := range []string{
		Structuring, Currency, Transmittal, Velocity, Layering,
		Dormancy, Concentration, Behaviour, Sanctions, Geography, Exposure, RoundAmount,
	} {
		if !got[want] {
			t.Errorf("typology %q is declared but no rule detects it", want)
		}
	}
}

func TestObligationsAreDeduplicatedAndComplete(t *testing.T) {
	obs := Obligations()
	if len(obs) == 0 {
		t.Fatal("the library must publish the obligations it claims to cover")
	}
	seen := map[string]bool{}
	for _, c := range obs {
		key := string(c.Authority) + "|" + c.Document + "|" + c.Locator
		if seen[key] {
			t.Errorf("obligation %s appears twice", key)
		}
		seen[key] = true
		if !c.Valid() {
			t.Errorf("obligation %+v is incomplete", c)
		}
	}
	// Every citation on every rule must appear in the published list, or the list
	// understates what the product relies on.
	for _, r := range Library("org") {
		for _, c := range r.Citations {
			if !seen[string(c.Authority)+"|"+c.Document+"|"+c.Locator] {
				t.Errorf("rule %q cites %s which the obligation list omits", r.ID, c.Locator)
			}
		}
	}
}

func TestLibraryScopesToTheOrganisation(t *testing.T) {
	for _, r := range Library("acme") {
		if r.OrgID != "acme" {
			t.Errorf("rule %q carries organisation %q", r.ID, r.OrgID)
		}
	}
}

func TestRecommendationSixteenIsCitedByItsCurrentTitle(t *testing.T) {
	// FATF renamed Recommendation 16 to "Payment transparency" in June 2025 and
	// still hosts the pre-revised text separately. A citation carrying the old
	// title points a reviewer at a superseded document.
	found := false
	for _, c := range Obligations() {
		if !strings.Contains(c.Locator, "Recommendation 16") {
			continue
		}
		found = true
		if !strings.Contains(c.Locator, "Payment transparency") {
			t.Errorf("Recommendation 16 must be cited by its current title, got %q", c.Locator)
		}
		if strings.Contains(strings.ToLower(c.Locator), "wire transfer") {
			t.Errorf("Recommendation 16 is no longer titled Wire transfers, got %q", c.Locator)
		}
		if strings.Contains(c.URL, "Feburary") || strings.Contains(c.URL, "February 2025") {
			t.Errorf("citation points at the pre-revised text: %q", c.URL)
		}
	}
	if !found {
		t.Fatal("the payment transparency standard is not cited anywhere in the library")
	}
}

func TestSuspiciousReportingThresholdIsNotOverstated(t *testing.T) {
	// The reporting section carries one threshold, $5,000, applying whether or not
	// a suspect is identified. The $25,000 figure belongs to the banking agencies'
	// separate rules and citing it here would attribute a threshold to a regulation
	// that does not contain it.
	for _, r := range Library("org") {
		for _, c := range r.Citations {
			if strings.Contains(c.Locator, "1020.320") && strings.Contains(r.DSL, "25000") {
				t.Errorf("rule %q cites the reporting section and tests 25000, which that section does not state", r.ID)
			}
		}
	}
}

func TestGapsAreStatedWithCitationAndRemedy(t *testing.T) {
	gaps := Gaps()
	if len(gaps) == 0 {
		t.Fatal("the library must publish the requirements it does not meet")
	}
	for i, g := range gaps {
		if !g.Citation.Valid() {
			t.Errorf("gap %d cites incompletely: %+v", i, g.Citation)
		}
		if len(strings.TrimSpace(g.Why)) < 40 {
			t.Errorf("gap %d does not say why the requirement is unmet: %q", i, g.Why)
		}
		if len(strings.TrimSpace(g.Needs)) < 20 {
			t.Errorf("gap %d does not say what would meet it: %q", i, g.Needs)
		}
	}
}

func TestNoRequirementIsBothCoveredAndDeclaredAGap(t *testing.T) {
	// A requirement cannot be claimed as covered and admitted as a gap. That
	// contradiction is how a coverage claim becomes unfalsifiable.
	covered := map[string]bool{}
	for _, c := range Obligations() {
		covered[string(c.Authority)+"|"+c.Document+"|"+c.Locator] = true
	}
	for _, g := range Gaps() {
		key := string(g.Citation.Authority) + "|" + g.Citation.Document + "|" + g.Citation.Locator
		if covered[key] {
			t.Errorf("%s is claimed as covered and declared as a gap", g.Citation.Locator)
		}
	}
}
