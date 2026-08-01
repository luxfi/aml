package api

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// The admission gate as a property: a tenant gets one of a kind at a time, a
// second is refused rather than queued, the slot is released for the next one,
// and two tenants never block each other.
func TestOnePerTenant(t *testing.T) {
	var g gate
	if !g.enter("hanzo/acme") {
		t.Fatal("the first was refused")
	}
	if g.enter("hanzo/acme") {
		t.Fatal("a second concurrent one for the same tenant was admitted")
	}
	if !g.enter("zoo/acme") {
		t.Fatal("another tenant was blocked by this one")
	}
	g.leave("hanzo/acme")
	if !g.enter("hanzo/acme") {
		t.Fatal("the slot was not released")
	}
	g.leave("hanzo/acme")
	g.leave("zoo/acme")
	if g.held() != 0 {
		t.Fatalf("%d slots held after everything left", g.held())
	}

	// Under a flood, exactly one wins.
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		won int
	)
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if g.enter("lux/acme") {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d concurrent claims were admitted for one tenant, want 1", won)
	}
	g.leave("lux/acme")
}

// TestOneRefusesTheSecondStudyAndFreesTheSlot.
//
// The combinator, not the gate: the wrapped operation is what a route registers,
// so this is the shape planes.go actually installs. ErrBusy answers 429 (see the
// refusal table) rather than 500, because nothing about the request was wrong.
func TestOneRefusesTheSecondStudyAndFreesTheSlot(t *testing.T) {
	var (
		g       gate
		release = make(chan struct{})
		running = make(chan struct{})
	)
	slow := one(&g, func(ctx context.Context, org string, in *struct{}) (*struct{}, error) {
		close(running)
		<-release
		return &struct{}{}, nil
	})
	quick := one(&g, func(ctx context.Context, org string, in *struct{}) (*struct{}, error) {
		return &struct{}{}, nil
	})

	done := make(chan error, 1)
	go func() { _, err := slow(context.Background(), "hanzo/acme", &struct{}{}); done <- err }()
	<-running

	if _, err := quick(context.Background(), "hanzo/acme", &struct{}{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second study for the same tenant: %v, want ErrBusy", err)
	}
	// Another institution is not refused because this one is studying. This is the
	// property that separates a per-tenant bound from a global one.
	if _, err := quick(context.Background(), "zoo/acme", &struct{}{}); err != nil {
		t.Fatalf("another tenant was refused because this one was busy: %v", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := quick(context.Background(), "hanzo/acme", &struct{}{}); err != nil {
		t.Fatalf("the slot was not released when the study finished: %v", err)
	}
}

// TestTheSlotSurvivesAPanic. The release runs from a defer, so an operation that
// panics does not leave a tenant permanently unable to start another one — which
// would be a durable denial of service caused by one bad request.
func TestTheSlotSurvivesAPanic(t *testing.T) {
	var g gate
	boom := one(&g, func(ctx context.Context, org string, in *struct{}) (*struct{}, error) {
		panic("in the middle of a trial")
	})
	func() {
		defer func() { _ = recover() }()
		_, _ = boom(context.Background(), "hanzo/acme", &struct{}{})
	}()
	if g.held() != 0 {
		t.Fatal("a panicking study kept its tenant's slot forever")
	}
}

// TestAStudyIsBoundedInTime.
//
// A grid whose every candidate is legitimate can still be minutes of arithmetic,
// so the cost of one request has to be statable without reasoning about the grid.
// The deadline is the statement, the work sees it as a cancelled context, and the
// refusal is a refusal (400) rather than a fault: the caller can ask for less.
func TestAStudyIsBoundedInTime(t *testing.T) {
	saw := make(chan error, 1)
	slow := within(20*time.Millisecond, func(ctx context.Context, org string, in *struct{}) (*struct{}, error) {
		<-ctx.Done()
		saw <- ctx.Err()
		return nil, ctx.Err()
	})
	if _, err := slow(context.Background(), "hanzo/acme", &struct{}{}); !errors.Is(err, ErrTooLong) {
		t.Fatalf("a study past its budget: %v, want ErrTooLong", err)
	}
	if err := <-saw; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the work saw %v, want a cancelled context — a deadline nothing cancels is not a bound", err)
	}

	// And a study that finishes inside the budget is untouched.
	quick := within(time.Minute, func(ctx context.Context, org string, in *struct{}) (*string, error) {
		out := "done"
		return &out, nil
	})
	out, err := quick(context.Background(), "hanzo/acme", &struct{}{})
	if err != nil || out == nil || *out != "done" {
		t.Fatalf("out=%v err=%v", out, err)
	}
}

// TestTheCallerCancelsTheStudy. The deadline is derived from the request's own
// context, so a client that goes away stops the work rather than leaving it
// running for the rest of the budget.
func TestTheCallerCancelsTheStudy(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	study := within(time.Minute, func(ctx context.Context, org string, in *struct{}) (*struct{}, error) {
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	})
	go func() {
		time.Sleep(10 * time.Millisecond)
		stop()
	}()
	if _, err := study(ctx, "hanzo/acme", &struct{}{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the caller's cancellation", err)
	}
	<-stopped
}
