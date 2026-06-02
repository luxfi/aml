// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package cases

import (
	"fmt"
	"strings"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

// FilingType constants for FinCEN SAR.
const (
	FilingInitial    = "initial"
	FilingContinuing = "continuing"
	FilingCorrection = "correction"
)

// SARDraft is a structured SAR (Suspicious Activity Report) draft.
// An analyst reviews and submits this — it is never auto-filed.
type SARDraft struct {
	// Filing metadata
	FilingType  string    `json:"filing_type"`
	FiledBy     string    `json:"filed_by,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`

	// Subject information (from entity)
	SubjectName       string `json:"subject_name"`
	SubjectType       string `json:"subject_type"` // individual, entity
	SubjectIDType     string `json:"subject_id_type,omitempty"`
	SubjectIDNumber   string `json:"subject_id_number,omitempty"`
	SubjectAddress    string `json:"subject_address,omitempty"`
	SubjectDOB        string `json:"subject_dob,omitempty"`
	SubjectSSN        string `json:"subject_ssn,omitempty"` // last 4 only
	SubjectOccupation string `json:"subject_occupation,omitempty"`

	// Suspicious activity details
	ActivityType     []string  `json:"activity_type"`
	InstrumentType   string    `json:"instrument_type,omitempty"`
	Amount           float64   `json:"amount"`
	Currency         string    `json:"currency"`
	DateRangeStart   time.Time `json:"date_range_start"`
	DateRangeEnd     time.Time `json:"date_range_end"`
	TransactionCount int       `json:"transaction_count"`

	// Narrative (the core of the SAR — must be human-reviewed)
	Narrative string `json:"narrative"`

	// Case reference
	CaseID   string   `json:"case_id"`
	AlertIDs []string `json:"alert_ids"`
}

// GenerateSARDraft creates a SAR draft from a case, its alerts, and the entity.
func GenerateSARDraft(c *types.Case, alerts []types.Alert, entity types.Entity) *SARDraft {
	draft := &SARDraft{
		FilingType:  FilingInitial,
		GeneratedAt: time.Now().UTC(),
		CaseID:      c.ID,
		AlertIDs:    c.AlertIDs,

		SubjectName: entity.Name,
		SubjectType: entity.EntityType,
		Currency:    "USD",
	}

	// Compute amount and date range from alerts.
	var totalAmount float64
	var earliest, latest time.Time
	activityTypes := make(map[string]bool)

	for _, a := range alerts {
		totalAmount += a.Score * 10000 // Rough estimate from score
		activityTypes[a.RuleName] = true

		if earliest.IsZero() || a.CreatedAt.Before(earliest) {
			earliest = a.CreatedAt
		}
		if a.CreatedAt.After(latest) {
			latest = a.CreatedAt
		}
	}

	draft.Amount = totalAmount
	draft.DateRangeStart = earliest
	draft.DateRangeEnd = latest
	draft.TransactionCount = len(alerts)

	types_ := make([]string, 0, len(activityTypes))
	for t := range activityTypes {
		types_ = append(types_, t)
	}
	draft.ActivityType = types_

	draft.Narrative = generateNarrative(c, alerts, entity)

	return draft
}

// generateNarrative creates the SAR narrative text from case + alerts + entity.
func generateNarrative(c *types.Case, alerts []types.Alert, entity types.Entity) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("This report is filed regarding suspicious activity involving %s ", entity.Name))
	b.WriteString(fmt.Sprintf("(entity type: %s", entity.EntityType))
	if entity.Jurisdiction != "" {
		b.WriteString(fmt.Sprintf(", jurisdiction: %s", entity.Jurisdiction))
	}
	b.WriteString(").\n\n")

	b.WriteString(fmt.Sprintf("Case #%d was opened on %s with severity %s. ",
		c.Number, c.OpenedAt.Format("2006-01-02"), c.Severity))
	b.WriteString(fmt.Sprintf("%d alert(s) triggered:\n\n", len(alerts)))

	for i, a := range alerts {
		b.WriteString(fmt.Sprintf("  %d. %s (severity: %s, action: %s, score: %.2f)\n",
			i+1, a.RuleName, a.Severity, a.ActionTaken, a.Score))
	}

	b.WriteString("\n")

	if entity.PEP {
		b.WriteString("NOTE: Subject is flagged as a Politically Exposed Person (PEP).\n")
	}
	if entity.SanctionsFlag {
		b.WriteString("NOTE: Subject has a sanctions list match.\n")
	}

	b.WriteString("\n[ANALYST: Review and supplement this narrative before filing.]")

	return b.String()
}
