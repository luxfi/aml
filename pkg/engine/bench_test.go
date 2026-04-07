package engine

import (
	"fmt"
	"testing"

	"github.com/luxfi/aml/pkg/rules"
	"github.com/luxfi/aml/pkg/types"
)

// BenchmarkEvaluate measures single-transaction evaluation throughput.
// Measures single-transaction evaluation throughput.
func BenchmarkEvaluate(b *testing.B) {
	eng := New(rules.StarterRules("bench"))

	tx := types.Transaction{
		ID:         "bench-tx",
		UserID:     "user-1",
		Symbol:     "AAPL",
		AssetClass: "us_equity",
		Side:       "buy",
		Qty:        10,
		Notional:   1500.0,
		Currency:   "USD",
	}
	entity := types.Entity{
		ID:           "user-1",
		Jurisdiction: "US",
		KYCLevel:     2,
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		eng.Evaluate(tx, entity)
	}
}

// BenchmarkEvaluateStructuring measures evaluation of a transaction that
// triggers the structuring rule (notional in $9000-$10000 range).
func BenchmarkEvaluateStructuring(b *testing.B) {
	eng := New(rules.StarterRules("bench"))

	tx := types.Transaction{
		ID:         "bench-struct",
		UserID:     "user-2",
		Symbol:     "BTC/USD",
		AssetClass: "us_crypto",
		Side:       "buy",
		Qty:        1,
		Notional:   9500.0,
		Currency:   "USD",
	}
	entity := types.Entity{
		ID:           "user-2",
		Jurisdiction: "US",
		KYCLevel:     2,
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		alerts, score, action := eng.Evaluate(tx, entity)
		_ = alerts
		_ = score
		_ = action
	}
}

// BenchmarkEvaluateParallel measures concurrent evaluation throughput.
func BenchmarkEvaluateParallel(b *testing.B) {
	eng := New(rules.StarterRules("bench"))

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			tx := types.Transaction{
				ID:         fmt.Sprintf("par-%d", i),
				UserID:     "user-par",
				Symbol:     "AAPL",
				AssetClass: "us_equity",
				Side:       "buy",
				Qty:        10,
				Notional:   1500.0,
				Currency:   "USD",
			}
			entity := types.Entity{ID: "user-par", Jurisdiction: "US", KYCLevel: 2}
			eng.Evaluate(tx, entity)
			i++
		}
	})
}
