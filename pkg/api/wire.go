// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

// The one assembly of this engine over one store.
//
// Everything a transaction touches is built here, in one function, and cmd/amld
// and every test in this package call it. That is not tidiness — it is the fix
// for the defect that produced the last review.
//
// A monitoring engine is a dozen planes joined to one ingest path. When the
// deployment joins them in one place and the tests join them in another, the two
// drift, and the drift is invisible: the suite is green because it is proving
// properties of the arrangement it built. The record fingerprint was written to a
// struct field on a shelf the tests wired and never to a column on the shelf
// cmd/amld wires, so every retry conflicted permanently in production while every
// test passed. The bug was not in the ledger. It was that there were two wirings.
//
// So there is one, and a test reads cmd/amld to keep it that way. A plane added
// here is wired for the deployment and for the suite at the same moment; a plane
// not added here does not exist for either.

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/base/core"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/cases"
	"github.com/luxfi/aml/pkg/dictionary"
	"github.com/luxfi/aml/pkg/engine"
	"github.com/luxfi/aml/pkg/history"
	"github.com/luxfi/aml/pkg/lists"
	"github.com/luxfi/aml/pkg/models"
	"github.com/luxfi/aml/pkg/receipt"
	"github.com/luxfi/aml/pkg/reference"
	"github.com/luxfi/aml/pkg/retention"
	"github.com/luxfi/aml/pkg/screen"
	"github.com/luxfi/aml/pkg/suppress"
	"github.com/luxfi/aml/pkg/token"
	"github.com/luxfi/aml/pkg/topology"
	"github.com/luxfi/aml/pkg/types"
	"github.com/luxfi/aml/pkg/velocity"
	"github.com/luxfi/aml/pkg/watch"
)

// Deployment is what differs between two installations of this engine.
//
// Everything NOT here — which store each plane sits on, which planes exist at
// all, how they are joined, what runs on a cadence — is the same everywhere and
// is [Wire]'s. A field belongs here only if a deployment genuinely answers it
// differently, and each one below does: who a caller is, which IAM application
// this is, what the institution detects, the key material it holds, the
// designations it screens against, its business day, and its rates.
type Deployment struct {
	// Identity resolves which tenant a request is authenticated to act on, and
	// which subject is deciding. cmd/amld supplies IAMIdentity; a test supplies a
	// fixed caller.
	Identity Identity
	// ClientID is this deployment's IAM application: the audience every token is
	// pinned to, and what the console reads its own identity from.
	ClientID string
	// Rules is the detection library. Installing it is what checks that every
	// rule's evidence exists, so it happens after the providers are attached and
	// a failure refuses the whole assembly.
	Rules []types.Rule
	// Keys is the tokenisation keyring over the KMS-held root.
	Keys *token.Keyring
	// Screen is the designation store, and Readiness is which publisher is behind
	// it. They are the deployment's because something outside this assembly fills
	// them on a cadence.
	Screen    *screen.Store
	Readiness *screen.Readiness
	// Zone is the business day calendar rules are answered in. Nil means UTC.
	Zone *time.Location
	// Rate converts an amount to the unit thresholds are stated in.
	Rate reference.Rates
	// Jurisdictions is the sanctioned-jurisdiction reference the rules read.
	Jurisdictions reference.Jurisdictions
	// Live lets the behavioural model contribute to a verdict.
	//
	// It is stated from the LIVE side so that the zero value is shadow. A
	// detection has to be testable before it is activated: a new deployment
	// scores, learns and publishes at GET /v1/aml/anomaly what it WOULD have
	// alerted on, and contributes nothing to any transaction's outcome until
	// somebody has read that. Written as `Shadow bool` the same field would arm
	// the model for anyone who did not think about it, which is the one direction
	// a default must never fail in.
	Live bool
	// Limit is the reporting limit transactions are judged against. Zero takes
	// the fallback.
	Limit float64
}

