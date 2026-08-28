// Copyright 2024-2026 Lux Industries Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// Package receipt is what an institution was told about a transaction, kept so
// that offering the same transaction again returns the same answer instead of
// computing a second one.
//
// # Why a retry is not a second transaction
//
// A client that never saw a response resends. Every number this engine exists to
// compute is a count of transactions over a window — nine deposits under a
// reporting limit, a sum over thirty days, a structuring spread — so a retry that
// is counted is an aggregate that is wrong by the number of times the network
// failed. Worse than wrong: the second offer of an UNCHANGED transaction sees a
// window it has already put itself in, so the verdict can flip from allow to
// block, and a blocked transaction is retained as a refusal under AMLR Art. 77(3).
// A client retry then files a regulatory record of a refusal the institution never
// made, and refuses a customer's payment on the way.
//
// The retained ledger has always been idempotent. Nothing else was, and an
// engine whose ledger holds one record while its aggregates hold three is worse
// than one that double counts everywhere, because the numbers disagree with each
// other and nothing says which is right.
//
// # What this is
//
// The identity of an offer, resolved ONCE, before anything is read or written,
// and the answer that was given for it. It is deliberately ignorant of what the
// answer says — the ledger is ignorant of what a body says for the same reason —
// so the engine's response shape is the engine's business and this stays a plane
// that keeps one opaque value under one identity.
//
// Two facts under one reference is not a retry. That is a caller with two
// different transactions under one id, it will never resolve on its own, and it
// is refused with [ErrDiffers] rather than answered from the wrong one.
//
// # How long a receipt is kept
//
// Exactly as long as the transaction can still affect an aggregate. [Window] is
// the widest window the aggregates keep, plus a day: past it the transaction has
// fallen out of every window it contributed to, so a re-offer cannot double count
// anything that is still being measured, and keeping the row longer would be a
// second copy of the ledger for no property. The bound is DERIVED from the
// windows rather than chosen, so tuning a window cannot leave the receipt outlived
// by the aggregate it protects.
//
// A receipt holds no personal data: a synthetic transaction reference, a keyed
// digest, and the engine's own verdict.
package receipt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/base/core"
	"github.com/hanzoai/dbx"

	"github.com/luxfi/aml/pkg/brand"
)

var (
	// ErrDiffers is two different transactions under one reference. The caller
	// has to resolve it and no retry ever will.
	ErrDiffers = errors.New("receipt: a different transaction has already been answered under this reference")
	// ErrRef is a missing reference. How LONG one may be is bounded once, at the
	// door, by types.MaxIdent — a reference is an identifier and there is one
	// answer to how big an identifier may be.
	ErrRef = errors.New("receipt: an offer with no reference cannot be recognised again")
	// ErrStore is a shelf that could not be read or written.
	ErrStore = errors.New("receipt: the receipts could not be read or written")
)

// Offer is a transaction as it was presented: what it is called, and what it
// said.
type Offer struct {
	// Ref is the caller's own transaction reference.
	Ref string
	// Mark names what the offer said. Two offers with one Ref and one Mark are
	// one transaction offered twice; with different Marks they are two
	// transactions under one name, which is refused. See [Mark].
	Mark string
}

