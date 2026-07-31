package api

import (
	"sync"
	"testing"
)

// TestOneReplayPerTenant is the gate as a property: a tenant gets one replay at
// a time, a second is refused rather than queued, and the slot is released for
// the next one. Two tenants never block each other.
func TestOneReplayPerTenant(t *testing.T) {
	if !startReplay("hanzo/acme") {
		t.Fatal("the first replay was refused")
	}
	if startReplay("hanzo/acme") {
		t.Fatal("a second concurrent replay for the same tenant was admitted")
	}
	if !startReplay("zoo/acme") {
		t.Fatal("another tenant was blocked by this one's replay")
	}
	endReplay("hanzo/acme")
	if !startReplay("hanzo/acme") {
		t.Fatal("the slot was not released")
	}
	endReplay("hanzo/acme")
	endReplay("zoo/acme")

	// Under a flood, exactly one wins.
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		won int
	)
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if startReplay("lux/acme") {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d concurrent replays were admitted for one tenant, want 1", won)
	}
	endReplay("lux/acme")
}
