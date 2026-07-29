// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package sanctions

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

// MemorySanctionsStore is an in-memory SanctionsStore for testing.
type MemorySanctionsStore struct {
	mu          sync.RWMutex
	entries     []types.SanctionsEntry
	lastRefresh time.Time
}

func NewMemorySanctionsStore() *MemorySanctionsStore {
	return &MemorySanctionsStore{}
}

func (m *MemorySanctionsStore) Save(_ context.Context, entries []types.SanctionsEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entries...)
	m.lastRefresh = time.Now().UTC()
	return nil
}

func (m *MemorySanctionsStore) Search(_ context.Context, name, dob string) ([]SearchResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []SearchResult
	for _, e := range m.entries {
		score := TokenMatch(name, e.Name)
		if score >= MatchThreshold {
			results = append(results, SearchResult{Entry: e, Score: score})
			continue
		}
		for _, alias := range e.Aliases {
			aliasScore := TokenMatch(name, alias)
			if aliasScore >= MatchThreshold {
				results = append(results, SearchResult{Entry: e, Score: aliasScore})
				break
			}
		}
	}
	return results, nil
}

func (m *MemorySanctionsStore) Count(_ context.Context) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries), nil
}

func (m *MemorySanctionsStore) LastRefresh(_ context.Context) (time.Time, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastRefresh, nil
}

func TestMemorySanctionsStore_Save(t *testing.T) {
	store := NewMemorySanctionsStore()
	entries := []types.SanctionsEntry{
		{ID: "e-1", Name: "John Doe", Type: types.SanctionIndividual},
		{ID: "e-2", Name: "Acme Corp", Type: types.SanctionEntity},
	}
	err := store.Save(context.Background(), entries)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	count, err := store.Count(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2, got %d", count)
	}
}

func TestMemorySanctionsStore_Search(t *testing.T) {
	store := NewMemorySanctionsStore()
	store.Save(context.Background(), []types.SanctionsEntry{
		{ID: "e-1", Name: "John Smith", Aliases: []string{"J. Smith", "Johnny Smith"}},
		{ID: "e-2", Name: "Jane Doe"},
		{ID: "e-3", Name: "Unrelated Person"},
	})

	// Exact-ish match
	results, err := store.Search(context.Background(), "John Smith", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result for 'John Smith'")
	}
	if results[0].Entry.Name != "John Smith" {
		t.Fatalf("expected John Smith, got %q", results[0].Entry.Name)
	}

	// No match
	results, err = store.Search(context.Background(), "Totally Different Name", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestMemorySanctionsStore_SearchAlias(t *testing.T) {
	store := NewMemorySanctionsStore()
	store.Save(context.Background(), []types.SanctionsEntry{
		{ID: "e-alias", Name: "Muhammad al-Bashir", Aliases: []string{"Omar al-Bashir", "Bashir"}},
	})

	results, err := store.Search(context.Background(), "Omar al-Bashir", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected alias match")
	}
}

func TestMemorySanctionsStore_LastRefresh(t *testing.T) {
	store := NewMemorySanctionsStore()

	before, _ := store.LastRefresh(context.Background())
	if !before.IsZero() {
		t.Fatal("expected zero time before any save")
	}

	store.Save(context.Background(), []types.SanctionsEntry{
		{ID: "e-1", Name: "Test"},
	})

	after, _ := store.LastRefresh(context.Background())
	if after.IsZero() {
		t.Fatal("expected non-zero time after save")
	}
}

func TestSanctionsStoreInterface(t *testing.T) {
	var _ SanctionsStore = (*MemorySanctionsStore)(nil)
}
