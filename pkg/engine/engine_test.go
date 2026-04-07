package engine

import (
	"testing"

	"github.com/luxfi/aml/pkg/rules"
	"github.com/luxfi/aml/pkg/types"
)

func TestEngineEvaluateAllow(t *testing.T) {
	eng := New(rules.StarterRules("test"))

	tx := types.Transaction{
		ID:       "tx1",
		OrgID:    "test",
		UserID:   "u1",
		Notional: 100,
		Currency: "USD",
	}
	entity := types.Entity{
		ID:    "u1",
		OrgID: "test",
	}

	alerts, score, action := eng.Evaluate(tx, entity)
	if action != types.ActionAllow {
		t.Errorf("expected allow for small transaction, got %s", action)
	}
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts, got %d", len(alerts))
	}
	if score != 0 {
		t.Errorf("expected score 0, got %f", score)
	}
}

func TestEngineEvaluateStructuring(t *testing.T) {
	eng := New(rules.StarterRules("test"))

	tx := types.Transaction{
		ID:       "tx2",
		OrgID:    "test",
		UserID:   "u1",
		Notional: 9500,
		Currency: "USD",
	}
	entity := types.Entity{
		ID:    "u1",
		OrgID: "test",
	}

	alerts, _, action := eng.Evaluate(tx, entity)
	// Structuring (flag) + Travel Rule (report) both fire at $9500.
	// Highest action wins: report > flag.
	if action != types.ActionReport {
		t.Errorf("expected report (travel rule outranks structuring flag), got %s", action)
	}

	var structuringFired bool
	for _, a := range alerts {
		if a.RuleName == "Structuring" {
			structuringFired = true
			break
		}
	}
	if !structuringFired {
		t.Error("expected Structuring rule to fire")
	}
}

func TestEngineEvaluateCTR(t *testing.T) {
	eng := New(rules.StarterRules("test"))

	tx := types.Transaction{
		ID:       "tx3",
		OrgID:    "test",
		UserID:   "u1",
		Notional: 15000,
		Currency: "USD",
	}
	entity := types.Entity{
		ID:    "u1",
		OrgID: "test",
	}

	alerts, _, action := eng.Evaluate(tx, entity)

	// CTR fires (report) and travel_rule fires (report), so action = report.
	if action != types.ActionReport {
		t.Errorf("expected report for CTR, got %s", action)
	}

	var ctrFired bool
	for _, a := range alerts {
		if a.RuleName == "CTR Threshold" {
			ctrFired = true
		}
	}
	if !ctrFired {
		t.Error("expected CTR Threshold rule to fire")
	}
}

func TestEngineEvaluatePEP(t *testing.T) {
	eng := New(rules.StarterRules("test"))

	tx := types.Transaction{
		ID:       "tx4",
		OrgID:    "test",
		UserID:   "u1",
		Notional: 15000,
		Currency: "USD",
	}
	entity := types.Entity{
		ID:    "u1",
		OrgID: "test",
		PEP:   true,
	}

	alerts, _, _ := eng.Evaluate(tx, entity)

	var pepFired bool
	for _, a := range alerts {
		if a.RuleName == "PEP Large Transaction" {
			pepFired = true
		}
	}
	if !pepFired {
		t.Error("expected PEP Large Transaction rule to fire")
	}
}

func TestEngineSetRules(t *testing.T) {
	eng := New(nil)
	if len(eng.Rules()) != 0 {
		t.Fatal("expected 0 rules")
	}

	eng.SetRules(rules.StarterRules("test"))
	if len(eng.Rules()) != 20 {
		t.Errorf("expected 20 rules, got %d", len(eng.Rules()))
	}
}

func TestResolveAction(t *testing.T) {
	tests := []struct {
		actions []string
		want    string
	}{
		{nil, types.ActionAllow},
		{[]string{types.ActionFlag}, types.ActionFlag},
		{[]string{types.ActionFlag, types.ActionReview}, types.ActionReview},
		{[]string{types.ActionReview, types.ActionBlock}, types.ActionBlock},
		{[]string{types.ActionReport, types.ActionBlock}, types.ActionBlock},
	}

	for _, tt := range tests {
		alerts := make([]types.Alert, len(tt.actions))
		for i, a := range tt.actions {
			alerts[i] = types.Alert{ActionTaken: a}
		}
		got := resolveAction(alerts)
		if got != tt.want {
			t.Errorf("resolveAction(%v) = %s, want %s", tt.actions, got, tt.want)
		}
	}
}
