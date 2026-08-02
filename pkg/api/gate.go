// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

// Admission for the expensive work, and the two combinators that apply it.
//
// Three operations on this engine cost real machine: a rule replay, a model
// search and a model fit. Each reads a bounded but large prefix of a tenant's
// retained history, opens every sealed body as it goes, and then computes over
// all of it. None of them is on the ingest path, and ingest is the request that
// must not wait — a transaction that cannot be recorded cannot be processed — so
// none of them may be startable in a quantity that occupies the engine.
//
// The bound is PER TENANT and the refusal only ever falls on the tenant that
// asked. One institution's study is never refused because another institution is
// studying, and no institution's state is touched by another's load: a gate is a
// map keyed by the tenant, entries exist only while work is in flight, and
// nothing is ever evicted from it. A global cap over a shared structure would
// make one tenant's volume another tenant's outage, which is the failure the
// record planes are keyed to avoid.
//
// Refusing beats queueing here. The caller asked for work that is already
// running for it; holding a connection open until the first one finishes spends
// a socket and a goroutine to arrive at the same answer later. The queue that
// does exist is for the MACHINE rather than for the tenant — see
// topology.Budget, which bounds how much of the CPU every study together may
// take, so that ingest keeps the rest.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/luxfi/aml/pkg/topology"
)

// ErrBusy is what an operation returns when this tenant already has one of its
// kind in flight.
var ErrBusy = errors.New("api: this tenant already has one of these running")

// maxStudy bounds how long one model search or fit may run.
//
// A bound in trials and a bound in events are bounds on the SHAPE of the work; a
// caller can still name a space whose every candidate is legitimate and whose
// total is minutes. The deadline is what makes the cost of one request statable
// without reasoning about the grid, and it is derived from the request's own
// context, so a client that goes away cancels the work rather than leaving it
// running for the rest of the budget.
const maxStudy = 2 * time.Minute

// gate is the set of tenants with one operation of a kind in flight. The zero
// value is ready.
type gate struct {
	mu sync.Mutex
	in map[string]bool
}

// enter claims this tenant's slot, reporting whether it got it.
func (g *gate) enter(tenant string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.in[tenant] {
		return false
	}
	if g.in == nil {
		g.in = map[string]bool{}
	}
	g.in[tenant] = true
	return true
}

// leave releases the slot. It runs from a defer, so a panic in the work cannot
// leave a tenant unable to start another one.
func (g *gate) leave(tenant string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.in, tenant)
}

// held reports how many tenants hold a slot. For tests.
func (g *gate) held() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.in)
}

// one admits a single operation of this kind per tenant.
//
// It is a combinator over the operation shape rather than a check inside each
// operation, so the routing table still reads as a routing table and a plane
// cannot forget it: a route that is not wrapped is visibly not wrapped, on one
// line, in one file.
func one[In, Out any](g *gate, fn op[In, Out]) op[In, Out] {
	return func(ctx context.Context, org string, in *In) (*Out, error) {
		if !g.enter(org) {
			return nil, ErrBusy
		}
		defer g.leave(org)
		return fn(ctx, org, in)
	}
}

// costly makes an operation take a share of the machine before it runs.
//
// It is for a read whose cost is bounded by ROWS rather than by a page: a fold
// over a window has no correct partial answer, so it examines up to fifty
// thousand activations however many the caller wanted, and any authenticated
// caller can ask for one in a loop. Row-bounded is not rate-bounded, and the
// resource a loop of them spends is the same CPU a model study spends — so it is
// the same budget, which is what makes "how much of this machine can a tenant
// take" one question with one answer.
//
// A paged read is deliberately NOT wrapped. A page is bounded work with a cursor,
// and putting the machine's budget in front of every list would make an ordinary
// console screen wait behind a study.
func costly[In, Out any](cores *topology.Budget, fn op[In, Out]) op[In, Out] {
	return func(ctx context.Context, org string, in *In) (*Out, error) {
		held, err := cores.Admit(ctx, 1)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrBusy, err)
		}
		defer held.Release()
		return fn(ctx, org, in)
	}
}

// within bounds how long an operation may run, counted from the moment it
// starts and cancelled with the request.
func within[In, Out any](d time.Duration, fn op[In, Out]) op[In, Out] {
	return func(ctx context.Context, org string, in *In) (*Out, error) {
		ctx, stop := context.WithTimeout(ctx, d)
		defer stop()
		out, err := fn(ctx, org, in)
		if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: it ran for %s without finishing", ErrTooLong, d)
		}
		return out, err
	}
}

// ErrTooLong is what an operation returns when it hit its deadline. It is a
// refusal and not a fault: the work was legitimate and too large, and the caller
// can ask for less of it.
var ErrTooLong = errors.New("api: the work did not finish inside its budget")
