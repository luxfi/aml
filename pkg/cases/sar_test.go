// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package cases

import (
	"strings"
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/types"
)

func TestGenerateSARDraft_Basic(t *testing.T) {
	now := time.Now().UTC()
	c := &types.Case{
		ID:       "case-001",
		OrgID:    "org-test",
		Number:   42,
		Status:   types.CaseOpen,
		Severity: types.SeverityHigh,
		AlertIDs: []string{"alert-1", "alert-2"},
		OpenedAt: now,
	}

	alerts := []types.Alert{
		{
			ID:          "alert-1",
			RuleName:    "CTR Threshold",
			Severity:    types.SeverityHigh,
			ActionTaken: types.ActionReport,
			Score:       0.3,
			CreatedAt:   now.Add(-2 * time.Hour),
		},
		{
			ID:          "alert-2",
			RuleName:    "Structuring",
			Severity:    types.SeverityMedium,
			ActionTaken: types.ActionFlag,
			Score:       0.25,
			CreatedAt:   now.Add(-1 * time.Hour),
		},
	}

	entity := types.Entity{
		ID:           "entity-1",
		Name:         "John Smith",
		EntityType:   types.EntityUser,
		Jurisdiction: "US",
	}

	draft := GenerateSARDraft(c, alerts, entity)

	if draft.CaseID != "case-001" {
		t.Fatalf("expected case-001, got %q", draft.CaseID)
	}
	if draft.FilingType != FilingInitial {
		t.Fatalf("expected initial, got %q", draft.FilingType)
	}
	if draft.SubjectName != "John Smith" {
		t.Fatalf("expected John Smith, got %q", draft.SubjectName)
	}
	if draft.SubjectType != types.EntityUser {
		t.Fatalf("expected user, got %q", draft.SubjectType)
	}
	if draft.TransactionCount != 2 {
		t.Fatalf("expected 2 transactions, got %d", draft.TransactionCount)
	}
	if len(draft.ActivityType) != 2 {
		t.Fatalf("expected 2 activity types, got %d", len(draft.ActivityType))
	}
	if draft.Narrative == "" {
		t.Fatal("expected non-empty narrative")
	}
	if !strings.Contains(draft.Narrative, "John Smith") {
		t.Fatal("narrative should contain subject name")
	}
	if !strings.Contains(draft.Narrative, "CTR Threshold") {
		t.Fatal("narrative should contain alert rule name")
	}
	if !strings.Contains(draft.Narrative, "ANALYST") {
		t.Fatal("narrative should contain analyst review notice")
	}
	if draft.DateRangeStart.After(draft.DateRangeEnd) {
		t.Fatal("date range start should be before end")
	}
}

func TestGenerateSARDraft_PEP(t *testing.T) {
	now := time.Now().UTC()
	c := &types.Case{
		ID:       "case-pep",
		Number:   1,
		Severity: types.SeverityCritical,
		AlertIDs: []string{"alert-pep"},
		OpenedAt: now,
	}
	alerts := []types.Alert{
		{
			ID: "alert-pep", RuleName: "PEP Large Transaction",
			Severity: types.SeverityHigh, ActionTaken: types.ActionReview,
			Score: 0.3, CreatedAt: now,
		},
	}
	entity := types.Entity{
		Name:       "Jane Minister",
		EntityType: types.EntityUser,
		PEP:        true,
	}

	draft := GenerateSARDraft(c, alerts, entity)
	if !strings.Contains(draft.Narrative, "Politically Exposed Person") {
		t.Fatal("PEP entity should have PEP note in narrative")
	}
}

func TestGenerateSARDraft_Sanctions(t *testing.T) {
	now := time.Now().UTC()
	c := &types.Case{
		ID:       "case-sanc",
		Number:   2,
		Severity: types.SeverityCritical,
		AlertIDs: []string{"alert-sanc"},
		OpenedAt: now,
	}
	alerts := []types.Alert{
		{
			ID: "alert-sanc", RuleName: "Sanctions Direct",
			Severity: types.SeverityCritical, ActionTaken: types.ActionBlock,
			Score: 0.5, CreatedAt: now,
		},
	}
	entity := types.Entity{
		Name:          "Blocked Entity",
		EntityType:    types.EntityAccount,
		SanctionsFlag: true,
		Jurisdiction:  "IR",
	}

	draft := GenerateSARDraft(c, alerts, entity)
	if !strings.Contains(draft.Narrative, "sanctions list match") {
		t.Fatal("sanctioned entity should have sanctions note in narrative")
	}
	if !strings.Contains(draft.Narrative, "IR") {
		t.Fatal("narrative should mention jurisdiction")
	}
}

func TestGenerateSARDraft_EmptyAlerts(t *testing.T) {
	c := &types.Case{
		ID:       "case-empty",
		Number:   3,
		Severity: types.SeverityLow,
		OpenedAt: time.Now().UTC(),
	}
	entity := types.Entity{
		Name:       "Nobody",
		EntityType: types.EntityUser,
	}

	draft := GenerateSARDraft(c, nil, entity)
	if draft.TransactionCount != 0 {
		t.Fatalf("expected 0 transactions, got %d", draft.TransactionCount)
	}
	if draft.Amount != 0 {
		t.Fatalf("expected 0 amount, got %f", draft.Amount)
	}
}

func TestSARDraftConstants(t *testing.T) {
	if FilingInitial != "initial" {
		t.Fatalf("FilingInitial = %q", FilingInitial)
	}
	if FilingContinuing != "continuing" {
		t.Fatalf("FilingContinuing = %q", FilingContinuing)
	}
	if FilingCorrection != "correction" {
		t.Fatalf("FilingCorrection = %q", FilingCorrection)
	}
}