// Wire assembles the whole engine over one store.
//
// The order is the design. The collections exist before any shelf opens over
// them; the shelves exist before the joins; the engine is built over the evidence
// providers; the rule library is installed LAST, because installing a rule is
// what checks that its evidence exists and that check is worth nothing if it runs
// before the providers are attached.
//
// It returns a Handler ready to Register, and it has already put on the app
// everything that has to run on a cadence for the durable state to stay honest.
func Wire(app core.App, d Deployment) (*Handler, error) {
	// Every collection this engine writes to, created idempotently, in one list.
	// A shelf whose column does not exist does not fail: it reads back a zero
	// value, which is a plausible answer, which is how a record fingerprint came
	// to be a struct field nothing stored.
	for _, ensure := range []func(core.App) error{
		history.Ensure, retention.Ensure, receipt.Ensure, cases.Ensure, EnsureAlerts,
		lists.Ensure, suppress.Ensure, watch.Ensure, dictionary.Ensure, models.Ensure,
	} {
		if err := ensure(app); err != nil {
			return nil, fmt.Errorf("a record plane cannot be created: %w", err)
		}
	}

	events := history.NewBase(app)
	deny := lists.NewBase(app)
	silence := suppress.NewBase(app)
	monitor := watch.NewBase(app)
	monitor.Cover = silence
	catalog := dictionary.NewBase(app)

	// The behavioural plane. Sliding aggregates are the substrate every
	// behavioural measure reads; the model reads them to score whether a
	// transaction is unusual for the entity that made it.
	windows := velocity.New(velocity.Config{})
	model, err := anomaly.New(anomaly.Config{Shadow: !d.Live}, windows)
	if err != nil {
		return nil, fmt.Errorf("the behavioural plane cannot be built: %w", err)
	}

	// Half the machine, at most, for every study and every replay together — the
	// other half is ingest's, and a transaction that cannot be recorded cannot be
	// processed. ONE budget for the process, because the CPU is one machine
	// however many planes ask for it. See topology.Budget.
	cores := topology.NewBudget(0)

	study := models.NewBase(app)
	study.Model = model
	study.Cores = cores
	// A model planted after a rollout or an admission asks the model plane what
	// this tenant last adopted, so an adopted control cannot go quiet without
	// somebody deciding it should.
	model.SetAdopted(study.Adopted)

	eng := engine.New(engine.Providers{
		History:   events,
		Lists:     deny,
		Screen:    d.Screen,
		Reference: d.Jurisdictions,
		Rate:      d.Rate,
		Zone:      d.Zone,
	})
	eng.SetScorer(model)
	if err := eng.SetRules(d.Rules); err != nil {
		return nil, fmt.Errorf("the detection library cannot be installed: %w", err)
	}

	h := &Handler{
		Identity:  d.Identity,
		ClientID:  d.ClientID,
		Engine:    eng,
		Cases:     cases.NewBase(app),
		Alerts:    NewAlertStoreBase(app),
		Screen:    d.Screen,
		Readiness: d.Readiness,
		History:   events,
		Rate:      d.Rate,
		Records:   retention.NewBase(app),
		Receipts:  receipt.NewBase(app),
		Keys:      d.Keys,
		Velocity:  windows,
		Anomaly:   model,
		Cores:     cores,
		Limit:     d.limit(),
		Planes: Planes{
			Lists: deny, Suppress: silence, Watch: monitor,
			Dictionary: catalog, Models: study,
		},
	}
	// The model plane reads a tenant's history through the same join a rule replay
	// uses, so a study of the model and a study of a rule see the same events.
	study.History = Replayed{H: h}

	tend(app, h, catalog)
	return h, nil
}

// limit is the reporting limit this deployment states, or the fallback.
func (d Deployment) limit() float64 {
	if d.Limit > 0 {
		return d.Limit
	}
	return reportLimit
}

// tend puts on the app everything that has to run on a cadence for the durable
// state to stay honest.
//
// It is part of the assembly and not the caller's, for the same reason the
// collections are: a flush nobody runs is a statistic that only ever reads as
// pending, and a disposal nobody runs is a ledger that grows forever. Both are
// properties of what was wired, so both are wired here.
func tend(app core.App, h *Handler, catalog *dictionary.Shelf) {
	// The field catalog accumulates in memory and is written on a cadence and at
	// shutdown.
	flush := func() {
		if err := catalog.Flush(context.Background()); err != nil {
			app.Logger().Error("field catalog flush failed", "error", err)
		}
	}
	app.Cron().Add("dictionary-flush", "*/5 * * * *", flush)
	app.OnTerminate().BindFunc(func(te *core.TerminateEvent) error {
		flush()
		return te.Next()
	})

	// Destroy records whose retention period has run out, daily.
	retention.Cron(app, h.Records)

	// And drop the receipts that can no longer affect an aggregate. It runs at a
	// different hour from the retention disposal so two bulk deletes do not meet
	// on one SQLite writer.
	app.Cron().Add("receipt-dispose", "0 4 * * *", func() {
		n, err := h.Receipts.Dispose(context.Background(), time.Now().UTC())
		if err != nil {
			app.Logger().Error("receipt disposal failed", "error", err)
			return
		}
		app.Logger().Info("receipts disposed", "count", n)
	})
}
