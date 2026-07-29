// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package engine

import (
	"context"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

// TransactionStore is the storage interface for transaction history queries.
// Implementations back the helper functions used in the expr-lang DSL.
type TransactionStore interface {
	// CountByUser returns the number of transactions for a user since the given time.
	CountByUser(ctx context.Context, userID string, since time.Time) (int, error)

	// SumNotionalByUser returns the total notional value for a user since the given time.
	SumNotionalByUser(ctx context.Context, userID string, since time.Time) (float64, error)

	// FirstTransaction returns true if this is the user's first transaction.
	FirstTransaction(ctx context.Context, userID string) (bool, error)

	// FirstCounterparty returns true if this is the first time the user has transacted
	// with the given counterparty.
	FirstCounterparty(ctx context.Context, userID, counterparty string) (bool, error)

	// LastTransactionTime returns the timestamp of the user's most recent transaction.
	LastTransactionTime(ctx context.Context, userID string) (time.Time, error)

	// RoundTrip returns true if the user has both bought and sold the same asset
	// within the given time window.
	RoundTrip(ctx context.Context, userID string, window time.Duration) (bool, error)

	// Save persists a transaction for subsequent queries.
	Save(ctx context.Context, tx types.Transaction) error
}
