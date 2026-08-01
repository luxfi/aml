// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package cases

import (
	"sort"
	"sync"

	"github.com/luxfi/aml/pkg/types"
)

// shelf is where cases and their timelines are kept.
//
// It is an interface for the same reason the retention ledger's is: an invariant
// that holds only in memory is not an invariant of this package. A case is the
// record of a decision about whether to report — kept under AMLR Art. 77(1)(b)
// whichever way the decision went — so the shelf an instance serves from has to
// be the durable one, and the memory one exists to make the tests fast rather
// than to run anything.
//
// Every method is keyed the way its caller is. Reads that name an org are scoped
// to it here; reads that name only an id are not, because the id is the key and
// the tenant is checked by the caller that holds it — the same shape the memory
// shelf always had, so the check stays in exactly one place per operation.
type shelf interface {
	// put writes a case, whether it is new or a change to one that exists.
	put(c *types.Case) error
	// get returns a case by id, or nil when there is none.
	get(id string) (*types.Case, error)
	// list returns an org's cases, optionally narrowed to one status.
	list(org, status string) ([]*types.Case, error)
	// each visits ONE org's cases. A sweep names whose cases it is sweeping:
	// counting, triage and eviction are each a tenant's own, and a sweep that
	// crossed the boundary read another institution's queue, escalated its
	// cases, and dropped its closed ones.
	each(org string, visit func(*types.Case) error) error
	// appendEvent adds one entry to a case's timeline.
	appendEvent(e types.CaseEvent) error
	// events is a case's timeline, oldest first.
	events(caseID string) ([]types.CaseEvent, error)
	// drop removes an org's cases and their timelines. The org is not made
	// redundant by the ids: it is what makes another tenant's case unreachable
	// from here rather than merely not asked for.
	drop(org string, ids []string) error
	// number is the next case number WITHIN an org. It is the shelf's because it
	// has to continue across a restart, and a counter that starts again at one
	// makes two cases with the same number.
	//
	// Per tenant, because a case number is what a file is referred to by and one
	// institution's file references should not carry another's volume: a shared
	// sequence puts gaps in B's numbering that count A's cases.
	number(org string) (int64, error)
}

// memory is the shelf tests run against. Nothing in it survives the process, so
// it is not what an instance serves from — see [NewBase].
type memory struct {
	mu       sync.RWMutex
	cases    map[string]*types.Case
	timeline map[string][]types.CaseEvent
	seq      map[string]int64
}

func newMemory() *memory {
	return &memory{
		cases:    make(map[string]*types.Case),
		timeline: make(map[string][]types.CaseEvent),
		seq:      make(map[string]int64),
	}
}

func (m *memory) put(c *types.Case) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copied := *c
	m.cases[c.ID] = &copied
	return nil
}

func (m *memory) get(id string) (*types.Case, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.cases[id]
	if !ok {
		return nil, nil
	}
	copied := *c
	return &copied, nil
}

func (m *memory) list(org, status string) ([]*types.Case, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []*types.Case
	for _, c := range m.cases {
		if c.OrgID != org {
			continue
		}
		if status != "" && c.Status != status {
			continue
		}
		copied := *c
		out = append(out, &copied)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number > out[j].Number })
	return out, nil
}

func (m *memory) each(org string, visit func(*types.Case) error) error {
	m.mu.RLock()
	ids := make([]*types.Case, 0, len(m.cases))
	for _, c := range m.cases {
		if c.OrgID != org {
			continue
		}
		copied := *c
		ids = append(ids, &copied)
	}
	m.mu.RUnlock()
	for _, c := range ids {
		if err := visit(c); err != nil {
			return err
		}
	}
	return nil
}

func (m *memory) appendEvent(e types.CaseEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.timeline[e.CaseID] = append(m.timeline[e.CaseID], e)
	return nil
}

func (m *memory) events(caseID string) ([]types.CaseEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]types.CaseEvent(nil), m.timeline[caseID]...), nil
}

func (m *memory) drop(org string, ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		c, ok := m.cases[id]
		if !ok || c.OrgID != org {
			continue
		}
		delete(m.cases, id)
		delete(m.timeline, id)
	}
	return nil
}

func (m *memory) number(org string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq[org]++
	return m.seq[org], nil
}
