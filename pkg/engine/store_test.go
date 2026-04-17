// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package engine

import (
	"context"
	"sync"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

// MemoryStore is an in-memory TransactionStore for testing.
type MemoryStore struct {
	mu  sync.RWMutex
	txs []types.Transaction
}

// NewMemoryStore creates an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

func (m *MemoryStore) Save(_ context.Context, tx types.Transaction) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tx.CreatedAt.IsZero() {
		tx.CreatedAt = time.Now().UTC()
	}
	m.txs = append(m.txs, tx)
	return nil
}

func (m *MemoryStore) CountByUser(_ context.Context, userID string, since time.Time) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	count := 0
	for _, tx := range m.txs {
		if tx.UserID == userID && !tx.CreatedAt.Before(since) {
			count++
		}
	}
	return count, nil
}

func (m *MemoryStore) SumNotionalByUser(_ context.Context, userID string, since time.Time) (float64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var total float64
	for _, tx := range m.txs {
		if tx.UserID == userID && !tx.CreatedAt.Before(since) {
			total += tx.Notional
		}
	}
	return total, nil
}

func (m *MemoryStore) FirstTransaction(_ context.Context, userID string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, tx := range m.txs {
		if tx.UserID == userID {
			return false, nil
		}
	}
	return true, nil
}

func (m *MemoryStore) FirstCounterparty(_ context.Context, userID, counterparty string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, tx := range m.txs {
		if tx.UserID == userID && tx.Counterparty == counterparty {
			return false, nil
		}
	}
	return true, nil
}

func (m *MemoryStore) LastTransactionTime(_ context.Context, userID string) (time.Time, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var latest time.Time
	for _, tx := range m.txs {
		if tx.UserID == userID && tx.CreatedAt.After(latest) {
			latest = tx.CreatedAt
		}
	}
	return latest, nil
}

func (m *MemoryStore) RoundTrip(_ context.Context, userID string, window time.Duration) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	since := time.Now().UTC().Add(-window)
	sides := make(map[string]map[string]bool)
	for _, tx := range m.txs {
		if tx.UserID != userID || tx.CreatedAt.Before(since) {
			continue
		}
		if tx.Symbol == "" || tx.Side == "" {
			continue
		}
		if sides[tx.Symbol] == nil {
			sides[tx.Symbol] = make(map[string]bool)
		}
		sides[tx.Symbol][tx.Side] = true
	}
	for _, sideSet := range sides {
		if sideSet["buy"] && sideSet["sell"] {
			return true, nil
		}
	}
	return false, nil
}
