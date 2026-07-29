// Package engine implements the real-time AML rule evaluation engine.
// It evaluates transactions against rules using expr-lang, scores hits
// using weight-of-evidence, and determines the enforcement action.
package engine

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/luxfi/aml/pkg/types"
)

// Engine is the core AML evaluation engine.
type Engine struct {
	evaluator *Evaluator
	mu        sync.RWMutex
	rules     []types.Rule
	scorer    Scorer
	faults    atomic.Int64
	rejects   atomic.Int64
}

// New creates an Engine with the given rules.
func New(rules []types.Rule) *Engine {
	return &Engine{
		evaluator: NewEvaluator(),
		rules:     rules,
	}
}

// SetRules replaces the rule set atomically. Returns an error if any rule
// has a negative weight (RED-19: prevents score suppression attacks).
func (e *Engine) SetRules(rules []types.Rule) error {
	for _, r := range rules {
		if r.Weight < 0 {
			return fmt.Errorf("rule %q has negative weight %f: negative weights are not allowed", r.ID, r.Weight)
		}
	}
	e.mu.Lock()
	e.rules = rules
	e.mu.Unlock()
	return nil
}

// Rules returns a copy of the current rule set.
func (e *Engine) Rules() []types.Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]types.Rule, len(e.rules))
	copy(out, e.rules)
	return out
}

// Evaluator returns the underlying evaluator for function registration.
func (e *Engine) Evaluator() *Evaluator {
	return e.evaluator
}

// SetScorer installs the Scorer consulted alongside the rules, or nil to run on
// rules alone.
func (e *Engine) SetScorer(s Scorer) {
	e.mu.Lock()
	e.scorer = s
	e.mu.Unlock()
}

// ScorerFaults is the number of times the Scorer failed and its evidence was
// discarded — a panic, a negative weight, or a weight that is not a number.
//
// It has to be readable. A Scorer that faults on every transaction contributes
// nothing while the engine keeps answering, and the only difference between that
// and a Scorer finding nothing to report is this counter.
func (e *Engine) ScorerFaults() int64 { return e.faults.Load() + e.rejects.Load() }

// assess consults the Scorer, and confines it.
//
// Three things a Scorer must not be able to do, each enforced here rather than
// trusted to the Scorer, because the property has to hold for one that is wrong
// as well as one that is honest:
//
// It must not take the rule plane down. The transaction has already been
// evaluated against every rule by the time this runs, and losing that verdict to
// a fault in the statistical plane would turn a degraded control into no control
// — so a panic is contained, counted, and the rules' answer stands.
//
// It must not weaken a verdict. Weight-of-evidence is a sum, so a negative weight
// would subtract from a score built by the rules, and a model would be able to
// argue a transaction down. Non-negative weights are already required of rules
// for that reason; a computed weight is held to it too, along with the NaN a
// computation can produce and a constant cannot.
//
// It must not act. Its action is capped at types.ActionCeiling, so the strongest
// thing a statistical judgement can do is put a transaction in front of a person.
func (e *Engine) assess(tx types.Transaction, entity types.Entity) (hit types.RuleHit, ok bool) {
	e.mu.RLock()
	sc := e.scorer
	e.mu.RUnlock()
	if sc == nil {
		return types.RuleHit{}, false
	}

	defer func() {
		if r := recover(); r != nil {
			e.faults.Add(1)
			hit, ok = types.RuleHit{}, false
		}
	}()

	hit, ok = sc.Assess(tx, entity)
	if !ok {
		return types.RuleHit{}, false
	}
	if hit.Rule.Weight < 0 || math.IsNaN(hit.Rule.Weight) || math.IsInf(hit.Rule.Weight, 0) {
		e.rejects.Add(1)
		return types.RuleHit{}, false
	}
	if types.ActionRank(hit.Rule.Action) > types.ActionRank(types.ActionCeiling) {
		hit.Rule.Action = types.ActionCeiling
	}
	hit.Match = true
	return hit, true
}

// Evaluate runs all rules against a transaction and its primary entity.
// Returns the computed alerts, the aggregate score, and the enforcement action.
func (e *Engine) Evaluate(tx types.Transaction, entity types.Entity) ([]types.Alert, float64, string) {
	e.mu.RLock()
	rules := e.rules
	e.mu.RUnlock()

	ctx := types.EvalContext{
		Tx:     tx,
		Entity: entity,
	}

	hits := e.evaluator.EvalAll(rules, ctx)

	// The Scorer is consulted after the rules and its evidence joins theirs, so
	// the statistical plane can only add to what the rules found. It can raise a
	// transaction no rule matched, which is the point of having it; it cannot
	// lower one they did.
	if hit, ok := e.assess(tx, entity); ok {
		hits = append(hits, hit)
	}

	if len(hits) == 0 {
		return nil, 0, types.ActionAllow
	}

	score, breakdown := Score(hits)

	now := time.Now().UTC()
	alerts := make([]types.Alert, 0, len(hits))
	for _, h := range hits {
		alerts = append(alerts, types.Alert{
			ID:             uuid.NewString(),
			OrgID:          tx.OrgID,
			TxID:           tx.ID,
			RuleID:         h.Rule.ID,
			RuleName:       h.Rule.Name,
			Severity:       h.Rule.Severity,
			Score:          breakdown[h.Rule.ID],
			ScoreBreakdown: breakdown,
			Causes:         h.Causes,
			ActionTaken:    h.Rule.Action,
			CreatedAt:      now,
			UpdatedAt:      now,
		})
	}

	action := resolveAction(alerts)
	return alerts, score, action
}

// resolveAction picks the most demanding action from a set of alerts.
func resolveAction(alerts []types.Alert) string {
	best := types.ActionAllow
	for _, a := range alerts {
		if types.ActionRank(a.ActionTaken) > types.ActionRank(best) {
			best = a.ActionTaken
		}
	}
	return best
}
