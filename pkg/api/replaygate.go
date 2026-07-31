// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

import "sync"

// replaying is the set of tenants with a rule replay in flight.
//
// A replay with no sample runs the candidate over the org's retained
// transactions — up to maxHistory of them, opening each sealed body as it goes.
// The row count is bounded; the work is not something a caller should be able to
// start again before the last one has finished. A handful of concurrent replays
// is enough to occupy every core on a single-replica engine, and the engine also
// has to answer ingest, which is the request that must not wait: a transaction
// that cannot be recorded cannot be processed.
//
// One in flight per tenant, so a replay is never blocked by another tenant's and
// never queues behind itself. The cap is per tenant rather than global for the
// same reason the stores are keyed that way — one institution's load is not
// another's to absorb.
var replaying = struct {
	mu sync.Mutex
	in map[string]bool
}{in: map[string]bool{}}

// startReplay claims the tenant's replay slot, reporting whether it got it.
func startReplay(tenant string) bool {
	replaying.mu.Lock()
	defer replaying.mu.Unlock()
	if replaying.in[tenant] {
		return false
	}
	replaying.in[tenant] = true
	return true
}

// endReplay releases the slot. It runs from a defer, so a panic in the replay
// cannot leave a tenant unable to run another one.
func endReplay(tenant string) {
	replaying.mu.Lock()
	defer replaying.mu.Unlock()
	delete(replaying.in, tenant)
}
