package api

import (
	"fmt"
	"testing"

	"github.com/luxfi/aml/pkg/types"
)

// TestAlertStoreEviction (RED-08) verifies that the alert store evicts oldest
// entries when capacity is exceeded.
func TestAlertStoreEviction(t *testing.T) {
	store := NewAlertStoreWithMax(100)

	// Add 200 items — store should evict down to ~90 (evicts 10% = 10 entries per trigger).
	for i := 0; i < 200; i++ {
		txID := fmt.Sprintf("tx-%d", i)
		store.Add(txID, []types.Alert{{ID: fmt.Sprintf("alert-%d", i)}})
	}

	size := store.Len()
	if size > 100 {
		t.Errorf("store size after eviction = %d, want <= 100", size)
	}

	// Oldest entries should be evicted.
	if alerts := store.ByTx("tx-0"); len(alerts) > 0 {
		t.Error("tx-0 should have been evicted")
	}

	// Most recent entries should still exist.
	if alerts := store.ByTx("tx-199"); len(alerts) == 0 {
		t.Error("tx-199 should still be present")
	}
}

// TestAlertStoreNoEvictionUnderCapacity (RED-08) verifies no eviction happens
// when store is under capacity.
func TestAlertStoreNoEvictionUnderCapacity(t *testing.T) {
	store := NewAlertStoreWithMax(200)

	for i := 0; i < 50; i++ {
		store.Add(fmt.Sprintf("tx-%d", i), []types.Alert{{ID: fmt.Sprintf("a-%d", i)}})
	}

	if store.Len() != 50 {
		t.Errorf("store size = %d, want 50", store.Len())
	}

	// All entries should be present.
	for i := 0; i < 50; i++ {
		if alerts := store.ByTx(fmt.Sprintf("tx-%d", i)); len(alerts) == 0 {
			t.Errorf("tx-%d should be present", i)
		}
	}
}

// TestAlertStoreDuplicateTxID verifies that adding to an existing tx_id
// does not double-count in the order list.
func TestAlertStoreDuplicateTxID(t *testing.T) {
	store := NewAlertStoreWithMax(100)

	store.Add("tx-1", []types.Alert{{ID: "a1"}})
	store.Add("tx-1", []types.Alert{{ID: "a2"}})

	if store.Len() != 1 {
		t.Errorf("store size = %d, want 1 (same tx_id)", store.Len())
	}

	alerts := store.ByTx("tx-1")
	if len(alerts) != 2 {
		t.Errorf("alert count = %d, want 2", len(alerts))
	}
}
