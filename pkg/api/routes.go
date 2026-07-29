// Package api serves the AML engine over HTTP under /v1/aml.
//
// Every route takes its organisation from the request and scopes to it. That is
// not a convenience: the engine aggregates a customer's history to decide whether
// to report them, and an aggregation that crosses tenants both leaks one
// institution's customers to another and produces a number neither institution
// can account for.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/pkg/cases"
	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/history"
	"github.com/luxfi/aml/pkg/rules"
	"github.com/luxfi/aml/pkg/sanctions"
	"github.com/luxfi/aml/pkg/screen"
	"github.com/luxfi/aml/pkg/types"
)

// Handler serves the engine, the case store and the screening lists.
type Handler struct {
	Engine  *engine.Engine
	Cases   *cases.Store
	Screen  *screen.Store
	History *history.Base
	Rate    engine.Rate
}

// Register mounts the routes.
func (h *Handler) Register(se *core.ServeEvent) {
	se.Router.POST("/v1/aml/transactions", h.ingest())
	se.Router.GET("/v1/aml/cases", h.listCases())
	se.Router.POST("/v1/aml/cases/{id}/events", h.addCaseEvent())
	se.Router.GET("/v1/aml/rules", h.listRules())
	se.Router.POST("/v1/aml/rules/test", h.tryRule())
	se.Router.POST("/v1/aml/sanctions/search", h.searchSanctions())
	se.Router.GET("/v1/aml/catalog", h.catalog())
	se.Router.GET("/v1/aml/health", h.health())
}

// org reads the organisation the request acts for.
func org(e *core.RequestEvent) (string, bool) {
	id := e.Request.Header.Get("X-Org-Id")
	return id, id != ""
}

func fail(e *core.RequestEvent, status int, msg string) error {
	return e.JSON(status, map[string]string{"error": msg})
}

// health reports whether the engine can actually evaluate.
//
// A monitoring system that answers "ok" while its screening lists are empty is
// reporting on its process rather than on its function. This reports the function:
// the readiness of the loaded lists, and how many rules are installed.
func (h *Handler) health() func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		body := map[string]any{
			"time":  time.Now().UTC().Format(time.RFC3339),
			"rules": len(h.Engine.Rules()),
			"terms": h.Engine.Evaluator().Vocabulary(),
		}
		status := http.StatusOK
		if h.Screen == nil {
			body["status"] = "degraded"
			body["screening"] = "no screening store configured"
			status = http.StatusServiceUnavailable
		} else if err := h.Screen.Ready(); err != nil {
			body["status"] = "degraded"
			body["screening"] = err.Error()
			body["lists"] = h.Screen.Sources()
			status = http.StatusServiceUnavailable
		} else {
			body["status"] = "ok"
			body["screening"] = "ready"
			body["lists"] = h.Screen.Sources()
			body["designations"] = h.Screen.Len()
		}
		return e.JSON(status, body)
	}
}

// catalog publishes the detection library with its citations, and the located
// requirements the engine does not meet.
//
// This is the coverage claim in the form a reviewer can check: what each rule
// detects, which requirement it implements, where to read that requirement, and
// what is outstanding. It is generated from the installed rules, so it cannot
// describe a control that is not in force.
func (h *Handler) catalog() func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		installed := h.Engine.Rules()
		entries := make([]map[string]any, 0, len(installed))
		for _, r := range installed {
			entries = append(entries, map[string]any{
				"id":          r.ID,
				"name":        r.Name,
				"typology":    r.Typology,
				"description": r.Description,
				"expression":  r.DSL,
				"severity":    r.Severity,
				"action":      r.Action,
				"enabled":     r.Enabled,
				"citations":   r.Citations,
			})
		}
		return e.JSON(http.StatusOK, map[string]any{
			"typologies":  rules.Typologies(),
			"rules":       entries,
			"obligations": rules.Obligations(),
			"gaps":        rules.Gaps(),
		})
	}
}

