package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
	"github.com/expr-lang/expr/vm"

	"github.com/luxfi/aml/pkg/types"
)

// Evaluator compiles rule expressions and evaluates them against transactions.
//
// A rule is admitted only if it parses, names only terms in the vocabulary, uses
// only terms whose evidence provider is configured, and yields a boolean. There
// is no fallback for a term the deployment cannot answer. The alternative — a
// term that returns a plausible constant when its provider is absent — produces
// a rule that is present in the catalog, visible in the interface, reported to
// the regulator, and incapable of ever firing.
type Evaluator struct {
	p Providers

	mu    sync.RWMutex
	cache map[string]*vm.Program
}

// NewEvaluator builds an evaluator over the given evidence providers.
func NewEvaluator(p Providers) *Evaluator {
	return &Evaluator{p: p, cache: make(map[string]*vm.Program)}
}

// Vocabulary lists the terms this evaluator can answer, sorted. Terms whose
// provider is not configured are omitted, so the list describes what this
// deployment can actually detect rather than what the language could express.
func (e *Evaluator) Vocabulary() []string {
	out := make([]string, 0, len(vocabulary))
	for term, cap := range vocabulary {
		if e.p.has(cap) {
			out = append(out, term)
		}
	}
	sort.Strings(out)
	return out
}

// terms collects the name of every function call in an expression.
type terms struct{ names map[string]struct{} }

func (t *terms) Visit(node *ast.Node) {
	call, ok := (*node).(*ast.CallNode)
	if !ok {
		return
	}
	if id, ok := call.Callee.(*ast.IdentifierNode); ok {
		t.names[id.Value] = struct{}{}
	}
}

// Admit compiles a rule and reports why it cannot be installed, if it cannot.
// The compiled program is cached, so admitting a rule also prepares it to run.
func (e *Evaluator) Admit(r types.Rule) (*vm.Program, error) {
	key := r.ID + ":" + r.DSL
	e.mu.RLock()
	prog, ok := e.cache[key]
	e.mu.RUnlock()
	if ok {
		return prog, nil
	}

	tree, err := parser.Parse(r.DSL)
	if err != nil {
		return nil, fmt.Errorf("rule %q: %w", r.ID, err)
	}

	seen := &terms{names: make(map[string]struct{})}
	ast.Walk(&tree.Node, seen)

	var missing []string
	for name := range seen.names {
		cap, known := vocabulary[name]
		if !known {
			continue // not an evidence term; the compile below validates it
		}
		if !e.p.has(cap) {
			missing = append(missing, fmt.Sprintf("%s (needs %s)", name, cap))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("rule %q: %w for %s", r.ID, ErrNoProvider, strings.Join(missing, ", "))
	}

	prog, err = expr.Compile(r.DSL, expr.Env(&scope{}), expr.AsBool())
	if err != nil {
		return nil, fmt.Errorf("rule %q: %w", r.ID, err)
	}

	e.mu.Lock()
	e.cache[key] = prog
	e.mu.Unlock()
	return prog, nil
}

// Eval runs one rule against a transaction. It is the path used to try a
// candidate rule before saving it; evaluating a whole set goes through EvalAll,
// which shares one scope so a window is fetched once.
func (e *Evaluator) Eval(ctx context.Context, r types.Rule, tx types.Transaction, ent types.Entity) (bool, error) {
	return e.eval(newScope(ctx, e.p, tx, ent), r)
}

// eval runs one rule against an existing scope.
func (e *Evaluator) eval(s *scope, r types.Rule) (bool, error) {
	prog, err := e.Admit(r)
	if err != nil {
		return false, err
	}
	out, err := expr.Run(prog, s)
	if err != nil {
		return false, fmt.Errorf("rule %q: %w", r.ID, err)
	}
	// Compiling with AsBool guarantees the type.
	return out.(bool), nil
}

// EvalAll evaluates every enabled, in-scope rule and returns the hits.
//
// A rule that fails at evaluation time becomes a hit carrying the failure. The
// rule set was admitted, so a failure here is a data or storage fault — an
// unknown currency, an absent subject, an unreachable store — and the
// transaction it happened on is precisely the one nobody has assessed. Passing
// it through as clean would turn an outage into an approval.
func (e *Evaluator) EvalAll(ctx context.Context, rules []types.Rule, tx types.Transaction, ent types.Entity) []types.RuleHit {
	// One scope for the whole rule set, so the window a dozen rules ask about is
	// fetched once. A scope per rule turns a rule set into a query per rule per
	// transaction, which is the cost that makes institutions disable rules.
	s := newScope(ctx, e.p, tx, ent)

	var hits []types.RuleHit
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if !scoped(r.JurisdictionFilter, ent.Jurisdiction) {
			continue
		}
		if !scoped(r.AssetClassFilter, tx.AssetClass) {
			continue
		}
		match, err := e.eval(s, r)
		if err != nil {
			hits = append(hits, types.RuleHit{Rule: r, Match: true, EvalErr: err.Error()})
			continue
		}
		if match {
			hits = append(hits, types.RuleHit{Rule: r, Match: true})
		}
	}
	return hits
}

// scoped reports whether a rule restricted to a set of values applies here. An
// empty restriction applies to everything.
func scoped(filter []string, val string) bool {
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
