// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// Package models keeps the record of what a tenant's model is, and of every
// study that produced it.
//
// Two things are kept, and both are kept because both are governed acts.
//
// A RUN is a search over the space of model shapes (pkg/topology) against this
// tenant's own history. JMLSG 5.7.18 requires new detection to be implemented and
// tested before live activation and FCG 3.2.5A requires a retirement to be
// justified against the outgoing configuration's performance; a search is how
// both questions get an answer, and an answer nobody can retrieve six months
// later did not answer them. The run holds the space asked for, the options,
// every trial, and the seed — so the whole thing can be re-run and checked rather
// than believed.
//
// A FIT is learned state built from this tenant's history under one shape. It is
// what makes a recommendation actionable: adopt it and the model starts warm
// rather than blind. Adoption is recorded with its decider, because a model that
// changed and cannot say who changed it is a control nobody owns.
//
// # The learned state never leaves the tenant's store
//
// A fit's snapshot is written and read here and is not on any answer this package
// returns. Mass counters describe where a tenant's activity is dense; handing
// them out over an API would publish the shape of an institution's customer
// behaviour to whoever holds a token, which is a disclosure the alerts themselves
// are careful not to make. Callers get the digest, the trial and whether the fit
// is adoptable — everything needed to decide, and nothing that describes the
// customers.
//
// Nothing here deletes and nothing evicts, on the same terms as every other
// record plane: disposal is pkg/retention's decision alone.
package models

import (
	"errors"
	"time"

	"github.com/luxfi/aml/pkg/topology"
	"github.com/luxfi/aml/pkg/types"
)

// Errors.
var (
	ErrNoHistory = errors.New("models: no history source is wired, so a search has nothing to replay")
	ErrNoModel   = errors.New("models: no live model is wired, so nothing can be adopted into one")
	ErrNotHere   = errors.New("models: no such run")
	ErrNoFit     = errors.New("models: no such fit")
	ErrDecider   = errors.New("models: no decider, so the record names nobody's decision")
	ErrShape     = errors.New("models: the fitted shape is not the shape the model is running")
	ErrAdopted   = errors.New("models: already adopted")
	ErrStore     = errors.New("models: store")
)

// Summary is a run as a list reads it: enough to choose one to open, and none of
// the trials.
//
// A run over a full grid carries a curve per candidate, which is the right size
// for one answer and the wrong size for a page of twenty. The two are separate
// shapes rather than one shape with a flag, so a list can never accidentally be
// the heavy read.
type Summary struct {
	ID      string        `json:"id"`
	At      time.Time     `json:"at"`
	By      string        `json:"by"`
	Trials  int           `json:"trials"`
	Events  int           `json:"events"`
	Judged  int           `json:"judged"`
	Elapsed time.Duration `json:"elapsed"`
	// Winner is the shape the run recommended, and Refusal is why it recommended
	// none. Exactly one of them is set, on the summary as on the report.
	Winner  *topology.Topology `json:"winner,omitempty"`
	Refusal string             `json:"refusal,omitempty"`
}

// Run is a whole search, kept.
type Run struct {
	ID      string           `json:"id"`
	Org     string           `json:"org"`
	At      time.Time        `json:"at"`
	By      string           `json:"by"`
	Space   topology.Space   `json:"space"`
	Options topology.Options `json:"options"`
	Report  topology.Report  `json:"report"`
}

// Fit is learned state built under one shape, as a caller sees it.
//
// The snapshot itself is deliberately absent — see the package doc. Digest is
// what identifies the shape, and Adoptable says whether the live model is running
// that shape today.
type Fit struct {
	ID  string    `json:"id"`
	Org string    `json:"org"`
	At  time.Time `json:"at"`
	By  string    `json:"by"`
	// Elapsed is what this fit cost the machine. It is on the record for the same
	// reason a run's is: the model plane is the expensive one, and a tenant's
	// spend on it has to be answerable from what was kept rather than from a
	// counter somewhere that a restart resets.
	Elapsed  time.Duration     `json:"elapsed"`
	Topology topology.Topology `json:"topology"`
	Digest   string            `json:"digest"`
	Trial    topology.Trial    `json:"trial"`
	// Adoptable is whether this fit's shape matches the model this deployment is
	// running. A fit of a different shape is a recommendation to change the
	// deployment, not something that can be installed by restoring a file.
	Adoptable bool      `json:"adoptable"`
	Adopted   time.Time `json:"adopted,omitzero"`
	AdoptedBy string    `json:"adopted_by,omitempty"`
}

// The typed operations.
//
// By is the decider on each of the three that record a decision, and it is a
// [types.Decider]: it carries `json:"-"`, so it is not a field of the wire
// contract at all, and the transport writes the authenticated subject onto it
// (pkg/api/typed.go). A search, a fit and an adoption are each a governed act,
// and the record of one has to name the identity that took it rather than the
// text a request body offered.
type (
	// SearchIn runs a search over this tenant's own history.
	SearchIn struct {
		Space   topology.Space   `json:"space"`
		Options topology.Options `json:"options,omitempty"`
		By      types.Decider    `json:"-"`
	}

	// RefIn names one run or one fit.
	RefIn struct {
		ID string `json:"id"`
	}

	// RunsIn lists this tenant's runs, most recent first.
	RunsIn struct {
		Limit int `json:"limit,omitempty"`
	}

	// Runs is the answer to RunsIn.
	Runs struct {
		Runs []Summary `json:"runs"`
		Cut  bool      `json:"cut,omitempty"`
	}

	// FitIn builds learned state under one shape.
	FitIn struct {
		Topology topology.Topology `json:"topology"`
		Options  topology.Options  `json:"options,omitempty"`
		By       types.Decider     `json:"-"`
	}

	// FitsIn lists this tenant's fits, most recent first.
	FitsIn struct {
		Limit int `json:"limit,omitempty"`
	}

	// Fits is the answer to FitsIn.
	Fits struct {
		Fits []Fit `json:"fits"`
		Cut  bool  `json:"cut,omitempty"`
	}

	// AdoptIn installs a fit into the live model.
	AdoptIn struct {
		ID     string        `json:"id"`
		Reason string        `json:"reason"`
		By     types.Decider `json:"-"`
	}
)

// DefaultLimit is how many rows a list returns when the caller names no bound.
const DefaultLimit = 50
