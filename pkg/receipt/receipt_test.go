// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package receipt

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/internal/instance"
	"github.com/luxfi/aml/pkg/brand"
)

const (
	acme  = "hanzo/acme"
	other = "zoo/acme"
)

func shelf(t *testing.T) (*Shelf, core.App) {
	t.Helper()
	app := instance.New(t)
	t.Cleanup(app.Cleanup)
	if err := Ensure(app); err != nil {
		t.Fatal(err)
	}
	return NewBase(app), app
}

func offer(ref, body string) Offer { return Offer{Ref: ref, Mark: Mark([]byte(body))} }

// TestTheSameOfferIsAnsweredFromWhatWasKept.
func TestTheSameOfferIsAnsweredFromWhatWasKept(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	of := offer("tx-1", `{"id":"tx-1","notional":25000}`)

	if _, held, err := s.Prior(ctx, acme, of); err != nil || held {
		t.Fatalf("a transaction nobody has offered: held=%v err=%v", held, err)
	}
	if err := s.Keep(ctx, acme, of, []byte(`{"action":"allow"}`), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	answer, held, err := s.Prior(ctx, acme, of)
	if err != nil || !held {
		t.Fatalf("the retry found no receipt: held=%v err=%v", held, err)
	}
	if string(answer) != `{"action":"allow"}` {
		t.Errorf("the retry was answered %q, want the answer that was kept", answer)
	}
}

// TestAReserialisedBodyIsTheSameOffer. A client that retries through a different
// JSON encoder sends the same transaction with different bytes, and a retry it is
// not recognised as is a double count.
func TestAReserialisedBodyIsTheSameOffer(t *testing.T) {
	first := Mark([]byte(`{"id":"tx-1","notional":25000,"currency":"USD"}`))
	again := Mark([]byte("{\n  \"currency\": \"USD\",\n  \"notional\": 25000,\n  \"id\": \"tx-1\"\n}"))
	if first != again {
		t.Errorf("one transaction re-serialised marked differently:\n %s\n %s", first, again)
	}
	if changed := Mark([]byte(`{"id":"tx-1","notional":25001,"currency":"USD"}`)); first == changed {
		t.Error("a different transaction marked the same")
	}
}

// TestADifferentTransactionUnderOneReferenceIsRefused. Answering from the wrong
// one would attach one transaction's verdict to another's identifier, and no
// retry ever resolves it, so it is refused rather than answered.
func TestADifferentTransactionUnderOneReferenceIsRefused(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	if err := s.Keep(ctx, acme, offer("tx-1", `{"notional":25000}`), []byte(`{"action":"allow"}`), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Prior(ctx, acme, offer("tx-1", `{"notional":40000}`)); !errors.Is(err, ErrDiffers) {
		t.Errorf("two transactions under one reference: err = %v, want ErrDiffers", err)
	}
}

// TestTenantIsolation. Two institutions using one transaction id are two
// transactions, exactly as they are in every other plane.
func TestTenantIsolation(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	of := offer("tx-1", `{"notional":25000}`)
	if err := s.Keep(ctx, acme, of, []byte(`{"action":"allow"}`), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, held, err := s.Prior(ctx, other, of); err != nil || held {
		t.Errorf("one tenant's receipt answered another's offer: held=%v err=%v", held, err)
	}
}

// TestBareOrgIsRefused over every operation. An unqualified org reaching a store
// index puts two brands' institutions of the same name into one tenant.
func TestBareOrgIsRefused(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	of := offer("tx-1", `{}`)
	if _, _, err := s.Prior(ctx, "acme", of); !errors.Is(err, brand.ErrTenant) {
		t.Errorf("Prior on a bare org: %v", err)
	}
	if err := s.Keep(ctx, "acme", of, nil, time.Now().UTC()); !errors.Is(err, brand.ErrTenant) {
		t.Errorf("Keep on a bare org: %v", err)
	}
}

// TestAnOfferWithNoReferenceIsRefused. A receipt is looked up by the caller's own
// transaction reference; without one there is nothing to recognise again.
func TestAnOfferWithNoReferenceIsRefused(t *testing.T) {
	s, _ := shelf(t)
	if err := s.Keep(context.Background(), acme, offer("  ", `{}`), nil, time.Now().UTC()); !errors.Is(err, ErrRef) {
		t.Errorf("an offer with no reference: %v", err)
	}
}

// TestRestart. The deployment is Recreate at one replica, so the process that
// answered the first offer is gone by the time a client retries across a
// rollout. A receipt held in memory would be no receipt at all.
func TestRestart(t *testing.T) {
	first := instance.New(t)
	if err := Ensure(first); err != nil {
		t.Fatal(err)
	}
	of := offer("tx-1", `{"notional":25000}`)
	if err := NewBase(first).Keep(context.Background(), acme, of, []byte(`{"action":"allow"}`), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	second := instance.Restart(t, first)
	t.Cleanup(second.Cleanup)
	answer, held, err := NewBase(second).Prior(context.Background(), acme, of)
	if err != nil || !held {
		t.Fatalf("the receipt did not survive the restart: held=%v err=%v", held, err)
	}
	if string(answer) != `{"action":"allow"}` {
		t.Errorf("the restarted instance answered %q", answer)
	}
	// And the tenant boundary still holds over the reopened bytes.
	if _, held, _ := NewBase(second).Prior(context.Background(), other, of); held {
		t.Error("the reopened instance answered another tenant's offer")
	}
}

// TestDisposalDropsOnlyWhatCanNoLongerCount. A receipt is kept exactly as long
// as the transaction can still affect an aggregate, and the bound is DERIVED from
// the windows rather than chosen, so tuning a window cannot leave the receipt
// outlived by the aggregate it protects.
func TestDisposalDropsOnlyWhatCanNoLongerCount(t *testing.T) {
	s, _ := shelf(t)
	ctx := context.Background()
	now := time.Now().UTC()
	s.Life = Window(30 * 24 * time.Hour)

	inside := offer("tx-inside", `{"a":1}`)
	outside := offer("tx-outside", `{"a":2}`)
	if err := s.Keep(ctx, acme, inside, []byte(`{}`), now.Add(-29*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.Keep(ctx, acme, outside, []byte(`{}`), now.Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	dropped, err := s.Dispose(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 1 {
		t.Errorf("disposal dropped %d receipts, want 1", dropped)
	}
	if _, held, _ := s.Prior(ctx, acme, inside); !held {
		t.Error("disposal dropped a receipt whose transaction is still inside a live window")
	}
	if _, held, _ := s.Prior(ctx, acme, outside); held {
		t.Error("disposal kept a receipt past every window it could affect")
	}
}

// TestTheWindowIsDerived. The extra day is the late feed and the daily disposal;
// what matters is that the receipt outlives the widest aggregate window and that
// the relationship is arithmetic rather than two constants that have to agree.
func TestTheWindowIsDerived(t *testing.T) {
	for _, widest := range []time.Duration{time.Hour, 24 * time.Hour, 30 * 24 * time.Hour, 90 * 24 * time.Hour} {
		if got := Window(widest); got <= widest {
			t.Errorf("Window(%v) = %v, which does not outlive the aggregate it protects", widest, got)
		}
	}
	if DefaultWindow <= 30*24*time.Hour {
		t.Errorf("DefaultWindow = %v, shorter than the standard 30-day aggregate", DefaultWindow)
	}
}
