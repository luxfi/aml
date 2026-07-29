// Package workflows provides cron-driven AML workflows: continuous KYC
// re-screening, high-risk entity monitoring, and entity change detection.
package workflows

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/luxfi/aml/pkg/sanctions"
	"github.com/luxfi/aml/pkg/types"
)

// EntitySource fetches active entities for re-screening.
// Production wires IAM or database queries; tests use in-memory stubs.
type EntitySource interface {
	// ActiveEntities returns all entities with approved KYC.
	ActiveEntities(ctx context.Context, orgID string) ([]types.Entity, error)
	// HighRiskEntities returns entities with risk_score > threshold or PEP=true.
	HighRiskEntities(ctx context.Context, orgID string, threshold float64) ([]types.Entity, error)
	// UpdateLastScreened marks an entity as screened at the given time.
	UpdateLastScreened(ctx context.Context, entityID string, at time.Time) error
}

// AlertSink receives new alerts and cases from screening.
type AlertSink interface {
	CreateAlert(ctx context.Context, alert types.Alert) error
	CreateCase(ctx context.Context, orgID, severity string, alertIDs, entityIDs []string) (*types.Case, error)
}

// ScreenResult captures the outcome of a single entity re-screen.
type ScreenResult struct {
	EntityID  string
	Matched   bool
	Score     float64
	ListID    string
	EntryName string
}

// ContinuousKYCConfig controls re-screening behavior.
type ContinuousKYCConfig struct {
	OrgID             string
	HighRiskThreshold float64       // entities above this risk_score get hourly rescreen
	MatchThreshold    float64       // Jaro-Winkler threshold for sanctions match
	BatchSize         int           // max entities per batch
	Timeout           time.Duration // per-entity timeout
}

// DefaultConfig returns conservative defaults.
func DefaultConfig(orgID string) ContinuousKYCConfig {
	return ContinuousKYCConfig{
		OrgID:             orgID,
		HighRiskThreshold: 0.7,
		MatchThreshold:    sanctions.MatchThreshold,
		BatchSize:         500,
		Timeout:           5 * time.Second,
	}
}

// ContinuousKYCScreen re-screens all active users against current sanctions lists.
// Intended to run daily at 02:00 UTC.
func ContinuousKYCScreen(
	ctx context.Context,
	cfg ContinuousKYCConfig,
	source EntitySource,
	sink AlertSink,
	entries []types.SanctionsEntry,
	logger *slog.Logger,
) (screened int, matches int, cases int) {
	entities, err := source.ActiveEntities(ctx, cfg.OrgID)
	if err != nil {
		logger.Error("failed to fetch active entities", "err", err)
		return 0, 0, 0
	}

	for _, entity := range entities {
		select {
		case <-ctx.Done():
			return screened, matches, cases
		default:
		}

		matched, result := screenEntity(entity, entries, cfg.MatchThreshold)
		screened++

		if err := source.UpdateLastScreened(ctx, entity.ID, time.Now().UTC()); err != nil {
			logger.Error("failed to update last_screened", "entity_id", entity.ID, "err", err)
		}

		if !matched {
			continue
		}

		matches++

		alert := types.Alert{
			OrgID:       cfg.OrgID,
			RuleID:      "continuous_kyc_rescreen",
			RuleName:    "Continuous KYC Rescreen",
			Severity:    types.SeverityHigh,
			ActionTaken: types.ActionReview,
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
		}

		if err := sink.CreateAlert(ctx, alert); err != nil {
			logger.Error("failed to create alert", "entity_id", entity.ID, "err", err)
			continue
		}

		_, err := sink.CreateCase(ctx, cfg.OrgID, types.SeverityHigh,
			[]string{alert.ID}, []string{entity.ID})
		if err != nil {
			logger.Error("failed to create case", "entity_id", entity.ID, "err", err)
			continue
		}
		cases++

		logger.Warn("sanctions match on rescreen",
			"entity_id", entity.ID,
			"entity_name", entity.Name,
			"match_score", result.Score,
			"match_entry", result.EntryName,
		)
	}

	logger.Info("continuous KYC screen complete",
		"screened", screened,
		"matches", matches,
		"cases", cases,
	)

	return screened, matches, cases
}

