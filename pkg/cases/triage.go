package cases

import (
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/luxfi/aml/pkg/types"
)

// TriageConfig controls auto-assignment, escalation SLAs, and auto-close behavior.
type TriageConfig struct {
	AutoAssignEnabled bool
	EscalationSLA     map[string]time.Duration // severity -> max time before escalation
	// StaleAfter is how long a case may sit unworked before it is escalated. A
	// case is never closed on a timer: closing asserts a decision was made.
	StaleAfter     time.Duration
	AssignmentMode string // "round_robin", "least_loaded", "manual"
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
		StaleAfter:     30 * 24 * time.Hour, // 30 days unworked is a governance failure, not a decision
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
		loads := make(map[string]int)
		_ = s.shelf.each(func(existing *types.Case) error {
			if existing.Status != types.CaseClosed && existing.AssigneeID != "" {
				loads[existing.AssigneeID]++
			}
			return nil
		})

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
	defer s.mu.Unlock()

	existing, err := s.shelf.get(c.ID)
	if err != nil || existing == nil {
		return false
	}

	now := time.Now().UTC()
	existing.Status = types.CaseEscalated
	existing.UpdatedAt = now
	if err := s.shelf.put(existing); err != nil {
		return false
	}

	_ = s.shelf.appendEvent(types.CaseEvent{
		ID:        uuid.NewString(),
		Kind:      types.EventStatusChange,
		CaseID:    c.ID,
		AuthorID:  "system",
		Body:      "auto-escalated: SLA breached",
		CreatedAt: now,
	})

	return true
}

// AutoEscalateStale escalates cases nobody has worked after the configured
// threshold, and returns how many it escalated.
//
// It used to CLOSE them, which is the defect this replaces. Closing a case asserts
// that somebody assessed it and decided; inactivity asserts the opposite. The two
// must not produce the same state, because a dismissed alert is a retained decision
// with its rationale — Regulation (EU) 2024/1624 Art. 77(1)(b) requires the record
// of the assessment whether or not it resulted in a report, and JMLSG 6.32 requires
// the compliance officer to consider why alerts were not escalated. "Nobody looked
// at it for thirty days" is not an assessment anybody can defend.
//
// It was also unreachable as a control. Store.Resolve refuses a closure with no
// assessment; this path wrote the same fields directly and walked around that
// invariant, and eviction then removed the closed case after its retention window —
// so an alert that was raised, never reviewed, and silently closed left no trace of
// having existed.
//
// Escalation is the honest outcome: an alert nobody has looked at is exactly what a
// compliance officer needs to see, and escalating puts it in front of one instead of
// deleting the question.
func (s *Store) AutoEscalateStale(config TriageConfig) int {
	if config.StaleAfter <= 0 {
		return 0
	}

	now := time.Now().UTC()
	threshold := now.Add(-config.StaleAfter)
	escalated := 0

	s.mu.Lock()
	defer s.mu.Unlock()

	_ = s.shelf.each(func(c *types.Case) error {
		if c.Status == types.CaseClosed || c.Status == types.CaseEscalated {
			return nil
		}
		if !c.UpdatedAt.Before(threshold) {
			return nil
		}

		// Every severity, not only low. The original only swept low-severity cases,
		// which reads as caution and is the wrong way round: a critical case nobody
		// has touched is the more serious governance failure, not the less.
		c.Status = types.CaseEscalated
		c.UpdatedAt = now
		if err := s.shelf.put(c); err != nil {
			return nil
		}

		_ = s.shelf.appendEvent(types.CaseEvent{
			ID:        uuid.NewString(),
			Kind:      types.EventStatusChange,
			CaseID:    c.ID,
			AuthorID:  "system",
			Body:      "escalated: no one has worked this case for " + config.StaleAfter.String(),
			CreatedAt: now,
		})
		escalated++
		return nil
	})

	return escalated
}

// TriageCheck runs the full triage cycle: escalate cases nobody has worked, assign
// the unassigned, escalate those past their SLA. Intended to run every few minutes.
//
// Nothing here closes a case. Closing requires an assessment and a person, and
// neither is available to a cron.
func (s *Store) TriageCheck(orgID string, analysts []string, config TriageConfig) (assigned, escalated, stale int) {
	// Stale first, before assignment refreshes UpdatedAt and hides the staleness.
	stale = s.AutoEscalateStale(config)

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

	return assigned, escalated, stale
}
