// Package cases provides case management — creating, updating, and
// querying compliance review cases and their event timelines.
package cases

import (
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/luxfi/aml/pkg/types"
)

// bound is a cap on what a shelf holds: how many cases before the ones already
// closed and past the window are dropped, and how long past closure that is.
//
// It is the MEMORY shelf's, and only this package's own tests set one. The
// durable shelf has no bound and [NewBase] takes no argument that could give it
// one — which is the whole of the fix, stated as a type.
//
// A case is the record that an alert was considered and what was decided, kept
// whichever way the decision went (AMLR Art. 77(1)(b); JMLSG 6.32), for five
// years (Dir. (EU) 2015/849 Art. 40). The plane that decides when a retained
// record goes is pkg/retention: it counts in years, per tenant, refuses a date
// its own clock has not reached, and proves what it destroyed before reporting
// a count. A second clock here, counting in days, cannot be reconciled with
// that one — whichever is shorter silently becomes the real retention period,
// and nobody wrote that one down.
//
// The zero value is NO bound. A Store is therefore unbounded unless something
// in this package asks for one.
type bound struct {
	cases     int
	retention time.Duration
}

// Store is the case plane: the cases, their timelines, and the decisions that
// closed them.
//
// The state lives on a [shelf], which is either durable or in memory. What an
// instance serves from is the durable one ([NewBase]); [NewStore] is the memory
// one and is for tests.
//
// The mutex is still here, and still does the same job: a status change is a
// read, a mutation and a write, and two of those interleaved lose one. It
// serialises them within the process. A second replica would need the database
// to do it instead — this deployment runs one, which is why one lock is enough
// and why that is worth stating rather than assuming.
type Store struct {
	mu    sync.Mutex
	shelf shelf
	bound bound
}

// NewStore creates an empty in-memory case store. Nothing in it survives the
// process; [NewBase] is what an instance serves from.
func NewStore() *Store {
	return &Store{shelf: newMemory()}
}

// newBounded is an in-memory store that evicts, for the tests that exercise
// eviction. It is unexported because a bounded case store is not something a
// deployment may construct: the exported constructors are [NewStore] and
// [NewBase], and neither of them takes a limit.
func newBounded(cases int, retention time.Duration) *Store {
	return &Store{shelf: newMemory(), bound: bound{cases: cases, retention: retention}}
}

// EvictExpired removes an org's cases that have been closed for longer than the
// store's bound allows, and returns how many it removed.
//
// On an unbounded store it removes nothing and returns 0. The durable store an
// instance serves from is unbounded, so there this method cannot destroy a
// record however it is called — expiry there is pkg/retention's, entirely.
//
// It evicts only what has been closed for longer than the window, so an open
// case is never dropped however many there are: the alternative — capping by
// count and evicting the oldest — throws away the investigations nobody has
// finished, which are the ones that matter.
//
// It names an org because eviction is a tenant's own. Unscoped, one
// institution's volume crossed a shared threshold and the sweep that followed
// did not ask whose evidence it was dropping.
func (s *Store) EvictExpired(org string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evictLocked(org)
}

func (s *Store) evictLocked(org string) int {
	if s.bound.retention <= 0 {
		return 0
	}
	cutoff := time.Now().UTC().Add(-s.bound.retention)
	var expired []string
	_ = s.shelf.each(org, func(c *types.Case) error {
		if !stillOpen(c, cutoff) {
			expired = append(expired, c.ID)
		}
		return nil
	})
	if len(expired) == 0 {
		return 0
	}
	if err := s.shelf.drop(org, expired); err != nil {
		return 0
	}
	return len(expired)
}

// Len is how many cases an org holds.
//
// There is no count across orgs, because no answer this plane gives is about
// more than one institution — and a total was what made a threshold everyone's
// to cross.
func (s *Store) Len(org string) int {
	n := 0
	_ = s.shelf.each(org, func(*types.Case) error { n++; return nil })
	return n
}

