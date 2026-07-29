package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/types"
)

// anomalyState reports the model a governance review has to be able to read: the
// appetite it was given, the mapping from typologies to the features it reads, the
// threshold that follows from the appetite, the rate it actually achieved against
// the rate it was asked for, what it declined to score and why, and the sample
// retained below the line so that what it missed can be measured.
//
// A bought-in system does not transfer responsibility: the firm has to understand
// how the scoring works and how it weights what it reads, and demonstrate that the
// scores reflect its own understanding of risk. This endpoint is where that is
// demonstrated from, so everything needed is here and nothing needed is only in a
// document.
//
// Scoped to the caller's tenant. One tenant's alert rate and volumes are not
// another's to read.
func (h *Handler) anomalyState() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		orgID, err := h.tenant(e)
		if err != nil {
			return refuse(e, err)
		}
		if h.Anomaly == nil {
			return e.JSON(http.StatusOK, map[string]any{
				"enabled": false,
				"reason":  "no model configured; transactions are evaluated on rules alone",
			})
		}
		return e.JSON(http.StatusOK, struct {
			Enabled bool `json:"enabled"`
			anomaly.State
			// Faults is how often the engine discarded the model's evidence. A
			// model that faults on everything contributes nothing while the
			// engine keeps answering, so the number belongs next to the rest.
			Faults int64 `json:"faults"`
		}{
			Enabled: true,
			State:   h.Anomaly.State(orgID),
			Faults:  h.Engine.ScorerFaults(),
		})
	}
}

// anomalyTest tries a candidate transaction against the tenant's model without
// learning from it, moving a counter, or touching the aggregates. It is the
// model's analogue of testing a rule expression: detection has to be testable
// before it is activated, and a score nobody can reproduce on a case they choose
// is not one anyone can be asked to rely on.
//
// The answer carries the full attribution and every coordinate the model read,
// including the ones that contributed nothing — what it looked at is as much part
// of the account as what it concluded.
func (h *Handler) anomalyTest() func(e *core.RequestEvent) error {
	return func(e *core.RequestEvent) error {
		orgID, err := h.tenant(e)
		if err != nil {
			return refuse(e, err)
		}
		if h.Anomaly == nil {
			return fail(e, http.StatusNotFound, "no model configured")
		}

		var in struct {
			Tx     types.Transaction `json:"tx"`
			Entity types.Entity      `json:"entity"`
		}
		if err := json.NewDecoder(e.Request.Body).Decode(&in); err != nil {
			return fail(e, http.StatusBadRequest, "invalid request body")
		}

		// The tenant comes from the credential, never from the body, or a caller
		// could score against another tenant's model and read its behaviour back
		// out of the answer one probe at a time.
		tx := in.Tx
		tx.OrgID = orgID
		tx.USD = engine.USD(tx.Notional, tx.Currency)
		if tx.Timestamp.IsZero() {
			tx.Timestamp = time.Now().UTC()
		}
		entity := in.Entity
		entity.OrgID = orgID

		return e.JSON(http.StatusOK, h.Anomaly.Inspect(tx, entity))
	}
}
