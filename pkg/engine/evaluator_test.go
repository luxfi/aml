package engine

import (
	"fmt"
	"testing"

	"github.com/luxfi/aml/pkg/types"
)

func TestEvalSimpleBool(t *testing.T) {
	eval := NewEvaluator()
	rule := types.Rule{
		ID:      "test",
		Name:    "test",
		DSL:     `tx.Notional > 10000`,
		Enabled: true,
	}

	ctx := types.EvalContext{
		Tx: types.Transaction{Notional: 15000},
	}

	match, err := eval.Eval(rule, ctx)
	if err != nil {
		t.Fatalf("Eval error: %v", err)
	}
	if !match {
		t.Error("expected match for notional 15000 > 10000")
	}
}

func TestEvalNotMatched(t *testing.T) {
	eval := NewEvaluator()
	rule := types.Rule{
		ID:      "test",
		Name:    "test",
		DSL:     `tx.Notional > 10000`,
		Enabled: true,
	}

	ctx := types.EvalContext{
		Tx: types.Transaction{Notional: 5000},
	}

	match, err := eval.Eval(rule, ctx)
	if err != nil {
		t.Fatalf("Eval error: %v", err)
	}
	if match {
		t.Error("expected no match for notional 5000 < 10000")
	}
}

func TestEvalCompoundCondition(t *testing.T) {
	eval := NewEvaluator()
	rule := types.Rule{
		ID:   "structuring",
		Name: "structuring",
		DSL:  `tx.Notional >= 9000 && tx.Notional < 10000`,
		Enabled: true,
	}

	tests := []struct {
		notional float64
		want     bool
	}{
		{9500, true},
		{9000, true},
		{8999, false},
		{10000, false},
		{10001, false},
	}

	for _, tt := range tests {
		ctx := types.EvalContext{
			Tx: types.Transaction{Notional: tt.notional},
		}
		match, err := eval.Eval(rule, ctx)
		if err != nil {
			t.Fatalf("Eval error for notional %f: %v", tt.notional, err)
		}
		if match != tt.want {
			t.Errorf("notional %f: got match=%v, want %v", tt.notional, match, tt.want)
		}
	}
}

func TestEvalEntityField(t *testing.T) {
	eval := NewEvaluator()
	rule := types.Rule{
		ID:   "pep",
		Name: "pep",
		DSL:  `entity.PEP == true && tx.Notional > 10000`,
		Enabled: true,
	}

	ctx := types.EvalContext{
		Tx:     types.Transaction{Notional: 15000},
		Entity: types.Entity{PEP: true},
	}

	match, err := eval.Eval(rule, ctx)
	if err != nil {
		t.Fatalf("Eval error: %v", err)
	}
	if !match {
		t.Error("expected match for PEP with notional 15000")
	}

	ctx.Entity.PEP = false
	match, err = eval.Eval(rule, ctx)
	if err != nil {
		t.Fatalf("Eval error: %v", err)
	}
	if match {
		t.Error("expected no match for non-PEP")
	}
}

func TestEvalStringComparison(t *testing.T) {
	eval := NewEvaluator()
	rule := types.Rule{
		ID:   "ctr",
		Name: "ctr",
		DSL:  `tx.Notional > 10000 && tx.Currency == "USD"`,
		Enabled: true,
	}

	ctx := types.EvalContext{
		Tx: types.Transaction{Notional: 15000, Currency: "USD"},
	}
	match, err := eval.Eval(rule, ctx)
	if err != nil {
		t.Fatalf("Eval error: %v", err)
	}
	if !match {
		t.Error("expected match for USD 15000")
	}

	ctx.Tx.Currency = "EUR"
	match, err = eval.Eval(rule, ctx)
	if err != nil {
		t.Fatalf("Eval error: %v", err)
	}
	if match {
		t.Error("expected no match for EUR")
	}
}

func TestEvalInvalidDSL(t *testing.T) {
	eval := NewEvaluator()
	rule := types.Rule{
		ID:   "bad",
		Name: "bad",
		DSL:  `this is not valid syntax !!!`,
		Enabled: true,
	}
	_, err := eval.Eval(rule, types.EvalContext{})
	if err == nil {
		t.Error("expected error for invalid DSL")
	}
}

func TestEvalAllFilters(t *testing.T) {
	eval := NewEvaluator()
	rules := []types.Rule{
		{
			ID: "r1", Name: "r1", DSL: `tx.Notional > 100`,
			Enabled: true, JurisdictionFilter: []string{"US"},
		},
		{
			ID: "r2", Name: "r2", DSL: `tx.Notional > 100`,
			Enabled: true, JurisdictionFilter: []string{"UK"},
		},
		{
			ID: "r3", Name: "r3", DSL: `tx.Notional > 100`,
			Enabled: false,
		},
	}

	ctx := types.EvalContext{
		Tx:     types.Transaction{Notional: 200},
		Entity: types.Entity{Jurisdiction: "US"},
	}

	hits := eval.EvalAll(rules, ctx)
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
	if hits[0].Rule.ID != "r1" {
		t.Errorf("expected r1, got %s", hits[0].Rule.ID)
	}
}

func TestMatchesFilter(t *testing.T) {
	if !matchesFilter(nil, "anything") {
		t.Error("empty filter should match everything")
	}
	if !matchesFilter([]string{"US", "UK"}, "us") {
		t.Error("case-insensitive match should work")
	}
	if matchesFilter([]string{"US", "UK"}, "DE") {
		t.Error("non-matching should return false")
	}
}

func TestEvalCaching(t *testing.T) {
	eval := NewEvaluator()
	rule := types.Rule{
		ID:   "cached",
		Name: "cached",
		DSL:  `tx.Notional > 0`,
		Enabled: true,
	}

	// First compile.
	_, err := eval.Compile(rule)
	if err != nil {
		t.Fatal(err)
	}

	// Second compile — should hit cache.
	_, err = eval.Compile(rule)
	if err != nil {
		t.Fatal(err)
	}

	if eval.CacheLen() != 1 {
		t.Errorf("cache should have 1 entry, got %d", eval.CacheLen())
	}
}

// TestEvaluatorCacheEviction (RED-15) verifies that the evaluator cache evicts
// entries when it exceeds maxCacheSize.
func TestEvaluatorCacheEviction(t *testing.T) {
	eval := NewEvaluatorWithMaxCache(100)

	// Compile 200 unique rules.
	for i := 0; i < 200; i++ {
		rule := types.Rule{
			ID:      fmt.Sprintf("r%d", i),
			Name:    fmt.Sprintf("r%d", i),
			DSL:     fmt.Sprintf("tx.Notional > %d", i),
			Enabled: true,
		}
		_, err := eval.Compile(rule)
		if err != nil {
			t.Fatalf("compile r%d: %v", i, err)
		}
	}

	size := eval.CacheLen()
	if size > 100 {
		t.Errorf("RED-15: cache size = %d, want <= 100 after eviction", size)
	}
}
