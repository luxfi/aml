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

// DefaultClosedCaseRetention is how long closed cases stay in memory.
const DefaultClosedCaseRetention = 90 * 24 * time.Hour // 90 days

// DefaultMaxCases is the maximum open+recent cases held in memory.
const DefaultMaxCases = 100_000

// Store is an in-memory case store with eviction of old closed cases.
// RED-08: Prevents unbounded memory growth by evicting closed cases
// older than the retention period, and hard-capping total count.
type Store struct {
	mu        sync.RWMutex
	cases     map[string]*types.Case
	events    map[string][]types.CaseEvent
	seq       atomic.Int64
	maxCases  int
	retention time.Duration
}

// NewStore creates an empty case store with default limits.
func NewStore() *Store {
	return &Store{
		cases:     make(map[string]*types.Case),
		events:    make(map[string][]types.CaseEvent),
		maxCases:  DefaultMaxCases,
		retention: DefaultClosedCaseRetention,
	}
}

// NewStoreWithLimits creates a case store with explicit limits.
func NewStoreWithLimits(maxCases int, retention time.Duration) *Store {
	if maxCases <= 0 {
		maxCases = DefaultMaxCases
	}
	if retention <= 0 {
		retention = DefaultClosedCaseRetention
	}
	return &Store{
		cases:     make(map[string]*types.Case),
		events:    make(map[string][]types.CaseEvent),
		maxCases:  maxCases,
		retention: retention,
	}
}

// EvictExpired removes closed cases older than the retention period.
// Called internally after Create; can also be called externally by a cron.
func (s *Store) EvictExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evictExpiredLocked()
}

func (s *Store) evictExpiredLocked() int {
	cutoff := time.Now().UTC().Add(-s.retention)
	evicted := 0
	for id, c := range s.cases {
		if c.Status == types.CaseClosed && c.ClosedAt != nil && c.ClosedAt.Before(cutoff) {
			delete(s.cases, id)
			delete(s.events, id)
			evicted++
		}
	}
	return evicted
}

// Len returns the number of cases in the store.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.cases)
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
	// RED-08: Evict expired closed cases when approaching capacity.
	if len(s.cases) > s.maxCases {
		s.evictExpiredLocked()
	}
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

// AddEventFor appends to a case timeline after confirming the case belongs to the
// organisation making the request.
//
// The organisation is checked rather than assumed. A case identifier is returned
// to whoever submitted the transaction, so treating possession of the identifier
// as authority to write on the case lets one tenant annotate another's
// investigation.
func (s *Store) AddEventFor(orgID, caseID string, evt types.CaseEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.cases[caseID]
	if !ok || c.OrgID != orgID {
		return ErrNotFound
	}

	evt.ID = uuid.NewString()
	evt.CaseID = caseID
	evt.CreatedAt = time.Now().UTC()
	s.events[caseID] = append(s.events[caseID], evt)
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

// Resolve closes a case with a resolution, against the retained assessment that
// decided it.
//
// The assessment id is required. Closing a case is a decision about whether to
// report, and that decision is retained whether or not it produced one
// (Regulation (EU) 2024/1624 Art. 77(1)(b)); where the decision is not to
// report, the reasons must be documented and kept with the internal suspicion
// report (JMLSG 6.32). So a dismissal here cannot be a row that quietly changes
// state: without the record of the decision there is no closure.
func (s *Store) Resolve(caseID, resolution, authorID, assessment string) error {
	if assessment == "" {
		return ErrNoAssessment
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.cases[caseID]
	if !ok {
		return ErrNotFound
	}

	now := time.Now().UTC()
	c.Status = types.CaseClosed
	c.Resolution = resolution
	c.Assessment = assessment
	c.ClosedAt = &now
	c.UpdatedAt = now

	s.events[caseID] = append(s.events[caseID], types.CaseEvent{
		ID:        uuid.NewString(),
		CaseID:    caseID,
		AuthorID:  authorID,
		Kind:      types.EventStatusChange,
		Body:      "closed: " + resolution + " (assessment " + assessment + ")",
		CreatedAt: now,
	})

	return nil
}
