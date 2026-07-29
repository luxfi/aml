package cases

import (
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

func TestCreateCase(t *testing.T) {
	s := NewStore()
	c := s.Create("org1", types.SeverityHigh, []string{"a1", "a2"}, []string{"e1"})

	if c.ID == "" {
		t.Fatal("case ID should not be empty")
	}
	if c.OrgID != "org1" {
		t.Errorf("orgID = %s, want org1", c.OrgID)
	}
	if c.Status != types.CaseOpen {
		t.Errorf("status = %s, want open", c.Status)
	}
	if c.Number != 1 {
		t.Errorf("number = %d, want 1", c.Number)
	}
	if len(c.AlertIDs) != 2 {
		t.Errorf("alertIDs len = %d, want 2", len(c.AlertIDs))
	}
}

func TestGetCase(t *testing.T) {
	s := NewStore()
	c := s.Create("org1", types.SeverityMedium, nil, nil)

	got := s.Get(c.ID)
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.ID != c.ID {
		t.Errorf("ID mismatch: %s vs %s", got.ID, c.ID)
	}

	if s.Get("nonexistent") != nil {
		t.Error("Get should return nil for missing case")
	}
}

func TestListCases(t *testing.T) {
	s := NewStore()
	s.Create("org1", types.SeverityLow, nil, nil)
	s.Create("org1", types.SeverityHigh, nil, nil)
	s.Create("org2", types.SeverityMedium, nil, nil)

	all := s.List("org1", "")
	if len(all) != 2 {
		t.Errorf("list org1 all: got %d, want 2", len(all))
	}

	open := s.List("org1", types.CaseOpen)
	if len(open) != 2 {
		t.Errorf("list org1 open: got %d, want 2", len(open))
	}

	closed := s.List("org1", types.CaseClosed)
	if len(closed) != 0 {
		t.Errorf("list org1 closed: got %d, want 0", len(closed))
	}
}

func TestUpdateStatus(t *testing.T) {
	s := NewStore()
	c := s.Create("org1", types.SeverityHigh, nil, nil)

	err := s.UpdateStatus(c.ID, types.CaseInReview, "user1")
	if err != nil {
		t.Fatalf("UpdateStatus error: %v", err)
	}

	got := s.Get(c.ID)
	if got.Status != types.CaseInReview {
		t.Errorf("status = %s, want in_review", got.Status)
	}

	events := s.Events(c.ID)
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	if events[0].Kind != types.EventStatusChange {
		t.Errorf("event kind = %s, want status_change", events[0].Kind)
	}
}

func TestUpdateStatusNotFound(t *testing.T) {
	s := NewStore()
	err := s.UpdateStatus("bad-id", types.CaseClosed, "user1")
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestAddEvent(t *testing.T) {
	s := NewStore()
	c := s.Create("org1", types.SeverityLow, nil, nil)

	err := s.AddEvent(c.ID, types.CaseEvent{
		AuthorID: "user1",
		Kind:     types.EventNote,
		Body:     "test note",
	})
	if err != nil {
		t.Fatalf("AddEvent error: %v", err)
	}

	events := s.Events(c.ID)
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	if events[0].Body != "test note" {
		t.Errorf("event body = %q, want 'test note'", events[0].Body)
	}
}

func TestAddEventNotFound(t *testing.T) {
	s := NewStore()
	err := s.AddEvent("bad-id", types.CaseEvent{})
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestAssign(t *testing.T) {
	s := NewStore()
	c := s.Create("org1", types.SeverityMedium, nil, nil)

	err := s.Assign(c.ID, "reviewer1")
	if err != nil {
		t.Fatalf("Assign error: %v", err)
	}

	got := s.Get(c.ID)
	if got.AssigneeID != "reviewer1" {
		t.Errorf("assignee = %s, want reviewer1", got.AssigneeID)
	}
}

func TestResolve(t *testing.T) {
	s := NewStore()
	c := s.Create("org1", types.SeverityHigh, nil, nil)

	err := s.Resolve(c.ID, types.ResolutionSARFiled, "user1", "assessment-1")
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}

	got := s.Get(c.ID)
	if got.Status != types.CaseClosed {
		t.Errorf("status = %s, want closed", got.Status)
	}
	if got.Resolution != types.ResolutionSARFiled {
		t.Errorf("resolution = %s, want sar_filed", got.Resolution)
	}
	if got.ClosedAt == nil {
		t.Error("closed_at should be set")
	}
}

func TestSequenceIncrement(t *testing.T) {
	s := NewStore()
	c1 := s.Create("org1", types.SeverityLow, nil, nil)
	c2 := s.Create("org1", types.SeverityLow, nil, nil)
	c3 := s.Create("org1", types.SeverityLow, nil, nil)

	if c1.Number != 1 || c2.Number != 2 || c3.Number != 3 {
		t.Errorf("sequence: %d, %d, %d — want 1, 2, 3", c1.Number, c2.Number, c3.Number)
	}
}

// TestCaseStoreEviction (RED-08) verifies that closed cases older than the
// retention period are evicted when the store exceeds capacity.
func TestCaseStoreEviction(t *testing.T) {
	retention := 1 * time.Millisecond // very short for test
	s := NewStoreWithLimits(10, retention)

	// Create and immediately close 15 cases.
	for i := 0; i < 15; i++ {
		c := s.Create("org1", types.SeverityLow, nil, nil)
		s.Resolve(c.ID, types.ResolutionCleared, "system", "assessment-1")
	}

	// Wait for closed cases to become stale.
	time.Sleep(5 * time.Millisecond)

	// Create one more to trigger eviction.
	s.Create("org1", types.SeverityHigh, nil, nil)

	if s.Len() > 10 {
		t.Errorf("store size after eviction = %d, want <= 10", s.Len())
	}
}

// TestCaseStoreEvictExpiredDirect (RED-08) tests the EvictExpired method directly.
func TestCaseStoreEvictExpiredDirect(t *testing.T) {
	retention := 1 * time.Millisecond
	s := NewStoreWithLimits(1000, retention)

	// Create 5 cases, close 3 of them.
	var openIDs []string
	for i := 0; i < 5; i++ {
		c := s.Create("org1", types.SeverityLow, nil, nil)
		if i < 3 {
			s.Resolve(c.ID, types.ResolutionCleared, "system", "assessment-1")
		} else {
			openIDs = append(openIDs, c.ID)
		}
	}

	time.Sleep(5 * time.Millisecond)
	evicted := s.EvictExpired()

	if evicted != 3 {
		t.Errorf("evicted = %d, want 3", evicted)
	}
	if s.Len() != 2 {
		t.Errorf("store size = %d, want 2 (open cases)", s.Len())
	}
	for _, id := range openIDs {
		if s.Get(id) == nil {
			t.Errorf("open case %s should not be evicted", id)
		}
	}
}
