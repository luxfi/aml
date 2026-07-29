// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package screen

import (
	"sort"
	"sync"
	"time"
)

// Readiness holds the outcome of the most recent load of each publisher's list.
//
// Store.Ready answers whether screening can be relied on at all. This answers
// which publisher is behind it, how many designations it last carried, and when —
// because "screening is not ready" without a source and a date is not something
// an operator can act on.
//
// It exists because the expensive failure in screening is silent. A list that has
// stopped loading produces no matches, and no matches is what a clean party also
// produces, so a retired endpoint or a changed schema is invisible from the
// outside. Two of these four sources had been returning nothing for months.
type Readiness struct {
	mu    sync.RWMutex
	state map[string]Fitness
}

// Fitness is the readiness of one publisher's list.
type Fitness struct {
	// Source is the publisher.
	Source string `json:"source"`
	// Designations is how many entries the last successful load carried.
	Designations int `json:"designations"`
	// LoadedAt is when that load happened. Zero means this list has never loaded,
	// which is not the same as a publisher who designates nobody.
	LoadedAt time.Time `json:"loaded_at"`
	// Digest is the digest of the payload that produced Designations, so a refresh
	// that changed nothing can be told from a refresh that did not run.
	Digest string `json:"digest,omitempty"`
	// Err is why the last attempt failed, empty if it succeeded.
	Err string `json:"error,omitempty"`
	// AttemptedAt is when the last attempt happened, successful or not.
	AttemptedAt time.Time `json:"attempted_at"`
}

// Fit reports whether this list is fit to screen against as of now.
//
// A list that has never loaded is not fit whatever its last attempt said, and
// neither is one that loaded zero designations — no publisher's list is empty, so
// an empty load means the fetch or the parse is wrong. A list whose latest attempt
// failed stays fit while what it did load is inside maxStale, because yesterday's
// designations still beat none; it stops being fit when that ages out rather than
// staying fit forever on one old success.
func (f Fitness) Fit(now time.Time, maxStale time.Duration) bool {
	if f.LoadedAt.IsZero() || f.Designations == 0 {
		return false
	}
	if maxStale <= 0 {
		return true
	}
	return now.Sub(f.LoadedAt) <= maxStale
}

// Age is how long since this list last loaded.
func (f Fitness) Age(now time.Time) time.Duration {
	if f.LoadedAt.IsZero() {
		return 0
	}
	return now.Sub(f.LoadedAt)
}

// NewReadiness builds a Readiness over the named sources, each recorded as never
// loaded — so a list that has not once succeeded is reported as unfit rather than
// being absent from the report entirely.
func NewReadiness(sources []string) *Readiness {
	r := &Readiness{state: make(map[string]Fitness, len(sources))}
	for _, s := range sources {
		r.state[s] = Fitness{Source: s}
	}
	return r
}

// Record folds the results of a refresh in.
//
// A failed attempt keeps the previous successful load's count and date: that data
// is still what screening runs against, and forgetting it would report the list as
// empty when it is only stale.
func (r *Readiness) Record(results []Result, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, res := range results {
		f := r.state[res.Source]
		f.Source = res.Source
		f.AttemptedAt = at
		if res.Err != nil {
			f.Err = res.Err.Error()
		} else {
			f.Err = ""
			f.Designations = len(res.Entries)
			f.Digest = res.Digest
			f.LoadedAt = at
		}
		r.state[res.Source] = f
	}
}

// Lists returns the readiness of every configured source, in a stable order.
func (r *Readiness) Lists() []Fitness {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Fitness, 0, len(r.state))
	for _, f := range r.state {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

// Unfit names the sources not fit to screen against.
//
// Fitness is all-or-nothing across the set by design, and the caller should treat
// a non-empty result as "cannot screen" rather than as partial coverage: screening
// against three of four lists is not three-quarters of a control, because a party
// designated only on the missing list passes completely and the answer is
// indistinguishable from a clean one.
func (r *Readiness) Unfit(now time.Time, maxStale time.Duration) []string {
	var unfit []string
	for _, f := range r.Lists() {
		if !f.Fit(now, maxStale) {
			unfit = append(unfit, f.Source)
		}
	}
	return unfit
}

// Designations is the total across every list that is currently loaded.
func (r *Readiness) Designations() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	total := 0
	for _, f := range r.state {
		total += f.Designations
	}
	return total
}
