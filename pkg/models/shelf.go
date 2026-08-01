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
	History(ctx context.Context, org string) (replay.History, error)
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
	if strings.TrimSpace(in.By) == "" {
		return nil, ErrDecider
	}
	if s.History == nil {
		return nil, ErrNoHistory
	}
	h, err := s.History.History(ctx, org)
	if err != nil {
		return nil, err
	}
	report, err := topology.Search(ctx, org, h, in.Space, in.Options)
	if err != nil {
		return nil, err
	}

	run := &Run{
		ID: uuid.NewString(), Org: org, At: s.now(), By: strings.TrimSpace(in.By),
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
	if strings.TrimSpace(in.By) == "" {
		return nil, ErrDecider
	}
	if s.History == nil {
		return nil, ErrNoHistory
	}
	h, err := s.History.History(ctx, org)
	if err != nil {
		return nil, err
	}
	snap, trial, err := topology.Fit(ctx, org, h, in.Topology, in.Options)
	if err != nil {
		return nil, err
	}

	fit := &Fit{
		ID: uuid.NewString(), Org: org, At: s.now(), By: strings.TrimSpace(in.By),
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
	if strings.TrimSpace(in.By) == "" {
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
	row.Set(fieldAdoptBy, strings.TrimSpace(in.By))
	row.Set(fieldAdoptWhy, strings.TrimSpace(in.Reason))
	if err := s.app.Save(row); err != nil {
		return nil, fmt.Errorf("%w: recording the adoption: %w", ErrStore, err)
	}
	return s.readFit(org, row)
}

func (s *Shelf) readFit(org string, row *core.Record) (*Fit, error) {
	f := &Fit{
		ID: row.Id, Org: org, At: row.GetDateTime(fieldAt).Time(), By: row.GetString(fieldBy),
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
