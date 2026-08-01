// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package topology

import (
	"context"
	"runtime"
	"sync"
)

// Budget is how much of the machine every study together may take.
//
// It is a bound on the PROCESS and not on a tenant, and that distinction is the
// whole of why it is safe. Nothing here is keyed by tenant, nothing here is
// stored, and nothing here is ever evicted: a budget is the CPU, which is a
// shared physical resource whichever way it is modelled. What it buys is the one
// property a monitoring engine cannot trade away — ingest always has the rest of
// the machine. A transaction that cannot be recorded cannot be processed, so the
// study that would have taken every core is the study that stops the institution
// taking payments.
//
// Tokens ARE trial workers. A study takes at least one before it reads a single
// event and holds them until it is done, which bounds three things with one
// mechanism:
//
//   - CPU: the trial goroutines running at once, across every tenant, never
//     exceed the budget, so the remaining cores are ingest's.
//   - MEMORY: a study holds its tenant's replayed events for its whole run, and
//     a study that holds no token has not read them yet, so the events in memory
//     are bounded by the budget rather than by the number of tenants.
//   - PROGRESS: the first token is waited for and the rest are taken only if
//     they are free, so a study that has started can always finish. A study that
//     waited for its full width would wait behind studies waiting for theirs.
//
// The wait is the queue, and it is cancellable: it ends when the caller's
// context does, which is either the client going away or the operation's own
// deadline. Waiting is a tenant waiting for the machine, never a tenant being
// refused because of another tenant.
//
// A nil Budget is unbounded. That is for the pure tests in this package and for
// a caller that has not wired one; a deployment wires one (cmd/amld).
type Budget struct{ tokens chan struct{} }

// NewBudget returns a budget of n workers. At or below zero it takes half the
// machine's cores, at least one — half, because the other half is ingest's, and
// at least one because a budget of nothing is a study that never runs.
func NewBudget(n int) *Budget {
	if n <= 0 {
		n = runtime.NumCPU() / 2
		if n < 1 {
			n = 1
		}
	}
	return &Budget{tokens: make(chan struct{}, n)}
}

// Size is how many workers this budget holds. Zero means unbounded.
func (b *Budget) Size() int {
	if b == nil {
		return 0
	}
	return cap(b.tokens)
}

// take acquires up to want workers, waiting for the first and taking the rest
// only if they are free. It reports how many it got and how to give them back.
//
// The release returns exactly what was taken, once, so a defer and a panic
// cannot between them leak or double-return a token.
func (b *Budget) take(ctx context.Context, want int) (int, func(), error) {
	if want < 1 {
		want = 1
	}
	if b == nil {
		return want, func() {}, nil
	}
	if want > cap(b.tokens) {
		want = cap(b.tokens)
	}
	select {
	case b.tokens <- struct{}{}:
	case <-ctx.Done():
		return 0, func() {}, ctx.Err()
	}
	held := 1
spare:
	for held < want {
		select {
		case b.tokens <- struct{}{}:
			held++
		default:
			break spare // the rest are somebody else's; one is enough to finish
		}
	}
	var once sync.Once
	return held, func() {
		once.Do(func() {
			for range held {
				<-b.tokens
			}
		})
	}, nil
}