// ingest evaluates a transaction and records it.
func (h *Handler) ingest() func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		orgID, ok := org(e)
		if !ok {
			return fail(e, http.StatusUnauthorized, "missing X-Org-Id")
		}

		var tx types.Transaction
		if err := json.NewDecoder(e.Request.Body).Decode(&tx); err != nil {
			return fail(e, http.StatusBadRequest, "invalid request body")
		}
		if tx.UserID == "" {
			return fail(e, http.StatusBadRequest, "user_id is required: the engine aggregates a customer's history and cannot do so for an unnamed customer")
		}
		if tx.ID == "" {
			tx.ID = uuid.NewString()
		}
		// The organisation comes from the request, never from the body, so a caller
		// cannot write a transaction into another tenant's history.
		tx.OrgID = orgID
		now := time.Now().UTC()
		tx.CreatedAt, tx.UpdatedAt = now, now
		if tx.Timestamp.IsZero() {
			tx.Timestamp = now
		}

		entity := types.Entity{
			ID:           tx.UserID,
			OrgID:        orgID,
			EntityType:   types.EntityUser,
			Name:         tx.CustomerName,
			Jurisdiction: tx.CustomerJurisdiction,
		}

		alerts, score, action := h.Engine.Evaluate(e.Request.Context(), tx, entity)

		// The transaction is recorded whatever the verdict, and recorded after
		// evaluation so it does not count towards the window used to judge it. A
		// blocked transaction is still part of the customer's pattern, and the
		// standards require attempted transactions to be reportable.
		if h.History != nil {
			usd := tx.Notional
			if h.Rate != nil {
				if converted, err := h.Rate.USD(e.Request.Context(), tx.Notional, tx.Currency); err == nil {
					usd = converted
				}
			}
			if err := h.History.Append(e.Request.Context(), orgID, history.Event{
				ID: tx.ID, At: tx.Timestamp, USD: usd, Currency: tx.Currency,
				Direction: tx.Direction, User: tx.UserID, Account: tx.AccountID,
				Counterparty: tx.Counterparty, Device: tx.DeviceFingerprint,
				Address: tx.IPAddress, Jurisdiction: tx.CustomerJurisdiction, Symbol: tx.Symbol,
			}); err != nil {
				return fail(e, http.StatusInternalServerError, "could not record the transaction: "+err.Error())
			}
		}

		result := types.EvalResult{Action: action, Score: score}
		result.AlertIDs = make([]string, 0, len(alerts))
		for _, a := range alerts {
			result.AlertIDs = append(result.AlertIDs, a.ID)
		}

		// A case is opened whenever a person has to look, which includes review, and
		// includes the case where a rule could not reach a verdict.
		if action == types.ActionBlock || action == types.ActionReport || action == types.ActionReview {
			severity := types.SeverityMedium
			for _, a := range alerts {
				if a.Severity == types.SeverityCritical {
					severity = types.SeverityCritical
					break
				}
				if a.Severity == types.SeverityHigh {
					severity = types.SeverityHigh
				}
			}
			c := h.Cases.Create(orgID, severity, result.AlertIDs, []string{tx.UserID})
			result.CaseID = c.ID
		}

		return e.JSON(http.StatusOK, result)
	}
}

func (h *Handler) listCases() func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		orgID, ok := org(e)
		if !ok {
			return fail(e, http.StatusUnauthorized, "missing X-Org-Id")
		}
		out := h.Cases.List(orgID, e.Request.URL.Query().Get("status"))
		if out == nil {
			out = []*types.Case{}
		}
		return e.JSON(http.StatusOK, out)
	}
}

// addCaseEvent appends to a case timeline.
//
// The case is checked to belong to the requesting organisation before it is
// touched. Without that check the identifier alone is the authorisation, and a
// case identifier is not a secret — it is returned to whoever submitted the
// transaction.
func (h *Handler) addCaseEvent() func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		orgID, ok := org(e)
		if !ok {
			return fail(e, http.StatusUnauthorized, "missing X-Org-Id")
		}
		id := e.Request.PathValue("id")

		var evt types.CaseEvent
		if err := json.NewDecoder(e.Request.Body).Decode(&evt); err != nil {
			return fail(e, http.StatusBadRequest, "invalid request body")
		}

		if err := h.Cases.AddEventFor(orgID, id, evt); err != nil {
			if errors.Is(err, cases.ErrNotFound) {
				return fail(e, http.StatusNotFound, "case not found")
			}
			return fail(e, http.StatusBadRequest, err.Error())
		}
		return e.JSON(http.StatusCreated, map[string]string{"status": "recorded"})
	}
}

