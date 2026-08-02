package topology

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/replay"
)

// TestTheBudgetIsTheMachinesShare.
//
// Tokens are trial workers, and the properties that matter are: a study never
// gets more than the budget holds, a study always gets at least one so it can
// finish, and a study that cannot get one waits rather than proceeding.
func TestTheBudgetIsTheMachinesShare(t *testing.T) {
	b := NewBudget(2)
	if b.Size() != 2 {
		t.Fatalf("size = %d", b.Size())
	}

	got, release, err := b.take(context.Background(), 8)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("a study asking for eight workers took %d, want the whole budget of 2 and no more", got)
	}

	// Nothing is left, so the next one waits — and the wait ends with the caller's
	// context rather than hanging.
	ctx, stop := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer stop()
	if _, _, err := b.take(ctx, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a study with no budget left: %v, want to have waited and been cancelled", err)
	}

	release()
	release() // returning twice must not return more than was taken
	if got, r2, err := b.take(context.Background(), 2); err != nil || got != 2 {
		t.Fatalf("after release: got %d, err %v — the tokens were not all returned", got, err)
	} else {
		r2()
	}
}

// TestABudgetOfOneStillFinishes. A study takes the first worker by waiting and
// the rest only if they are free, so it can always make progress once it has
// started. Asking for its full width first is how two studies deadlock each
// other on a small machine.
func TestABudgetOfOneStillFinishes(t *testing.T) {
	b := NewBudget(1)
	got, release, err := b.take(context.Background(), 4)
	if err != nil || got != 1 {
		t.Fatalf("got %d, err %v; want exactly one worker", got, err)
	}
	release()
}

// TestTheDefaultBudgetLeavesTheMachineForIngest.
func TestTheDefaultBudgetLeavesTheMachineForIngest(t *testing.T) {
	b := NewBudget(0)
	if b.Size() < 1 {
		t.Fatalf("a budget of nothing is a study that never runs: %d", b.Size())
	}
	// A nil budget is unbounded and is what the pure tests in this package use.
	var none *Budget
	if got, release, err := none.take(context.Background(), 3); err != nil || got != 3 {
		t.Fatalf("nil budget: got %d, err %v", got, err)
	} else {
		release()
	}
}

// TestAStudyTakesTheMachineBeforeItReadsTheHistory.
//
// This is the memory half of the bound and it is the reason the token is taken
// before collect() rather than around each trial. A study that had read its
// tenant's fifty thousand events and was then waiting for a worker would hold
// that memory for the whole wait, so the events in memory would scale with the
// number of TENANTS studying rather than with the budget.
//
// It is also the queue: the second study waits, it is not refused, and no
// tenant's work is dropped because another tenant is busy.
// The admission is what a caller waits on, and the caller is what reads. See
// models.TestNoStudyReadsAHistoryItHasNotPaidFor for the same property at the
// layer where the reading actually happens.
func TestAStudyTakesTheMachineBeforeItReadsTheHistory(t *testing.T) {
	b := NewBudget(1)
	first, err := b.Admit(context.Background(), 1)
	if err != nil || first.Workers() != 1 {
		t.Fatal(err)
	}

	read := make(chan struct{})
	events := &watched{inner: stream(50, true), read: read}

	done := make(chan error, 1)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go func() {
		// Exactly what models.Shelf does: hold the machine, then read.
		held, err := b.Admit(ctx, 1)
		if err != nil {
			done <- err
			return
		}
		defer held.Release()
		_, err = Search(ctx, "hanzo/acme", events, small(), Options{Seed: 7}, held)
		done <- err
	}()

	select {
	case <-read:
		t.Fatal("the study read its tenant's history before it held any of the machine")
	case <-time.After(30 * time.Millisecond):
	}

	first.Release()
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		t.Fatal("the study never started after the machine was free — a queue that does not drain is a refusal")
	}
	if err := <-done; err != nil {
		t.Fatalf("the queued study failed: %v", err)
	}
}

// TestReleaseIsIdempotent: a defer and a panic must not between them leak or
// double-return a token, and a caller with no budget must still run.
func TestReleaseIsIdempotent(t *testing.T) {
	b := NewBudget(1)
	held, err := b.Admit(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	held.Release()
	held.Release()

	again, err := b.Admit(context.Background(), 1)
	if err != nil {
		t.Fatal("the token was not returned")
	}
	again.Release()

	var none *Grant
	if none.Workers() != 1 {
		t.Errorf("a caller with no budget got %d workers, want 1", none.Workers())
	}
	none.Release()
}

// TestWorkersAreClamped. Workers arrives on the wire, and the grid holds up to
// MaxTrials candidates, so without a ceiling one request names 256 goroutines.
func TestWorkersAreClamped(t *testing.T) {
	for _, tc := range []struct {
		name       string
		asked      int
		candidates int
		want       int
	}{
		{"a caller asking for a thousand", 1000, 256, MaxWorkers},
		{"a caller asking for none", 0, 256, DefaultWorkers},
		{"a caller asking for one", 1, 256, 1},
		{"a caller asking for negative", -5, 256, DefaultWorkers},
		{"more workers than candidates", 4, 2, 2},
	} {
		if got := want(tc.asked, tc.candidates); got != tc.want {
			t.Errorf("%s: %d workers, want %d", tc.name, got, tc.want)
		}
	}
	if MaxWorkers >= MaxTrials {
		t.Fatalf("MaxWorkers %d does not bound a full grid of %d", MaxWorkers, MaxTrials)
	}
}

// TestOneTenantsStudyIsNeverAnothersRefusal. Waiting for the machine is waiting;
// it is never an error, and the second tenant's study returns its own report.
func TestOneTenantsStudyIsNeverAnothersRefusal(t *testing.T) {
	b := NewBudget(1)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, org := range []string{"hanzo/acme", "zoo/acme"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			held, err := b.Admit(context.Background(), 1)
			if err != nil {
				errs[i] = err
				return
			}
			defer held.Release()
			_, errs[i] = Search(context.Background(), org, stream(120, true), small(), Options{Seed: 3}, held)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("study %d was refused because the other tenant was studying: %v", i, err)
		}
	}
}

// watched is a history that reports when it is first read.
type watched struct {
	inner replay.History
	read  chan struct{}
	once  sync.Once
}

func (w *watched) Each(fn func(replay.Event) error) error {
	w.once.Do(func() { close(w.read) })
	return w.inner.Each(fn)
}
