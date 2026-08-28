// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

// What this engine is holding for a tenant, and what it has had to let go.
//
// Every live control in this process is bounded, and each bound can bind. When
// one does, an aggregate stops being kept, a model is not planted, an observation
// is turned away — and none of those raises an error, because refusing a payment
// because a cache is full would be the worse failure. What is NOT allowed is for
// them to happen quietly: an aggregate that reads zero is what a clean account
// also reads, and a model that is warming reports nothing, which is what a clean
// institution also reports. A control that switches itself off without saying so
// is worse than no control, because it is a control somebody is relying on.
//
// So there is ONE door for the question "is anything of mine quietly degraded",
// per tenant, and it answers in words rather than in ratios a reader has to
// interpret. Nothing here names another institution: the tenant's own numbers,
// and the process's own counts, which are facts about a deployment rather than
// about anybody's business.

import (
	"context"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/dictionary"
	"github.com/luxfi/aml/pkg/velocity"
)

// LoadIn takes no arguments: a caller asks about itself and cannot ask about
// anyone else.
type LoadIn struct{}

// Load is what this engine holds for one tenant.
type Load struct {
	Org string `json:"org"`
	// Aggregates is the sliding windows every behavioural measure reads: how many
	// keys this tenant holds against its own ceiling in bytes, how many it has
	// had to drop, and a grade. A tenant at "full" is losing aggregates it would
	// otherwise have found structuring in, and the answer is a bigger ceiling.
	Aggregates velocity.Load `json:"aggregates"`
	// Model says whether this tenant has a behavioural model at all. Crowded
	// means the process holds models for as many institutions as it may and this
	// one is not among them, so nothing behavioural has examined its
	// transactions — the rules still ran, the model did not.
	Model Model `json:"model"`
	// Fields is the payload catalog's accumulator: what it holds for this
	// process, and how many institutions it was full for. A tenant the roster
	// refused has no catalog at all, and an empty catalog reads exactly like a
	// payload surface with nothing in it.
	Fields dictionary.Pressure `json:"fields"`
	// Engine is what the process is holding overall. It carries no tenant.
	Engine Engine `json:"engine"`
}

// Model is the behavioural half, reduced to what says whether it is running.
type Model struct {
	Planted  bool  `json:"planted"`
	Restored bool  `json:"restored"`
	Learned  int64 `json:"learned"`
	Warm     bool  `json:"warm"`
	Crowded  bool  `json:"crowded"`
}

// Engine is the process's own reading.
type Engine struct {
	// Orgs and Room are how many institutions this process holds live state for
	// and how many it may.
	Orgs int `json:"orgs"`
	Room int `json:"room"`
	// Refused counts the institutions turned away for want of a place, and
	// Crowded the transactions that reached no model because of it.
	Refused int64 `json:"refused"`
	Crowded int64 `json:"crowded"`
	// Held and Ceiling are the aggregate store's bytes, across every tenant.
	Held    int64 `json:"held"`
	Ceiling int64 `json:"ceiling"`
	// Late counts transactions that arrived older than their window and were
	// folded to its leading edge, so an operator can see a feed running behind.
	Late int64 `json:"late"`
}

// load answers for the caller's own tenant.
func (h *Handler) load(_ context.Context, org string, _ *LoadIn) (*Load, error) {
	out := &Load{Org: org}
	if h.Velocity != nil {
		out.Aggregates = h.Velocity.Load(org)
		p := h.Velocity.Pressure()
		out.Engine.Orgs, out.Engine.Room = p.Orgs, p.Room
		out.Engine.Refused, out.Engine.Late = p.Refused, p.Late
		out.Engine.Held, out.Engine.Ceiling = p.Held, p.Ceiling
	}
	if d := h.Planes.Dictionary; d != nil {
		out.Fields = d.Pressure()
	}
	if h.Anomaly != nil {
		st := h.Anomaly.State(org)
		out.Model = Model{
			Planted: !st.Planted.IsZero(), Restored: st.Restored,
			Learned: st.Learned, Warm: st.Warm, Crowded: st.Crowded,
		}
		out.Engine.Crowded = h.Anomaly.Pressure().Crowded
	}
	return out, nil
}

// grades is what a reviewer reads first. Kept beside the type it describes so
// that a new grade cannot be added without a name a person can act on.
var _ = []velocity.Grade{velocity.GradeClear, velocity.GradeCrowded, velocity.GradeFull}

var _ = anomaly.ReasonCrowded
