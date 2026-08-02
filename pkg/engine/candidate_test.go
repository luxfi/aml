// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package engine

import (
	"context"
	"fmt"
	"runtime"
	"testing"

	"github.com/luxfi/aml/internal/source"
	"github.com/luxfi/aml/pkg/history"
	"github.com/luxfi/aml/pkg/types"
)

// A candidate rule is not an installed one, and the engine must not remember it.
//
// /v1/aml/rules/test replays a candidate over a tenant's history through the
// evaluator that answers production. The candidate's text arrives on the wire.
// An evaluator that kept every rule it was ever asked to compile would therefore
// hold a table whose key is a caller's string and whose size a caller chooses —
// no cap, no eviction, no tenant in the key, in the memory every other
// institution's ingest runs in. On a one-replica pod the end of that is an OOM,
// and an OOM of this process is every control off at once.
//
// The fix is not a bigger cap: it is that the compiled form of a rule is a VALUE
// with an owner, so a study's programs are dropped when the study is. Nothing
// shared, nothing to grow.

// TestTheEvaluatorHoldsNoTable reads the source, because the property is about
// what the type can still express rather than what today's callers do.
func TestTheEvaluatorHoldsNoTable(t *testing.T) {
	source.NoTable(t, "evaluator.go", "Evaluator",
		"A rule's compiled form is a value held by whoever asked for it (see Ready). A table here is keyed by a candidate rule the caller wrote, so the caller decides how many entries exist and nothing ever removes one.")
}

// dsl is a distinct, valid candidate expression. Each one is a rule somebody
// could type into the replay screen.
func dsl(i int) string {
	return fmt.Sprintf("Tx.Notional > %d.0 && Tx.Currency == %q", 1000+i, "USD")
}

// TestManyCandidatesLeaveNothingBehind measures it.
//
// Two thousand distinct candidates, each compiled and run the way a replay
// compiles and runs one, then the heap is read. The published property is that
// this engine's retained memory is a function of the rules the DEPLOYMENT
// installed, so the growth here must be a rounding error against a pod, not a
// multiple of what a caller asked for.
func TestManyCandidatesLeaveNothingBehind(t *testing.T) {
	e := NewEvaluator(providers(history.NewMemory(nil)))
	tx := types.Transaction{ID: "tx-1", OrgID: "hanzo/acme", Currency: "USD", Notional: 25_000}
	ent := types.Entity{ID: "u1"}

	const candidates = 2000
	before := heap()
	for i := range candidates {
		set, err := e.Ready([]types.Rule{{
			ID: "candidate", Name: "candidate", DSL: dsl(i),
			Severity: types.SeverityLow, Weight: 0.1, Action: types.ActionFlag, Enabled: true,
		}})
		if err != nil {
			t.Fatalf("candidate %d: %v", i, err)
		}
		set.EvalAll(context.Background(), tx, ent)
	}
	after := heap()

	// A compiled program plus its source text is a few kilobytes; two thousand of
	// them is tens of megabytes and would clear this by a wide margin. The
	// threshold is deliberately loose — what is being measured is the difference
	// between "bounded by the deployment" and "chosen by the caller".
	const ceiling = 4 << 20
	if grew := after - before; grew > ceiling {
		t.Errorf("%d candidate rules left %d bytes behind, want under %d: the evaluator is remembering what callers asked it to compile",
			candidates, grew, ceiling)
	}
}

// heap is the live heap after a collection, in bytes.
func heap() int64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return int64(m.HeapAlloc)
}

// TestAnInstalledRuleIsCompiledOnce is the property the table existed for, kept.
//
// The engine evaluates its whole library on every transaction. Compiling on each
// one would be the cost that makes an institution disable rules, so the library
// is compiled at installation and the engine runs the programs.
func TestAnInstalledRuleIsCompiledOnce(t *testing.T) {
	eng := New(providers(history.NewMemory(nil)))
	if err := eng.SetRules([]types.Rule{{
		ID: "ctr", Name: "CTR", DSL: "Tx.Notional > 10000.0",
		Severity: types.SeverityHigh, Weight: 0.3, Action: types.ActionReport, Enabled: true,
	}}); err != nil {
		t.Fatal(err)
	}
	first := eng.ready()
	eng.Evaluate(context.Background(), types.Transaction{ID: "tx-1", OrgID: "hanzo/acme", Notional: 25_000}, types.Entity{ID: "u1"})
	eng.Evaluate(context.Background(), types.Transaction{ID: "tx-2", OrgID: "hanzo/acme", Notional: 25_000}, types.Entity{ID: "u1"})
	if eng.ready() != first {
		t.Error("the engine recompiled its installed library while evaluating")
	}
}
