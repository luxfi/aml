package cases

import (
	"sync/atomic"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

// TriageConfig controls auto-assignment, escalation SLAs, and auto-close behavior.
type TriageConfig struct {
	AutoAssignEnabled bool
	EscalationSLA     map[string]time.Duration // severity -> max time before escalation
	AutoCloseAfter    time.Duration            // auto-close low-severity if no activity
	AssignmentMode    string                   // "round_robin", "least_loaded", "manual"
}

// DefaultTriageConfig returns production-safe SLA defaults.
func DefaultTriageConfig() TriageConfig {
	return TriageConfig{
		AutoAssignEnabled: true,
		EscalationSLA: map[string]time.Duration{
			types.SeverityCritical: 1 * time.Hour,
			types.SeverityHigh:     4 * time.Hour,
			types.SeverityMedium:   24 * time.Hour,
			types.SeverityLow:      72 * time.Hour,
		},
		AutoCloseAfter: 30 * 24 * time.Hour, // 30 days
		AssignmentMode: "round_robin",
	}
}

// rrIndex tracks round-robin state for assignment.
var rrIndex atomic.Int64

// AutoAssign assigns an unassigned case to an analyst.
// Returns the assigned analyst ID, or empty if no analysts available.
func (s *Store) AutoAssign(c *types.Case, analysts []string, mode string) string {
	if len(analysts) == 0 {
		return ""
	}

	var assignee string

	switch mode {
	case "round_robin":
		idx := rrIndex.Add(1) - 1
		assignee = analysts[idx%int64(len(analysts))]

	case "least_loaded":
		// Count open cases per analyst.
		s.mu.RLock()
		loads := make(map[string]int)
		for _, existing := range s.cases {
			if existing.Status != types.CaseClosed && existing.AssigneeID != "" {
				loads[existing.AssigneeID]++
			}
		}
		s.mu.RUnlock()

		minLoad := -1
		for _, a := range analysts {
			load := loads[a]
			if minLoad == -1 || load < minLoad {
				minLoad = load
				assignee = a
			}
		}

	default: // "manual" or unknown: no auto-assign
		return ""
	}

	if assignee != "" {
		_ = s.Assign(c.ID, assignee)
	}

	return assignee
}

// CheckEscalation returns true and escalates the case if the SLA is breached.
func (s *Store) CheckEscalation(c *types.Case, config TriageConfig) bool {
	if c.Status == types.CaseClosed || c.Status == types.CaseEscalated {
		return false
	}

	sla, ok := config.EscalationSLA[c.Severity]
	if !ok {
		return false
	}

	deadline := c.OpenedAt.Add(sla)
	if time.Now().UTC().Before(deadline) {
		return false
	}

	// SLA breached: escalate.
	s.mu.Lock()
	existing, ok := s.cases[c.ID]
	if !ok {
		s.mu.Unlock()
		return false
	}

	now := time.Now().UTC()
	existing.Status = types.CaseEscalated
	existing.UpdatedAt = now

	s.events[c.ID] = append(s.events[c.ID], types.CaseEvent{
		Kind:      types.EventStatusChange,
		CaseID:    c.ID,
		AuthorID:  "system",
		Body:      "auto-escalated: SLA breached",
		CreatedAt: now,
	})
	s.mu.Unlock()

	return true
}

// AutoClose closes low-severity cases with no activity after the configured threshold.
// Returns the number of cases closed.
func (s *Store) AutoClose(config TriageConfig) int {
	if config.AutoCloseAfter <= 0 {
		return 0
	}

	now := time.Now().UTC()
	threshold := now.Add(-config.AutoCloseAfter)
	closed := 0

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, c := range s.cases {
		if c.Status == types.CaseClosed || c.Status == types.CaseEscalated {
			continue
		}
		if c.Severity != types.SeverityLow {
			continue
		}

		// Check last activity: use UpdatedAt as proxy.
		if c.UpdatedAt.Before(threshold) {
			c.Status = types.CaseClosed
			c.Resolution = "auto_closed_inactive"
			c.ClosedAt = &now
			c.UpdatedAt = now

			s.events[c.ID] = append(s.events[c.ID], types.CaseEvent{
				Kind:      types.EventStatusChange,
				CaseID:    c.ID,
				AuthorID:  "system",
				Body:      "auto-closed: no activity for " + config.AutoCloseAfter.String(),
				CreatedAt: now,
			})
			closed++
		}
	}

	return closed
}

// TriageCheck runs the full triage cycle: auto-assign unassigned, escalate overdue, auto-close stale.
// Intended to run every 5 minutes.
func (s *Store) TriageCheck(orgID string, analysts []string, config TriageConfig) (assigned, escalated, autoClosed int) {
	// Auto-close stale low-severity FIRST — before assign refreshes UpdatedAt.
	autoClosed = s.AutoClose(config)

	cases := s.List(orgID, "")
	for _, c := range cases {
		if c.Status == types.CaseClosed {
			continue
		}

		// Auto-assign unassigned cases.
		if config.AutoAssignEnabled && c.AssigneeID == "" {
			if a := s.AutoAssign(c, analysts, config.AssignmentMode); a != "" {
				assigned++
			}
		}

		// Check escalation.
		if s.CheckEscalation(c, config) {
			escalated++
		}
	}

	return assigned, escalated, autoClosed
}
