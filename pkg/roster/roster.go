// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// Package roster is the set of tenants a process holds state for, and the
// ceiling on how many.
//
// # Why it exists
//
// A monitoring engine keeps live state per institution: sliding aggregates, a
// behavioural model. That state is memory, memory is finite, and the obvious
// structure is one map of every tenant's state with one cap over it and a
// least-recently-used eviction when the cap is reached.
//
// That structure is a defect, and it is the same defect every time. The cap is
// spent out of a pool every tenant draws from, so a busy institution's traffic
// takes a quiet one's state — and what the quiet institution loses is a CONTROL.
// Its aggregates return to zero, its model returns to warming, and both of those
// read exactly like an institution with nothing to report. There is no error, no
// refusal and nothing in a log: the failure mode is a supervisor being told that
// a bank is clean by a system that stopped looking. It is also cheap to cause on
// purpose, from any tenant, with ordinary traffic.
//
// # What this is instead
//
// Admission, never eviction. A tenant that is held keeps what it holds until it
// releases it itself. A tenant arriving when the roster is full is REFUSED, and
// the refusal is counted and published, because a tenant with no state is a real
// gap in a real control and the operator has to be able to see it. Refusing the
// arrival is the only choice that harms nobody who is already being monitored.
//
// There is no removal in this package at all — no Drop, no evict, no delete.
// That is not an omission: it is what makes the property structural rather than
// intended. A caller cannot compose an eviction out of operations that do not
// exist, and [source.NoRemoval] reads this file to keep it that way.
//
// The bound HERE is the number of tenants. What each tenant may then hold is the
// caller's own per-tenant bound, and the two multiply to a statable ceiling on
// the process.
package roster

import "sync"

// Roster holds one value per tenant, up to a ceiling.
//
// Safe for concurrent use. The zero value is not usable; see [New].
type Roster[T any] struct {
	ceiling int

	mu      sync.RWMutex
	held    map[string]T
	refused int64
}

// New returns a roster with room for ceiling tenants. At or below zero it takes
// the default, because a roster of nothing holds no state for anybody.
func New[T any](ceiling int) *Roster[T] {
	if ceiling <= 0 {
		ceiling = Default
	}
	return &Roster[T]{ceiling: ceiling, held: make(map[string]T, ceiling)}
}

// Default is the ceiling a caller that names none gets.
//
// It is ONE number and it answers one question — how many institutions does
// this process hold live state for — so every per-tenant store in the engine
// multiplies its own per-tenant bound by the same figure and the products can be
// added up against a pod's memory limit. Two stores with two different tenant
// ceilings would be two answers to one question.
//
// Eight, because a deployment of this engine serves a dedicated IAM application
// per financial institution: the tenant count is a small deployment fact and not
// something traffic moves. Against a 1 GiB pod it puts the sliding aggregates at
// 256 MiB and the behavioural models at under 3 MiB, which leaves the rest to
// the store, the designation lists and the runtime. A deployment that genuinely
// serves more raises it and states the new product.
const Default = 8

// Get is this tenant's state, if the roster holds it.
func (r *Roster[T]) Get(org string) (T, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.held[org]
	return v, ok
}

// Hold is this tenant's state, admitting it if there is room.
//
// make is called at most once per admission and only while there is room for
// the tenant, so a caller cannot pay to build state the roster is about to
// refuse. It reports false when the roster is full, which is a tenant this
// process is not monitoring — never another tenant's state being taken.
func (r *Roster[T]) Hold(org string, make func() T) (T, bool) {
	if v, ok := r.Get(org); ok {
		return v, true
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.held[org]; ok {
		return v, true
	}
	if len(r.held) >= r.ceiling {
		r.refused++
		var zero T
		return zero, false
	}
	v := make()
	r.held[org] = v
	return v, true
}

// Put installs this tenant's state, replacing what it had.
//
// It is how a tenant's own state is rebuilt — restored from a durable record,
// refitted — and it is bounded the same way: a tenant the roster does not
// already hold is admitted only if there is room. Replacing a tenant's own
// state is never an eviction, because the only state it can reach is the state
// of the tenant named.
func (r *Roster[T]) Put(org string, v T) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.held[org]; !ok {
		if len(r.held) >= r.ceiling {
			r.refused++
			return false
		}
	}
	r.held[org] = v
	return true
}

// Each visits every tenant held, stopping when the visitor says to.
//
// The visitor runs under the roster's read lock, so it must not call back into
// the roster. It exists for the state views, which have to be able to report
// what the process is holding for whom.
func (r *Roster[T]) Each(visit func(org string, v T) bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for org, v := range r.held {
		if !visit(org, v) {
			return
		}
	}
}

// Held is how many tenants this process holds state for.
func (r *Roster[T]) Held() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.held)
}

// Ceiling is how many it may hold.
func (r *Roster[T]) Ceiling() int { return r.ceiling }

// Refused counts the admissions turned away because the roster was full.
//
// It is the number that must never sit at zero in a report while a tenant
// wonders why it has no findings: a refused tenant is one this process is not
// keeping the state for, and that is a fact about a control, not a statistic.
func (r *Roster[T]) Refused() int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.refused
}

// Full reports whether the next new tenant will be refused.
func (r *Roster[T]) Full() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.held) >= r.ceiling
}
