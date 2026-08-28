// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package retention

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// memory is an in-process shelf. It exists so the obligations can be exercised at
// the level a reviewer cares about — retain this, close that, does the clock
// cascade — without a database, and it applies exactly the rules the durable shelf
// applies, because a test against a shelf with weaker rules than production proves
// nothing about production.
//
// It does not survive a restart, which is why it is not what an instance serves
// from. A retention ledger that does not survive a restart breaches the five-year
// obligation it exists to discharge.
type memory struct {
	// mu is nil inside a transaction, where the caller already holds it. Go has no
	// re-entrant lock, and the alternative — a second set of methods that do not
	// lock — is two ways to do one thing.
	mu *sync.Mutex
	// held is named rather than embedded because the shelf answers questions called
	// inside and party, and so do two of the indexes.
	held *held
}

// held is what a memory shelf holds, by pointer so that the shelf handed to a
// transaction is the same records under a lock already taken.
type held struct {
	records map[string]*Record
	// party maps org and party to the records naming that party, which is what
	// makes the Art. 78 lookback an index read rather than a scan.
	party map[string][]string
	// inside maps a relationship to the records retained inside it, so ending the
	// relationship reaches them.
	inside map[string][]string
	// identity maps an identity to the record already retained under it, so a
	// client's retry finds that record instead of writing a second one.
	identity map[string]string
}

func newMemory() *memory {
	return &memory{
		mu: new(sync.Mutex),
		held: &held{
			records:  make(map[string]*Record),
			party:    make(map[string][]string),
			inside:   make(map[string][]string),
			identity: make(map[string]string),
		},
	}
}

func (m *memory) lock() {
	if m.mu != nil {
		m.mu.Lock()
	}
}

func (m *memory) unlock() {
	if m.mu != nil {
		m.mu.Unlock()
	}
}

// tx runs fn with the lock held, against a shelf that shares these records. There
// is nothing to roll back: the ledger reports a failed sequence as failed and the
// next run repeats it, which is the same contract the durable shelf keeps.
func (m *memory) tx(fn func(shelf) error) error {
	m.lock()
	defer m.unlock()
	return fn(&memory{held: m.held})
}

func key(org, value string) string { return org + "\x00" + value }

func (m *memory) read(org, id string) (Record, error) {
	m.lock()
	defer m.unlock()
	return m.get(org, id)
}

func (m *memory) get(org, id string) (Record, error) {
	r, ok := m.held.records[id]
	if !ok || r.Org != org {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return r.clone(), nil
}

func (m *memory) retained(org, identity string) (Record, error) {
	m.lock()
	defer m.unlock()
	id, ok := m.held.identity[key(org, identity)]
	if !ok {
		return Record{}, fmt.Errorf("%w: identity %s", ErrNotFound, identity)
	}
	return m.get(org, id)
}

func (m *memory) insert(r Record) error {
	m.lock()
	defer m.unlock()
	if _, taken := m.held.identity[key(r.Org, r.identity)]; taken {
		return fmt.Errorf("%w: %s", ErrConflict, r.identity)
	}
	stored := r.clone()
	m.held.records[r.ID] = &stored
	m.held.identity[key(r.Org, r.identity)] = r.ID
	for _, p := range r.Parties {
		k := key(r.Org, p)
		m.held.party[k] = append(m.held.party[k], r.ID)
	}
	if r.Relationship != "" {
		k := key(r.Org, r.Relationship)
		m.held.inside[k] = append(m.held.inside[k], r.ID)
	}
	return nil
}

func (m *memory) update(r Record) error {
	m.lock()
	defer m.unlock()
	stored, ok := m.held.records[r.ID]
	if !ok || stored.Org != r.Org {
		return fmt.Errorf("%w: %s", ErrNotFound, r.ID)
	}
	kept := r.clone()
	m.held.records[r.ID] = &kept
	return nil
}

func (m *memory) erase(rs []Record) error {
	m.lock()
	defer m.unlock()
	for _, r := range rs {
		delete(m.held.records, r.ID)
		delete(m.held.identity, key(r.Org, r.identity))
		for _, p := range r.Parties {
			k := key(r.Org, p)
			if rest := drop(m.held.party[k], r.ID); len(rest) == 0 {
				delete(m.held.party, k)
			} else {
				m.held.party[k] = rest
			}
		}
		if r.Relationship != "" {
			k := key(r.Org, r.Relationship)
			if rest := drop(m.held.inside[k], r.ID); len(rest) == 0 {
				delete(m.held.inside, k)
			} else {
				m.held.inside[k] = rest
			}
		}
		// A destroyed relationship stops indexing what was retained inside it.
		// Anything of its own that outlives it did so by extension, on purpose.
		delete(m.held.inside, key(r.Org, r.ID))
	}
	return nil
}

func (m *memory) inside(org, relationship string) ([]Record, error) {
	m.lock()
	defer m.unlock()
	return m.collect(org, m.held.inside[key(org, relationship)])
}

func (m *memory) party(org, party string) ([]Record, error) {
	m.lock()
	defer m.unlock()
	return m.collect(org, m.held.party[key(org, party)])
}

func (m *memory) collect(org string, ids []string) ([]Record, error) {
	out := make([]Record, 0, len(ids))
	for _, id := range ids {
		r, err := m.get(org, id)
		if err != nil {
			// An index entry pointing at a record that is not there. It is not this
			// read's business to repair it; disposal is what proves the indexes,
			// and it reports exactly this as a failure.
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// each visits an org's records of one class, oldest event first. The order is
// settled first on the cheap fields and each record is copied as it is visited, so
// a walk does not double the ledger in memory and does not hold the lock across
// the caller's callback.
func (m *memory) each(org string, c Class, visit func(Record) error) error {
	type place struct {
		id string
		at time.Time
	}

	m.lock()
	order := make([]place, 0, len(m.held.records))
	for id, r := range m.held.records {
		if r.Org != org || (c != "" && r.Class != c) {
			continue
		}
		order = append(order, place{id: id, at: r.Occurred})
	}
	m.unlock()

	slices.SortFunc(order, func(a, b place) int {
		if d := a.at.Compare(b.at); d != 0 {
			return d
		}
		return strings.Compare(a.id, b.id)
	})

	for _, p := range order {
		m.lock()
		r, err := m.get(org, p.id)
		m.unlock()
		if err != nil {
			// Destroyed between settling the order and reading it. A record that no
			// longer exists is not visited.
			continue
		}
		if err := visit(r); err != nil {
			return err
		}
	}
	return nil
}

func (m *memory) expired(now time.Time, limit int) ([]Record, error) {
	m.lock()
	defer m.unlock()

	var doomed []Record
	for _, r := range m.held.records {
		if r.Expired(now) {
			doomed = append(doomed, r.clone())
		}
	}
	slices.SortFunc(doomed, func(a, b Record) int { return strings.Compare(a.ID, b.ID) })
	if limit > 0 && len(doomed) > limit {
		doomed = doomed[:limit]
	}
	return doomed, nil
}

func (m *memory) orphans(limit int) ([]string, error) {
	m.lock()
	defer m.unlock()

	var out []string
	for _, ids := range m.held.party {
		for _, id := range ids {
			if _, ok := m.held.records[id]; !ok {
				out = append(out, id)
			}
		}
	}
	slices.Sort(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memory) count() (int, error) {
	m.lock()
	defer m.unlock()
	return len(m.held.records), nil
}

func drop(ids []string, id string) []string {
	out := ids[:0:0]
	for _, existing := range ids {
		if existing != id {
			out = append(out, existing)
		}
	}
	return out
}
