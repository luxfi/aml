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
	fieldOrg          = "org_id"
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
// The filter is parameterised. Interpolating an identifier into the filter string
// lets a value chosen by the caller change the shape of the query: an identifier
// containing the filter language's own syntax can widen the window to another
// tenant's transactions or narrow it to none, and narrowing it to none is the
// dangerous direction, because a velocity rule over an empty window reports no
// velocity.
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
	filter := fmt.Sprintf("%s = {:org} && %s = {:id} && %s >= {:since}", fieldOrg, field, fieldAt)

	records, err := b.app.FindRecordsByFilter(
		Collection,
		filter,
		"-"+fieldAt,
		0,
		0,
		dbx.Params{
			"org":   subj.OrgID,
			"id":    subj.ID,
			"since": since.Format(time.RFC3339),
		},
	)
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
func (b *Base) Append(ctx context.Context, orgID string, e Event) error {
	collection, err := b.app.FindCollectionByNameOrId(Collection)
	if err != nil {
		return fmt.Errorf("history: collection %s: %w", Collection, err)
	}
	if orgID == "" {
		return fmt.Errorf("history: refusing to store an event with no organisation")
	}

	r := core.NewRecord(collection)
	r.Set(fieldOrg, orgID)
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
