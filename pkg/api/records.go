// Copyright 2024-2026 Lux Partners Limited. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/luxfi/aml/pkg/replay"
	"github.com/luxfi/aml/pkg/retention"
	"github.com/luxfi/aml/pkg/token"
	"github.com/luxfi/aml/pkg/types"
)

// The record plane is where the ledger, the tokeniser and the engine meet. Each
// of the three knows nothing of the other two: retention holds records and does
// not know they are sealed, token seals bytes and does not know they are records,
// replay reads events and cannot write. This file is the only place that joins
// them, which is why it is the only place that has to be right about all three.

var (
	// errNoRecords is the refusal that keeps the engine from processing a
	// transaction it cannot record.
	errNoRecords = errors.New("record plane is not available")
	errNoParty   = errors.New("record names no party")
	errFull      = errors.New("history is full")
)

// maxHistory bounds a replay. The ledger holds five years of records, and a
// replay is an operator action, so it reads a bounded prefix rather than however
// much a five-year ledger happens to contain.
const maxHistory = 100_000

// maxSample bounds a caller-supplied history.
const maxSample = 1000

// maxReason bounds the recorded reason for a refusal.
const maxReason = 512

// vault is the org's tokeniser, or the reason there is none. A missing key is a
// refusal and never a pass-through: the alternative is a clean receipt for a
// transaction nobody can produce a record of.
func (h *Handler) vault(orgID string) (*token.Vault, error) {
	if h.Records == nil || h.Keys == nil {
		return nil, errNoRecords
	}
	v, err := h.Keys.Org(orgID)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNoRecords, err)
	}
	return v, nil
}

// slot binds a sealed body to the record that holds it. Both halves are
// unsealed metadata, so the binding is recomputable when the record is read and a
// body cannot be moved to another record or another class.
func slot(class retention.Class, ref string) string {
	return string(class) + ":" + ref
}

// parties are the keys a record is found by. Direct identifiers do not reach the
// ledger in the clear: what is indexed is the pseudonym, deterministic within
// this org and unrelated to the same person in any other.
func parties(v *token.Vault, of map[token.Domain]string) ([]string, error) {
	out := make([]string, 0, len(of))
	for _, d := range []token.Domain{token.DomainSubject, token.DomainName, token.DomainAccount, token.DomainWallet} {
		value := strings.TrimSpace(of[d])
		if value == "" {
			continue
		}
		p, err := v.Pseudonym(d, value)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errNoParty
	}
	return out, nil
}

// seal marshals and seals what a record has to be able to reconstruct. The whole
// body is sealed as one piece, which is what keeps protection at rest from
// becoming redaction: nothing is dropped and nothing is masked.
//
// It returns the fingerprint alongside, because the ledger identifies a retry by
// what a record SAYS and a seal says it differently every time — see
// token.Vault.Fingerprint and retention.Record.Fingerprint. Sealing and naming
// the same bytes happens here, once, so the two cannot be computed over different
// things.
func seal(v *token.Vault, class retention.Class, ref string, of any) (body []byte, mark string, err error) {
	plain, err := json.Marshal(of)
	if err != nil {
		return nil, "", fmt.Errorf("marshal record body: %w", err)
	}
	at := slot(class, ref)
	sealed, err := v.Seal(at, plain)
	if err != nil {
		return nil, "", err
	}
	return sealed, v.Fingerprint(at, plain), nil
}

// retain writes the record for a transaction the engine has just evaluated.
//
// A blocked transaction is a refusal to carry out a transaction, and its
// retention runs from the date of refusal rather than from the end of any
// relationship (AMLR Art. 77(3) third trigger) — so it is retained as a refusal
// with the rules that caused it, not as an ordinary transaction record.
func (h *Handler) retain(v *token.Vault, tx types.Transaction, entity types.Entity, relationship string, alerts []types.Alert, action string) (string, error) {
	party, err := parties(v, map[token.Domain]string{
		token.DomainSubject: tx.UserID,
		token.DomainName:    entity.Name,
		token.DomainAccount: tx.AccountID,
	})
	if err != nil {
		return "", err
	}
	if tx.Counterparty != "" {
		other, err := v.Pseudonym(token.DomainSubject, tx.Counterparty)
		if err != nil {
			return "", err
		}
		party = append(party, other)
	}

	record := retention.Record{
		Org:      tx.OrgID,
		Ref:      tx.ID,
		Parties:  party,
		Occurred: tx.Timestamp,
	}
	switch action {
	case types.ActionBlock:
		record.Class = retention.ClassRefusal
		record.Trigger = retention.TriggerRefusal
		record.Reason = refusalReason(alerts)
	default:
		record.Class = retention.ClassTransaction
		if relationship != "" {
			record.Trigger = retention.TriggerRelationshipEnd
			record.Relationship = relationship
		} else {
			record.Trigger = retention.TriggerOccasional
		}
	}

	body, mark, err := seal(v, record.Class, record.Ref, types.EvalContext{Tx: tx, Entity: entity})
	if err != nil {
		return "", err
	}
	record.Body, record.Fingerprint = body, mark

	return h.Records.Retain(record)
}

