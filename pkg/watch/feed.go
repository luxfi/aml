// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package watch

import "sync/atomic"

// The live feed.
//
// It is a convenience over the durable plane and never a substitute for it.
// Everything it carries is already written, every question is answered from the
// rows, and a subscriber that misses one can read it back with Feed. What the
// channel adds is latency, and that is all it adds.
//
// A subscriber names its tenant and receives that tenant's activations and
// nothing else. There is no feed over every tenant, and there is no subscription
// whose tenant is decided after the fact — which is the same reason every read in
// this repo takes the tenant as an argument rather than as a filter.

// Buffer is how many activations a subscriber may fall behind by before
// deliveries to it are abandoned.
//
// A blocking send would make one slow reader stop the ingest path, which is the
// wrong failure: the monitoring plane exists to watch transactions, not to be
// paced by whoever is watching it. So the send is non-blocking, the loss is
// counted per tenant, and the count is published on every Feed — because a
// monitor that skipped a detection and does not say so reports calm.
const Buffer = 256

type feed struct {
	org string
	out chan Activation
}

// Subscribe opens a live feed of one tenant's activations. The returned function
// closes it, and closing twice is safe.
//
// The channel is closed when the subscription ends, so a range over it
// terminates. It is never closed by the publisher: a publisher that closed a
// subscriber's channel would panic the next send racing it.
func (s *Shelf) Subscribe(org string) (<-chan Activation, func()) {
	f := &feed{org: org, out: make(chan Activation, Buffer)}

	s.mu.Lock()
	s.feeds[org] = append(s.feeds[org], f)
	if s.dropped[org] == nil {
		s.dropped[org] = &atomic.Int64{}
	}
	s.mu.Unlock()

	var once bool
	return f.out, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if once {
			return
		}
		once = true
		kept := s.feeds[org][:0]
		for _, other := range s.feeds[org] {
			if other != f {
				kept = append(kept, other)
			}
		}
		if len(kept) == 0 {
			delete(s.feeds, org)
		} else {
			s.feeds[org] = kept
		}
		close(f.out)
	}
}

// publish offers a written activation to that tenant's subscribers.
//
// It holds the read lock across the sends so that a subscription being closed
// cannot close a channel between the lookup and the send. The sends themselves
// never block, so holding the lock costs one non-blocking send per subscriber.
func (s *Shelf) publish(a Activation) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, f := range s.feeds[a.Org] {
		select {
		case f.out <- a:
		default:
			if c := s.dropped[a.Org]; c != nil {
				c.Add(1)
			}
		}
	}
}

// Dropped is how many live deliveries this instance has abandoned for a tenant.
//
// It is per tenant and not global: a busy institution's slow console must not
// make another institution's feed look lossy, and the number travels on that
// tenant's own Feed answer.
func (s *Shelf) Dropped(org string) int64 {
	s.mu.RLock()
	c := s.dropped[org]
	s.mu.RUnlock()
	if c == nil {
		return 0
	}
	return c.Load()
}
