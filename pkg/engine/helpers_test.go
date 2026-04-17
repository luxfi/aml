// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package engine

import (
	"context"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

func seedStore(t *testing.T) (*MemoryStore, *Engine) {
	t.Helper()
	store := NewMemoryStore()

	// Seed data: 3 transactions for user-A
	now := time.Now().UTC()
	txs := []types.Transaction{
		{
			ID: "tx-1", UserID: "user-A", Notional: 5000, Currency: "USD",
			Symbol: "BTC", Side: "buy", Counterparty: "exchange-1",
			CreatedAt: now.Add(-2 * time.Hour),
		},
		{
			ID: "tx-2", UserID: "user-A", Notional: 15000, Currency: "USD",
			Symbol: "BTC", Side: "sell", Counterparty: "exchange-2",
			CreatedAt: now.Add(-1 * time.Hour),
		},
		{
			ID: "tx-3", UserID: "user-A", Notional: 8000, Currency: "USD",
			Symbol: "ETH", Side: "buy", Counterparty: "exchange-1",
			CreatedAt: now.Add(-30 * time.Minute),
		},
	}
	for _, tx := range txs {
		if err := store.Save(context.Background(), tx); err != nil {
			t.Fatalf("seed save: %v", err)
		}
	}

	rules := []types.Rule{
		{
			ID: "test-rule", Name: "test", DSL: "true",
			Enabled: true, Weight: 1.0, Action: types.ActionFlag,
		},
	}
	eng := New(rules)
	RegisterStoreHelpers(eng, store)

	return store, eng
}

func TestHelpers_CountLast24h(t *testing.T) {
	_, eng := seedStore(t)
	eval := eng.Evaluator()

	fn := eval.funcs["count_last_24h"]
	result, err := fn("user-A")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	count, ok := result.(int)
	if !ok {
		t.Fatalf("expected int, got %T", result)
	}
	if count != 3 {
		t.Fatalf("expected 3 transactions, got %d", count)
	}
}

func TestHelpers_CountLast24h_NoTxs(t *testing.T) {
	_, eng := seedStore(t)
	eval := eng.Evaluator()

	fn := eval.funcs["count_last_24h"]
	result, err := fn("user-B")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	count := result.(int)
	if count != 0 {
		t.Fatalf("expected 0, got %d", count)
	}
}

func TestHelpers_SumLast24h(t *testing.T) {
	_, eng := seedStore(t)
	eval := eng.Evaluator()

	fn := eval.funcs["sum_last_24h"]
	result, err := fn("user-A")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sum, ok := result.(float64)
	if !ok {
		t.Fatalf("expected float64, got %T", result)
	}
	if sum != 28000 { // 5000 + 15000 + 8000
		t.Fatalf("expected 28000, got %f", sum)
	}
}

func TestHelpers_SumLast30d(t *testing.T) {
	_, eng := seedStore(t)
	eval := eng.Evaluator()

	fn := eval.funcs["sum_last_30d"]
	result, err := fn("user-A")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sum := result.(float64)
	if sum != 28000 {
		t.Fatalf("expected 28000, got %f", sum)
	}
}

func TestHelpers_FirstTx(t *testing.T) {
	_, eng := seedStore(t)
	eval := eng.Evaluator()

	fn := eval.funcs["first_tx"]

	// user-A has transactions
	result, err := fn("user-A")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.(bool) != false {
		t.Fatal("expected false (not first tx)")
	}

	// user-B has no transactions
	result, err = fn("user-B")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.(bool) != true {
		t.Fatal("expected true (first tx)")
	}
}

func TestHelpers_FirstCounterparty(t *testing.T) {
	_, eng := seedStore(t)
	eval := eng.Evaluator()

	fn := eval.funcs["first_counterparty"]

	// user-A has transacted with exchange-1
	result, err := fn("user-A", "exchange-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.(bool) != false {
		t.Fatal("expected false (not first counterparty)")
	}

	// user-A has NOT transacted with exchange-3
	result, err = fn("user-A", "exchange-3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.(bool) != true {
		t.Fatal("expected true (first counterparty)")
	}
}

func TestHelpers_LastTxAge(t *testing.T) {
	_, eng := seedStore(t)
	eval := eng.Evaluator()

	fn := eval.funcs["last_tx_age"]

	// user-A's last tx was ~30 min ago = 0 days
	result, err := fn("user-A")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	days := result.(int)
	if days != 0 {
		t.Fatalf("expected 0 days (recent), got %d", days)
	}

	// user-B has no transactions = 999 days (sentinel)
	result, err = fn("user-B")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	days = result.(int)
	if days != 999 {
		t.Fatalf("expected 999 days (never transacted), got %d", days)
	}
}

func TestHelpers_IsRoundTrip(t *testing.T) {
	_, eng := seedStore(t)
	eval := eng.Evaluator()

	fn := eval.funcs["is_round_trip"]

	// user-A bought and sold BTC within 24h
	result, err := fn("user-A", "24h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.(bool) != true {
		t.Fatal("expected true (round trip detected)")
	}

	// user-B has no transactions
	result, err = fn("user-B", "24h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.(bool) != false {
		t.Fatal("expected false (no round trip)")
	}
}

func TestHelpers_MissingParams(t *testing.T) {
	_, eng := seedStore(t)
	eval := eng.Evaluator()

	tests := []struct {
		name string
		fn   string
		args []any
	}{
		{"count_last_24h no args", "count_last_24h", nil},
		{"sum_last_24h no args", "sum_last_24h", nil},
		{"sum_last_30d no args", "sum_last_30d", nil},
		{"first_tx no args", "first_tx", nil},
		{"first_counterparty no args", "first_counterparty", nil},
		{"first_counterparty one arg", "first_counterparty", []any{"user-A"}},
		{"last_tx_age no args", "last_tx_age", nil},
		{"is_round_trip no args", "is_round_trip", nil},
		{"is_round_trip one arg", "is_round_trip", []any{"user-A"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn := eval.funcs[tt.fn]
			_, err := fn(tt.args...)
			if err == nil {
				t.Fatalf("expected error for %s with args %v", tt.fn, tt.args)
			}
		})
	}
}

func TestHelpers_InvalidTypes(t *testing.T) {
	_, eng := seedStore(t)
	eval := eng.Evaluator()

	tests := []struct {
		name string
		fn   string
		args []any
	}{
		{"count_last_24h int arg", "count_last_24h", []any{123}},
		{"first_tx int arg", "first_tx", []any{123}},
		{"first_counterparty int,string", "first_counterparty", []any{123, "cp"}},
		{"first_counterparty string,int", "first_counterparty", []any{"uid", 123}},
		{"last_tx_age int arg", "last_tx_age", []any{123}},
		{"is_round_trip int,string", "is_round_trip", []any{123, "24h"}},
		{"is_round_trip string,int", "is_round_trip", []any{"uid", 123}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn := eval.funcs[tt.fn]
			_, err := fn(tt.args...)
			if err == nil {
				t.Fatalf("expected error for %s with args %v", tt.fn, tt.args)
			}
		})
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
		wantErr  bool
	}{
		{"24h", 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"30d", 30 * 24 * time.Hour, false},
		{"15m", 15 * time.Minute, false},
		{"1h", 1 * time.Hour, false},
		{"", 0, true},
		{"x", 0, true},
		{"24x", 0, true},
		{"abc", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseDuration(tt.input)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.expected {
				t.Fatalf("expected %v, got %v", tt.expected, got)
			}
		})
	}
}

func TestMemoryStore_Interface(t *testing.T) {
	var _ TransactionStore = (*MemoryStore)(nil)
}