// Mark names what a request said, canonically.
//
// It is a digest of the request body decoded and re-encoded, so a client that
// re-serialises — a different key order, different whitespace — is still
// recognised as having offered the same transaction. What it deliberately does
// NOT cover is anything the engine derives: an identifier it generated, a clock
// it read, a rate it applied. A digest over a value this process invented would
// differ on every offer, which is precisely the defect that made every retry a
// permanent conflict once before.
//
// A body that is not valid JSON is digested as the bytes it is, because there is
// nothing else honest to do with it and the request is about to be refused
// anyway.
func Mark(body []byte) string {
	canonical := body
	var any any
	if err := json.Unmarshal(body, &any); err == nil {
		// Go marshals map keys in sorted order, so this is a canonical form.
		if re, err := json.Marshal(any); err == nil {
			canonical = re
		}
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// Shelf keeps receipts durably.
//
// It has to be durable. The deployment is one replica with a Recreate strategy,
// so the process that answered the first offer is gone by the time a client
// retries across a rollout, and a receipt held in memory would be no receipt at
// all — the retry would be counted as a second transaction by every aggregate.
type Shelf struct {
	app core.App
	// Life is how long a receipt is held. Zero takes [DefaultWindow]; a
	// deployment passes [Window] of the aggregate windows it actually runs.
	Life time.Duration
}

// NewBase returns the durable shelf. [Ensure] has to have run first.
func NewBase(app core.App) *Shelf { return &Shelf{app: app} }

// Window is how long a receipt must be kept: the widest aggregate window plus a
// day.
//
// Derived and not chosen. A transaction inside a live window can still change
// what that window says, so a re-offer inside it must be recognised; past every
// window it can change nothing, so it need not be. The extra day covers a feed
// that delivers late (see velocity.Config.MaxLateness) and a disposal that runs
// once a day.
func Window(widest time.Duration) time.Duration { return widest + 24*time.Hour }

// DefaultWindow is what a shelf with no Keep set uses: thirty days plus a day,
// the standard aggregate windows.
const DefaultWindow = 31 * 24 * time.Hour

func (s *Shelf) life() time.Duration {
	if s.Life > 0 {
		return s.Life
	}
	return DefaultWindow
}

// Prior is the answer this tenant was already given for this offer.
//
// It reports false when there is none, which is the ordinary path. It returns
// [ErrDiffers] when the reference is held by a different transaction, because
// answering from the wrong one would attach one transaction's verdict to
// another's identifier.
func (s *Shelf) Prior(ctx context.Context, org string, of Offer) ([]byte, bool, error) {
	if err := brand.Tenant(org); err != nil {
		return nil, false, err
	}
	ref, err := reference(of.Ref)
	if err != nil {
		return nil, false, err
	}
	rows, err := receipts.Find(s.app, org, fieldRef+" = {:ref}", "", 1, dbx.Params{"ref": ref})
	if err != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrStore, err)
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	if held := rows[0].GetString(fieldMark); held != of.Mark {
		return nil, false, fmt.Errorf("%w: %s", ErrDiffers, ref)
	}
	return []byte(rows[0].GetString(fieldAnswer)), true, nil
}

// Keep records the answer this tenant was given.
//
// It is written after the work and before the response, so a receipt exists for
// every answer a caller could have received and for no answer it could not. A
// process that dies mid-transaction leaves no receipt, which is correct: the
// caller was never answered, so the offer has not happened yet.
func (s *Shelf) Keep(ctx context.Context, org string, of Offer, answer []byte, at time.Time) error {
	if err := brand.Tenant(org); err != nil {
		return err
	}
	ref, err := reference(of.Ref)
	if err != nil {
		return err
	}
	row, err := receipts.New(s.app, org)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}
	row.Set(fieldRef, ref)
	row.Set(fieldMark, of.Mark)
	row.Set(fieldAnswer, string(answer))
	row.Set(fieldAt, at.UTC())
	if err := s.app.Save(row); err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}
	return nil
}

// Dispose drops the receipts that can no longer affect an aggregate.
//
// It crosses tenants because it is the operator's housekeeping and not any
// institution's, exactly as retention's disposal does — and unlike that one it
// destroys nothing an obligation covers: a receipt is this engine's own memory
// of having answered, never a record it is required to keep.
func (s *Shelf) Dispose(ctx context.Context, now time.Time) (int, error) {
	before := now.UTC().Add(-s.life())
	rows, err := receipts.Across(s.app, fieldAt+" < {:before}", fieldAt, disposeLimit, dbx.Params{"before": before})
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrStore, err)
	}
	for _, row := range rows {
		if err := s.app.Delete(row); err != nil {
			return 0, fmt.Errorf("%w: %w", ErrStore, err)
		}
	}
	return len(rows), nil
}

// disposeLimit bounds one disposal pass, so a long-idle deployment's first run
// is a bounded amount of work rather than however many rows accumulated. The
// pass repeats daily and the surplus goes on the next one.
const disposeLimit = 50_000

// Held is how many receipts this shelf holds, for capacity monitoring.
func (s *Shelf) Held() (int, error) { return receipts.Count(s.app) }

func reference(ref string) (string, error) {
	if ref = strings.TrimSpace(ref); ref == "" {
		return "", ErrRef
	}
	return ref, nil
}

// Forget drops this tenant's receipt for one transaction.
//
// It is the state a process leaves behind when it dies between the last write
// and the response: the work is done and the caller was never answered. Nothing
// in the engine calls it — the answer is kept, not un-kept — and it exists so a
// test can produce that state and prove that every plane recognises the
// transaction on its own rather than merely being sequenced behind this one.
func (s *Shelf) Forget(ctx context.Context, org, ref string) error {
	if err := brand.Tenant(org); err != nil {
		return err
	}
	rows, err := receipts.Find(s.app, org, fieldRef+" = {:ref}", "", 1, dbx.Params{"ref": ref})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStore, err)
	}
	for _, row := range rows {
		if err := s.app.Delete(row); err != nil {
			return fmt.Errorf("%w: %w", ErrStore, err)
		}
	}
	return nil
}
