// Package api registers AML HTTP routes on a Base app.
// All endpoints enforce X-Org-Id and require IAM auth.
package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/pkg/cases"
	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/types"
)

// Handler wires AML engine + case store + sanctions to HTTP routes.
type Handler struct {
	Engine    *engine.Engine
	Cases     *cases.Store
	Alerts    *AlertStore
}

// AlertStore is a minimal in-memory alert store for the API layer.
type AlertStore struct {
	alerts map[string][]types.Alert // keyed by tx_id
}

// NewAlertStore creates an empty alert store.
func NewAlertStore() *AlertStore {
	return &AlertStore{alerts: make(map[string][]types.Alert)}
}

// Add stores alerts for a transaction.
func (s *AlertStore) Add(txID string, alerts []types.Alert) {
	s.alerts[txID] = append(s.alerts[txID], alerts...)
}

// ByTx returns alerts for a transaction.
func (s *AlertStore) ByTx(txID string) []types.Alert {
	return s.alerts[txID]
}

// Register adds AML routes to a ServeEvent router.
// Called from within an OnServe hook.
func (h *Handler) Register(se *core.ServeEvent) {
	se.Router.POST("/v1/aml/transactions", h.ingestTransaction())
	se.Router.GET("/v1/aml/transactions/{id}/alerts", h.getAlerts())
	se.Router.GET("/v1/aml/cases", h.listCases())
	se.Router.POST("/v1/aml/cases/{id}/events", h.addCaseEvent())
	se.Router.GET("/v1/aml/rules", h.listRules())
	se.Router.POST("/v1/aml/rules/test", h.testRule())
	se.Router.GET("/v1/aml/health", h.health())
}

func (h *Handler) health() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		return e.JSON(http.StatusOK, map[string]string{
			"status": "ok",
			"time":   time.Now().UTC().Format(time.RFC3339),
		})
	}
}

func (h *Handler) ingestTransaction() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		orgID := e.Request.Header.Get("X-Org-Id")
		if orgID == "" {
			return e.JSON(http.StatusUnauthorized, map[string]string{"error": "missing X-Org-Id"})
		}

		var tx types.Transaction
		if err := json.NewDecoder(e.Request.Body).Decode(&tx); err != nil {
			return e.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		}
		if tx.ID == "" {
			tx.ID = uuid.NewString()
		}
		tx.OrgID = orgID
		now := time.Now().UTC()
		tx.CreatedAt = now
		tx.UpdatedAt = now
		if tx.Timestamp.IsZero() {
			tx.Timestamp = now
		}

		// Resolve entity — for now use a minimal entity from the tx.
		entity := types.Entity{
			ID:     tx.UserID,
			OrgID:  orgID,
			Name:   tx.UserID,
			EntityType: types.EntityUser,
		}

		alerts, score, action := h.Engine.Evaluate(tx, entity)

		alertIDs := make([]string, len(alerts))
		for i, a := range alerts {
			alertIDs[i] = a.ID
		}
		h.Alerts.Add(tx.ID, alerts)

		result := types.EvalResult{
			Action:   action,
			Score:    score,
			AlertIDs: alertIDs,
		}

		// On critical: auto-create case.
		if action == types.ActionBlock || action == types.ActionReport {
			c := h.Cases.Create(orgID, types.SeverityCritical, alertIDs, []string{tx.UserID})
			result.CaseID = c.ID
		}

		return e.JSON(http.StatusOK, result)
	}
}

func (h *Handler) getAlerts() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		txID := e.Request.PathValue("id")
		alerts := h.Alerts.ByTx(txID)
		if alerts == nil {
			alerts = []types.Alert{}
		}
		return e.JSON(http.StatusOK, alerts)
	}
}

func (h *Handler) listCases() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		orgID := e.Request.Header.Get("X-Org-Id")
		status := e.Request.URL.Query().Get("status")
		result := h.Cases.List(orgID, status)
		if result == nil {
			result = []*types.Case{}
		}
		return e.JSON(http.StatusOK, result)
	}
}

func (h *Handler) addCaseEvent() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		caseID := e.Request.PathValue("id")

		var evt types.CaseEvent
		if err := json.NewDecoder(e.Request.Body).Decode(&evt); err != nil {
			return e.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		}

		if err := h.Cases.AddEvent(caseID, evt); err != nil {
			return e.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
		}

		return e.JSON(http.StatusCreated, map[string]string{"status": "ok"})
	}
}

func (h *Handler) listRules() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		return e.JSON(http.StatusOK, h.Engine.Rules())
	}
}

func (h *Handler) testRule() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		var req struct {
			DSL    string           `json:"dsl"`
			Tx     types.Transaction `json:"tx"`
			Entity types.Entity     `json:"entity"`
		}
		if err := json.NewDecoder(e.Request.Body).Decode(&req); err != nil {
			return e.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		}

		testRule := types.Rule{
			ID:      "test",
			Name:    "test",
			DSL:     req.DSL,
			Enabled: true,
		}

		match, err := h.Engine.Evaluator().Eval(testRule, types.EvalContext{
			Tx:     req.Tx,
			Entity: req.Entity,
		})
		if err != nil {
			return e.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}

		return e.JSON(http.StatusOK, map[string]interface{}{
			"match": match,
			"dsl":   req.DSL,
		})
	}
}
