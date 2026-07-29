package engine

import (
	"context"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

// A rule the deployment can evaluate, whose evidence exists.
func scored(id, dsl string) types.Rule {
	return types.Rule{
		ID: id, Name: "rule-" + id, DSL: dsl,
		Enabled: true, Severity: types.SeverityMedium,
		Weight: 1, Action: types.ActionReview, Priority: 1,
	}
}

// faulting builds an engine whose rules are admitted but fail at evaluation time.
//
// This is the real failure, not a contrived one: a rule is admitted because its
// evidence term exists, then the provider behind that term is unreachable at
// evaluation and the rule errors on every transaction it sees.
func faulting(t *testing.T, rules ...types.Rule) *Engine {
	t.Helper()
	e := New(Providers{Rate: refusingRate{}, Zone: time.UTC})
	if err := e.SetRules(rules); err != nil {
		t.Fatalf("SetRules: %v", err)
	}
	return e
}

// refusingRate stands in for a provider that is reachable at admission and broken
// at evaluation.
type refusingRate struct{}

func (refusingRate) USD(context.Context, float64, string) (float64, error) {
	return 0, context.DeadlineExceeded
}

func benignTx() (types.Transaction, types.Entity) {
	return types.Transaction{ID: "tx-1", OrgID: "acme", Notional: 100, USD: 100, Currency: "USD"},
		types.Entity{ID: "e-1", Jurisdiction: "US"}
}

// The defect this guards. Rules whose store was unreachable each produced a hit
// with Match=true at the rule's full weight, so an ordinary $100 payment came back
// with six alerts and a saturated score. Every transaction alerted, which ranks
// exactly as well as nothing alerting and costs more to read.
//
// The fix is not to hide the failure — it is still reported per rule, and still
// forces review. It is that a rule which reached no verdict carries no weight.
func TestRulesThatCannotRunCarryNoWeight(t *testing.T) {
	e := faulting(t,
		scored("r1", `USD() > 10000.0`),
		scored("r2", `USD() > 20000.0`),
		scored("r3", `USD() > 30000.0`),
	)
	tx, ent := benignTx()

	alerts, score, action := e.Evaluate(context.Background(), tx, ent)

	faults := 0
	for _, a := range alerts {
		if a.EvalErr == "" {
			t.Errorf("rule %q reported a verdict it could not reach", a.RuleID)
			continue
		}
		faults++
		if a.Score != 0 {
			t.Errorf("rule %q could not run yet scored %v", a.RuleID, a.Score)
		}
		if a.ActionTaken != types.ActionReview {
			t.Errorf("rule %q could not run yet its action is %q, want review", a.RuleID, a.ActionTaken)
		}
	}
	if faults == 0 {
		t.Fatal("no rule reported its failure — a rule that did not run must never be silent")
	}

	// The aggregate is what saturated, and it is what a queue is ordered by.
	if score != 0 {
		t.Errorf("aggregate score = %v, want 0 — no rule reached a verdict, so there is no evidence of risk", score)
	}
	// But the transaction is not cleared: it was not fully assessed.
	if action != types.ActionReview {
		t.Errorf("action = %q, want review — a partly assessed transaction has not been cleared", action)
	}
}

// A working rule set must leave an ordinary transaction alone. Without this the
// engine cannot rank work at all: if everything alerts, the score orders nothing.
func TestBenignTransactionProducesNoAlerts(t *testing.T) {
	e := New(Providers{Zone: time.UTC})
	if err := e.SetRules([]types.Rule{
		scored("r1", `Tx.Notional > 10000.0`),
		scored("r2", `Tx.Notional > 50000.0`),
	}); err != nil {
		t.Fatalf("SetRules: %v", err)
	}
	tx, ent := benignTx()

	alerts, score, action := e.Evaluate(context.Background(), tx, ent)
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

// A fault must not dilute or mask a real match. The matching rule keeps its score;
// the failing rule is reported alongside it and contributes nothing.
func TestFaultDoesNotDiluteARealMatch(t *testing.T) {
	e := faulting(t,
		scored("match", `Tx.Notional > 50.0`), // evaluable, fires
		scored("fault", `USD() > 1000000.0`),  // errors
	)
	tx, ent := benignTx()

	alerts, score, action := e.Evaluate(context.Background(), tx, ent)
	if len(alerts) != 2 {
		t.Fatalf("got %d alerts, want 2 (one match, one fault)", len(alerts))
	}
	if score <= 0 {
		t.Fatalf("score = %v, want the matching rule's contribution", score)
	}

	for _, a := range alerts {
		switch a.RuleID {
		case "match":
			if a.EvalErr != "" {
				t.Errorf("the evaluable rule reported a failure: %s", a.EvalErr)
			}
			if a.Score <= 0 {
				t.Errorf("the matching rule scored %v, want its weight", a.Score)
			}
		case "fault":
			if a.EvalErr == "" {
				t.Error("the failing rule did not report its failure")
			}
			if a.Score != 0 {
				t.Errorf("the failing rule scored %v, want 0", a.Score)
			}
		}
	}
	if action != types.ActionReview {
		t.Errorf("action = %q, want review", action)
	}
}

// A failing rule must not be able to act. A rule configured to block that cannot
// evaluate would otherwise decline every payment the moment its provider broke.
func TestAFailingBlockRuleCannotDeclineEverything(t *testing.T) {
	block := scored("blocker", `USD() > 1.0`)
	block.Action = types.ActionBlock
	e := faulting(t, block)
	tx, ent := benignTx()

	alerts, _, action := e.Evaluate(context.Background(), tx, ent)
	if len(alerts) != 1 {
		t.Fatalf("got %d alerts, want 1", len(alerts))
	}
	if action == types.ActionBlock {
		t.Fatal("a block rule that could not evaluate blocked the transaction anyway")
	}
	if action != types.ActionReview {
		t.Errorf("action = %q, want review", action)
	}
}
