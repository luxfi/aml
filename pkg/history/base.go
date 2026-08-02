package history

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/base/core"
	"github.com/hanzoai/dbx"
)

// Collection is where transaction events are stored.
const Collection = "aml_transactions"

// Store field names. They are named once here because the writer and the reader
// have to agree, and a silent disagreement produces an empty window rather than
// an error — which reads as a customer with no history.
const (
	fieldUser         = "user_id"
	fieldAccount      = "account_id"
	fieldCounterparty = "counterparty"
	fieldDevice       = "device"
	fieldAddress      = "address"
	fieldJurisdiction = "jurisdiction"
	fieldSymbol       = "symbol"
	fieldDirection    = "direction"
	fieldCurrency     = "currency"
	fieldUSD          = "usd"
	fieldAt           = "at"
	fieldTxID         = "tx_id"
)

// subjectField maps a subject kind to the column that identifies it.
var subjectField = map[string]string{
	SubjectUser:         fieldUser,
	SubjectAccount:      fieldAccount,
	SubjectCounterparty: fieldCounterparty,
	SubjectDevice:       fieldDevice,
	SubjectAddress:      fieldAddress,
}

// Base reads event windows from a Base collection.
type Base struct{ app core.App }

// NewBase builds a Store over a Base app.
func NewBase(app core.App) *Base { return &Base{app: app} }

// Window returns the subject's events over the lookback, most recent first.
//
// The subject's identifier is bound as a parameter. Written into the filter as text
// it could change the shape of the query: an identifier containing the filter
// language's own syntax can widen the window to another tenant's transactions or
// narrow it to none, and narrowing it to none is the dangerous direction, because a
// velocity rule over an empty window reports no velocity.
//
// The window is bounded by the transaction's own timestamp, not by when the record
// was written. Those differ whenever events are replayed or backfilled, and
// aggregating a reporting period by insertion time attributes activity to the day
// the importer ran.
func (b *Base) Window(ctx context.Context, subj Subject, lookback time.Duration) ([]Event, error) {
	if err := subj.Valid(); err != nil {
		return nil, err
	}
	field := subjectField[subj.Kind]

	since := time.Now().UTC().Add(-lookback)
	filter := fmt.Sprintf("%s = {:id} && %s >= {:since}", field, fieldAt)

	records, err := events.Find(b.app, subj.OrgID, filter, "-"+fieldAt, 0, dbx.Params{
		"id":    subj.ID,
		"since": since,
	})
	if err != nil {
		return nil, fmt.Errorf("history: window for %s %q: %w", subj.Kind, subj.ID, err)
	}

	out := make([]Event, 0, len(records))
	for _, r := range records {
		out = append(out, Event{
			ID:           r.GetString(fieldTxID),
			At:           r.GetDateTime(fieldAt).Time(),
			USD:          r.GetFloat(fieldUSD),
			Currency:     r.GetString(fieldCurrency),
			Direction:    r.GetString(fieldDirection),
			User:         r.GetString(fieldUser),
			Counterparty: r.GetString(fieldCounterparty),
			Account:      r.GetString(fieldAccount),
			Device:       r.GetString(fieldDevice),
			Address:      r.GetString(fieldAddress),
			Jurisdiction: r.GetString(fieldJurisdiction),
			Symbol:       r.GetString(fieldSymbol),
		})
	}
	return out, nil
}

// Append records an event so it appears in later windows.
//
// USD is stored as converted at the time of the transaction. Converting on read
// instead would let a rate movement change what a past window totalled, so an
// aggregate that crossed a reporting threshold yesterday could fall below it
// today and a filed report would no longer be reproducible from the data.
// An event IS one transaction, so an id this tenant has already recorded is not
// a second event and is not appended. That matters beyond a client retry: the
// answer to an offer is kept AFTER the writes, so a process that dies between
// them leaves the work done and no record of having answered, and the client —
// never answered — offers again. Every plane the ingest path writes to has to
// recognise the transaction by itself rather than rely on being sequenced behind
// something that does, or the count every aggregate rule reads is wrong by the
// number of times the process was interrupted.
func (b *Base) Append(ctx context.Context, orgID string, e Event) error {
	held, err := events.Find(b.app, orgID, fieldTxID+" = {:tx}", "", 1, dbx.Params{"tx": e.ID})
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}
	if len(held) > 0 {
		return nil
	}
	r, err := events.New(b.app, orgID)
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}
	r.Set(fieldTxID, e.ID)
	r.Set(fieldAt, e.At.UTC())
	r.Set(fieldUSD, e.USD)
	r.Set(fieldCurrency, e.Currency)
	r.Set(fieldDirection, e.Direction)
	r.Set(fieldCounterparty, e.Counterparty)
	r.Set(fieldAccount, e.Account)
	r.Set(fieldDevice, e.Device)
	r.Set(fieldAddress, e.Address)
	r.Set(fieldJurisdiction, e.Jurisdiction)
	r.Set(fieldSymbol, e.Symbol)
	r.Set(fieldUser, e.User)
	return b.app.Save(r)
}
