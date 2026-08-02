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
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/luxfi/aml/pkg/types"
)

// Engine evaluates transactions against an admitted rule set.
type Engine struct {
	eval *Evaluator

	mu sync.RWMutex
	// rules is the installed library, compiled. It is the ONE thing this process
	// keeps compiled rules in, and what it holds is what the deployment
	// installed — see Evaluator, which keeps none.
	rules  *Ruleset
	scorer Scorer

	faults  atomic.Int64
	rejects atomic.Int64
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
	set, err := e.eval.Ready(rules)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.rules = set
	e.mu.Unlock()
	return nil
}

// Rules returns a copy of the installed rule set.
func (e *Engine) Rules() []types.Rule { return e.ready().Rules() }

// ready is the installed library as it stands, compiled.
func (e *Engine) ready() *Ruleset {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.rules
}

// Evaluator exposes the evaluator, for admitting a candidate rule before saving
// it and for reporting the vocabulary this deployment can answer.
func (e *Engine) Evaluator() *Evaluator { return e.eval }

// SetScorer installs the Scorer consulted alongside the rules, or nil to run on
// rules alone.
func (e *Engine) SetScorer(s Scorer) {
	e.mu.Lock()
	e.scorer = s
	e.mu.Unlock()
}

// ScorerFaults counts the times the Scorer's evidence was discarded — a panic, a
// negative weight, or a weight that is not a number.
//
// It has to be readable. A Scorer that faults on every transaction contributes
// nothing while the engine keeps answering, and this counter is the only thing
// that distinguishes that from a Scorer finding nothing to report.
func (e *Engine) ScorerFaults() int64 { return e.faults.Load() + e.rejects.Load() }

// assess consults the Scorer, and confines it.
//
// Three things a Scorer must not be able to do, each enforced here rather than
// trusted to the Scorer, because the property has to hold for a broken one as
// well as an honest one:
//
// It must not take the rule plane down. Every rule has already been evaluated by
// the time this runs, and losing that verdict to a fault in the statistical plane
// would turn a degraded control into no control — so a panic is contained,
// counted, and the rules' answer stands.
//
// It must not weaken a verdict. Weight-of-evidence is a sum, so a negative weight
// would subtract from a score the rules built and a model could argue a
// transaction down. Non-negative weights are already required of rules; a
// computed weight is held to the same bar, along with the NaN a computation can
// produce and a constant cannot.
//
// It must not act. Its action is capped at ActionCeiling, so the strongest thing a
// statistical judgement can do is put a transaction in front of a person.
func (e *Engine) assess(tx types.Transaction, ent types.Entity) (hit types.RuleHit, ok bool) {
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

	hit, ok = sc.Assess(tx, ent)
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

// Evaluate runs the rule set against a transaction and its customer, returning
// the alerts raised, the aggregate risk score, and the action to take.
func (e *Engine) Evaluate(ctx context.Context, tx types.Transaction, ent types.Entity) ([]types.Alert, float64, string) {
	hits := e.ready().EvalAll(ctx, tx, ent)

	// The Scorer contributes evidence the rule library cannot express. It is
	// consulted after the rules so it can only add to what they found: it can
	// raise a transaction no rule matched, which is why it exists, and cannot
	// lower one they did.
	if hit, ok := e.assess(tx, ent); ok {
		hits = append(hits, hit)
	}
	if len(hits) == 0 {
		return nil, 0, types.ActionAllow
	}

	// Only rules that reached a verdict carry weight. A rule that failed to
	// evaluate has established nothing about this transaction, and because a rule
	// that fails once fails on every transaction, letting it score saturates every
	// score and the queue stops ranking — which is how an ordinary payment comes
	// back at the maximum. It still raises its own alert naming its own failure,
	// and action() still sends that to a person.
	evidential := make([]types.RuleHit, 0, len(hits))
	for _, h := range hits {
		if h.EvalErr == "" {
			evidential = append(evidential, h)
		}
	}

	score, breakdown := Score(evidential)

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
			Causes:         h.Causes,
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
	best, bestRank := types.ActionAllow, 0
	for _, a := range alerts {
		if r := types.ActionRank(a.ActionTaken); r > bestRank {
			best, bestRank = a.ActionTaken, r
		}
	}
	return best
}