// Create opens a new case from a set of alerts.
func (s *Store) Create(orgID string, severity string, alertIDs []string, entityIDs []string) *types.Case {
	s.mu.Lock()
	defer s.mu.Unlock()

	number, err := s.shelf.number(orgID)
	if err != nil {
		return nil
	}
	now := time.Now().UTC()
	c := &types.Case{
		ID:        uuid.NewString(),
		OrgID:     orgID,
		Number:    number,
		Status:    types.CaseOpen,
		Severity:  severity,
		AlertIDs:  alertIDs,
		EntityIDs: entityIDs,
		OpenedAt:  now,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.shelf.put(c); err != nil {
		return nil
	}
	// Only a bounded store evicts, and the durable one is never bounded, so on
	// the shelf an instance serves from opening a case cannot destroy one.
	//
	// It cannot count them either. The total this used to take on every Create
	// was a full scan of every tenant's cases, under this lock, on the path the
	// replay gate calls the request that must not wait — and once the threshold
	// was crossed with nothing old enough to drop, every Create paid two of them
	// forever.
	if s.bound.cases > 0 && s.Len(orgID) > s.bound.cases {
		s.evictLocked(orgID)
	}
	return c
}

// Get returns a case by ID, or nil.
//
// It does not scope by tenant, because the caller holding an id is the one that
// knows whose it should be — every caller checks OrgID against its own tenant,
// and doing it here as well would put the check in two places and make the
// second one the easy one to forget.
func (s *Store) Get(id string) *types.Case {
	c, err := s.shelf.get(id)
	if err != nil {
		return nil
	}
	return c
}

// List returns cases for an org, optionally filtered by status, newest first.
func (s *Store) List(orgID, status string) []*types.Case {
	out, err := s.shelf.list(orgID, status)
	if err != nil {
		return nil
	}
	return out
}

// UpdateStatus changes a case's status and records the event.
func (s *Store) UpdateStatus(orgID, caseID, status, authorID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, err := s.owned(orgID, caseID)
	if err != nil {
		return err
	}

	old := c.Status
	now := time.Now().UTC()
	c.Status = status
	c.UpdatedAt = now
	if status == types.CaseClosed {
		c.ClosedAt = &now
	}
	if err := s.shelf.put(c); err != nil {
		return err
	}
	return s.shelf.appendEvent(types.CaseEvent{
		ID:        uuid.NewString(),
		CaseID:    caseID,
		AuthorID:  authorID,
		Kind:      types.EventStatusChange,
		Body:      old + " -> " + status,
		CreatedAt: now,
	})
}

// AddEvent appends to a case timeline after confirming the case belongs to the
// tenant making the request.
//
// The tenant is checked, not assumed. A case identifier is handed back to whoever
// submitted the transaction, so treating possession of the identifier as authority
// to write on the case would let one tenant annotate another's investigation. This
// is the ONE way to append an event: there is deliberately no unchecked variant to
// reach for, because the earlier one was reachable one refactor away from a
// cross-tenant write.
func (s *Store) AddEvent(orgID, caseID string, evt types.CaseEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.owned(orgID, caseID); err != nil {
		return err
	}
	evt.ID = uuid.NewString()
	evt.CaseID = caseID
	evt.CreatedAt = time.Now().UTC()
	return s.shelf.appendEvent(evt)
}

// Events returns the timeline for a case, oldest first.
func (s *Store) Events(caseID string) []types.CaseEvent {
	out, err := s.shelf.events(caseID)
	if err != nil {
		return nil
	}
	return out
}

// Assign sets the assignee for a case.
func (s *Store) Assign(caseID, assigneeID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, err := s.shelf.get(caseID)
	if err != nil {
		return err
	}
	if c == nil {
		return ErrNotFound
	}
	c.AssigneeID = assigneeID
	c.UpdatedAt = time.Now().UTC()
	return s.shelf.put(c)
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
func (s *Store) Resolve(orgID, caseID, resolution, authorID, assessment string) error {
	if assessment == "" {
		return ErrNoAssessment
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	c, err := s.owned(orgID, caseID)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	c.Status = types.CaseClosed
	c.Resolution = resolution
	c.Assessment = assessment
	c.ClosedAt = &now
	c.UpdatedAt = now
	if err := s.shelf.put(c); err != nil {
		return err
	}
	return s.shelf.appendEvent(types.CaseEvent{
		ID:        uuid.NewString(),
		CaseID:    caseID,
		AuthorID:  authorID,
		Kind:      types.EventStatusChange,
		Body:      "closed: " + resolution + " (assessment " + assessment + ")",
		CreatedAt: now,
	})
}

// owned resolves a case within a tenant. A case in another tenant is reported the
// same way one that does not exist is, because the caller may not tell them apart:
// a case id is not a secret, and a different answer for each would enumerate
// another institution's cases.
func (s *Store) owned(orgID, caseID string) (*types.Case, error) {
	c, err := s.shelf.get(caseID)
	if err != nil {
		return nil, err
	}
	if c == nil || c.OrgID != orgID {
		return nil, ErrNotFound
	}
	return c, nil
}
