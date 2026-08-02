// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

import (
	"encoding/json"
	"fmt"

	"github.com/hanzoai/base/core"
	"github.com/hanzoai/dbx"

	"github.com/luxfi/aml/pkg/store"
	"github.com/luxfi/aml/pkg/types"
)

// Alert field names. The body is the alert itself, as JSON.
//
// One column rather than a column per field, because an alert is only ever read
// whole: the routes hand back the alerts for a transaction, and the replay reads
// which rules fired. Nothing filters on a rule's score or its citations, so
// nothing gains an index by being one, and a column per field is a second place
// for the shape of an alert to be declared.
const (
	fieldTx        = "tx_id"
	fieldAlert     = "alert_id"
	fieldRaised    = "raised"
	fieldAlertBody = "body"
)

// alertKind is where alerts live.
var alertKind = store.Kind{
	Name: "aml_alerts",
	Fields: []core.Field{
		&core.TextField{Name: fieldTx, Required: true},
		&core.TextField{Name: fieldAlert, Required: true},
		&core.DateField{Name: fieldRaised, Required: true},
		&core.JSONField{Name: fieldAlertBody, Required: true},
	},
	Indexes: []store.Index{
		// The read: this tenant's alerts on this transaction.
		{Name: "tx", Fields: []string{store.Org, fieldTx, fieldRaised}},
		// One row per alert. A retried ingest must not double the evidence.
		{Name: "alert", Fields: []string{store.Org, fieldAlert}, Unique: true},
	},
}

// EnsureAlerts creates what alerts are kept in. Idempotent, so it runs on every
// start.
func EnsureAlerts(app core.App) error { return alertKind.Ensure(app) }

// NewAlertStoreBase returns an alert store that survives a restart.
// [EnsureAlerts] has to have run first.
//
// An alert is what a rule said about a transaction at the time it was judged.
// The case that cites it and the record it was raised against are both durable
// now, so an alert store that empties on restart leaves a case naming evidence
// that is no longer there — and the replay that measures a rule's false-positive
// rate reads the same alerts to know which rules actually fired.
func NewAlertStoreBase(app core.App) *AlertStore {
	return &AlertStore{app: app, alerts: nil, maxItems: DefaultMaxAlerts}
}

// add writes alerts to the durable shelf.
//
// What a rule concluded about a transaction is a property of the transaction, so
// a tenant that already has alerts on one has already judged it and the evidence
// on record stands. Re-evaluating would mint fresh alert ids and file a second
// copy of one judgement — which a case then cites twice and a replay counts
// twice. It matters because the answer to an offer is kept AFTER these writes: a
// process that dies in between leaves the alerts filed and the caller
// unanswered, and the caller offers again.
//
// Nothing is deleted and nothing is overwritten. The first judgement is the one
// the institution made.
func (s *AlertStore) add(txID string, alerts []types.Alert) error {
	if len(alerts) == 0 {
		return nil
	}
	held, err := alertKind.Find(s.app, alerts[0].OrgID, fieldTx+" = {:tx}", "", 1, dbx.Params{"tx": txID})
	if err != nil {
		return fmt.Errorf("alerts: read %s: %w", txID, err)
	}
	if len(held) > 0 {
		return nil
	}
	for _, a := range alerts {
		body, err := json.Marshal(a)
		if err != nil {
			return err
		}
		r, err := alertKind.New(s.app, a.OrgID)
		if err != nil {
			return err
		}
		r.Set(fieldTx, txID)
		r.Set(fieldAlert, a.ID)
		r.Set(fieldRaised, a.CreatedAt)
		r.Set(fieldAlertBody, string(body))
		if err := s.app.Save(r); err != nil {
			return fmt.Errorf("alerts: save %s: %w", a.ID, err)
		}
	}
	return nil
}

// judged reports whether this tenant has already recorded alerts on a
// transaction, which is the question "has this transaction been judged before".
func (s *AlertStore) judged(org, txID string) bool {
	held, err := alertKind.Find(s.app, org, fieldTx+" = {:tx}", "", 1, dbx.Params{"tx": txID})
	return err == nil && len(held) > 0
}

// byTx reads a tenant's alerts for a transaction from the durable shelf.
func (s *AlertStore) byTx(org, txID string) []types.Alert {
	found, err := alertKind.Find(s.app, org, fieldTx+" = {:tx}", fieldRaised, 0, dbx.Params{"tx": txID})
	if err != nil {
		return nil
	}
	out := make([]types.Alert, 0, len(found))
	for _, r := range found {
		var a types.Alert
		if err := json.Unmarshal([]byte(r.GetString(fieldAlertBody)), &a); err != nil {
			continue
		}
		out = append(out, a)
	}
	return out
}
