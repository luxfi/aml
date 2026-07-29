package cases

import (
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

func TestAutoAssign_RoundRobin(t *testing.T) {
	s := NewStore()
	c1 := s.Create("org1", types.SeverityMedium, nil, nil)
	c2 := s.Create("org1", types.SeverityMedium, nil, nil)

	analysts := []string{"analyst-a", "analyst-b"}

	a1 := s.AutoAssign(c1, analysts, "round_robin")
	a2 := s.AutoAssign(c2, analysts, "round_robin")

	if a1 == "" || a2 == "" {
		t.Fatal("expected assignments, got empty")
	}
	if a1 == a2 {
		t.Errorf("round-robin should alternate: got %q and %q", a1, a2)
	}
}

func TestAutoAssign_LeastLoaded(t *testing.T) {
	s := NewStore()

	// Give analyst-a two open cases.
	c1 := s.Create("org1", types.SeverityMedium, nil, nil)
	_ = s.Assign(c1.ID, "analyst-a")
	c2 := s.Create("org1", types.SeverityMedium, nil, nil)
	_ = s.Assign(c2.ID, "analyst-a")

	// New unassigned case.
	c3 := s.Create("org1", types.SeverityMedium, nil, nil)

	assignee := s.AutoAssign(c3, []string{"analyst-a", "analyst-b"}, "least_loaded")
	if assignee != "analyst-b" {
		t.Errorf("assignee = %q, want analyst-b (least loaded)", assignee)
	}
}

func TestAutoAssign_Manual(t *testing.T) {
	s := NewStore()
	c := s.Create("org1", types.SeverityMedium, nil, nil)
	assignee := s.AutoAssign(c, []string{"analyst-a"}, "manual")
	if assignee != "" {
		t.Errorf("manual mode should return empty, got %q", assignee)
	}
}

func TestAutoAssign_NoAnalysts(t *testing.T) {
	s := NewStore()
	c := s.Create("org1", types.SeverityMedium, nil, nil)
	assignee := s.AutoAssign(c, nil, "round_robin")
	if assignee != "" {
		t.Errorf("expected empty with no analysts, got %q", assignee)
	}
}

func TestCheckEscalation_SLABreached(t *testing.T) {
	s := NewStore()
	c := s.Create("org1", types.SeverityCritical, nil, nil)

	// Backdate the case to trigger SLA breach.
	s.mu.Lock()
	s.cases[c.ID].OpenedAt = time.Now().UTC().Add(-2 * time.Hour)
	s.mu.Unlock()

	config := DefaultTriageConfig()
	escalated := s.CheckEscalation(c, config)
	if !escalated {
		t.Error("expected escalation for 2h-old critical case (1h SLA)")
	}

	got := s.Get(c.ID)
	if got.Status != types.CaseEscalated {
		t.Errorf("status = %q, want %q", got.Status, types.CaseEscalated)
	}
}

func TestCheckEscalation_NotBreached(t *testing.T) {
	s := NewStore()
	c := s.Create("org1", types.SeverityLow, nil, nil)

	config := DefaultTriageConfig()
	escalated := s.CheckEscalation(c, config)
	if escalated {
		t.Error("should not escalate fresh low-severity case")
	}
}

func TestCheckEscalation_AlreadyClosed(t *testing.T) {
	s := NewStore()
	c := s.Create("org1", types.SeverityCritical, nil, nil)
	_ = s.Resolve(c.ID, "cleared", "analyst")

	config := DefaultTriageConfig()
	escalated := s.CheckEscalation(c, config)
	if escalated {
		t.Error("should not escalate closed case")
	}
}

func TestAutoClose(t *testing.T) {
	s := NewStore()

	// Low-severity case with old activity.
	c := s.Create("org1", types.SeverityLow, nil, nil)
	s.mu.Lock()
	s.cases[c.ID].UpdatedAt = time.Now().UTC().Add(-60 * 24 * time.Hour) // 60 days old
	s.mu.Unlock()

	// Medium-severity case that should not be auto-closed.
	c2 := s.Create("org1", types.SeverityMedium, nil, nil)
	s.mu.Lock()
	s.cases[c2.ID].UpdatedAt = time.Now().UTC().Add(-60 * 24 * time.Hour)
	s.mu.Unlock()

	config := DefaultTriageConfig()
	closed := s.AutoClose(config)
	if closed != 1 {
		t.Errorf("closed = %d, want 1 (only low-severity)", closed)
	}

	got := s.Get(c.ID)
	if got.Status != types.CaseClosed {
		t.Errorf("low-severity status = %q, want closed", got.Status)
	}
	if got.Resolution != "auto_closed_inactive" {
		t.Errorf("resolution = %q, want auto_closed_inactive", got.Resolution)
	}

	got2 := s.Get(c2.ID)
	if got2.Status == types.CaseClosed {
		t.Error("medium-severity case should not be auto-closed")
	}
}

func TestAutoClose_RecentActivity(t *testing.T) {
	s := NewStore()
	c := s.Create("org1", types.SeverityLow, nil, nil)
	// UpdatedAt is now (fresh), so auto-close should skip it.

	config := DefaultTriageConfig()
	closed := s.AutoClose(config)
	if closed != 0 {
		t.Errorf("closed = %d, want 0 (recent activity)", closed)
	}
	_ = c
}

func TestTriageCheck_FullCycle(t *testing.T) {
	s := NewStore()

	// Unassigned case.
	s.Create("org1", types.SeverityMedium, nil, nil)

	// Critical case past SLA.
	c2 := s.Create("org1", types.SeverityCritical, nil, nil)
	s.mu.Lock()
	s.cases[c2.ID].OpenedAt = time.Now().UTC().Add(-3 * time.Hour)
	s.mu.Unlock()

	// Stale low-severity case.
	c3 := s.Create("org1", types.SeverityLow, nil, nil)
	s.mu.Lock()
	s.cases[c3.ID].UpdatedAt = time.Now().UTC().Add(-60 * 24 * time.Hour)
	s.mu.Unlock()

	config := DefaultTriageConfig()
	assigned, escalated, autoClosed := s.TriageCheck("org1", []string{"analyst-a"}, config)

	// All 3 unassigned cases get assigned (including the stale one before it's closed).
	if assigned < 2 {
		t.Errorf("assigned = %d, want at least 2", assigned)
	}
	if escalated != 1 {
		t.Errorf("escalated = %d, want 1", escalated)
	}
	if autoClosed != 1 {
		t.Errorf("autoClosed = %d, want 1", autoClosed)
	}
}

func TestDefaultTriageConfig(t *testing.T) {
	cfg := DefaultTriageConfig()
	if !cfg.AutoAssignEnabled {
		t.Error("auto-assign should be enabled by default")
	}
	if cfg.EscalationSLA[types.SeverityCritical] != 1*time.Hour {
		t.Error("critical SLA should be 1 hour")
	}
	if cfg.EscalationSLA[types.SeverityHigh] != 4*time.Hour {
		t.Error("high SLA should be 4 hours")
	}
	if cfg.EscalationSLA[types.SeverityMedium] != 24*time.Hour {
		t.Error("medium SLA should be 24 hours")
	}
	if cfg.EscalationSLA[types.SeverityLow] != 72*time.Hour {
		t.Error("low SLA should be 72 hours")
	}
}
