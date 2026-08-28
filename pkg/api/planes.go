// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

// The five record planes this file puts on the router, and the one place ingest
// meets them.
//
// Every route here is `post(h, <plane>.<Op>)` or `get(h, <plane>.<Op>)` and
// nothing else. There is no handler with a body: the operation is the contract,
// the adapter is in typed.go, and this file is a routing table. That is what makes
// the cloud mount a wrap of the same functions rather than a second
// implementation of them — a route that unpacked fields here would have to be
// unpacked again there, differently, and the difference is what a customer finds.

import (
	"context"
	"log"

	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/pkg/dictionary"
	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/lists"
	"github.com/luxfi/aml/pkg/models"
	"github.com/luxfi/aml/pkg/replay"
	"github.com/luxfi/aml/pkg/suppress"
	"github.com/luxfi/aml/pkg/topology"
	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/watch"
)

// Planes are the five record planes the Handler serves alongside the engine.
//
// Each is optional and each is nil-checked at registration, so a deployment that
// has not wired one serves no route for it — which is the honest state. A route
// that answered an empty list for a plane nobody wired would report an
// institution with no deny lists and no suppressions, and that is exactly the
// reading a reviewer must never be handed by accident.
type Planes struct {
	Lists      *lists.Shelf
	Suppress   *suppress.Shelf
	Watch      *watch.Shelf
	Dictionary *dictionary.Shelf
	Models     *models.Shelf
}

// registerPlanes puts the planes on the router. Called from Register.
func (h *Handler) registerPlanes(se *core.ServeEvent) {
	if l := h.Planes.Lists; l != nil {
		se.Router.GET("/v1/aml/lists", get(h, l.Catalog))
		se.Router.POST("/v1/aml/lists", post(h, l.Declare, true))
		se.Router.GET("/v1/aml/lists/{name}/entries", get(h, l.Entries))
		se.Router.POST("/v1/aml/lists/{name}/entries", post(h, l.Add, false))
		se.Router.POST("/v1/aml/lists/{name}/entries/remove", post(h, l.Remove, false))
		se.Router.GET("/v1/aml/lists/{name}/lookup", get(h, l.Lookup))
	}
	if s := h.Planes.Suppress; s != nil {
		se.Router.GET("/v1/aml/suppressions", get(h, s.Ledger))
		se.Router.POST("/v1/aml/suppressions", post(h, s.Suppress, true))
		se.Router.POST("/v1/aml/suppressions/{id}/lift", post(h, s.Lift, false))
	}
	if w := h.Planes.Watch; w != nil {
		se.Router.GET("/v1/aml/activations", get(h, w.Feed))
		se.Router.POST("/v1/aml/activations", post(h, w.Record, true))
		// A fold over a window, not a page: it examines up to MaxExamined rows
		// however many the caller asked for, so it is admitted one per tenant and
		// takes the machine's budget while it runs. See costly in gate.go.
		se.Router.GET("/v1/aml/activations/rates", get(h, one(&h.folds, costly(h.Cores, w.Rates))))
		se.Router.GET("/v1/aml/rungs", get(h, w.Ladder))
		se.Router.POST("/v1/aml/rungs", post(h, w.Declare, true))
		se.Router.POST("/v1/aml/rungs/{id}/retire", post(h, w.Retire, false))
	}
	if d := h.Planes.Dictionary; d != nil {
		se.Router.GET("/v1/aml/dictionary", get(h, d.Catalog))
	}
	if m := h.Planes.Models; m != nil {
		// The two that cost the machine are admitted one per tenant and bounded in
		// time. A search replays a tenant's history through up to MaxTrials
		// candidate detectors and a fit replays it through one; either is minutes
		// of pure arithmetic asked for by one request, on a single-replica engine
		// that also has to answer ingest. See gate.go, and topology.Budget for the
		// share of the machine every study together may take.
		se.Router.GET("/v1/aml/models/runs", get(h, m.Runs))
		se.Router.POST("/v1/aml/models/runs", post(h, one(&h.studies, within(maxStudy, m.Search)), true))
		se.Router.GET("/v1/aml/models/runs/{id}", get(h, m.Run))
		se.Router.GET("/v1/aml/models/fits", get(h, m.Fits))
		se.Router.POST("/v1/aml/models/fits", post(h, one(&h.studies, within(maxStudy, m.Fit)), true))
		se.Router.POST("/v1/aml/models/fits/{id}/adopt", post(h, m.Adopt, false))
	}
}