// HighRiskRescreen re-screens entities above the risk threshold or flagged as PEP.
// Intended to run hourly.
func HighRiskRescreen(
	ctx context.Context,
	cfg ContinuousKYCConfig,
	source EntitySource,
	sink AlertSink,
	entries []types.SanctionsEntry,
	logger *slog.Logger,
) (screened int, matches int, cases int) {
	entities, err := source.HighRiskEntities(ctx, cfg.OrgID, cfg.HighRiskThreshold)
	if err != nil {
		logger.Error("failed to fetch high-risk entities", "err", err)
		return 0, 0, 0
	}

	for _, entity := range entities {
		select {
		case <-ctx.Done():
			return screened, matches, cases
		default:
		}

		matched, result := screenEntity(entity, entries, cfg.MatchThreshold)
		screened++

		if err := source.UpdateLastScreened(ctx, entity.ID, time.Now().UTC()); err != nil {
			logger.Error("failed to update last_screened", "entity_id", entity.ID, "err", err)
		}

		if !matched {
			continue
		}

		matches++

		alert := types.Alert{
			OrgID:       cfg.OrgID,
			RuleID:      "high_risk_rescreen",
			RuleName:    "High-Risk Rescreen",
			Severity:    types.SeverityCritical,
			ActionTaken: types.ActionBlock,
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
		}

		if err := sink.CreateAlert(ctx, alert); err != nil {
			logger.Error("failed to create alert", "entity_id", entity.ID, "err", err)
			continue
		}

		_, err := sink.CreateCase(ctx, cfg.OrgID, types.SeverityCritical,
			[]string{alert.ID}, []string{entity.ID})
		if err != nil {
			logger.Error("failed to create case", "entity_id", entity.ID, "err", err)
			continue
		}
		cases++

		logger.Warn("high-risk sanctions match",
			"entity_id", entity.ID,
			"entity_name", entity.Name,
			"risk_score", entity.RiskScore,
			"match_score", result.Score,
		)
	}

	logger.Info("high-risk rescreen complete",
		"screened", screened,
		"matches", matches,
		"cases", cases,
	)

	return screened, matches, cases
}

// screenEntity checks one entity against all sanctions entries.
func screenEntity(entity types.Entity, entries []types.SanctionsEntry, threshold float64) (bool, ScreenResult) {
	for _, entry := range entries {
		// Full name match.
		score := sanctions.JaroWinkler(entity.Name, entry.Name)
		if score >= threshold {
			return true, ScreenResult{
				EntityID:  entity.ID,
				Matched:   true,
				Score:     score,
				ListID:    entry.ListID,
				EntryName: entry.Name,
			}
		}

		// Token match for partial name coverage.
		tokenScore := sanctions.TokenMatch(entity.Name, entry.Name)
		if tokenScore >= threshold {
			return true, ScreenResult{
				EntityID:  entity.ID,
				Matched:   true,
				Score:     tokenScore,
				ListID:    entry.ListID,
				EntryName: entry.Name,
			}
		}

		// Also check aliases.
		for _, alias := range entry.Aliases {
			aliasScore := sanctions.JaroWinkler(entity.Name, alias)
			if aliasScore >= threshold {
				return true, ScreenResult{
					EntityID:  entity.ID,
					Matched:   true,
					Score:     aliasScore,
					ListID:    entry.ListID,
					EntryName: alias,
				}
			}
		}
	}

	return false, ScreenResult{EntityID: entity.ID}
}

// --- In-memory stubs for testing ---

// MemEntitySource is an in-memory EntitySource for testing.
type MemEntitySource struct {
	mu       sync.RWMutex
	entities []types.Entity
	screened map[string]time.Time
}

// NewMemEntitySource creates a MemEntitySource with the given entities.
func NewMemEntitySource(entities []types.Entity) *MemEntitySource {
	return &MemEntitySource{
		entities: entities,
		screened: make(map[string]time.Time),
	}
}

func (m *MemEntitySource) ActiveEntities(_ context.Context, orgID string) ([]types.Entity, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []types.Entity
	for _, e := range m.entities {
		if e.OrgID == orgID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *MemEntitySource) HighRiskEntities(_ context.Context, orgID string, threshold float64) ([]types.Entity, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []types.Entity
	for _, e := range m.entities {
		if e.OrgID == orgID && (e.RiskScore > threshold || e.PEP) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *MemEntitySource) UpdateLastScreened(_ context.Context, entityID string, at time.Time) error {
	m.mu.Lock()
	m.screened[entityID] = at
	m.mu.Unlock()
	return nil
}

func (m *MemEntitySource) LastScreened(entityID string) (time.Time, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.screened[entityID]
	return t, ok
}

// MemAlertSink is an in-memory AlertSink for testing.
type MemAlertSink struct {
	mu     sync.Mutex
	Alerts []types.Alert
	Cases  []types.Case
}

func (m *MemAlertSink) CreateAlert(_ context.Context, a types.Alert) error {
	m.mu.Lock()
	m.Alerts = append(m.Alerts, a)
	m.mu.Unlock()
	return nil
}

func (m *MemAlertSink) CreateCase(_ context.Context, orgID, severity string, alertIDs, entityIDs []string) (*types.Case, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := types.Case{
		ID:        "case-" + orgID,
		OrgID:     orgID,
		Severity:  severity,
		AlertIDs:  alertIDs,
		EntityIDs: entityIDs,
		Status:    types.CaseOpen,
		OpenedAt:  time.Now().UTC(),
	}
	m.Cases = append(m.Cases, c)
	return &c, nil
}
