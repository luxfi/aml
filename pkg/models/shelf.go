// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package models

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hanzoai/base/core"
	"github.com/hanzoai/dbx"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/brand"
	"github.com/luxfi/aml/pkg/replay"
	"github.com/luxfi/aml/pkg/store"
	"github.com/luxfi/aml/pkg/topology"
)

const (
	fieldAt      = "at"
	fieldBy      = "by"
	fieldSpace   = "space"
	fieldOptions = "options"
	fieldReport  = "report"
	fieldTrials  = "trials"
	fieldEvents  = "events"
	fieldJudged  = "judged"
	fieldWinner  = "winner"
	fieldRefusal = "refusal"
	fieldElapsed = "elapsed"

	fieldTopology = "topology"
	fieldDigest   = "digest"
	fieldTrial    = "trial"
	fieldState    = "state"
	fieldAdopted  = "adopted"
	fieldAdoptBy  = "adopted_by"
	fieldAdoptWhy = "adopt_why"
)

var runKind = store.Kind{
	Name: "aml_model_runs",
	Fields: []core.Field{
		&core.DateField{Name: fieldAt, Required: true},
		&core.TextField{Name: fieldBy, Required: true},
		&core.JSONField{Name: fieldSpace, Required: true},
		&core.JSONField{Name: fieldOptions},
		&core.JSONField{Name: fieldReport, Required: true},
		// The summary columns. Denormalised deliberately: a list of runs must not
		// read a report per row, and a report is the one thing here that is large.
		&core.NumberField{Name: fieldTrials},
		&core.NumberField{Name: fieldEvents},
		&core.NumberField{Name: fieldJudged},
		&core.NumberField{Name: fieldElapsed},
		&core.JSONField{Name: fieldWinner},
		&core.TextField{Name: fieldRefusal},
	},
	Indexes: []store.Index{
		{Name: "at", Fields: []string{store.Org, fieldAt}},
	},
}

var fitKind = store.Kind{
	Name: "aml_model_fits",
	Fields: []core.Field{
		&core.DateField{Name: fieldAt, Required: true},
		&core.TextField{Name: fieldBy, Required: true},
		&core.NumberField{Name: fieldElapsed},
		&core.JSONField{Name: fieldTopology, Required: true},
		&core.TextField{Name: fieldDigest, Required: true},
		&core.JSONField{Name: fieldTrial, Required: true},
		// The learned state. It is written and read here and never leaves on an
		// answer — see the package doc.
		&core.JSONField{Name: fieldState, Required: true},
		&core.DateField{Name: fieldAdopted},
		&core.TextField{Name: fieldAdoptBy},
		&core.TextField{Name: fieldAdoptWhy},
	},
	Indexes: []store.Index{
		{Name: "at", Fields: []string{store.Org, fieldAt}},
		{Name: "digest", Fields: []string{store.Org, fieldDigest}},
		// The reload: this tenant's adopted fits, most recent first. A model that
		// is planted after a rollout or an eviction asks this index what it was
		// last told to be — see Adopted.
		{Name: "adopted", Fields: []string{store.Org, fieldAdopted}},
	},
}

// Ensure creates what runs and fits are kept in. Idempotent, so it runs on every
// start.
func Ensure(app core.App) error {
	if err := runKind.Ensure(app); err != nil {
		return err
	}
	return fitKind.Ensure(app)
}

// Source hands out a tenant's replayable history.
//
// One method, and it takes the tenant, so this package cannot reach history any
// other way — the same seam pkg/replay uses, for the same reason.
type Source interface {
	// History is this tenant's replayable events. held is what the CALLER took
	// from the machine's budget before asking, and it is an argument rather than
	// an assumption because reading a history is the expensive half: a token
	// taken after the read bounds the arithmetic and nothing else. A source that
	// materialises records refuses a nil grant.
	History(ctx context.Context, org string, held *topology.Grant) (replay.History, error)
}

