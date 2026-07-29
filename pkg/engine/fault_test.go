package engine

import (
	"strings"
	"testing"

	"github.com/luxfi/aml/pkg/types"
)

// broken is a rule whose expression cannot evaluate, standing in for the real
// cause: a helper backed by a store that is unreachable, which fails identically
// on every transaction.
func broken(id string) types.Rule {
	return types.Rule{
		ID: id, Name: "broken-" + id,
		DSL:      `no_such_helper(tx.Notional)`,
		Enabled:  true, Severity: types.SeverityHigh,
		Weight:   1,
		Action:   types.ActionReview,
		Priority: 1,
	}
}

func working(id, dsl string) types.Rule {
	return types.Rule{
		ID: id, Name: "working-" + id,
		DSL:      dsl,
		Enabled:  true, Severity: types.SeverityMedium,
		Weight:   1,
		Action:   types.ActionReview,
		Priority: 1,
	}
}

func benign() (types.Transaction, types.Entity) {
	return types.Transaction{ID: "tx-1", OrgID: "acme", Notional: 100, Currency: "USD"},
		types.Entity{ID: "e-1", Jurisdiction: "US"}
}

// The defect this guards: six rules whose store was unreachable each produced a
// hit with Match=true, so an ordinary $100 payment came back with six typology
// alerts and a saturated score. Every transaction alerted, which is the same as
// none of them alerting, except more expensive to read.
func TestBrokenRulesDoNotProduceTypologyAlerts(t *testing.T) {
	e := New([]types.Rule{broken("r1"), broken("r2"), broken("r3"), broken("r4"), broken("r5"), broken("r6")})
	tx, entity := benign()

	alerts, score, action := e.Evaluate(tx, entity)

	// Exactly one alert, describing the fault — not one per broken rule.
	if len(alerts) != 1 {
		names := make([]string, 0, len(alerts))
		for _, a := range alerts {
			names = append(names, a.RuleName)
		}
		t.Fatalf("6 unevaluable rules produced %d alerts (%v), want 1 fault alert", len(alerts), names)
	}

	fault := alerts[0]
	if !strings.Contains(fault.RuleName, "could not be evaluated") {
		t.Errorf("the alert does not say the rules failed to run: %q", fault.RuleName)
	}
	if len(fault.Causes) != 6 {
		t.Errorf("fault alert names %d rules, want all 6", len(fault.Causes))
	}

	// A rule that did not run is not evidence of risk, so it must not score.
	if score != 0 {
		t.Errorf("score = %v, want 0 — a rule that failed to run is not evidence of risk", score)
	}
	if fault.Score != 0 {
		t.Errorf("fault alert score = %v, want 0", fault.Score)
	}

	// But the transaction must not be cleared: it was not fully assessed.
	if action == types.ActionAllow {
		t.Error("a transaction assessed by none of its rules was allowed — the failure must not pass silently")
	}
	if action != types.ActionReview {
		t.Errorf("action = %q, want %q", action, types.ActionReview)
	}
}

// A working rule set must leave an ordinary transaction alone. Without this, the
// engine cannot rank work: if everything alerts, the score orders nothing.
func TestBenignTransactionProducesNoAlerts(t *testing.T) {
	e := New([]types.Rule{
		working("r1", `tx.Notional > 10000`),
		working("r2", `tx.Notional > 50000`),
	})
	tx, entity := benign()

	alerts, score, action := e.Evaluate(tx, entity)
	if len(alerts) != 0 {
		t.Fatalf("a $100 payment produced %d alerts, want 0", len(alerts))
	}
	if score != 0 {
		t.Errorf("score = %v, want 0", score)
	}
	if action != types.ActionAllow {
		t.Errorf("action = %q, want %q", action, types.ActionAllow)
	}
}

// A fault must not mask or dilute a real match: the typology alert keeps its own
// score, and the fault is reported alongside it.
func TestFaultAndMatchAreBothReportedAndScoreCountsOnlyTheMatch(t *testing.T) {
	e := New([]types.Rule{
		working("r1", `tx.Notional > 50`), // matches
		broken("r2"),                      // cannot run
	})
	tx, entity := benign()

	alerts, score, action := e.Evaluate(tx, entity)
	if len(alerts) != 2 {
		t.Fatalf("got %d alerts, want 2 (one match, one fault)", len(alerts))
	}
	if score <= 0 {
		t.Errorf("score = %v, want the matching rule's contribution", score)
	}

	var matched, faulted *types.Alert
	for i := range alerts {
		if strings.Contains(alerts[i].RuleName, "could not be evaluated") {
			faulted = &alerts[i]
		} else {
			matched = &alerts[i]
		}
	}
	if matched == nil || faulted == nil {
		t.Fatalf("expected one match and one fault, got %+v", alerts)
	}
	if matched.RuleID != "r1" {
		t.Errorf("matched alert is for rule %q, want r1", matched.RuleID)
	}
	// The fault names only the broken rule, so an analyst is not told a working
	// rule failed.
	if len(faulted.Causes) != 1 || faulted.Causes[0].Feature != "broken-r2" {
		t.Errorf("fault names %+v, want only broken-r2", faulted.Causes)
	}
	if action != types.ActionReview {
		t.Errorf("action = %q, want %q", action, types.ActionReview)
	}
}

// EvalAll must report a failed rule as a fault and not as a match, since that is
// the distinction Evaluate depends on.
func TestEvalAllReportsFailureWithoutClaimingAMatch(t *testing.T) {
	hits := NewEvaluator().EvalAll([]types.Rule{broken("r1")}, types.EvalContext{})
	if len(hits) != 1 {
		t.Fatalf("got %d results, want 1", len(hits))
	}
	if hits[0].EvalErr == "" {
		t.Error("the failure was not reported at all — a rule that did not run must never be silent")
	}
	if hits[0].Match {
		t.Error("a rule that could not be evaluated reported Match=true, which is what alerted on every transaction")
	}
}