// refusalReason names the rules that refused the transaction, because "refused"
// on its own does not answer why the firm refrained.
func refusalReason(alerts []types.Alert) string {
	names := make([]string, 0, len(alerts))
	for _, a := range alerts {
		if a.ActionTaken == types.ActionBlock {
			names = append(names, a.RuleName)
		}
	}
	if len(names) == 0 {
		return "blocked on the aggregate score"
	}
	reason := "blocked by " + strings.Join(names, ", ")
	if len(reason) > maxReason {
		reason = reason[:maxReason]
	}
	return reason
}

// history reads the org's retained transactions back into replayable events.
//
// The bodies are sealed, so this is where the vault opens them, under the
// monitoring purpose — testing a typology before activation is monitoring work
// and not a commercial read (AMLD4 Art. 41(2)). A body that does not open is an
// error and not a skipped event: a replay over a silently shortened history is
// the same lie as a replay over an empty one.
func (h *Handler) history(orgID string, v *token.Vault) (replay.Slice, error) {
	judged, err := h.dispositions(orgID)
	if err != nil {
		return nil, err
	}

	var out replay.Slice
	err = h.Records.Each(retention.PurposeMonitoring, orgID, retention.ClassTransaction, func(r retention.Record) error {
		if len(out) >= maxHistory {
			return errFull
		}
		plain, err := v.Open(slot(r.Class, r.Ref), r.Body)
		if err != nil {
			return fmt.Errorf("record %s: %w", r.ID, err)
		}
		var ctx types.EvalContext
		if err := json.Unmarshal(plain, &ctx); err != nil {
			return fmt.Errorf("record %s: %w", r.ID, err)
		}

		event := replay.Event{Tx: ctx.Tx, Entity: ctx.Entity}
		for _, a := range h.Alerts.ByTx(orgID, ctx.Tx.ID) {
			event.Alerted = append(event.Alerted, a.RuleID)
			if d, ok := judged[a.ID]; ok {
				event.Disposition = d
			}
		}
		out = append(out, event)
		return nil
	})
	if err != nil && !errors.Is(err, errFull) {
		return nil, err
	}
	return out, nil
}

// dispositions is what humans concluded, keyed by the alert they concluded it
// about. It comes from the retained assessments, which is the only place a
// decision not to report is recorded.
func (h *Handler) dispositions(orgID string) (map[string]replay.Disposition, error) {
	out := make(map[string]replay.Disposition)
	err := h.Records.Each(retention.PurposeMonitoring, orgID, retention.ClassAssessment, func(r retention.Record) error {
		if r.Assessment == nil {
			return nil
		}
		d := replay.Unproductive
		if r.Assessment.Result == retention.Reported {
			d = replay.Productive
		}
		for _, alert := range r.Assessment.Alerts {
			out[alert] = d
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// assess retains the Art. 69(2) assessment behind a case resolution: the
// information and circumstances considered, and the result, whether or not it
// produced a report (AMLR Art. 77(1)(b)). A dismissal is a retained decision.
func (h *Handler) assess(v *token.Vault, orgID string, c *types.Case, in resolution) (string, error) {
	party := make([]string, 0, len(c.EntityIDs))
	for _, entity := range c.EntityIDs {
		if strings.TrimSpace(entity) == "" {
			continue
		}
		p, err := v.Pseudonym(token.DomainSubject, entity)
		if err != nil {
			return "", err
		}
		party = append(party, p)
	}
	if len(party) == 0 {
		return "", errNoParty
	}

	result := retention.NotReported
	if in.Resolution == types.ResolutionSARFiled {
		result = retention.Reported
	}
	decided := time.Now().UTC()

	body, mark, err := seal(v, retention.ClassAssessment, c.ID, map[string]any{
		"case":       c,
		"resolution": in.Resolution,
	})
	if err != nil {
		return "", err
	}

	return h.Records.Retain(retention.Record{
		Org:         orgID,
		Class:       retention.ClassAssessment,
		Trigger:     retention.TriggerOccasional,
		Ref:         c.ID,
		Parties:     party,
		Occurred:    decided,
		Body:        body,
		Fingerprint: mark,
		Assessment: &retention.Assessment{
			Alerts:     c.AlertIDs,
			Case:       c.ID,
			Considered: in.Considered,
			Result:     result,
			Rationale:  in.Rationale,
			By:         in.By.Trim(),
			At:         decided,
		},
	})
}
