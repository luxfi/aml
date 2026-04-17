package rules

import (
	"testing"

	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/types"
)

func TestStarterRulesCount(t *testing.T) {
	rules := StarterRules("test")
	if len(rules) != 23 {
		t.Errorf("expected 23 starter rules, got %d", len(rules))
	}
}

func TestStarterRulesAllEnabled(t *testing.T) {
	for _, r := range StarterRules("test") {
		if !r.Enabled {
			t.Errorf("rule %q should be enabled", r.Name)
		}
	}
}

func TestStarterRulesUniqueIDs(t *testing.T) {
	seen := make(map[string]bool)
	for _, r := range StarterRules("test") {
		if seen[r.ID] {
			t.Errorf("duplicate rule ID: %s", r.ID)
		}
		seen[r.ID] = true
	}
}

func TestStarterRulesUniquePriorities(t *testing.T) {
	seen := make(map[int]bool)
	for _, r := range StarterRules("test") {
		if seen[r.Priority] {
			t.Errorf("duplicate priority %d for rule %q", r.Priority, r.Name)
		}
		seen[r.Priority] = true
	}
}

func TestStarterRulesAllCompile(t *testing.T) {
	eval := engine.NewEvaluator()
	for _, r := range StarterRules("test") {
		_, err := eval.Compile(r)
		if err != nil {
			t.Errorf("rule %q failed to compile: %v", r.Name, err)
		}
	}
}

func TestStarterRulesValidSeverity(t *testing.T) {
	valid := map[string]bool{
		types.SeverityLow:      true,
		types.SeverityMedium:   true,
		types.SeverityHigh:     true,
		types.SeverityCritical: true,
	}
	for _, r := range StarterRules("test") {
		if !valid[r.Severity] {
			t.Errorf("rule %q has invalid severity: %s", r.Name, r.Severity)
		}
	}
}

func TestStarterRulesValidAction(t *testing.T) {
	valid := map[string]bool{
		types.ActionFlag:   true,
		types.ActionReview: true,
		types.ActionBlock:  true,
		types.ActionReport: true,
	}
	for _, r := range StarterRules("test") {
		if !valid[r.Action] {
			t.Errorf("rule %q has invalid action: %s", r.Name, r.Action)
		}
	}
}

func TestStarterRulesAllHaveWeight(t *testing.T) {
	for _, r := range StarterRules("test") {
		if r.Weight <= 0 {
			t.Errorf("rule %q has non-positive weight: %f", r.Name, r.Weight)
		}
	}
}

func TestStarterRulesOrgID(t *testing.T) {
	for _, r := range StarterRules("my-org") {
		if r.OrgID != "my-org" {
			t.Errorf("rule %q has org_id %s, want my-org", r.Name, r.OrgID)
		}
	}
}

// Individual rule evaluation tests.

func TestRuleCTRFires(t *testing.T) {
	eval := engine.NewEvaluator()
	r := findRule("rule-ctr-threshold")
	ctx := types.EvalContext{
		Tx: types.Transaction{Notional: 15000, Currency: "USD"},
	}
	match, err := eval.Eval(r, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !match {
		t.Error("CTR should fire for $15,000 USD")
	}
}

func TestRuleCTRNoFireEUR(t *testing.T) {
	eval := engine.NewEvaluator()
	r := findRule("rule-ctr-threshold")
	ctx := types.EvalContext{
		Tx: types.Transaction{Notional: 15000, Currency: "EUR"},
	}
	match, err := eval.Eval(r, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if match {
		t.Error("CTR should not fire for EUR")
	}
}

func TestRuleStructuringFires(t *testing.T) {
	eval := engine.NewEvaluator()
	r := findRule("rule-structuring")
	ctx := types.EvalContext{
		Tx: types.Transaction{Notional: 9500},
	}
	match, err := eval.Eval(r, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !match {
		t.Error("Structuring should fire for $9,500")
	}
}

func TestRuleStructuringNoFire(t *testing.T) {
	eval := engine.NewEvaluator()
	r := findRule("rule-structuring")
	ctx := types.EvalContext{
		Tx: types.Transaction{Notional: 10000},
	}
	match, err := eval.Eval(r, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if match {
		t.Error("Structuring should not fire at exactly $10,000")
	}
}

func TestRulePEPFires(t *testing.T) {
	eval := engine.NewEvaluator()
	r := findRule("rule-pep-large")
	ctx := types.EvalContext{
		Tx:     types.Transaction{Notional: 20000},
		Entity: types.Entity{PEP: true},
	}
	match, err := eval.Eval(r, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !match {
		t.Error("PEP rule should fire for PEP with $20,000")
	}
}

func TestRulePEPNoFireLow(t *testing.T) {
	eval := engine.NewEvaluator()
	r := findRule("rule-pep-large")
	ctx := types.EvalContext{
		Tx:     types.Transaction{Notional: 5000},
		Entity: types.Entity{PEP: true},
	}
	match, err := eval.Eval(r, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if match {
		t.Error("PEP rule should not fire for $5,000")
	}
}

func TestRuleSanctionedJurisdiction(t *testing.T) {
	eval := engine.NewEvaluator()
	r := findRule("rule-sanctioned-jurisdiction")
	ctx := types.EvalContext{
		Entity: types.Entity{Jurisdiction: "IR"},
	}
	match, err := eval.Eval(r, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !match {
		t.Error("sanctioned jurisdiction should fire for IR")
	}
}

func TestRuleSanctionedJurisdictionSafe(t *testing.T) {
	eval := engine.NewEvaluator()
	r := findRule("rule-sanctioned-jurisdiction")
	ctx := types.EvalContext{
		Entity: types.Entity{Jurisdiction: "US"},
	}
	match, err := eval.Eval(r, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if match {
		t.Error("sanctioned jurisdiction should not fire for US")
	}
}

func TestRuleTravelRule(t *testing.T) {
	eval := engine.NewEvaluator()
	r := findRule("rule-travel-rule")
	ctx := types.EvalContext{
		Tx: types.Transaction{Notional: 5000, Currency: "USD"},
	}
	match, err := eval.Eval(r, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !match {
		t.Error("travel rule should fire for $5,000 (threshold $3,000)")
	}
}

func TestRuleTravelRuleBelowThreshold(t *testing.T) {
	eval := engine.NewEvaluator()
	r := findRule("rule-travel-rule")
	ctx := types.EvalContext{
		Tx: types.Transaction{Notional: 2000, Currency: "USD"},
	}
	match, err := eval.Eval(r, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if match {
		t.Error("travel rule should not fire for $2,000")
	}
}

func findRule(id string) types.Rule {
	for _, r := range StarterRules("test") {
		if r.ID == id {
			return r
		}
	}
	panic("rule not found: " + id)
}
