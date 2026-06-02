// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/base/core"
	"github.com/luxfi/aml/pkg/types"
)

const txCollection = "aml_transactions"

// BaseStore implements TransactionStore backed by Base collections.
type BaseStore struct {
	app core.App
}

// NewBaseStore creates a TransactionStore using Base collections.
func NewBaseStore(app core.App) *BaseStore {
	return &BaseStore{app: app}
}

func (s *BaseStore) CountByUser(ctx context.Context, userID string, since time.Time) (int, error) {
	records, err := s.app.FindRecordsByFilter(
		txCollection,
		fmt.Sprintf("user_id = '%s' && created >= '%s'", userID, since.UTC().Format(time.RFC3339)),
		"", // sort
		0,  // limit (0 = all)
		0,  // offset
	)
	if err != nil {
		return 0, fmt.Errorf("count by user: %w", err)
	}
	return len(records), nil
}

func (s *BaseStore) SumNotionalByUser(ctx context.Context, userID string, since time.Time) (float64, error) {
	records, err := s.app.FindRecordsByFilter(
		txCollection,
		fmt.Sprintf("user_id = '%s' && created >= '%s'", userID, since.UTC().Format(time.RFC3339)),
		"",
		0,
		0,
	)
	if err != nil {
		return 0, fmt.Errorf("sum notional: %w", err)
	}
	var total float64
	for _, r := range records {
		total += r.GetFloat("notional")
	}
	return total, nil
}

func (s *BaseStore) FirstTransaction(ctx context.Context, userID string) (bool, error) {
	records, err := s.app.FindRecordsByFilter(
		txCollection,
		fmt.Sprintf("user_id = '%s'", userID),
		"",
		1,
		0,
	)
	if err != nil {
		return true, fmt.Errorf("first tx check: %w", err)
	}
	return len(records) == 0, nil
}

func (s *BaseStore) FirstCounterparty(ctx context.Context, userID, counterparty string) (bool, error) {
	records, err := s.app.FindRecordsByFilter(
		txCollection,
		fmt.Sprintf("user_id = '%s' && counterparty = '%s'", userID, counterparty),
		"",
		1,
		0,
	)
	if err != nil {
		return true, fmt.Errorf("first counterparty check: %w", err)
	}
	return len(records) == 0, nil
}

func (s *BaseStore) LastTransactionTime(ctx context.Context, userID string) (time.Time, error) {
	records, err := s.app.FindRecordsByFilter(
		txCollection,
		fmt.Sprintf("user_id = '%s'", userID),
		"-created",
		1,
		0,
	)
	if err != nil {
		return time.Time{}, fmt.Errorf("last tx time: %w", err)
	}
	if len(records) == 0 {
		return time.Time{}, nil
	}
	return records[0].GetDateTime("created").Time(), nil
}

func (s *BaseStore) RoundTrip(ctx context.Context, userID string, window time.Duration) (bool, error) {
	since := time.Now().UTC().Add(-window)
	records, err := s.app.FindRecordsByFilter(
		txCollection,
		fmt.Sprintf("user_id = '%s' && created >= '%s'", userID, since.Format(time.RFC3339)),
		"",
		0,
		0,
	)
	if err != nil {
		return false, fmt.Errorf("round trip check: %w", err)
	}

	// Check if any symbol has both "buy" and "sell" sides.
	sides := make(map[string]map[string]bool) // symbol -> set of sides
	for _, r := range records {
		sym := r.GetString("symbol")
		side := r.GetString("side")
		if sym == "" || side == "" {
			continue
		}
		if sides[sym] == nil {
			sides[sym] = make(map[string]bool)
		}
		sides[sym][side] = true
	}
	for _, sideSet := range sides {
		if sideSet["buy"] && sideSet["sell"] {
			return true, nil
		}
	}
	return false, nil
}

func (s *BaseStore) Save(ctx context.Context, tx types.Transaction) error {
	collection, err := s.app.FindCollectionByNameOrId(txCollection)
	if err != nil {
		return fmt.Errorf("find collection %s: %w", txCollection, err)
	}

	record := core.NewRecord(collection)
	record.Set("tx_id", tx.ID)
	record.Set("org_id", tx.OrgID)
	record.Set("user_id", tx.UserID)
	record.Set("symbol", tx.Symbol)
	record.Set("side", tx.Side)
	record.Set("notional", tx.Notional)
	record.Set("currency", tx.Currency)
	record.Set("counterparty", tx.Counterparty)
	record.Set("ip_address", tx.IPAddress)
	record.Set("asset_class", tx.AssetClass)

	return s.app.Save(record)
}
