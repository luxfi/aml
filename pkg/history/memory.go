package history

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Memory is an in-process Store.
//
// It exists so the detection logic can be exercised at the level a reviewer cares
// about — given this sequence of transactions, does the rule fire — without
// standing up a database. It applies the same tenant scoping and the same
// timestamp semantics as the persistent store, because a test against a store
// with weaker rules than production proves nothing about production.
type Memory struct {
	mu     sync.RWMutex
	events []Event
	orgs   []string
	now    func() time.Time
}

// NewMemory builds an empty in-process store. now supplies the reference instant
// for the lookback; nil means the wall clock.
func NewMemory(now func() time.Time) *Memory {
	return &Memory{now: now}
}

// Append records an event for an organisation.
func (m *Memory) Append(orgID string, e Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	m.orgs = append(m.orgs, orgID)
}

// Window returns the subject's events over the lookback, most recent first.
func (m *Memory) Window(_ context.Context, subj Subject, lookback time.Duration) ([]Event, error) {
	field, ok := identify(subj.Kind)
	if !ok {
		return nil, unknownKind(subj.Kind)
	}
	if subj.OrgID == "" || subj.ID == "" {
		return nil, incomplete(subj)
	}

	since := m.reference().Add(-lookback)

	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []Event
	for i, e := range m.events {
		if m.orgs[i] != subj.OrgID {
			continue
		}
		if field(e) != subj.ID {
			continue
		}
		if e.At.Before(since) {
			continue
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

func (m *Memory) reference() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now().UTC()
}

// identify returns the accessor for a subject kind.
func identify(kind string) (func(Event) string, bool) {
	switch kind {
	case SubjectUser:
		return func(e Event) string { return e.User }, true
	case SubjectAccount:
		return func(e Event) string { return e.Account }, true
	case SubjectCounterparty:
		return func(e Event) string { return e.Counterparty }, true
	case SubjectDevice:
		return func(e Event) string { return e.Device }, true
	case SubjectAddress:
		return func(e Event) string { return e.Address }, true
	}
	return nil, false
}
