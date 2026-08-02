package models

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/replay"
	"github.com/luxfi/aml/pkg/topology"
)

// The budget bounds the machine AND the memory, and it can only do the second
// if the hold is taken by whatever does the reading.
//
// A study's expensive half is not the arithmetic. It is the read: in the
// deployment this shelf runs in, the Source materialises up to a hundred
// thousand retained records, opens every sealed body, unmarshals each one and
// asks the alert store about each event. A token taken after that read bounds
// the trials and nothing else, so eight tenants queued behind a budget of one
// worker held eight whole histories at once — the memory scaled with the number
// of TENANTS studying rather than with the budget, which is precisely what the
// budget's own documentation promised it would not.

// counted is a Source that reports how many histories are being held at once.
type counted struct {
	inner Source
	// live is how many reads are in flight; peak is the most there have ever
	// been. A read is "in flight" from the moment it starts until the study that
	// asked for it is done, because that is how long the events stay in memory.
	live, peak atomic.Int64
	release    chan struct{}
}

func (c *counted) History(ctx context.Context, org string, held *topology.Grant) (replay.History, error) {
	n := c.live.Add(1)
	for {
		was := c.peak.Load()
		if n <= was || c.peak.CompareAndSwap(was, n) {
			break
		}
	}
	<-c.release // hold the events, as a study computing over them does
	return c.inner.History(ctx, org, held)
}

// TestNoStudyReadsAHistoryItHasNotPaidFor is red's measurement, as a property.
func TestNoStudyReadsAHistoryItHasNotPaidFor(t *testing.T) {
	const tenants = 8

	s := shelf(t)
	source := &counted{inner: s.History, release: make(chan struct{})}
	s.History = source
	s.Cores = topology.NewBudget(1)

	var wg sync.WaitGroup
	for i := range tenants {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each tenant is its own institution, so no gate refuses any of them:
			// what holds them back must be the budget or nothing.
			org := tenant(i)
			_, _ = s.Fit(context.Background(), org, &FitIn{
				Topology: shape, Options: topology.Options{Seed: 7}, By: "a.mensah",
			})
		}()
	}

	// Let every goroutine reach whatever is going to stop it — the budget, or
	// the read — and then look at how many histories are being held.
	settle(source)
	peak := source.peak.Load()
	close(source.release)
	wg.Wait()

	if budget := int64(s.Cores.Size()); peak > budget {
		t.Errorf("budget = %d worker(s); peak concurrent history reads = %d. A study waiting for the machine is holding a tenant's whole history while it waits.",
			budget, peak)
	}
}

func tenant(i int) string {
	return "hanzo/org-" + string(rune('a'+i))
}

// settle waits until the number of reads in flight has stopped moving. Polling
// rather than a barrier, because what is being measured is how many goroutines
// GET to the read: a barrier they never reach is a test that hangs instead of
// one that fails.
func settle(c *counted) {
	last, still := int64(-1), 0
	for range 400 {
		n := c.live.Load()
		if n == last {
			if still++; still >= 20 {
				return
			}
		} else {
			last, still = n, 0
		}
		time.Sleep(5 * time.Millisecond)
	}
}