// Shelf is the durable model plane.
type Shelf struct {
	app core.App
	// History is where a search and a fit read a tenant's events from. Without it
	// both refuse: a search over no history would report that every shape alerts
	// on nothing, which is what a good shape and a broken store look like alike.
	History Source
	// Model is the live detector. Only Adopt touches it, and only to restore.
	Model *anomaly.Store
	// Cores is the share of the machine every study together may take
	// (topology.Budget). Nil is unbounded and is for tests; a deployment wires
	// one, because a search that takes every core takes the cores ingest needs.
	Cores *topology.Budget
	// Now supplies the instant. Tests set it.
	Now func() time.Time
}

// NewBase returns the durable shelf. Ensure has to have run first.
func NewBase(app core.App) *Shelf { return &Shelf{app: app} }

func (s *Shelf) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Search runs a search over this tenant's history and keeps it.
//
// The run is written before it is returned. A study whose conclusion reached a
// screen and not the store is one nobody can produce when asked why the model
// looks the way it does.
func (s *Shelf) Search(ctx context.Context, org string, in *SearchIn) (*Run, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	if in.By.Trim() == "" {
		return nil, ErrDecider
	}
	if s.History == nil {
		return nil, ErrNoHistory
	}

	// The machine, before the history.
	//
	// Reading a tenant's history is the expensive half: up to a hundred thousand
	// retained records, each sealed body opened and unmarshalled. A token taken
	// after that read bounds the arithmetic and nothing else, so every tenant
	// waiting for a worker was holding a whole history while it waited. The hold
	// is taken HERE, in the caller that reads, and handed to the callee that
	// computes. topology.Width is pure, so the width — and the validity of the
	// space — is known without loading anything.
	width, err := topology.Width(in.Space, in.Options)
	if err != nil {
		return nil, err
	}
	held, err := s.Cores.Admit(ctx, width)
	if err != nil {
		return nil, err
	}
	defer held.Release()

	h, err := s.History.History(ctx, org, held)
	if err != nil {
		return nil, err
	}
	report, err := topology.Search(ctx, org, h, in.Space, in.Options, held)
	if err != nil {
		return nil, err
	}

	run := &Run{
		ID: uuid.NewString(), Org: org, At: s.now(), By: in.By.Trim(),
		Space: in.Space, Options: in.Options, Report: report,
	}
	row, err := runKind.New(s.app, org)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	row.Id = run.ID
	row.Set(fieldAt, run.At)
	row.Set(fieldBy, run.By)
	if err := setJSON(row, fieldSpace, run.Space); err != nil {
		return nil, err
	}
	if err := setJSON(row, fieldOptions, run.Options); err != nil {
		return nil, err
	}
	if err := setJSON(row, fieldReport, run.Report); err != nil {
		return nil, err
	}
	row.Set(fieldTrials, len(report.Trials))
	row.Set(fieldEvents, report.Events)
	row.Set(fieldJudged, report.Judged)
	row.Set(fieldElapsed, int64(report.Elapsed))
	row.Set(fieldRefusal, report.Refusal)
	if report.Winner != nil {
		if err := setJSON(row, fieldWinner, report.Winner.Topology); err != nil {
			return nil, err
		}
	} else {
		row.Set(fieldWinner, "")
	}
	if err := s.app.Save(row); err != nil {
		return nil, fmt.Errorf("%w: keeping the run: %w", ErrStore, err)
	}
	return run, nil
}

// Run reads one whole search back.
func (s *Shelf) Run(ctx context.Context, org string, in *RefIn) (*Run, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	rows, err := runKind.Find(s.app, org, "id = {:id}", "", 1, dbx.Params{"id": strings.TrimSpace(in.ID)})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNotHere, in.ID)
	}
	run := &Run{ID: rows[0].Id, Org: org, At: rows[0].GetDateTime(fieldAt).Time(), By: rows[0].GetString(fieldBy)}
	if err := rows[0].UnmarshalJSONField(fieldSpace, &run.Space); err != nil {
		return nil, fmt.Errorf("%w: %s space: %w", ErrStore, rows[0].Id, err)
	}
	if err := rows[0].UnmarshalJSONField(fieldOptions, &run.Options); err != nil {
		return nil, fmt.Errorf("%w: %s options: %w", ErrStore, rows[0].Id, err)
	}
	if err := rows[0].UnmarshalJSONField(fieldReport, &run.Report); err != nil {
		return nil, fmt.Errorf("%w: %s report: %w", ErrStore, rows[0].Id, err)
	}
	return run, nil
}