// monitor records what the rules did to a transaction and returns the response
// the monitoring plane concluded.
//
// This is the one place suppression and elevation reach an outcome. Both are
// declared decisions of the institution — a suppression carries a reason and a
// decider, a rung carries both as well — so the transaction's action is the
// strongest RESPONSE across the activations, not the strongest action the rules
// asked for. That difference is the whole of what suppression does, and it is
// visible on every row: Action is what the rule wanted, Response is what happened.
//
// A failure to record is a refusal to answer, on the same terms as a failure to
// retain: a control whose activations are not being recorded cannot be shown to
// have run, and processing a transaction it cannot account for is worse than
// declining it.
func (h *Handler) monitor(ctx context.Context, org string, tx types.Transaction, alerts []types.Alert, action string) (string, error) {
	w := h.Planes.Watch
	if w == nil || len(alerts) == 0 {
		return action, nil
	}
	// The subject the activation is about: the account the aggregates are kept
	// under, falling back to the user, which is the same choice the model makes.
	subject := watch.Subject{Kind: "account", Value: tx.AccountID}
	if subject.Value == "" {
		subject = watch.Subject{Kind: "user", Value: tx.UserID}
	}
	if subject.Value == "" {
		// Nothing to attribute the activations to. The rules' own verdict stands;
		// recording an activation against an empty subject would pool every
		// anonymous transaction in the tenant under one imaginary customer, which
		// is the failure anomaly refuses by name.
		return action, nil
	}

	best := types.ActionAllow
	for _, a := range alerts {
		rec, err := w.Record(ctx, org, &watch.RecordIn{
			Rule: a.RuleID, RuleName: a.RuleName, Typology: a.Typology,
			Severity: a.Severity, Action: a.ActionTaken, Score: a.Score,
			Tx: tx.ID, Subject: subject, At: tx.Timestamp,
		})
		if err != nil {
			return "", err
		}
		if types.ActionRank(rec.Response) > types.ActionRank(best) {
			best = rec.Response
		}
	}
	return best, nil
}

// observe offers a transaction to the field catalog.
//
// A failure is logged and nothing else. The catalog is a diagnostic — nothing is
// decided on it — so a statistic that could not be taken must not be able to
// refuse a payment. That is the opposite of the rule for every record plane
// above, and the difference is exactly whether an obligation rests on it.
func (h *Handler) observe(org string, tx types.Transaction) {
	if d := h.Planes.Dictionary; d != nil {
		if err := d.Observe(org, tx); err != nil {
			log.Printf("[aml] dictionary: %v", err)
		}
	}
}

// Replayed hands a tenant's retained transactions to a model search.
//
// It is the models plane's Source, satisfied by the SAME join that answers a rule
// replay — one history seam, so a study of the model and a study of a rule see
// the same events and their answers can be held against each other.
type Replayed struct{ H *Handler }

// History returns this tenant's replayable events, opened under the monitoring
// purpose. Testing a shape before activation is monitoring work and not a
// commercial read (AMLD4 Art. 41(2)), which is what entitles this read to the
// sealed bodies at all.
func (r Replayed) History(ctx context.Context, org string, held *topology.Grant) (replay.History, error) {
	vault, err := r.H.vault(org)
	if err != nil {
		return nil, err
	}
	return r.H.history(held, org, vault)
}

// Compiled hands the engine's own compiler to a rule replay.
//
// The sandbox reaches the engine through a one-method interface, so it has
// nothing it could write to and the candidate is judged by the code that judges
// production. Go's interfaces are structural but not covariant in a return type,
// so the join is one line — and it is here, at the composition root beside
// Replayed, rather than in either package.
type Compiled struct{ E *engine.Evaluator }

// Ready compiles a candidate rule set for the length of one replay.
func (c Compiled) Ready(rules []types.Rule) (replay.Ruleset, error) { return c.E.Ready(rules) }
