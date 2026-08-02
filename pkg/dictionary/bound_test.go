// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package dictionary

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/luxfi/aml/internal/source"
	"github.com/luxfi/aml/pkg/roster"
	"github.com/luxfi/aml/pkg/types"
)

// Two bounds this catalog publishes, and what each one is a bound ON.
//
// The catalog is a diagnostic: it may never refuse a payment, so every bound
// here degrades rather than errors. That makes the bounds easy to state wrongly.
//
// A bound over a COUNT of caller-sized things is not a bound. "One thousand and
// twenty-four custom names per tenant" says nothing about memory while the NAME
// is a string the caller wrote — a thousand names of a megabyte each is a
// gigabyte, held in the memory every other institution's ingest runs in, on a
// process that is one replica. The count is a bound only once the size of one is.
//
// And a per-tenant bound is not a process bound. Per tenant is the right shape —
// it degrades only the institution that reached it — but the accumulator holds
// one census per tenant in a table nothing caps, so the figure an operator needs
// is the product, and a product needs both factors.

// TestOneNamesCeilingIsInBytes weighs a full accumulator for one tenant.
//
// The published per-tenant figure has to be an upper bound on a real worst case,
// not on a convenient one, so this fills the vocabulary with names of the
// greatest length the door admits and measures what that costs.
func TestOneNamesCeilingIsInBytes(t *testing.T) {
	s := &Shelf{pending: roster.New[*census](roster.Default)}

	// The worst case a tenant can actually reach, and the worst case it can ASK
	// for. A caller does not stop at the length the catalog admits, so the traffic
	// is names far past it as well as names at it: what is being weighed is what
	// the accumulator ends up holding, not what a well-behaved payload would have
	// put there.
	at := strings.Repeat("k", MaxName-6)
	past := strings.Repeat("K", 64*1024)
	for i := 0; i < MaxCustom*2; i++ {
		raw, err := json.Marshal(map[string]any{
			fmt.Sprintf("%s%06d", at, i):   "v",
			fmt.Sprintf("%s%06d", past, i): "v",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Observe("hanzo/acme", types.Transaction{ID: fmt.Sprintf("tx-%d", i), Raw: raw}); err != nil {
			t.Fatal(err)
		}
	}

	held, ok := s.pending.Get("hanzo/acme")
	if !ok {
		t.Fatal("the tenant was not admitted")
	}
	if held.names > MaxCustom {
		t.Fatalf("the vocabulary holds %d custom names, over the bound of %d", held.names, MaxCustom)
	}
	if held.crowded == 0 {
		t.Error("the tenant reached its vocabulary bound and nothing was counted as turned away")
	}

	// And the published ceiling is an upper bound on what that costs.
	if weighed, ceiling := weigh(held), Ceiling(); weighed > ceiling {
		t.Errorf("a full vocabulary weighs %d bytes against a published per-tenant ceiling of %d: the ceiling under-states the worst case",
			weighed, ceiling)
	}
}

// TestAnOversizedNameIsRefusedAtTheDoor. Refusing the NAME and not the payload is
// the whole of it: a diagnostic may not decide whether a transaction is
// acceptable, so the reading is turned away and counted, and the payment is
// unaffected.
func TestAnOversizedNameIsRefusedAtTheDoor(t *testing.T) {
	s := &Shelf{pending: roster.New[*census](roster.Default)}
	raw, err := json.Marshal(map[string]any{strings.Repeat("k", MaxName+1): "v", "ok": "v"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Observe("hanzo/acme", types.Transaction{ID: "tx-1", Raw: raw}); err != nil {
		t.Fatalf("an oversized name refused the payload: %v", err)
	}
	held, _ := s.pending.Get("hanzo/acme")
	if held.names != 1 {
		t.Errorf("the catalog took %d names, want 1: the oversized one was catalogued", held.names)
	}
	if held.crowded == 0 {
		t.Error("a name was turned away and nothing counted it")
	}
}

// TestTheAccumulatorHoldsNoTable. One census per tenant in a map with no cap is
// the same shape as one model per tenant in a map with a cap: the first ends in
// an OOM that takes every institution down, the second in one institution's
// traffic evicting another's. Per-tenant state goes in roster.Roster, which
// admits, never removes, and states its ceiling.
func TestTheAccumulatorHoldsNoTable(t *testing.T) {
	source.NoTable(t, "shelf.go", "Shelf",
		"Per-tenant state goes in roster.Roster, which admits and never removes, so how many institutions this process accumulates for is ONE number and the per-tenant bound multiplies by it.")
}

// TestTheProcessCeilingIsTheProduct. An operator sizes a pod from one figure, so
// the figure has to exist and it has to be the product of the two bounds rather
// than either one of them.
func TestTheProcessCeilingIsTheProduct(t *testing.T) {
	s := &Shelf{pending: roster.New[*census](roster.Default)}
	if got, want := s.Ceiling(), int64(roster.Default)*Ceiling(); got != want {
		t.Errorf("process ceiling = %d, want %d (%d tenants x %d bytes)", got, want, roster.Default, Ceiling())
	}
}
