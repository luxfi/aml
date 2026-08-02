package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

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
//
// It holds NO compiled rules. A rule's compiled form is a value — see [Ruleset]
// — belonging to whoever asked for it, and dropped with them. That is not an
// optimisation left undone: a candidate rule's text arrives on the wire at
// /v1/aml/rules/test, so a table of compiled rules kept here would be keyed by a
// string a caller wrote, sized by how many distinct ones a caller sends, with no
// cap and no removal, in the memory every institution's ingest runs in.
type Evaluator struct {
	p Providers
}

// NewEvaluator builds an evaluator over the given evidence providers.
func NewEvaluator(p Providers) *Evaluator { return &Evaluator{p: p} }

// Ruleset is a rule set compiled once, held by whoever asked for it.
//
// It is what makes "compile once, run over a hundred thousand events" and "keep
// nothing a caller named" the same design rather than opposing ones: the
// programs have an owner and a lifetime. The engine's owner is the installed
// library; a replay's owner is the one request.
type Ruleset struct {
	p     Providers
	rules []types.Rule
	progs []*vm.Program
}

// Ready admits every rule in a set and returns them compiled, or reports every
// one that cannot be evaluated.
//
// The whole set is rejected if any rule fails. A partially compiled set is a
// control surface nobody can describe: the operator believes the catalog is in
// force, and the difference is discoverable only by reading startup logs.
func (e *Evaluator) Ready(rules []types.Rule) (*Ruleset, error) {
	out := &Ruleset{p: e.p, rules: make([]types.Rule, 0, len(rules)), progs: make([]*vm.Program, 0, len(rules))}
	var errs []error
	for _, r := range rules {
		if r.Weight < 0 {
			errs = append(errs, fmt.Errorf("rule %q: weight %v is negative, which would subtract from the risk score", r.ID, r.Weight))
			continue
		}
		prog, err := e.Admit(r)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out.rules = append(out.rules, r)
		out.progs = append(out.progs, prog)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("rule set rejected, %d of %d rules cannot be evaluated: %w",
			len(errs), len(rules), errors.Join(errs...))
	}
	return out, nil
}

// Rules is the set as it was admitted, copied.
func (rs *Ruleset) Rules() []types.Rule {
	if rs == nil {
		return nil
	}
	out := make([]types.Rule, len(rs.rules))
	copy(out, rs.rules)
	return out
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
// The program it returns belongs to the caller; nothing is kept here.
func (e *Evaluator) Admit(r types.Rule) (*vm.Program, error) {
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

	prog, err := expr.Compile(r.DSL, expr.Env(&scope{}), expr.AsBool())
	if err != nil {
		return nil, fmt.Errorf("rule %q: %w", r.ID, err)
	}
	return prog, nil
}

// run executes one compiled rule against an existing scope.
func run(prog *vm.Program, s *scope, r types.Rule) (bool, error) {
	out, err := expr.Run(prog, s)
	if err != nil {
		return false, fmt.Errorf("rule %q: %w", r.ID, err)
	}
	// Compiling with AsBool guarantees the type.
	return out.(bool), nil
}

// EvalAll evaluates every enabled, in-scope rule of this set and returns the
// hits.
//
// A rule that fails at evaluation time becomes a hit carrying the failure. The
// rule set was admitted, so a failure here is a data or storage fault — an
// unknown currency, an absent subject, an unreachable store — and the
// transaction it happened on is precisely the one nobody has assessed. Passing
// it through as clean would turn an outage into an approval.
func (rs *Ruleset) EvalAll(ctx context.Context, tx types.Transaction, ent types.Entity) []types.RuleHit {
	if rs == nil {
		return nil
	}
	// One scope for the whole rule set, so the window a dozen rules ask about is
	// fetched once. A scope per rule turns a rule set into a query per rule per
	// transaction, which is the cost that makes institutions disable rules.
	s := newScope(ctx, rs.p, tx, ent)

	var hits []types.RuleHit
	for i, r := range rs.rules {
		if !r.Enabled {
			continue
		}
		if !scoped(r.JurisdictionFilter, ent.Jurisdiction) {
			continue
		}
		if !scoped(r.AssetClassFilter, tx.AssetClass) {
			continue
		}
		match, err := run(rs.progs[i], s, r)
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
