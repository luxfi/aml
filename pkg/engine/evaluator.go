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

// DefaultMaxCacheSize is the default max number of compiled programs in the cache.
const DefaultMaxCacheSize = 10_000

// Evaluator compiles and evaluates expr-lang rule DSL against transaction contexts.
// Compiled programs are cached per rule ID + DSL hash for reuse.
// RED-15: Cache has a max size with LRU eviction to prevent memory exhaustion.
type Evaluator struct {
	mu           sync.RWMutex
	cache        map[string]*vm.Program // key = rule.ID + ":" + rule.DSL
	cacheOrder   []string               // insertion order for LRU eviction
	maxCacheSize int
	funcs        map[string]ExprFunc
}

// NewEvaluator creates an Evaluator with the default helper functions.
func NewEvaluator() *Evaluator {
	return NewEvaluatorWithMaxCache(DefaultMaxCacheSize)
}

// NewEvaluatorWithMaxCache creates an Evaluator with a custom cache limit.
func NewEvaluatorWithMaxCache(maxCache int) *Evaluator {
	if maxCache <= 0 {
		maxCache = DefaultMaxCacheSize
	}
	e := &Evaluator{
		cache:        make(map[string]*vm.Program),
		maxCacheSize: maxCache,
		funcs:        make(map[string]ExprFunc),
	}
	e.registerDefaults()
	return e
}

// CacheLen returns the number of compiled programs in the cache.
func (e *Evaluator) CacheLen() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.cache)
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
	e.funcs["travel_rule_complete"] = func(params ...any) (any, error) { return true, nil }
	e.funcs["chain_risk_score"] = func(params ...any) (any, error) { return 0.0, nil }
	e.funcs["chain_is_sanctioned"] = func(params ...any) (any, error) { return false, nil }

	// RED-13: notional_usd converts tx.Notional to USD using approximate FX rates.
	// This ensures CTR rules fire correctly for non-USD currencies.
	e.funcs["notional_usd"] = func(params ...any) (any, error) {
		if len(params) < 1 {
			return 0.0, fmt.Errorf("notional_usd requires a transaction")
		}
		// expr-lang passes the struct directly.
		switch v := params[0].(type) {
		case types.Transaction:
			return USD(v.Notional, v.Currency), nil
		case *types.Transaction:
			return USD(v.Notional, v.Currency), nil
		case map[string]interface{}:
			notional, _ := v["Notional"].(float64)
			currency, _ := v["Currency"].(string)
			return USD(notional, currency), nil
		default:
			return 0.0, fmt.Errorf("notional_usd: unexpected type %T", params[0])
		}
	}
}

// RED-13: fxRates maps currency codes to their approximate USD exchange rate.
// Updated by the sanctions refresh cron in production; these are conservative defaults.
var fxRates = map[string]float64{
	"USD": 1.0, "EUR": 1.08, "GBP": 1.27, "CHF": 1.12,
	"JPY": 0.0067, "INR": 0.012, "BRL": 0.20, "AUD": 0.66,
	"CAD": 0.74, "SGD": 0.75, "AED": 0.27, "KRW": 0.00075,
	"CNY": 0.14, "MXN": 0.058, "ZAR": 0.055, "TRY": 0.031,
	"RUB": 0.011, "HKD": 0.13, "NZD": 0.61, "SEK": 0.096,
	"NOK": 0.094, "DKK": 0.14, "PLN": 0.25, "THB": 0.028,
}

// USD converts a notional amount to USD using approximate FX rates.
// Unknown currencies return the raw notional value (conservative — assumes USD).
//
// Exported because it is the one conversion in the engine and every caller has to
// use the same one. A second table anywhere would mean two answers to what a
// transaction is worth, and a threshold is only a threshold if the value it is
// compared against was computed one way.
func USD(notional float64, currency string) float64 {
	if currency == "" || currency == "USD" {
		return notional
	}
	rate, ok := fxRates[strings.ToUpper(currency)]
	if !ok {
		return notional // unknown currency → assume raw = conservative
	}
	return notional * rate
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
	// RED-15: Evict oldest entries when cache exceeds max size.
	if len(e.cache) >= e.maxCacheSize {
		evictCount := e.maxCacheSize / 10
		if evictCount < 1 {
			evictCount = 1
		}
		for i := 0; i < evictCount && i < len(e.cacheOrder); i++ {
			delete(e.cache, e.cacheOrder[i])
		}
		if evictCount <= len(e.cacheOrder) {
			e.cacheOrder = e.cacheOrder[evictCount:]
		} else {
			e.cacheOrder = nil
		}
	}
	e.cache[key] = prog
	e.cacheOrder = append(e.cacheOrder, key)
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
			// A rule that could not be evaluated is reported, so the transaction
			// is never silently cleared by a rule that did not run. But it is
			// reported as a fault and NOT as a match: a rule that failed has
			// established nothing about this transaction, and a rule that fails
			// once fails on every transaction — so recording the fault as a match
			// alerts on all of them and empties an alert of its meaning.
			// Evaluate separates the two.
			hits = append(hits, types.RuleHit{
				Rule:    r,
				Match:   false,
				EvalErr: err.Error(),
			})
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
