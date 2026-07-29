// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package sanctions

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/base/core"
	"github.com/luxfi/aml/pkg/types"
)

const sanctionsCollection = "aml_sanctions"

// SanctionsStore is the interface for persistent sanctions data.
type SanctionsStore interface {
	// Save persists a batch of sanctions entries.
	Save(ctx context.Context, entries []types.SanctionsEntry) error
	// Search finds entries matching a name above the match threshold.
	Search(ctx context.Context, name, dob string) ([]SearchResult, error)
	// Count returns the total number of entries.
	Count(ctx context.Context) (int, error)
	// LastRefresh returns the time of the most recent data load.
	LastRefresh(ctx context.Context) (time.Time, error)
}

// SearchResult is a sanctions search match.
type SearchResult struct {
	Entry types.SanctionsEntry
	Score float64
}

// BaseSanctionsStore implements SanctionsStore using Base collections.
type BaseSanctionsStore struct {
	app         core.App
	lastRefresh time.Time
}

// NewBaseSanctionsStore creates a BaseSanctionsStore.
func NewBaseSanctionsStore(app core.App) *BaseSanctionsStore {
	return &BaseSanctionsStore{app: app}
}

func (s *BaseSanctionsStore) Save(ctx context.Context, entries []types.SanctionsEntry) error {
	collection, err := s.app.FindCollectionByNameOrId(sanctionsCollection)
	if err != nil {
		return fmt.Errorf("find sanctions collection: %w", err)
	}

	for _, e := range entries {
		record := core.NewRecord(collection)
		record.Set("ref_id", e.RefID)
		record.Set("list_id", e.ListID)
		record.Set("name", e.Name)
		record.Set("aliases", strings.Join(e.Aliases, "|"))
		record.Set("dob", e.DOB)
		record.Set("nationality", e.Nationality)
		record.Set("address", e.Address)
		record.Set("entry_type", e.Type)

		if err := s.app.Save(record); err != nil {
			return fmt.Errorf("save sanctions entry %s: %w", e.RefID, err)
		}
	}

	s.lastRefresh = time.Now().UTC()
	return nil
}

func (s *BaseSanctionsStore) Search(ctx context.Context, name, dob string) ([]SearchResult, error) {
	records, err := s.app.FindRecordsByFilter(
		sanctionsCollection,
		"", // no filter — we do fuzzy matching in-process
		"",
		0,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("search sanctions: %w", err)
	}

	var results []SearchResult
	for _, r := range records {
		entryName := r.GetString("name")
		score := TokenMatch(name, entryName)
		if score >= MatchThreshold {
			results = append(results, SearchResult{
				Entry: recordToEntry(r),
				Score: score,
			})
			continue
		}
		// Check aliases.
		aliasStr := r.GetString("aliases")
		if aliasStr != "" {
			for _, alias := range strings.Split(aliasStr, "|") {
				aliasScore := TokenMatch(name, alias)
				if aliasScore >= MatchThreshold && aliasScore > score {
					results = append(results, SearchResult{
						Entry: recordToEntry(r),
						Score: aliasScore,
					})
					break
				}
			}
		}
	}
	return results, nil
}

func (s *BaseSanctionsStore) Count(ctx context.Context) (int, error) {
	records, err := s.app.FindRecordsByFilter(sanctionsCollection, "", "", 0, 0)
	if err != nil {
		return 0, err
	}
	return len(records), nil
}

func (s *BaseSanctionsStore) LastRefresh(_ context.Context) (time.Time, error) {
	return s.lastRefresh, nil
}

func recordToEntry(r *core.Record) types.SanctionsEntry {
	aliasStr := r.GetString("aliases")
	var aliases []string
	if aliasStr != "" {
		aliases = strings.Split(aliasStr, "|")
	}
	return types.SanctionsEntry{
		ID:          r.Id,
		ListID:      r.GetString("list_id"),
		RefID:       r.GetString("ref_id"),
		Name:        r.GetString("name"),
		Aliases:     aliases,
		DOB:         r.GetString("dob"),
		Nationality: r.GetString("nationality"),
		Address:     r.GetString("address"),
		Type:        r.GetString("entry_type"),
	}
}
