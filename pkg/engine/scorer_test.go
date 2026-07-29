package engine

import (
	"math"
	"testing"

	"github.com/luxfi/aml/pkg/types"
)

// scorer is a Scorer that returns exactly what a test tells it to, including the
// things an honest one never would.
type scorer struct {
	hit   types.RuleHit
	ok    bool
	panic bool
}

func (s scorer) Assess(types.Transaction, types.Entity) (types.RuleHit, bool) {
	if s.panic {
		panic("scorer fault")
	}
	return s.hit, s.ok
}

func evidence(weight float64, action string) types.RuleHit {
	return types.RuleHit{
		Rule: types.Rule{
			ID: "anomaly", Name: "Unusual for this customer",
			Severity: types.SeverityMedium, Weight: weight, Action: action, Enabled: true,
		},
		Match: true,
	}
}

func flagRule() types.Rule {
	return types.Rule{
		ID: "r-flag", Name: "flag", DSL: `tx.Notional > 100`,
		Severity: types.SeverityMedium, Weight: 0.3, Action: types.ActionFlag, Enabled: true,
	}
}

var tx = types.Transaction{ID: "t1", OrgID: "acme", UserID: "u1", Notional: 500}

// A fault in the statistical plane must not take the rule plane with it. Every
// rule has already been evaluated by the time the Scorer runs, and losing that
// verdict to a panic would turn a degraded control into no control.
func TestPanickingScorerDoesNotLoseTheRulesVerdict(t *testing.T) {
	e := New([]types.Rule{flagRule()})
	e.SetScorer(scorer{panic: true})

	alerts, score, action := e.Evaluate(tx, types.Entity{})
	if action != types.ActionFlag {
		t.Fatalf("action = %q, want the rule's %q", action, types.ActionFlag)
	}
	if len(alerts) != 1 || score <= 0 {
		t.Fatalf("rule verdict lost: %d alerts, score %v", len(alerts), score)
	}
	// The fault must be countable. A Scorer that faults on every transaction
	// contributes nothing while the engine keeps answering, and this counter is
	// the only difference between that and one that finds nothing to report.
	if e.ScorerFaults() != 1 {
		t.Fatalf("faults = %d, want 1", e.ScorerFaults())
	}
}

// Weight-of-evidence is a sum, so a negative weight would let a model argue a
// transaction down below what the rules found. Rules are already forbidden
// negative weights; a computed weight is held to the same rule, and so are the
// values only a computation can produce.
func TestScorerCannotWeakenAVerdict(t *testing.T) {
	base := New([]types.Rule{flagRule()})
	_, want, wantAction := base.Evaluate(tx, types.Entity{})

	for name, weight := range map[string]float64{
		"negative":  -5,
		"nan":       math.NaN(),
		"+infinity": math.Inf(1),
		"-infinity": math.Inf(-1),
	} {
		e := New([]types.Rule{flagRule()})
		e.SetScorer(scorer{hit: evidence(weight, types.ActionReview), ok: true})
		alerts, got, action := e.Evaluate(tx, types.Entity{})

		if got < want {
			t.Errorf("%s weight lowered the score from %v to %v", name, want, got)
		}
		if math.IsNaN(got) {
			t.Errorf("%s weight produced a score that is not a number", name)
		}
		if types.ActionRank(action) < types.ActionRank(wantAction) {
			t.Errorf("%s weight weakened the action from %q to %q", name, wantAction, action)
		}
		if len(alerts) != 1 {
			t.Errorf("%s weight: %d alerts, want the rule's 1", name, len(alerts))
		}
		if e.ScorerFaults() != 1 {
			t.Errorf("%s weight was not counted as a fault", name)
		}
	}
}

// The model summons a person; it does not act. Whatever a Scorer asks for, the
// strongest thing it can obtain is a review — enforced on the Scorer's output
// rather than trusted to the Scorer, because the property has to hold for one
// that is wrong as well as one that is honest.
func TestScorerActionIsCappedAtTheCeiling(t *testing.T) {
	for _, asked := range []string{types.ActionBlock, types.ActionReport} {
		e := New(nil)
		e.SetScorer(scorer{hit: evidence(0.9, asked), ok: true})
		alerts, _, action := e.Evaluate(tx, types.Entity{})
		if action != types.ActionCeiling {
			t.Errorf("scorer asked for %q and got %q, ceiling is %q", asked, action, types.ActionCeiling)
		}
		if len(alerts) != 1 || alerts[0].ActionTaken != types.ActionCeiling {
			t.Errorf("scorer asked for %q and the alert carries %q", asked, alerts[0].ActionTaken)
		}
	}
	// At or below the ceiling it gets what it asked for.
	for _, asked := range []string{types.ActionFlag, types.ActionReview} {
		e := New(nil)
		e.SetScorer(scorer{hit: evidence(0.9, asked), ok: true})
		if _, _, action := e.Evaluate(tx, types.Entity{}); action != asked {
			t.Errorf("scorer asked for %q and got %q", asked, action)
		}
	}
}

