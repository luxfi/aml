// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package suppress

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/luxfi/aml/internal/instance"
	"github.com/luxfi/aml/pkg/types"
)

// A lifted suppression must not be able to page out a live one.
//
// Lifting never deletes, by design: a lifted suppression is the record of a
// decision and the ledger keeps it. The crowding bound counts only what is in
// FORCE, so declare-and-lift churn grows the rows on a rule without ever tripping
// it — and if the cover read pages over all of them unordered, the dead rows
// eventually fill the page and the institution's live, declared suppression stops
// being found.
//
// What that looks like from outside is the worst failure this plane has: the
// MLRO's decision silently stops applying, alerts start arriving that a
// suppression was supposed to silence, and only a query for Activation.Unchecked
// would ever say why. It is marked, which is the right failure direction, but a
// control that stops applying without saying so is the thing this whole package
// exists to prevent.
//
// The fix is not a bigger page. A row that CANNOT cover is not a candidate, so it
// is not read: the predicate lives in one function that both the cover read and
// the crowding count use, and the index carries it.

// TestLiftedRowsCannotPageOutALiveSuppression is red's repro, at the same ratio
// the constants have.
func TestLiftedRowsCannotPageOutALiveSuppression(t *testing.T) {
	app := instance.New(t)
	t.Cleanup(app.Cleanup)
	if err := Ensure(app); err != nil {
		t.Fatal(err)
	}
	s := NewBase(app)
	// The same 2000/500 ratio as MaxCandidates/MaxInForce, small enough to run.
	s.candidates, s.inForceMax = 8, 2
	ctx := context.Background()

	// The institution's live, narrow decision, declared first so an unordered
	// read finds it last.
	live, err := s.Suppress(ctx, acme, &SuppressIn{
		Rule: "structuring", Kind: "account", Value: "acct-1",
		Reason: "reviewed and cleared by the MLRO", By: types.Decider("u-mlro"),
		Until: time.Now().UTC().Add(30 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Then churn: declare and lift, over and over, the way a year of operating a
	// suppression ledger does.
	for i := range 12 {
		sup, err := s.Suppress(ctx, acme, &SuppressIn{
			Rule: "structuring", Kind: "account", Value: fmt.Sprintf("acct-churn-%d", i),
			Reason: "temporary, pending review", By: types.Decider("u-analyst"),
			Until: time.Now().UTC().Add(24 * time.Hour),
		})
		if err != nil {
			t.Fatalf("declare %d: %v", i, err)
		}
		if _, err := s.Lift(ctx, acme, &LiftIn{ID: sup.ID, Reason: "review complete", By: types.Decider("u-mlro")}); err != nil {
			t.Fatalf("lift %d: %v", i, err)
		}
	}

	cover, err := s.Cover(ctx, acme, &CoverIn{Rule: "structuring", Kind: "account", Value: "acct-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !cover.Covered {
		t.Errorf("after 12 declare+lift cycles the live suppression %s is no longer found: covered=%v partial=%v",
			live.ID, cover.Covered, cover.Partial)
	}
	if cover.Partial {
		t.Errorf("the cover read went partial over rows that cannot cover anything: partial=%v", cover.Partial)
	}
	if cover.Suppression == nil || cover.Suppression.ID != live.ID {
		t.Errorf("cover names %+v, want the live suppression %q", cover.Suppression, live.ID)
	}
}

// TestChurnDoesNotCrowdTheLedger. The crowding bound counts what is in force,
// and the cover read reads what could be in force. If those two counted
// different sets, a ledger could be crowded for the read and clear for the
// bound — which is how the defect above got in. One predicate, both callers.
func TestChurnDoesNotCrowdTheLedger(t *testing.T) {
	app := instance.New(t)
	t.Cleanup(app.Cleanup)
	if err := Ensure(app); err != nil {
		t.Fatal(err)
	}
	s := NewBase(app)
	s.candidates, s.inForceMax = 8, 2
	ctx := context.Background()

	for i := range 12 {
		sup, err := s.Suppress(ctx, acme, &SuppressIn{
			Rule: "structuring", Kind: "account", Value: fmt.Sprintf("acct-%d", i),
			Reason: "temporary, pending review", By: types.Decider("u-analyst"),
			Until: time.Now().UTC().Add(24 * time.Hour),
		})
		if err != nil {
			t.Fatalf("declare %d after %d lifted rows: %v — the institution is refused for its own history", i, i, err)
		}
		if _, err := s.Lift(ctx, acme, &LiftIn{ID: sup.ID, Reason: "done", By: types.Decider("u-mlro")}); err != nil {
			t.Fatal(err)
		}
	}
}
