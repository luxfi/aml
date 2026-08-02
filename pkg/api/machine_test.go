// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/topology"
	"github.com/luxfi/aml/pkg/watch"
)

// One machine, one budget, and every door that spends it takes a token.
//
// Row-bounded is not rate-bounded. A read that examines fifty thousand
// activations however few the caller asked for, or replays a hundred thousand
// retained records, costs the same CPU a model study costs — and any
// authenticated caller can issue one in a loop, on the single-replica process
// that also has to answer ingest. Bounding the ROWS one call reads says nothing
// about how many calls there are.
//
// So there is one budget for the process, because the CPU is one machine however
// many planes ask for it, and these tests are the list of doors that reach it.

// exhausted is a handler whose machine is fully spoken for, and the release.
func exhausted(t *testing.T) (*Handler, func()) {
	t.Helper()
	h := plane(t)
	h.Cores = topology.NewBudget(1)
	held, err := h.Cores.Admit(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	return h, held.Release
}

// soon is a request context that gives up rather than queueing forever, which is
// what a caller waiting on a busy machine actually experiences.
func soon(r *http.Request) (*http.Request, func()) {
	ctx, stop := context.WithTimeout(r.Context(), 150*time.Millisecond)
	return r.WithContext(ctx), stop
}

// TestARuleReplayTakesTheMachine. The per-tenant gate bounds how many replays
// ONE tenant may have in flight; only the budget bounds how much of the machine
// every tenant's replays and studies together may take.
func TestARuleReplayTakesTheMachine(t *testing.T) {
	h, release := exhausted(t)
	defer release()

	if rec := ingest(t, h, payment("tx-1", 25_000)); rec.Code != http.StatusOK {
		t.Fatalf("ingest: %d", rec.Code)
	}

	e, rec := send(http.MethodPost, "/v1/aml/rules/test", map[string]any{"dsl": "Tx.Notional > 10000.0"})
	req, stop := soon(e.Request)
	defer stop()
	e.Request = req
	if err := h.testRule()(e); err != nil {
		t.Fatalf("transport error: %v", err)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("a replay against a fully-spent machine = %d, want 429: %s", rec.Code, rec.Body.String())
	}
}

// TestTheRateFoldIsRegisteredCostly. The mechanism below is worth nothing if the
// route does not use it, and a behavioural test that calls the combinator
// directly proves the combinator rather than the wiring. planes.go is a routing
// table read as source for exactly this reason.
func TestTheRateFoldIsRegisteredCostly(t *testing.T) {
	if !strings.Contains(readSource(t, "planes.go"), `get(h, one(&h.folds, costly(h.Cores, w.Rates)))`) {
		t.Error("GET /v1/aml/activations/rates is registered as a plain read: it folds up to MaxExamined rows per call, so any authenticated caller can spend the machine in a loop")
	}
}

// TestTheRateFoldTakesTheMachine. watch.Rates folds up to MaxExamined rows per
// call whatever the caller asked for, so it is not a page and is not admitted
// like one.
func TestTheRateFoldTakesTheMachine(t *testing.T) {
	h, release := exhausted(t)
	defer release()

	ctx, stop := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer stop()
	_, err := one(&h.folds, costly(h.Cores, h.Planes.Watch.Rates))(ctx, acme, &watch.RatesIn{})
	if !errors.Is(err, ErrBusy) {
		t.Errorf("a rate fold against a fully-spent machine returned %v, want ErrBusy", err)
	}
}

// TestAHistoryReadDemandsTheHold is the class, not the two doors above.
//
// Handler.history is the ONE place a tenant's whole retained history is
// materialised. Demanding the hold THERE is what makes an ungated read something
// somebody has to write on purpose: a *topology.Grant comes from Budget.Admit and
// from nowhere else, so a new door cannot reach the expensive read by forgetting.
func TestAHistoryReadDemandsTheHold(t *testing.T) {
	h := plane(t)
	if rec := ingest(t, h, payment("tx-1", 25_000)); rec.Code != http.StatusOK {
		t.Fatalf("ingest: %d", rec.Code)
	}
	vault, err := h.vault(acme)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := h.history(nil, acme, vault); !errors.Is(err, errUnpaid) {
		t.Errorf("a whole-history read with no hold on the machine returned %v, want errUnpaid", err)
	}
	if _, err := h.history(paid(), acme, vault); err != nil {
		t.Errorf("a paid-for history read failed: %v", err)
	}
}

// TestTheMachineIsOneBudget. Two budgets would be two answers to "how much of
// this machine may studies take", and the second one is always the one nobody
// sized the pod for.
func TestTheMachineIsOneBudget(t *testing.T) {
	app := shelves(t)
	h, err := Wire(app, deployment())
	if err != nil {
		t.Fatal(err)
	}
	if h.Cores == nil || h.Planes.Models == nil {
		t.Fatal("nothing to compare")
	}
	if h.Cores != h.Planes.Models.Cores {
		t.Error("the replay plane and the model plane hold different budgets")
	}
}
