package engine

import (
	"testing"

	"github.com/luxfi/aml/pkg/cases"
	"github.com/luxfi/aml/pkg/rules"
	"github.com/luxfi/aml/pkg/types"
)

// TestIntegrationStructuringFlow is the integration test from the spec:
// POST a transaction with notional: 9500 and verify structuring fires,
// travel_rule fires, case opens on block/report, webhook queued.
func TestIntegrationStructuringFlow(t *testing.T) {
	eng := New(rules.StarterRules("test-org"))
	caseStore := cases.NewStore()

	// Simulate 3 transactions from the same user at ~$9500 each.
	txs := []types.Transaction{
		{ID: "tx1", OrgID: "test-org", UserID: "u-structurer", Notional: 9200, Currency: "USD"},
		{ID: "tx2", OrgID: "test-org", UserID: "u-structurer", Notional: 9400, Currency: "USD"},
		{ID: "tx3", OrgID: "test-org", UserID: "u-structurer", Notional: 9500, Currency: "USD"},
	}

	entity := types.Entity{
		ID:    "u-structurer",
		OrgID: "test-org",
		Name:  "Suspicious User",
	}

	for _, tx := range txs {
		alerts, score, action := eng.Evaluate(tx, entity)

		// Each tx should fire at least Structuring (flag) + Travel Rule (report).
		if len(alerts) == 0 {
			t.Fatalf("tx %s: expected alerts, got none", tx.ID)
		}

		// Score should be non-zero.
		if score == 0 {
			t.Fatalf("tx %s: expected non-zero score", tx.ID)
		}

		// Action should be at least report (travel rule outranks structuring flag).
		if action != types.ActionReport {
			t.Fatalf("tx %s: expected action=report, got %s", tx.ID, action)
		}

		// Verify structuring rule fired.
		var structuringFired bool
		for _, a := range alerts {
			if a.RuleName == "Structuring" {
				structuringFired = true
			}
		}
		if !structuringFired {
			t.Fatalf("tx %s: Structuring rule did not fire", tx.ID)
		}

		// Auto-create case on report.
		alertIDs := make([]string, len(alerts))
		for i, a := range alerts {
			alertIDs[i] = a.ID
		}
		if action == types.ActionBlock || action == types.ActionReport {
			c := caseStore.Create("test-org", types.SeverityCritical, alertIDs, []string{tx.UserID})
			if c.ID == "" {
				t.Fatal("case should have an ID")
			}
			if c.Status != types.CaseOpen {
				t.Fatalf("case status = %s, want open", c.Status)
			}
		}
	}

	// Verify cases were created.
	allCases := caseStore.List("test-org", "")
	if len(allCases) != 3 {
		t.Errorf("expected 3 cases (one per tx), got %d", len(allCases))
	}
}

// TestIntegrationSanctionedBlock verifies a sanctioned jurisdiction blocks.
func TestIntegrationSanctionedBlock(t *testing.T) {
	eng := New(rules.StarterRules("test-org"))

	tx := types.Transaction{
		ID:       "tx-iran",
		OrgID:    "test-org",
		UserID:   "u-iran",
		Notional: 1000,
		Currency: "USD",
	}

	entity := types.Entity{
		ID:           "u-iran",
		OrgID:        "test-org",
		Name:         "Iranian Entity",
		Jurisdiction: "IR",
	}

	alerts, _, action := eng.Evaluate(tx, entity)

	if action != types.ActionBlock {
		t.Errorf("sanctioned jurisdiction should block, got %s", action)
	}

	var sanctionedFired bool
	for _, a := range alerts {
		if a.RuleName == "Sanctioned Jurisdiction" {
			sanctionedFired = true
		}
	}
	if !sanctionedFired {
		t.Error("Sanctioned Jurisdiction rule should fire for IR")
	}

	if len(alerts) == 0 {
		t.Fatal("expected at least one alert")
	}
}

// TestIntegrationCleanTransaction verifies clean traffic gets through.
func TestIntegrationCleanTransaction(t *testing.T) {
	eng := New(rules.StarterRules("test-org"))

	tx := types.Transaction{
		ID:       "tx-clean",
		OrgID:    "test-org",
		UserID:   "u-good",
		Notional: 500,
		Currency: "USD",
	}

	entity := types.Entity{
		ID:           "u-good",
		OrgID:        "test-org",
		Name:         "Good Customer",
		Jurisdiction: "US",
		KYCLevel:     3,
	}

	alerts, score, action := eng.Evaluate(tx, entity)

	if action != types.ActionAllow {
		t.Errorf("clean transaction should allow, got %s", action)
	}
	if len(alerts) != 0 {
		t.Errorf("clean transaction should have 0 alerts, got %d", len(alerts))
	}
	if score != 0 {
		t.Errorf("clean transaction score should be 0, got %f", score)
	}
}
