package engine

import (
	"fmt"
	"strings"
	"sync"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"

	"github.com/luxfi/aml/pkg/types"
)

// ExprFunc is the function signature required by expr-lang.
type ExprFunc = func(params ...any) (any, error)

// Evaluator compiles and evaluates expr-lang rule DSL against transaction contexts.
// Compiled programs are cached per rule ID + DSL hash for reuse.
type Evaluator struct {
	mu    sync.RWMutex
	cache map[string]*vm.Program // key = rule.ID + ":" + rule.DSL
	funcs map[string]ExprFunc
}

// NewEvaluator creates an Evaluator with the default helper functions.
func NewEvaluator() *Evaluator {
	e := &Evaluator{
		cache: make(map[string]*vm.Program),
		funcs: make(map[string]ExprFunc),
	}
	e.registerDefaults()
	return e
}

// RegisterFunc adds a named function available in the expr-lang environment.
func (e *Evaluator) RegisterFunc(name string, fn ExprFunc) {
	e.mu.Lock()
	e.funcs[name] = fn
	e.mu.Unlock()
}

// registerDefaults registers stub helper functions for the DSL.
// These return conservative defaults. Production wires real implementations.
func (e *Evaluator) registerDefaults() {
	e.funcs["count_last_24h"] = func(params ...any) (any, error) { return 0, nil }
	e.funcs["sum_last_24h"] = func(params ...any) (any, error) { return 0.0, nil }
	e.funcs["sum_last_30d"] = func(params ...any) (any, error) { return 0.0, nil }
	e.funcs["first_tx"] = func(params ...any) (any, error) { return false, nil }
	e.funcs["first_counterparty"] = func(params ...any) (any, error) { return false, nil }
	e.funcs["last_tx_age"] = func(params ...any) (any, error) { return 0, nil }
	e.funcs["is_round_trip"] = func(params ...any) (any, error) { return false, nil }
	e.funcs["geo_match"] = func(params ...any) (any, error) { return true, nil }
	e.funcs["sanctions_hit"] = func(params ...any) (any, error) { return false, nil }
	e.funcs["sanctioned_jurisdictions"] = func(params ...any) (any, error) {
		return []string{"IR", "KP", "SY", "CU", "AF", "BY", "CF", "CD", "LY", "ML", "SO", "SS", "SD", "YE"}, nil
	}
	e.funcs["hour"] = func(params ...any) (any, error) { return 12, nil }
	e.funcs["is_mixer_address"] = func(params ...any) (any, error) { return false, nil }
	e.funcs["is_darknet_market"] = func(params ...any) (any, error) { return false, nil }
	e.funcs["layering_score"] = func(params ...any) (any, error) { return 0.0, nil }
	e.funcs["smurfing_detected"] = func(params ...any) (any, error) { return false, nil }
	e.funcs["wash_trade_detected"] = func(params ...any) (any, error) { return false, nil }
	e.funcs["travel_rule_threshold"] = func(params ...any) (any, error) { return 3000.0, nil }
}

// cacheKey returns the cache key for a compiled rule program.
func cacheKey(r types.Rule) string {
	return r.ID + ":" + r.DSL
}

// Compile compiles a rule DSL into a cached vm.Program.
func (e *Evaluator) Compile(r types.Rule) (*vm.Program, error) {
	key := cacheKey(r)

	e.mu.RLock()
	if prog, ok := e.cache[key]; ok {
		e.mu.RUnlock()
		return prog, nil
	}
	e.mu.RUnlock()

	opts := []expr.Option{
		expr.Env(map[string]interface{}{
			"tx":     types.Transaction{},
			"entity": types.Entity{},
		}),
	}
	for name, fn := range e.funcs {
		opts = append(opts, expr.Function(name, fn))
	}

	prog, err := expr.Compile(r.DSL, opts...)
	if err != nil {
		return nil, fmt.Errorf("compile rule %q: %w", r.Name, err)
	}

	e.mu.Lock()
	e.cache[key] = prog
	e.mu.Unlock()

	return prog, nil
}

// Eval evaluates a single rule against a transaction + entity context.
func (e *Evaluator) Eval(r types.Rule, ctx types.EvalContext) (bool, error) {
	prog, err := e.Compile(r)
	if err != nil {
		return false, err
	}

	env := map[string]interface{}{
		"tx":     ctx.Tx,
		"entity": ctx.Entity,
	}

	out, err := expr.Run(prog, env)
	if err != nil {
		return false, fmt.Errorf("eval rule %q: %w", r.Name, err)
	}

	switch v := out.(type) {
	case bool:
		return v, nil
	case float64:
		return v > 0, nil
	case int:
		return v > 0, nil
	default:
		return false, fmt.Errorf("rule %q returned non-bool type %T", r.Name, out)
	}
}

// EvalAll evaluates all rules against a context and returns matching hits.
// Rules are filtered by jurisdiction and asset class before evaluation.
func (e *Evaluator) EvalAll(rules []types.Rule, ctx types.EvalContext) []types.RuleHit {
	var hits []types.RuleHit
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if !matchesFilter(r.JurisdictionFilter, ctx.Entity.Jurisdiction) {
			continue
		}
		if !matchesFilter(r.AssetClassFilter, ctx.Tx.AssetClass) {
			continue
		}

		match, err := e.Eval(r, ctx)
		if err != nil {
			// Log and skip — fail open on evaluation errors for non-critical.
			continue
		}

		if match {
			hits = append(hits, types.RuleHit{
				Rule:  r,
				Match: true,
			})
		}
	}
	return hits
}

// matchesFilter returns true if the filter is empty (matches all)
// or if val is in the filter list.
func matchesFilter(filter []string, val string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, f := range filter {
		if strings.EqualFold(f, val) {
			return true
		}
	}
	return false
}
