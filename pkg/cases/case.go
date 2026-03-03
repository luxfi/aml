// Package cases provides case management — creating, updating, and
// querying compliance review cases and their event timelines.
package cases

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/luxfi/aml/pkg/types"
)

// Store is an in-memory case store. Production uses Base collections;
// this provides the domain logic without a DB dependency.
type Store struct {
	mu     sync.RWMutex
	cases  map[string]*types.Case
	events map[string][]types.CaseEvent
	seq    atomic.Int64
}

// NewStore creates an empty case store.
func NewStore() *Store {
	return &Store{
		cases:  make(map[string]*types.Case),
		events: make(map[string][]types.CaseEvent),
	}
}

// Create opens a new case from a set of alerts.
func (s *Store) Create(orgID string, severity string, alertIDs []string, entityIDs []string) *types.Case {
	now := time.Now().UTC()
	c := &types.Case{
		ID:        uuid.NewString(),
		OrgID:     orgID,
		Number:    s.seq.Add(1),
		Status:    types.CaseOpen,
		Severity:  severity,
		AlertIDs:  alertIDs,
		EntityIDs: entityIDs,
		OpenedAt:  now,
		CreatedAt: now,
		UpdatedAt: now,
	}

	s.mu.Lock()
	s.cases[c.ID] = c
	s.mu.Unlock()

	return c
}

// Get returns a case by ID.
func (s *Store) Get(id string) *types.Case {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cases[id]
}

// List returns cases for an org, optionally filtered by status.
func (s *Store) List(orgID, status string) []*types.Case {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var out []*types.Case
	for _, c := range s.cases {
		if c.OrgID != orgID {
			continue
		}
		if status != "" && c.Status != status {
			continue
		}
		out = append(out, c)
	}
	return out
}

// UpdateStatus changes a case's status and records the event.
func (s *Store) UpdateStatus(caseID, status, authorID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.cases[caseID]
	if !ok {
		return ErrNotFound
	}

	oldStatus := c.Status
	c.Status = status
	now := time.Now().UTC()
	c.UpdatedAt = now

	if status == types.CaseClosed {
		c.ClosedAt = &now
	}

	s.events[caseID] = append(s.events[caseID], types.CaseEvent{
		ID:        uuid.NewString(),
		CaseID:    caseID,
		AuthorID:  authorID,
		Kind:      types.EventStatusChange,
		Body:      oldStatus + " -> " + status,
		CreatedAt: now,
	})

	return nil
}

// AddEvent appends a note, file, or other event to a case timeline.
func (s *Store) AddEvent(caseID string, evt types.CaseEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.cases[caseID]; !ok {
		return ErrNotFound
	}

	evt.ID = uuid.NewString()
	evt.CaseID = caseID
	evt.CreatedAt = time.Now().UTC()
	s.events[caseID] = append(s.events[caseID], evt)
	return nil
}

// Events returns the timeline for a case.
func (s *Store) Events(caseID string) []types.CaseEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.events[caseID]
}

// Assign sets the assignee for a case.
func (s *Store) Assign(caseID, assigneeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.cases[caseID]
	if !ok {
		return ErrNotFound
	}
	c.AssigneeID = assigneeID
	c.UpdatedAt = time.Now().UTC()
	return nil
}

// Resolve closes a case with a resolution.
func (s *Store) Resolve(caseID, resolution, authorID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.cases[caseID]
	if !ok {
		return ErrNotFound
	}

	now := time.Now().UTC()
	c.Status = types.CaseClosed
	c.Resolution = resolution
	c.ClosedAt = &now
	c.UpdatedAt = now

	s.events[caseID] = append(s.events[caseID], types.CaseEvent{
		ID:        uuid.NewString(),
		CaseID:    caseID,
		AuthorID:  authorID,
		Kind:      types.EventStatusChange,
		Body:      "closed: " + resolution,
		CreatedAt: now,
	})

	return nil
}