// Runs lists this tenant's searches, most recent first, as summaries.
func (s *Shelf) Runs(ctx context.Context, org string, in *RunsIn) (*Runs, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	rows, err := runKind.Find(s.app, org, "", "-"+fieldAt, limit+1, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	out := &Runs{Runs: make([]Summary, 0, len(rows))}
	if len(rows) > limit {
		rows, out.Cut = rows[:limit], true
	}
	for _, row := range rows {
		sum := Summary{
			ID: row.Id, At: row.GetDateTime(fieldAt).Time(), By: row.GetString(fieldBy),
			Trials: row.GetInt(fieldTrials), Events: row.GetInt(fieldEvents), Judged: row.GetInt(fieldJudged),
			Elapsed: time.Duration(row.GetInt(fieldElapsed)), Refusal: row.GetString(fieldRefusal),
		}
		if raw := row.GetString(fieldWinner); raw != "" && raw != "null" {
			var t topology.Topology
			if err := json.Unmarshal([]byte(raw), &t); err == nil {
				sum.Winner = &t
			}
		}
		out.Runs = append(out.Runs, sum)
	}
	return out, nil
}

// Fit builds learned state under one shape and keeps it.
func (s *Shelf) Fit(ctx context.Context, org string, in *FitIn) (*Fit, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	if in.By.Trim() == "" {
		return nil, ErrDecider
	}
	if s.History == nil {
		return nil, ErrNoHistory
	}

	// One candidate, so one worker — held before the history is read, for the
	// reason stated in Search.
	held, err := s.Cores.Admit(ctx, 1)
	if err != nil {
		return nil, err
	}
	defer held.Release()

	h, err := s.History.History(ctx, org, held)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	snap, trial, err := topology.Fit(ctx, org, h, in.Topology, in.Options, held)
	if err != nil {
		return nil, err
	}

	fit := &Fit{
		ID: uuid.NewString(), Org: org, At: s.now(), By: in.By.Trim(),
		Elapsed:  time.Since(started),
		Topology: in.Topology, Digest: trial.Digest, Trial: trial,
		Adoptable: s.Model != nil && s.Model.Digest() == trial.Digest,
	}
	row, err := fitKind.New(s.app, org)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	row.Id = fit.ID
	row.Set(fieldAt, fit.At)
	row.Set(fieldBy, fit.By)
	row.Set(fieldElapsed, int64(fit.Elapsed))
	row.Set(fieldDigest, fit.Digest)
	for field, v := range map[string]any{fieldTopology: fit.Topology, fieldTrial: fit.Trial, fieldState: snap} {
		if err := setJSON(row, field, v); err != nil {
			return nil, err
		}
	}
	if err := s.app.Save(row); err != nil {
		return nil, fmt.Errorf("%w: keeping the fit: %w", ErrStore, err)
	}
	return fit, nil
}

// Fits lists this tenant's fitted states, most recent first.
func (s *Shelf) Fits(ctx context.Context, org string, in *FitsIn) (*Fits, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	rows, err := fitKind.Find(s.app, org, "", "-"+fieldAt, limit+1, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	out := &Fits{Fits: make([]Fit, 0, len(rows))}
	if len(rows) > limit {
		rows, out.Cut = rows[:limit], true
	}
	for _, row := range rows {
		f, err := s.readFit(org, row)
		if err != nil {
			return nil, err
		}
		out.Fits = append(out.Fits, *f)
	}
	return out, nil
}

// Adopt installs a fit into the live model.
//
// Three refusals, and each one is the model's own rather than this package's
// opinion: no live model, a shape the running model is not, or state that fails
// the mass invariant. anomaly.Restore performs the last two, so a fit adopted
// here has passed exactly the checks a snapshot restored anywhere else does.
func (s *Shelf) Adopt(ctx context.Context, org string, in *AdoptIn) (*Fit, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, err
	}
	if in.By.Trim() == "" {
		return nil, ErrDecider
	}
	if s.Model == nil {
		return nil, ErrNoModel
	}
	rows, err := fitKind.Find(s.app, org, "id = {:id}", "", 1, dbx.Params{"id": strings.TrimSpace(in.ID)})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStore, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoFit, in.ID)
	}
	row := rows[0]
	if !row.GetDateTime(fieldAdopted).Time().IsZero() {
		return nil, fmt.Errorf("%w: %s", ErrAdopted, row.Id)
	}

	var snap anomaly.Snapshot
	if err := row.UnmarshalJSONField(fieldState, &snap); err != nil {
		return nil, fmt.Errorf("%w: %s state: %w", ErrStore, row.Id, err)
	}
	// The tenant is taken from the row's own scope and not from the snapshot, so
	// a snapshot whose OrgID says otherwise cannot install another tenant's model.
	if snap.OrgID != org {
		return nil, fmt.Errorf("%w: the fit was built for %q and is being adopted into %q", ErrShape, snap.OrgID, org)
	}
	if err := s.Model.Restore(snap); err != nil {
		return nil, err
	}

	row.Set(fieldAdopted, s.now())
	row.Set(fieldAdoptBy, in.By.Trim())
	row.Set(fieldAdoptWhy, strings.TrimSpace(in.Reason))
	if err := s.app.Save(row); err != nil {
		return nil, fmt.Errorf("%w: recording the adoption: %w", ErrStore, err)
	}
	return s.readFit(org, row)
}