// The statistical plane may raise a transaction no rule matched — that is the
// point of having it — and may never lower one they did.
func TestScorerOnlyAdds(t *testing.T) {
	rules := []types.Rule{flagRule()}
	plain := New(rules)
	_, want, wantAction := plain.Evaluate(tx, types.Entity{})

	e := New(rules)
	e.SetScorer(scorer{hit: evidence(0.2, types.ActionReview), ok: true})
	alerts, got, action := e.Evaluate(tx, types.Entity{})
	if got <= want {
		t.Fatalf("evidence did not add: %v -> %v", want, got)
	}
	if types.ActionRank(action) < types.ActionRank(wantAction) {
		t.Fatalf("evidence weakened the action: %q -> %q", wantAction, action)
	}
	if len(alerts) != 2 {
		t.Fatalf("%d alerts, want the rule's and the model's", len(alerts))
	}

	// With no rules at all the model can still raise a transaction.
	only := New(nil)
	only.SetScorer(scorer{hit: evidence(0.2, types.ActionReview), ok: true})
	if _, score, action := only.Evaluate(tx, types.Entity{}); score <= 0 || action != types.ActionReview {
		t.Fatalf("model alone produced score %v action %q", score, action)
	}
}

// Declining to score contributes nothing at all — not an alert with no weight,
// which downstream would read as a verdict of normal.
func TestScorerDecliningContributesNothing(t *testing.T) {
	e := New(nil)
	e.SetScorer(scorer{hit: evidence(0.9, types.ActionReview), ok: false})
	alerts, score, action := e.Evaluate(tx, types.Entity{})
	if len(alerts) != 0 || score != 0 || action != types.ActionAllow {
		t.Fatalf("a declining scorer produced %d alerts, score %v, action %q", len(alerts), score, action)
	}
	if e.ScorerFaults() != 0 {
		t.Fatal("declining to score was counted as a fault")
	}
}

// A model's alert is a number until it carries its reasons, so the attribution has
// to survive the trip into the alert an investigator opens.
func TestCausesReachTheAlert(t *testing.T) {
	hit := evidence(0.2, types.ActionReview)
	hit.Causes = []types.Cause{{
		Feature: "subthreshold", Typology: "structuring",
		Indicator: "transactions split to circumvent reporting limits",
		Citation:  "EBA/GL/2021/02, Guideline 4.60(a)", Severity: types.SeverityHigh,
		Unit:     "share of transactions falling just below the reporting threshold",
		Observed: 11, Baseline: 11, Without: 0.31, Share: 0.62,
	}}
	e := New(nil)
	e.SetScorer(scorer{hit: hit, ok: true})

	alerts, _, _ := e.Evaluate(tx, types.Entity{})
	if len(alerts) != 1 {
		t.Fatalf("%d alerts", len(alerts))
	}
	got := alerts[0].Causes
	if len(got) != 1 {
		t.Fatalf("alert carries %d causes", len(got))
	}
	if got[0].Feature != "subthreshold" || got[0].Citation == "" || got[0].Typology == "" {
		t.Fatalf("attribution arrived incomplete: %+v", got[0])
	}
	// A rule alert explains itself and must not acquire causes it never had.
	rules := New([]types.Rule{flagRule()})
	ruleAlerts, _, _ := rules.Evaluate(tx, types.Entity{})
	if len(ruleAlerts[0].Causes) != 0 {
		t.Fatal("a rule alert carries model attribution")
	}
}

// Once a weight is computed rather than read from a constant, arithmetic can
// produce a value that is not a number, and NaN compares false against every
// bound — so the obvious two-comparison clamp passes it straight through, where it
// becomes a transaction's risk score and fails to marshal into the response.
func TestScoreClampHandlesNotANumber(t *testing.T) {
	if got := clamp01(math.NaN()); math.IsNaN(got) {
		t.Fatal("clamp01 passed NaN through as a risk score")
	}
	if got := clamp01(math.NaN()); got != 1 {
		t.Fatalf("clamp01(NaN) = %v, want the top of the range so a broken score reaches a person", got)
	}
	if clamp01(math.Inf(1)) != 1 || clamp01(math.Inf(-1)) != 0 {
		t.Fatal("clamp01 does not bound the infinities")
	}
	if clamp01(0.4) != 0.4 {
		t.Fatal("clamp01 altered a value already in range")
	}
}

// No Scorer at all must leave the engine exactly as it was.
func TestNoScorerChangesNothing(t *testing.T) {
	rules := []types.Rule{flagRule()}
	a := New(rules)
	wantAlerts, want, wantAction := a.Evaluate(tx, types.Entity{})

	b := New(rules)
	b.SetScorer(nil)
	gotAlerts, got, gotAction := b.Evaluate(tx, types.Entity{})
	if got != want || gotAction != wantAction || len(gotAlerts) != len(wantAlerts) {
		t.Fatalf("installing a nil scorer changed the verdict: %v/%q vs %v/%q", got, gotAction, want, wantAction)
	}
	if b.ScorerFaults() != 0 {
		t.Fatal("a nil scorer produced faults")
	}
}
