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
	c = age(t, s, c.ID, time.Now().UTC().Add(-2*time.Hour), time.Time{})
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
	_ = s.Resolve(c.OrgID, c.ID, "cleared", "analyst", "assessment-1")

	config := DefaultTriageConfig()
	escalated := s.CheckEscalation(c, config)
	if escalated {
		t.Error("should not escalate closed case")
	}
}

// A case nobody has worked must be escalated, never closed.
//
// Closing asserts that somebody assessed the case and decided; inactivity asserts
// the opposite, and the two must not produce the same state. Store.Resolve refuses
// a closure with no assessment for exactly this reason — a dismissed alert is a
// retained decision with its rationale (AMLR Art. 77(1)(b), JMLSG 6.32) — and the
// old auto-close path wrote the same fields directly and walked around it. Eviction
// then removed the closed case, so an alert that was raised and never reviewed left
// no trace of having existed.
func TestAStaleCaseIsEscalatedNotClosed(t *testing.T) {
	s := NewStore()
	stale := func(severity string) *types.Case {
		c := s.Create("org1", severity, nil, nil)
		s.mu.Lock()
		age(t, s, c.ID, time.Time{}, time.Now().UTC().Add(-60*24*time.Hour))
		s.mu.Unlock()
		return c
	}

	low := stale(types.SeverityLow)
	// A critical case nobody has touched is the more serious governance failure, not
	// the less — the old path swept only low severity, which had it the wrong way up.
	critical := stale(types.SeverityCritical)

	if n := s.AutoEscalateStale("org1", DefaultTriageConfig()); n != 2 {
		t.Fatalf("escalated %d stale cases, want 2 — every severity, not only low", n)
	}

	for _, c := range []*types.Case{low, critical} {
		got := s.Get(c.ID)
		if got.Status == types.CaseClosed {
			t.Errorf("a case nobody assessed was CLOSED, asserting a decision that was never made")
		}
		if got.Status != types.CaseEscalated {
			t.Errorf("status = %q, want escalated", got.Status)
		}
		if got.Resolution != "" {
			t.Errorf("resolution = %q — inactivity is not a resolution", got.Resolution)
		}
		if got.Assessment != "" {
			t.Errorf("assessment = %q — no assessment was made", got.Assessment)
		}
	}
}

// Nothing on a timer may close a case, whatever it is called. Closing requires an
// assessment and a person, and a cron has neither.
func TestNoTimerPathCanCloseACase(t *testing.T) {
	s := NewStore()
	c := s.Create("org1", types.SeverityLow, nil, nil)
	s.mu.Lock()
	age(t, s, c.ID, time.Time{}, time.Now().UTC().Add(-365*24*time.Hour))
	s.mu.Unlock()

	s.AutoEscalateStale("org1", DefaultTriageConfig())
	s.TriageCheck("org1", []string{"analyst-1"}, DefaultTriageConfig())

	if got := s.Get(c.ID); got.Status == types.CaseClosed {
		t.Fatal("a triage cycle closed a case; only a person with an assessment may")
	}

	// And the closure path still refuses without one.
	if err := s.Resolve(c.OrgID, c.ID, "cleared", "analyst-1", ""); err == nil {
		t.Fatal("a case was closed with no retained assessment")
	}
}

// A case somebody worked recently is left alone.
func TestRecentActivityIsNotStale(t *testing.T) {
	s := NewStore()
	c := s.Create("org1", types.SeverityLow, nil, nil)

	if n := s.AutoEscalateStale("org1", DefaultTriageConfig()); n != 0 {
		t.Errorf("escalated %d cases, want 0 — this one was just created", n)
	}
	if got := s.Get(c.ID); got.Status != types.CaseOpen {
		t.Errorf("status = %q, want open", got.Status)
	}
}

func TestTriageCheck_FullCycle(t *testing.T) {
	s := NewStore()

	// Unassigned case.
	s.Create("org1", types.SeverityMedium, nil, nil)

	// Critical case past SLA.
	c2 := s.Create("org1", types.SeverityCritical, nil, nil)
	s.mu.Lock()
	c2 = age(t, s, c2.ID, time.Now().UTC().Add(-3*time.Hour), time.Time{})
	s.mu.Unlock()

	// Stale low-severity case.
	c3 := s.Create("org1", types.SeverityLow, nil, nil)
	s.mu.Lock()
	age(t, s, c3.ID, time.Time{}, time.Now().UTC().Add(-60*24*time.Hour))
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