// Adopted is this tenant's most recently adopted state, if it has one.
//
// It is the reload, and it exists because adoption was the one governed act
// whose EFFECT was not durable. The fit is a row; the adoption is a row; but what
// adoption does is install learned state into a model that lives in memory, and
// memory does not survive a rollout — the deployment is one replica with a
// Recreate strategy — or an eviction, since the live store holds a bounded number
// of tenants' models at once. A control that was adopted six months ago and
// silently returned to warming on a Tuesday deploy is exactly the failure a
// monitoring programme cannot detect from the outside: it reports no alerts,
// which is what a quiet institution also reports.
//
// So the model asks this when it plants a tenant's model (anomaly.Store.Warm),
// and a control cannot go quiet without somebody deciding it should. It is per
// tenant, it reads only that tenant's rows, and it is a read: nothing here
// installs anything, because the installing is the model's own Restore with the
// model's own digest and mass checks.
func (s *Shelf) Adopted(org string) (anomaly.Snapshot, bool) {
	if err := brand.Tenant(org); err != nil {
		return anomaly.Snapshot{}, false
	}
	rows, err := fitKind.Find(s.app, org, fieldAdopted+" != ''", "-"+fieldAdopted, 1, nil)
	if err != nil || len(rows) == 0 {
		return anomaly.Snapshot{}, false
	}
	var snap anomaly.Snapshot
	if err := rows[0].UnmarshalJSONField(fieldState, &snap); err != nil {
		return anomaly.Snapshot{}, false
	}
	// The tenant is the row's own scope. A snapshot whose OrgID says otherwise is
	// not this tenant's state and is not installed into this tenant's model, for
	// the same reason Adopt refuses one.
	if snap.OrgID != org {
		return anomaly.Snapshot{}, false
	}
	return snap, true
}

func (s *Shelf) readFit(org string, row *core.Record) (*Fit, error) {
	f := &Fit{
		ID: row.Id, Org: org, At: row.GetDateTime(fieldAt).Time(), By: row.GetString(fieldBy),
		Elapsed: time.Duration(row.GetInt(fieldElapsed)),
		Digest:  row.GetString(fieldDigest),
		Adopted: row.GetDateTime(fieldAdopted).Time(), AdoptedBy: row.GetString(fieldAdoptBy),
	}
	if err := row.UnmarshalJSONField(fieldTopology, &f.Topology); err != nil {
		return nil, fmt.Errorf("%w: %s topology: %w", ErrStore, row.Id, err)
	}
	if err := row.UnmarshalJSONField(fieldTrial, &f.Trial); err != nil {
		return nil, fmt.Errorf("%w: %s trial: %w", ErrStore, row.Id, err)
	}
	f.Adoptable = s.Model != nil && s.Model.Digest() == f.Digest
	return f, nil
}

func setJSON(row *core.Record, field string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrStore, field, err)
	}
	row.Set(field, string(raw))
	return nil
}
