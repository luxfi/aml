// Package engine implements the real-time AML rule evaluation engine.
// It evaluates transactions against rules using expr-lang, scores hits
// using weight-of-evidence, and determines the enforcement action.
package engine

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/luxfi/aml/pkg/types"
)

// Engine is the core AML evaluation engine.
type Engine struct {
	evaluator *Evaluator
	mu        sync.RWMutex
	rules     []types.Rule
}

// New creates an Engine with the given rules.
func New(rules []types.Rule) *Engine {
	return &Engine{
		evaluator: NewEvaluator(),
		rules:     rules,
	}
}

// SetRules replaces the rule set atomically.
func (e *Engine) SetRules(rules []types.Rule) {
	e.mu.Lock()
	e.rules = rules
	e.mu.Unlock()
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
			ActionTaken:    h.Rule.Action,
			CreatedAt:      now,
			UpdatedAt:      now,
		})
	}

	action := resolveAction(alerts)
	return alerts, score, action
}

// resolveAction picks the most severe action from a set of alerts.
// Priority: block > report > review > flag > allow.
func resolveAction(alerts []types.Alert) string {
	priority := map[string]int{
		types.ActionBlock:  4,
		types.ActionReport: 3,
		types.ActionReview: 2,
		types.ActionFlag:   1,
		types.ActionAllow:  0,
	}

	best := types.ActionAllow
	bestP := 0
	for _, a := range alerts {
		if p, ok := priority[a.ActionTaken]; ok && p > bestP {
			best = a.ActionTaken
			bestP = p
		}
	}
	return best
}
