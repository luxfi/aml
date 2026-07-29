package workflows

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/luxfi/aml/pkg/types"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestContinuousKYCScreen_NoMatch(t *testing.T) {
	entities := []types.Entity{
		{ID: "e1", OrgID: "org1", Name: "Alice Smith"},
		{ID: "e2", OrgID: "org1", Name: "Bob Jones"},
	}
	source := NewMemEntitySource(entities)
	sink := &MemAlertSink{}

	// Sanctions list with no matching names.
	entries := []types.SanctionsEntry{
		{Name: "Vladimir Petrov", ListID: "ofac"},
	}

	cfg := DefaultConfig("org1")
	screened, matches, cases := ContinuousKYCScreen(
		context.Background(), cfg, source, sink, entries, testLogger(),
	)

	if screened != 2 {
		t.Errorf("screened = %d, want 2", screened)
	}
	if matches != 0 {
		t.Errorf("matches = %d, want 0", matches)
	}
	if cases != 0 {
		t.Errorf("cases = %d, want 0", cases)
	}

	// Verify all entities were marked as screened.
	for _, e := range entities {
		if _, ok := source.LastScreened(e.ID); !ok {
			t.Errorf("entity %s not marked as screened", e.ID)
		}
	}
}

func TestContinuousKYCScreen_WithMatch(t *testing.T) {
	entities := []types.Entity{
		{ID: "e1", OrgID: "org1", Name: "Vladimir Petrov"},
		{ID: "e2", OrgID: "org1", Name: "Clean Name"},
	}
	source := NewMemEntitySource(entities)
	sink := &MemAlertSink{}

	entries := []types.SanctionsEntry{
		{Name: "Vladimir Petrov", ListID: "ofac"},
	}

	cfg := DefaultConfig("org1")
	screened, matches, cases := ContinuousKYCScreen(
		context.Background(), cfg, source, sink, entries, testLogger(),
	)

	if screened != 2 {
		t.Errorf("screened = %d, want 2", screened)
	}
	if matches != 1 {
		t.Errorf("matches = %d, want 1", matches)
	}
	if cases != 1 {
		t.Errorf("cases = %d, want 1", cases)
	}
	if len(sink.Alerts) != 1 {
		t.Errorf("alerts = %d, want 1", len(sink.Alerts))
	}
}

func TestContinuousKYCScreen_OrgFilter(t *testing.T) {
	entities := []types.Entity{
		{ID: "e1", OrgID: "org1", Name: "Test User"},
		{ID: "e2", OrgID: "org2", Name: "Other Org"},
	}
	source := NewMemEntitySource(entities)
	sink := &MemAlertSink{}

	cfg := DefaultConfig("org1")
	screened, _, _ := ContinuousKYCScreen(
		context.Background(), cfg, source, sink, nil, testLogger(),
	)

	if screened != 1 {
		t.Errorf("screened = %d, want 1 (org-filtered)", screened)
	}
}

func TestHighRiskRescreen(t *testing.T) {
	entities := []types.Entity{
		{ID: "e1", OrgID: "org1", Name: "Normal User", RiskScore: 0.3},
		{ID: "e2", OrgID: "org1", Name: "Risky User", RiskScore: 0.9},
		{ID: "e3", OrgID: "org1", Name: "PEP User", RiskScore: 0.4, PEP: true},
	}
	source := NewMemEntitySource(entities)
	sink := &MemAlertSink{}

	cfg := DefaultConfig("org1")
	screened, _, _ := HighRiskRescreen(
		context.Background(), cfg, source, sink, nil, testLogger(),
	)

	// Only e2 (risk 0.9 > 0.7) and e3 (PEP) should be screened.
	if screened != 2 {
		t.Errorf("screened = %d, want 2 (high-risk + PEP only)", screened)
	}
}

func TestHighRiskRescreen_MatchCreatesCriticalCase(t *testing.T) {
	entities := []types.Entity{
		{ID: "e1", OrgID: "org1", Name: "Sanctioned Person", RiskScore: 0.9},
	}
	source := NewMemEntitySource(entities)
	sink := &MemAlertSink{}

	entries := []types.SanctionsEntry{
		{Name: "Sanctioned Person", ListID: "ofac"},
	}

	cfg := DefaultConfig("org1")
	_, matches, cases := HighRiskRescreen(
		context.Background(), cfg, source, sink, entries, testLogger(),
	)

	if matches != 1 {
		t.Errorf("matches = %d, want 1", matches)
	}
	if cases != 1 {
		t.Errorf("cases = %d, want 1", cases)
	}
	if len(sink.Cases) > 0 && sink.Cases[0].Severity != types.SeverityCritical {
		t.Errorf("case severity = %q, want %q", sink.Cases[0].Severity, types.SeverityCritical)
	}
}

func TestContinuousKYCScreen_Cancellation(t *testing.T) {
	entities := make([]types.Entity, 100)
	for i := range entities {
		entities[i] = types.Entity{ID: "e-many", OrgID: "org1", Name: "Test"}
	}
	source := NewMemEntitySource(entities)
	sink := &MemAlertSink{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	cfg := DefaultConfig("org1")
	screened, _, _ := ContinuousKYCScreen(ctx, cfg, source, sink, nil, testLogger())

	// Should stop quickly due to cancellation.
	if screened > 1 {
		t.Errorf("screened = %d, expected early exit due to context cancellation", screened)
	}
}

func TestScreenEntity_AliasMatch(t *testing.T) {
	entity := types.Entity{ID: "e1", Name: "Vlad Petrov"}
	entries := []types.SanctionsEntry{
		{Name: "Vladimir Petrovich Petrov", Aliases: []string{"Vlad Petrov"}},
	}

	matched, result := screenEntity(entity, entries, 0.85)
	if !matched {
		t.Error("expected alias match")
	}
	if result.Score < 0.85 {
		t.Errorf("score = %.2f, expected >= 0.85", result.Score)
	}
}
