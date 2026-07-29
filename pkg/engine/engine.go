// Package engine evaluates transactions against the rule library.
//
// The engine holds a rule set it has admitted: every rule in it parses, names
// only evidence this deployment can supply, and returns a boolean. Admission is
// the point. A rule set that installs regardless of whether its evidence exists
// produces a monitoring programme whose coverage claim cannot be checked by
// reading it.
package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/luxfi/aml/pkg/types"
)

// Engine evaluates transactions against an admitted rule set.
type Engine struct {
	eval *Evaluator

	mu    sync.RWMutex
	rules []types.Rule
}

// New builds an engine over the given evidence providers with an empty rule set.
// Install rules with SetRules, which reports any the deployment cannot support.
func New(p Providers) *Engine {
	return &Engine{eval: NewEvaluator(p)}
}

// SetRules installs a rule set, or installs nothing.
//
// Every rule is admitted first and the whole set is rejected if any rule fails.
// A partially installed set is a control surface nobody can describe: the
// operator believes the catalog is in force, and the difference is discoverable
// only by reading startup logs.
func (e *Engine) SetRules(rules []types.Rule) error {
	var errs []error
	for _, r := range rules {
		if r.Weight < 0 {
			errs = append(errs, fmt.Errorf("rule %q: weight %v is negative, which would subtract from the risk score", r.ID, r.Weight))
			continue
		}
		if _, err := e.eval.Admit(r); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("rule set rejected, %d of %d rules cannot be evaluated: %w",
			len(errs), len(rules), errors.Join(errs...))
	}

	e.mu.Lock()
	e.rules = rules
	e.mu.Unlock()
	return nil
}

// Rules returns a copy of the installed rule set.
func (e *Engine) Rules() []types.Rule {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]types.Rule, len(e.rules))
	copy(out, e.rules)
	return out
}

// Evaluator exposes the evaluator, for admitting a candidate rule before saving
// it and for reporting the vocabulary this deployment can answer.
func (e *Engine) Evaluator() *Evaluator { return e.eval }

// Evaluate runs the rule set against a transaction and its customer, returning
// the alerts raised, the aggregate risk score, and the action to take.
func (e *Engine) Evaluate(ctx context.Context, tx types.Transaction, ent types.Entity) ([]types.Alert, float64, string) {
	e.mu.RLock()
	rules := e.rules
	e.mu.RUnlock()

	hits := e.eval.EvalAll(ctx, rules, tx, ent)
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
			Typology:       h.Rule.Typology,
			Citations:      h.Rule.Citations,
			Severity:       h.Rule.Severity,
			Score:          breakdown[h.Rule.ID],
			ScoreBreakdown: breakdown,
			ActionTaken:    action(h),
			EvalErr:        h.EvalErr,
			CreatedAt:      now,
			UpdatedAt:      now,
		})
	}

	return alerts, score, resolve(alerts)
}

// action is the response a hit calls for. A hit carrying an evaluation failure
// goes to review whatever the rule would otherwise have done: the rule reached
// no verdict, so neither blocking nor clearing is supportable and a person has
// to look.
func action(h types.RuleHit) string {
	if h.EvalErr != "" {
		return types.ActionReview
	}
	return h.Rule.Action
}

// resolve picks the most restrictive action across a set of alerts.
func resolve(alerts []types.Alert) string {
	rank := map[string]int{
		types.ActionAllow:  0,
		types.ActionFlag:   1,
		types.ActionReview: 2,
		types.ActionReport: 3,
		types.ActionBlock:  4,
	}
	best, bestRank := types.ActionAllow, 0
	for _, a := range alerts {
		if r, ok := rank[a.ActionTaken]; ok && r > bestRank {
			best, bestRank = a.ActionTaken, r
		}
	}
	return best
}
