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
	// each visits every case in every org. It is what a sweep is: triage and
	// eviction ask about cases regardless of whose they are.
	each(visit func(*types.Case) error) error
	// appendEvent adds one entry to a case's timeline.
	appendEvent(e types.CaseEvent) error
	// events is a case's timeline, oldest first.
	events(caseID string) ([]types.CaseEvent, error)
	// drop removes cases and their timelines.
	drop(ids []string) error
	// number is the next case number. It is the shelf's because it has to
	// continue across a restart, and a counter that starts again at one makes
	// two cases with the same number.
	number() (int64, error)
}

// memory is the shelf tests run against. Nothing in it survives the process, so
// it is not what an instance serves from — see [NewBase].
type memory struct {
	mu       sync.RWMutex
	cases    map[string]*types.Case
	timeline map[string][]types.CaseEvent
	seq      int64
}

func newMemory() *memory {
	return &memory{
		cases:    make(map[string]*types.Case),
		timeline: make(map[string][]types.CaseEvent),
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

func (m *memory) each(visit func(*types.Case) error) error {
	m.mu.RLock()
	ids := make([]*types.Case, 0, len(m.cases))
	for _, c := range m.cases {
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

func (m *memory) drop(ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		delete(m.cases, id)
		delete(m.timeline, id)
	}
	return nil
}

func (m *memory) number() (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	return m.seq, nil
}
