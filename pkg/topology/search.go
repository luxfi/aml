// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package topology

import (
	"context"
	crypto "crypto/rand"
	"encoding/binary"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/replay"
	"github.com/luxfi/aml/pkg/velocity"
)

// reportLimit is the fallback reporting limit the aggregates are kept against.
// The same number the live path falls back to, for the same reason: a payment
// sitting just under a reporting limit is only visible as structuring if the
// aggregates know where the limit is.
const reportLimit = 10_000.0

// Search replays a tenant's history through every candidate shape and reports
// what each one would have done.
//
// The org is the tenant KEY, and it is not decoration: the detector's geometry is
// seeded from it (anomaly mix), so a search run under a different key studies a
// different set of trees than the one the tenant would get. It is required for
// the same reason a history window refuses an empty tenant.
func Search(ctx context.Context, org string, h replay.History, space Space, opt Options) (Report, error) {
	if org == "" {
		return Report{}, ErrOrg
	}
	if h == nil {
		return Report{}, ErrNoHistory
	}
	grid, err := space.Grid()
	if err != nil {
		return Report{}, err
	}
	events, cut, from, to, judged, err := collect(h, opt.Events)
	if err != nil {
		return Report{}, err
	}
	seed, err := seedOf(opt.Seed)
	if err != nil {
		return Report{}, err
	}

	started := time.Now()
	report := Report{
		Events: len(events), From: from, To: to, Judged: judged, Cut: cut, Seed: seed,
		Trials: make([]Trial, len(grid)),
	}

	workers := opt.Workers
	if workers <= 0 {
		workers = DefaultWorkers
	}
	if workers > len(grid) {
		workers = len(grid)
	}

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
		next  = make(chan int)
	)
	go func() {
		defer close(next)
		for i := range grid {
			select {
			case next <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				t, err := run(ctx, org, grid[i], events, seed, opt)
				mu.Lock()
				if err != nil && first == nil {
					first = err
				}
				report.Trials[i] = t
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if first != nil {
		return Report{}, first
	}
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}

	report.Elapsed = time.Since(started)
	rank(&report)
	return report, nil
}

// Fit builds one shape's learned state from a tenant's own history and hands back
// a snapshot of it.
//
// The snapshot is restorable into a live model ONLY where that model already has
// this shape — anomaly.Restore checks the digest, and Trial.Digest is what a
// caller compares against the running one. That is the property that stops a
// model's shape being changed underneath a tenant by restoring a file, and it is
// why a search's winner is a recommendation to a deployment rather than something
// this package can install.
func Fit(ctx context.Context, org string, h replay.History, t Topology, opt Options) (anomaly.Snapshot, Trial, error) {
	if org == "" {
		return anomaly.Snapshot{}, Trial{}, ErrOrg
	}
	if h == nil {
		return anomaly.Snapshot{}, Trial{}, ErrNoHistory
	}
	if err := t.Valid(); err != nil {
		return anomaly.Snapshot{}, Trial{}, err
	}
	events, _, _, _, _, err := collect(h, opt.Events)
	if err != nil {
		return anomaly.Snapshot{}, Trial{}, err
	}
	seed, err := seedOf(opt.Seed)
	if err != nil {
		return anomaly.Snapshot{}, Trial{}, err
	}

	trial, snap, err := fit(ctx, org, t, events, seed, opt)
	if err != nil {
		return anomaly.Snapshot{}, Trial{}, err
	}
	return snap, trial, nil
}

// collect drains the history once, so N candidates cost one pass over the store
// rather than N.
func collect(h replay.History, bound int) (events []replay.Event, cut bool, from, to time.Time, judged int, err error) {
	max := bound
	if max <= 0 || max > MaxEvents {
		max = MaxEvents
	}
	err = h.Each(func(e replay.Event) error {
		if len(events) >= max {
			cut = true
			return nil
		}
		if ts := e.Tx.Timestamp; !ts.IsZero() {
			if from.IsZero() || ts.Before(from) {
				from = ts.UTC()
			}
			if ts.After(to) {
				to = ts.UTC()
			}
		}
		if e.Disposition != replay.Unjudged {
			judged++
		}
		events = append(events, e)
		return nil
	})
	if err != nil {
		return nil, false, time.Time{}, time.Time{}, 0, err
	}
	if len(events) == 0 {
		// The same refusal pkg/replay makes, for the same reason: "no alerts" is
		// exactly what a quiet shape looks like, and choosing a model on the
		// strength of an empty replay is the failure this package exists to
		// prevent.
		return nil, false, time.Time{}, time.Time{}, 0, ErrEmpty
	}
	return events, cut, from, to, judged, nil
}

func seedOf(given uint64) (uint64, error) {
	if given != 0 {
		return given, nil
	}
	var b [8]byte
	if _, err := crypto.Read(b[:]); err != nil {
		return 0, fmt.Errorf("topology: seed: %w", err)
	}
	s := binary.LittleEndian.Uint64(b[:])
	if s == 0 {
		s = 1
	}
	return s, nil
}

// run is one candidate, replayed.
func run(ctx context.Context, org string, t Topology, events []replay.Event, seed uint64, opt Options) (Trial, error) {
	trial, _, err := fit(ctx, org, t, events, seed, opt)
	return trial, err
}

// fit is the one place a candidate meets the data: it builds a detector and an
// aggregate store of its own, replays the events through the detector's own
// learning path, and drops both. Nothing it touches outlives it.
func fit(ctx context.Context, org string, t Topology, events []replay.Event, seed uint64, opt Options) (Trial, anomaly.Snapshot, error) {
	vel := velocity.New(velocity.Config{})
	model, err := anomaly.New(t.Config(seed), vel)
	if err != nil {
		return Trial{}, anomaly.Snapshot{}, err
	}
	limit := opt.Limit
	if limit <= 0 {
		limit = reportLimit
	}

	points := opt.Curve
	if points <= 0 {
		points = DefaultCurve
	}
	every := len(events) / points
	if every < 1 {
		every = 1
	}

	trial := Trial{
		Topology: t, Digest: model.Digest(), Seed: seed,
		Events: len(events), Stated: t.Review,
		Refused: map[string]int64{}, Blind: map[string]int64{},
	}

	var (
		scores    []float64
		positives []bool
		shares    = map[string]*Contribution{}
		windowSum float64
		windowN   int64
	)

	for i, e := range events {
		if i%512 == 0 {
			if err := ctx.Err(); err != nil {
				return Trial{}, anomaly.Snapshot{}, err
			}
		}
		tx := e.Tx
		tx.OrgID = org
		// The aggregates are what the features are computed from, and the live
		// path records the transaction BEFORE it scores it, so the numbers an
		// alert quotes are the ones an investigator sees. A replay that scored
		// first would measure a different thing than production does.
		for _, k := range anomaly.Keys(tx) {
			vel.Record(k, tx.Timestamp, tx.USD, limit)
		}
		a := model.Learn(tx, e.Entity)

		if a.Reason != "" {
			trial.Refused[a.Reason]++
		}
		if a.Scored {
			trial.Scored++
			windowSum += a.Score
			windowN++
			if a.Alert {
				trial.Alerted++
				for _, c := range a.Causes {
					held := shares[c.Feature]
					if held == nil {
						held = &Contribution{Feature: c.Feature}
						shares[c.Feature] = held
					}
					held.Alerts++
					held.Share += c.Share
				}
			}
			for _, v := range a.Values {
				if v.Blind {
					trial.Blind[v.Feature]++
				}
			}
			if e.Disposition != replay.Unjudged {
				scores = append(scores, a.Score)
				positives = append(positives, e.Disposition == replay.Productive)
			}
		}

		// The curve is sampled over the REPLAY and not over the scored events.
		// Sampled over the scored ones it would have no points at all through the
		// warm-up, which is the part of the curve that says whether the history is
		// long enough for the shape.
		if (i+1)%every == 0 || i == len(events)-1 {
			st := model.State(org)
			p := Point{Learned: st.Learned, Cut: st.Cut, Scored: trial.Scored, Alerted: trial.Alerted}
			if trial.Scored > 0 {
				p.Realised = float64(trial.Alerted) / float64(trial.Scored)
			}
			if windowN > 0 {
				p.Mean = windowSum / float64(windowN)
			}
			windowSum, windowN = 0, 0
			trial.Curve = append(trial.Curve, p)
		}
	}

	st := model.State(org)
	trial.Warm, trial.Saturated = st.Warm, st.Saturated
	if trial.Scored > 0 {
		trial.Realised = float64(trial.Alerted) / float64(trial.Scored)
	}
	trial.Drift = trial.Realised - trial.Stated
	if trial.Drift < 0 {
		trial.Drift = -trial.Drift
	}
	trial.Judged = len(scores)
	if v, ok := auc(scores, positives); ok && finite(v) {
		trial.Separation = &v
	}
	trial.Features = make([]Contribution, 0, len(shares))
	for _, c := range shares {
		if c.Alerts > 0 {
			c.Share /= float64(c.Alerts)
		}
		trial.Features = append(trial.Features, *c)
	}
	sort.Slice(trial.Features, func(i, j int) bool {
		if trial.Features[i].Alerts != trial.Features[j].Alerts {
			return trial.Features[i].Alerts > trial.Features[j].Alerts
		}
		return trial.Features[i].Feature < trial.Features[j].Feature
	})

	snap, _ := model.Snapshot(org)
	return trial, snap, nil
}

// rank picks the winner, or states why there is none.
//
// Eligibility first: a candidate that never warmed scored nothing, and a
// saturated one cannot honour any appetite. Then separation, because that is the
// only outcome measured against something a human concluded. Ties go to the
// candidate whose realised share sits closest to the one asked for, and then to
// the SMALLER model — a shape that ties on evidence and costs less is the better
// answer, and preferring the bigger one would drift the fleet upward every time a
// search ran.
func rank(r *Report) {
	if r.Judged == 0 {
		r.Refusal = RefusalUnjudged
		return
	}
	var eligible []int
	warm := false
	for i := range r.Trials {
		if r.Trials[i].Warm {
			warm = true
		}
		if r.Trials[i].Warm && !r.Trials[i].Saturated && r.Trials[i].Separation != nil {
			eligible = append(eligible, i)
		}
	}
	if len(eligible) == 0 {
		if !warm {
			r.Refusal = RefusalWarming
		} else {
			r.Refusal = RefusalSaturated
		}
		return
	}
	sort.SliceStable(eligible, func(a, b int) bool {
		x, y := r.Trials[eligible[a]], r.Trials[eligible[b]]
		if *x.Separation != *y.Separation {
			return *x.Separation > *y.Separation
		}
		if x.Drift != y.Drift {
			return x.Drift < y.Drift
		}
		return x.Topology.Nodes() < y.Topology.Nodes()
	})
	winner := r.Trials[eligible[0]]
	r.Winner = &winner
}