func (h *Handler) listRules() func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		if _, ok := org(e); !ok {
			return fail(e, http.StatusUnauthorized, "missing X-Org-Id")
		}
		return e.JSON(http.StatusOK, h.Engine.Rules())
	}
}

// maxExpression bounds a submitted rule expression.
const maxExpression = 2048

// tryRule admits a candidate expression and evaluates it against a supplied
// transaction, so an author finds out that a rule cannot be answered before it is
// installed rather than by watching it never fire.
func (h *Handler) tryRule() func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		orgID, ok := org(e)
		if !ok {
			return fail(e, http.StatusUnauthorized, "missing X-Org-Id")
		}

		var req struct {
			Expression string            `json:"expression"`
			Tx         types.Transaction `json:"tx"`
			Entity     types.Entity      `json:"entity"`
		}
		if err := json.NewDecoder(e.Request.Body).Decode(&req); err != nil {
			return fail(e, http.StatusBadRequest, "invalid request body")
		}
		if len(req.Expression) > maxExpression {
			return fail(e, http.StatusBadRequest, "expression exceeds the maximum length")
		}
		req.Tx.OrgID = orgID
		req.Entity.OrgID = orgID

		candidate := types.Rule{ID: "candidate", Name: "candidate", DSL: req.Expression, Enabled: true}
		if _, err := h.Engine.Evaluator().Admit(candidate); err != nil {
			return e.JSON(http.StatusBadRequest, map[string]any{
				"admitted": false,
				"error":    err.Error(),
				"terms":    h.Engine.Evaluator().Vocabulary(),
			})
		}

		match, err := h.Engine.Evaluator().Eval(e.Request.Context(), candidate, req.Tx, req.Entity)
		if err != nil {
			return e.JSON(http.StatusOK, map[string]any{
				"admitted": true,
				"match":    false,
				"error":    err.Error(),
			})
		}
		return e.JSON(http.StatusOK, map[string]any{"admitted": true, "match": match})
	}
}

// searchSanctions screens a name against the loaded lists.
func (h *Handler) searchSanctions() func(*core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		if _, ok := org(e); !ok {
			return fail(e, http.StatusUnauthorized, "missing X-Org-Id")
		}

		var req struct {
			Name        string `json:"name"`
			Birth       string `json:"birth"`
			Nationality string `json:"nationality"`
			Kind        string `json:"kind"`
			Address     string `json:"address"`
		}
		if err := json.NewDecoder(e.Request.Body).Decode(&req); err != nil {
			return fail(e, http.StatusBadRequest, "invalid request body")
		}
		if h.Screen == nil {
			return fail(e, http.StatusServiceUnavailable, "no screening store configured")
		}
		if req.Name == "" && req.Address == "" {
			return fail(e, http.StatusBadRequest, "name or address is required")
		}

		var matches []sanctions.Match
		var err error
		if req.Address != "" {
			matches, err = h.Screen.Chain(req.Address)
		} else {
			// The date of birth and nationality are passed through, because they are
			// what allows a namesake to be cleared. Screening on a name alone is
			// supported and reports no corroboration, which the result states.
			matches, err = h.Screen.Search(sanctions.Query{
				Name:        req.Name,
				Birth:       sanctions.Birth{From: req.Birth, To: req.Birth},
				Nationality: req.Nationality,
				Kind:        req.Kind,
			}, sanctions.Threshold)
		}
		if err != nil {
			// A screening question that cannot be answered is an error, never an
			// empty result. An empty result is indistinguishable from a clean one.
			return fail(e, http.StatusServiceUnavailable, err.Error())
		}
		if matches == nil {
			matches = []sanctions.Match{}
		}
		return e.JSON(http.StatusOK, matches)
	}
}
