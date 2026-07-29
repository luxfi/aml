// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package engine

import (
	"context"
	"fmt"
	"time"
)

// RegisterStoreHelpers replaces the default stub helper functions with
// real implementations backed by a TransactionStore. Call this at startup
// after creating the engine and store.
func RegisterStoreHelpers(e *Engine, store TransactionStore) {
	eval := e.Evaluator()

	eval.RegisterFunc("count_last_24h", func(params ...any) (any, error) {
		return countLastNh(store, params, 24)
	})

	eval.RegisterFunc("sum_last_24h", func(params ...any) (any, error) {
		return sumLastNh(store, params, 24)
	})

	eval.RegisterFunc("sum_last_30d", func(params ...any) (any, error) {
		return sumLastNd(store, params, 30)
	})

	eval.RegisterFunc("first_tx", func(params ...any) (any, error) {
		if len(params) < 1 {
			return false, fmt.Errorf("first_tx requires userID")
		}
		userID, ok := params[0].(string)
		if !ok {
			return false, fmt.Errorf("first_tx: userID must be string")
		}
		return store.FirstTransaction(context.Background(), userID)
	})

	eval.RegisterFunc("first_counterparty", func(params ...any) (any, error) {
		if len(params) < 2 {
			return false, fmt.Errorf("first_counterparty requires userID and counterparty")
		}
		userID, ok := params[0].(string)
		if !ok {
			return false, fmt.Errorf("first_counterparty: userID must be string")
		}
		counterparty, ok := params[1].(string)
		if !ok {
			return false, fmt.Errorf("first_counterparty: counterparty must be string")
		}
		return store.FirstCounterparty(context.Background(), userID, counterparty)
	})

	eval.RegisterFunc("last_tx_age", func(params ...any) (any, error) {
		if len(params) < 1 {
			return 0, fmt.Errorf("last_tx_age requires userID")
		}
		userID, ok := params[0].(string)
		if !ok {
			return 0, fmt.Errorf("last_tx_age: userID must be string")
		}
		lastTime, err := store.LastTransactionTime(context.Background(), userID)
		if err != nil {
			return 0, err
		}
		if lastTime.IsZero() {
			return 999, nil // Never transacted = very old
		}
		days := int(time.Since(lastTime).Hours() / 24)
		return days, nil
	})

	eval.RegisterFunc("is_round_trip", func(params ...any) (any, error) {
		if len(params) < 2 {
			return false, fmt.Errorf("is_round_trip requires userID and window")
		}
		userID, ok := params[0].(string)
		if !ok {
			return false, fmt.Errorf("is_round_trip: userID must be string")
		}
		windowStr, ok := params[1].(string)
		if !ok {
			return false, fmt.Errorf("is_round_trip: window must be string")
		}
		window, err := parseDuration(windowStr)
		if err != nil {
			return false, fmt.Errorf("is_round_trip: %w", err)
		}
		return store.RoundTrip(context.Background(), userID, window)
	})
}

// countLastNh counts transactions in the last N hours for a user.
func countLastNh(store TransactionStore, params []any, hours int) (any, error) {
	if len(params) < 1 {
		return 0, fmt.Errorf("count_last_%dh requires userID", hours)
	}
	userID, ok := params[0].(string)
	if !ok {
		return 0, fmt.Errorf("count_last_%dh: userID must be string", hours)
	}
	since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)
	return store.CountByUser(context.Background(), userID, since)
}

// sumLastNh sums notional in the last N hours for a user.
func sumLastNh(store TransactionStore, params []any, hours int) (any, error) {
	if len(params) < 1 {
		return 0.0, fmt.Errorf("sum_last_%dh requires userID", hours)
	}
	userID, ok := params[0].(string)
	if !ok {
		return 0.0, fmt.Errorf("sum_last_%dh: userID must be string", hours)
	}
	since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour)
	return store.SumNotionalByUser(context.Background(), userID, since)
}

// sumLastNd sums notional in the last N days for a user.
func sumLastNd(store TransactionStore, params []any, days int) (any, error) {
	if len(params) < 1 {
		return 0.0, fmt.Errorf("sum_last_%dd requires userID", days)
	}
	userID, ok := params[0].(string)
	if !ok {
		return 0.0, fmt.Errorf("sum_last_%dd: userID must be string", days)
	}
	since := time.Now().UTC().AddDate(0, 0, -days)
	return store.SumNotionalByUser(context.Background(), userID, since)
}

// parseDuration parses human-friendly duration strings like "24h", "7d", "30d".
func parseDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	unit := s[len(s)-1]
	numStr := s[:len(s)-1]
	var num int
	_, err := fmt.Sscanf(numStr, "%d", &num)
	if err != nil {
		return 0, fmt.Errorf("invalid duration number %q: %w", numStr, err)
	}
	switch unit {
	case 'h':
		return time.Duration(num) * time.Hour, nil
	case 'd':
		return time.Duration(num) * 24 * time.Hour, nil
	case 'm':
		return time.Duration(num) * time.Minute, nil
	default:
		return 0, fmt.Errorf("unknown duration unit %q (use h/d/m)", string(unit))
	}
}
